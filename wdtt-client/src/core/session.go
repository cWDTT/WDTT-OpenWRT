package core

import (
	"context"
	"crypto/cipher"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cbeuw/connutil"
	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

const (
	workerSendBuf      = 128
	sessionReadTimeout = 30 * time.Minute // Increased from 60s to 30min
	readBufSize        = 1600
	socketBufSize      = 625 * 1024
	keepaliveByte      = 0xFF // DTLS-level keepalive marker
	keepaliveInterval  = 15 * time.Second
)

// Handshake semaphore: limit to 3 concurrent DTLS handshakes
var handshakeSem = make(chan struct{}, 3)

// NullLoggerFactory подавляет логи pion
type NullLoggerFactory struct{}

func (n *NullLoggerFactory) NewLogger(_ string) logging.LeveledLogger { return &NullLogger{} }

type NullLogger struct{}

func (n *NullLogger) Trace(_ string)                    {}
func (n *NullLogger) Tracef(_ string, _ ...interface{}) {}
func (n *NullLogger) Debug(_ string)                    {}
func (n *NullLogger) Debugf(_ string, _ ...interface{}) {}
func (n *NullLogger) Info(_ string)                     {}
func (n *NullLogger) Infof(_ string, _ ...interface{})  {}
func (n *NullLogger) Warn(_ string)                     {}
func (n *NullLogger) Warnf(_ string, _ ...interface{})  {}
func (n *NullLogger) Error(_ string)                    {}
func (n *NullLogger) Errorf(_ string, _ ...interface{}) {}

// connectedUDPConn — обёртка для connected UDP socket → PacketConn
type connectedUDPConn struct{ *net.UDPConn }

func (c *connectedUDPConn) WriteTo(p []byte, _ net.Addr) (int, error) { return c.Write(p) }

// dialTURNConn открывает сокет до TURN-сервера и оборачивает его в
// net.PacketConn, которого ждёт turn.ClientConfig.Conn. По умолчанию — UDP.
// При tcp=true поднимает обычное TCP-соединение и оборачивает через
// turn.NewSTUNConn — это штатная возможность pion/turn (examples/turn-client/tcp),
// а не самописный протокол: NewSTUNConn сам разбирает STUN/ChannelData framing
// поверх потокового TCP. Нужен на сетях, где UDP до relay душится или
// дропается, а TCP до того же relay проходит.
func dialTURNConn(turnAddr string, tcp bool) (net.PacketConn, io.Closer, error) {
	if !tcp {
		resolved, err := net.ResolveUDPAddr("udp", turnAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("резолв TURN: %w", err)
		}
		c, err := net.DialUDP("udp", nil, resolved)
		if err != nil {
			return nil, nil, fmt.Errorf("подключение TURN UDP: %w", err)
		}
		_ = c.SetReadBuffer(socketBufSize)
		_ = c.SetWriteBuffer(socketBufSize)
		return &connectedUDPConn{c}, c, nil
	}

	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.Dial("tcp", turnAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("подключение TURN TCP: %w", err)
	}
	if tcpConn, ok := c.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
		_ = tcpConn.SetReadBuffer(socketBufSize)
		_ = tcpConn.SetWriteBuffer(socketBufSize)
	}
	return turn.NewSTUNConn(c), c, nil
}

// turnAddressDead сообщает, что по этой ошибке адрес relay стоит забанить:
// квота, недоступность, таймаут — всё это свойство конкретного адреса, а не
// пароля или хеша.
func turnAddressDead(errStr string) bool {
	for _, marker := range []string{
		"quota", "486", "unreachable", "timeout",
		"connection refused", "no route to host", "i/o timeout",
	} {
		if strings.Contains(errStr, marker) {
			return true
		}
	}
	return false
}

func RunSession(
	ctx context.Context,
	tp *TurnParams,
	peer *net.UDPAddr,
	d *Dispatcher,
	localPort string,
	getConfig bool,
	configCh chan<- string,
	sessionID int,
	creds *Credentials,
	deviceID, password string,
	stats *Stats,
	allocateGate <-chan time.Time,
) (bool, error) {
	configDelivered := false

	if len(creds.TurnURLs) == 0 {
		return false, fmt.Errorf("нет TURN URL в учетных данных")
	}
	// Мёртвые relay пропускаем — иначе воркер циклично долбит один и тот же
	// недоступный адрес вместо рабочих из того же пула.
	usableURLs := GlobalBlacklist.Available(creds.TurnURLs)
	selectedURL := usableURLs[sessionID%len(usableURLs)]

	urlhost, urlport, err := net.SplitHostPort(selectedURL)
	if err != nil {
		return false, fmt.Errorf("разбор TURN URL %q: %w", selectedURL, err)
	}
	if tp.Host != "" {
		urlhost = tp.Host
	}
	if tp.Port != "" {
		urlport = tp.Port
	}
	turnAddr := net.JoinHostPort(urlhost, urlport)

	turnConn, turnConnCloser, err := dialTURNConn(turnAddr, tp.TCPTransport)
	if err != nil {
		if turnAddressDead(strings.ToLower(err.Error())) {
			GlobalBlacklist.Ban(selectedURL)
		}
		return false, err
	}
	defer turnConnCloser.Close()

	if tp.TCPTransport {
		log.Printf("[СЕССИЯ #%d] TURN TCP (%s)", sessionID, turnAddr)
	} else {
		log.Printf("[СЕССИЯ #%d] TURN UDP (%s)", sessionID, turnAddr)
	}

	// RequestedAddressFamily
	var addrFamily turn.RequestedAddressFamily
	if peer.IP.To4() != nil {
		addrFamily = turn.RequestedAddressFamilyIPv4
	} else {
		addrFamily = turn.RequestedAddressFamilyIPv6
	}

	// TURN Client (pion/turn/v5)
	tc, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr:         turnAddr,
		TURNServerAddr:         turnAddr,
		Conn:                   turnConn,
		Username:               creds.User,
		Password:               creds.Pass,
		RequestedAddressFamily: addrFamily,
		LoggerFactory:          &NullLoggerFactory{},
	})
	if err != nil {
		return false, fmt.Errorf("TURN клиент: %w", err)
	}
	defer tc.Close()

	if err = tc.Listen(); err != nil {
		return false, fmt.Errorf("TURN Listen: %w", err)
	}

	if allocateGate != nil {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-allocateGate:
		}
	}

	relay, err := tc.Allocate()
	if err != nil {
		if isAuthError(err) {
			handleAuthError(creds.CacheStreamID)
		}
		errStr := err.Error()
		if turnAddressDead(strings.ToLower(errStr)) {
			GlobalBlacklist.Ban(selectedURL)
		}
		if strings.Contains(errStr, "Quota") || strings.Contains(errStr, "486") {
			return false, fmt.Errorf("TURN квота: %w", err)
		}
		return false, fmt.Errorf("TURN Allocate: %w", err)
	}
	defer relay.Close()

	// Reset error count on successful allocation
	getStreamCache(creds.CacheStreamID).errorCount.Store(0)

	log.Printf("[СЕССИЯ #%d] Relay: %s", sessionID, relay.LocalAddr())

	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()

	var pipeA, pipeB net.PacketConn
	var activeConn net.Conn

	// Keepalive goroutine (TURN binding request)
	var sessionWg sync.WaitGroup
	sessionWg.Add(1)
	go func() {
		defer sessionWg.Done()
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-t.C:
				tc.SendBindingRequest()
			}
		}
	}()

	var relayWg sync.WaitGroup
	useWrap := len(tp.WrapKey) == wrapKeyLen

	if tp.RawMode {
		if !useWrap {
			return false, fmt.Errorf("RAW-режим требует WRAP-ключ (пароль)")
		}
		direct, dirErr := newObfsDirectConn(relay, peer, tp.WrapKey, tp.ObfsMode)
		if dirErr != nil {
			return false, fmt.Errorf("RAW obfs: %w", dirErr)
		}
		activeConn = direct
		log.Printf("[ВОРКЕР #%d] [ПРЯМОЙ] Без DTLS, только RTP-obfs AEAD ✓", sessionID)
	} else {
		relayWg.Add(2)

		// Initialize obfs config per session
		var obfsCfg *ObfsConfig
		var obfsWriteState *ObfsState
		var wrapAEAD cipher.AEAD
		if useWrap {
			obfsCfg = NewObfsConfig(tp.ObfsMode)
			obfsWriteState = NewObfsState()
			var aeadErr error
			wrapAEAD, aeadErr = getAEAD(tp.WrapKey)
			if aeadErr != nil {
				return false, fmt.Errorf("obfs aead: %w", aeadErr)
			}
		}

		pipeA, pipeB = connutil.AsyncPacketPipe()
		// Закрываем на любом выходе: ниже есть ранние return (хендшейк, конфиг).
		defer func() {
			_ = pipeA.Close()
			_ = pipeB.Close()
		}()

		stopRelay := context.AfterFunc(sessCtx, func() {
			_ = relay.SetDeadline(time.Now())
			_ = pipeA.SetDeadline(time.Now())
		})
		defer stopRelay()

		// relay → pipeA (UNWRAP: strip RTP header + decrypt)
		go func() {
			defer relayWg.Done()
			defer sessCancel()
			// Max incoming: RTP header (12) + AEAD tag (16) + padding (video up to 60).
			readBufLen := readBufSize + 120
			buf := make([]byte, readBufLen)
			plain := make([]byte, readBufSize)
			for {
				n, _, readErr := relay.ReadFrom(buf)
				if readErr != nil {
					return
				}
				payload := buf[:n]
				if useWrap {
					if !obfsIsRTPPacket(payload) {
						log.Printf("[СЕССИЯ #%d] OBFS unwrap: unexpected packet (n=%d)", sessionID, n)
						continue
					}
					m, wrapErr := obfsUnwrapPacketAEAD(wrapAEAD, payload, plain)
					if wrapErr != nil {
						log.Printf("[СЕССИЯ #%d] OBFS unwrap: %v (n=%d)", sessionID, wrapErr, n)
						continue
					}
					payload = plain[:m]
				}
				if _, writeErr := pipeA.WriteTo(payload, peer); writeErr != nil {
					return
				}
			}
		}()

		// pipeA → relay (WRAP: add RTP header + encrypt, zero-alloc into txBuf)
		go func() {
			defer relayWg.Done()
			defer sessCancel()
			b := make([]byte, readBufSize)
			var txBuf []byte
			if useWrap && obfsCfg != nil {
				txBuf = make([]byte, obfsWrapWireLen(readBufSize, obfsCfg))
			}
			for {
				n, _, readErr := pipeA.ReadFrom(b)
				if readErr != nil {
					return
				}
				out := b[:n]
				if useWrap {
					if obfsCfg != nil && obfsWriteState != nil && wrapAEAD != nil {
						wn, wrapErr := obfsWrapPacketInto(txBuf, wrapAEAD, out, obfsCfg, obfsWriteState)
						if wrapErr != nil {
							log.Printf("[СЕССИЯ #%d] OBFS wrap: %v", sessionID, wrapErr)
							return
						}
						out = txBuf[:wn]
					}
				}
				if _, writeErr := relay.WriteTo(out, peer); writeErr != nil {
					return
				}
			}
		}()

		// DTLS с поддержкой Connection ID (без SNI)
		cert, err := selfsign.GenerateSelfSigned()
		if err != nil {
			return false, fmt.Errorf("генерация сертификата: %w", err)
		}

		// Acquire handshake semaphore
		select {
		case handshakeSem <- struct{}{}:
		case <-sessCtx.Done():
			return false, sessCtx.Err()
		}

		dtlsCfg := &dtls.Config{
			Certificates:          []tls.Certificate{cert},
			InsecureSkipVerify:    true,
			ExtendedMasterSecret:  dtls.RequireExtendedMasterSecret,
			CipherSuites:          []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
			ConnectionIDGenerator: dtls.OnlySendCIDGenerator(),
			// No ServerName (SNI) — less detectable by DPI
		}

		dtlsConn, err := dtls.Client(pipeB, peer, dtlsCfg)
		if err != nil {
			<-handshakeSem
			return false, fmt.Errorf("DTLS клиент: %w", err)
		}
		defer dtlsConn.Close()

		hctx, hcancel := context.WithTimeout(sessCtx, 50*time.Second)
		log.Printf("[ВОРКЕР #%d] [DTLS] Рукопожатие (Handshake)...", sessionID)
		err = dtlsConn.HandshakeContext(hctx)
		hcancel()
		<-handshakeSem // RELEASE SEMAPHORE IMMEDIATELY AFTER HANDSHAKE

		if err != nil {
			if useWrap {
				errStr := strings.ToLower(err.Error())
				if strings.Contains(errStr, "deadline") || strings.Contains(errStr, "timeout") {
					return false, fmt.Errorf("WRAP_AUTH_TIMEOUT: DTLS timeout, пароль/WRAP не подтверждён")
				}
			}
			return false, fmt.Errorf("DTLS хендшейк: %w", err)
		}
		log.Printf("[ВОРКЕР #%d] [DTLS] Соединение установлено ✓", sessionID)
		activeConn = dtlsConn
	}

	atomic.AddInt32(&stats.ActiveConnections, 1)
	defer atomic.AddInt32(&stats.ActiveConnections, -1)

	// Запрос конфига
	// Без конфига туннеля нет вообще, поэтому неудачную попытку завершаем
	// ошибкой: сессия закроется, право на запрос вернётся в пул, и следующая
	// сессия попробует снова. Раньше воркер уходил в READY, держал право
	// запроса до самой своей смерти и конфиг не запрашивал больше никто.
	if getConfig && configCh != nil && tp.RawMode {
		ip, dnsCSV, mtu, confErr := RequestRawConfig(activeConn, deviceID, password)
		switch {
		case confErr != nil:
			if strings.Contains(confErr.Error(), "FATAL_AUTH") {
				return false, confErr
			}
			return false, fmt.Errorf("RAW-конфиг не получен, сервер запущен без -listen-raw?: %w", confErr)
		case ip == "":
			return false, fmt.Errorf("сервер ещё не назначил raw IP, повторим сессию")
		default:
			conf := fmt.Sprintf("RAWCONF:%s|%s|%d", ip, dnsCSV, mtu)
			select {
			case configCh <- conf:
				configDelivered = true
				log.Printf("[ВОРКЕР #%d] RAW-конфиг получен (ip=%s)", sessionID, ip)
			default:
				configDelivered = true
			}
		}
	} else if getConfig && configCh != nil {
		conf, confErr := RequestConfig(activeConn, localPort, deviceID, password)
		switch {
		case confErr != nil:
			if strings.Contains(confErr.Error(), "FATAL_AUTH") {
				return false, confErr
			}
			return false, fmt.Errorf("WireGuard-конфиг не получен: %w", confErr)
		case conf == "":
			return false, fmt.Errorf("сервер ещё не выдал WireGuard-конфиг, повторим сессию")
		default:
			select {
			case configCh <- conf:
				configDelivered = true
				log.Printf("[ВОРКЕР #%d] Конфиг получен", sessionID)
			default:
				configDelivered = true
				log.Printf("[ВОРКЕР #%d] Конфиг уже был доставлен другим воркером", sessionID)
			}
		}
	} else if authErr := SendAuth(activeConn, deviceID, password); authErr != nil {
		log.Printf("[ВОРКЕР #%d] Ошибка авторизации: %v", sessionID, authErr)
	}

	log.Printf("[ВОРКЕР #%d] [READY] Туннель готов к работе ✓", sessionID)

	// Регистрация в диспетчере
	slot := &WorkerSlot{
		ID:     sessionID,
		SendCh: make(chan []byte, workerSendBuf),
		PrioCh: make(chan []byte, prioBuf),
	}
	d.Register(slot)
	defer d.Unregister(slot)

	// Proxy DTLS ↔ Dispatcher
	var proxyWg sync.WaitGroup
	proxyWg.Add(3) // +1 for keepalive goroutine
	sessionErrCh := make(chan error, 1)

	stopDTLS := context.AfterFunc(sessCtx, func() {
		_ = activeConn.SetDeadline(time.Now())
	})
	defer stopDTLS()

	// Keepalive: не даёт TURN allocation и каналу простаивать
	go func() {
		defer proxyWg.Done()
		t := time.NewTicker(keepaliveInterval)
		defer t.Stop()
		ping := []byte{keepaliveByte}
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-t.C:
				_ = activeConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err := activeConn.Write(ping); err != nil {
					return
				}
			}
		}
	}()

	// Writer: dispatcher → туннель. PrioCh (мелкие пакеты, в основном TCP ACK)
	// всегда обгоняет SendCh, иначе ACK ждёт весь chunk данных перед собой.
	go func() {
		defer proxyWg.Done()
		defer sessCancel()

		writePkt := func(pkt []byte) bool {
			_ = activeConn.SetWriteDeadline(time.Now().Add(sessionReadTimeout))
			_, writeErr := activeConn.Write(pkt)
			putPktBuf(pkt)
			if writeErr != nil {
				log.Printf("[ВОРКЕР #%d] Ошибка Writer: %v", sessionID, writeErr)
				select {
				case sessionErrCh <- fmt.Errorf("transport writer: %w", writeErr):
				default:
				}
				return false
			}
			return true
		}

		for {
			// Сначала осушаем приоритетную очередь.
			select {
			case pkt, ok := <-slot.PrioCh:
				if !ok {
					return
				}
				if !writePkt(pkt) {
					return
				}
				continue
			default:
			}

			select {
			case <-sessCtx.Done():
				return
			case pkt, ok := <-slot.PrioCh:
				if !ok {
					return
				}
				if !writePkt(pkt) {
					return
				}
			case pkt, ok := <-slot.SendCh:
				if !ok {
					return
				}
				if !writePkt(pkt) {
					return
				}
			}
		}
	}()

	// Reader: туннель → dispatcher
	go func() {
		defer proxyWg.Done()
		defer sessCancel()
		for {
			pkt := getPktBuf(2048)
			_ = activeConn.SetReadDeadline(time.Now().Add(sessionReadTimeout))
			n, readErr := activeConn.Read(pkt)
			if readErr != nil {
				putPktBuf(pkt)
				if sessCtx.Err() != nil {
					return
				}
				if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
					continue
				}
				log.Printf("[ВОРКЕР #%d] Ошибка Reader: %v", sessionID, readErr)
				select {
				case sessionErrCh <- fmt.Errorf("transport reader: %w", readErr):
				default:
				}
				return
			}

			// Skip keepalive pong from server
			if n == 1 && pkt[0] == keepaliveByte {
				putPktBuf(pkt)
				continue
			}

			pkt = pkt[:n]
			select {
			case d.ReturnCh <- pkt:
			case <-sessCtx.Done():
				putPktBuf(pkt)
				return
			}
		}
	}()

	proxyWg.Wait()
	sessCancel()
	relayWg.Wait()
	sessionWg.Wait()
	log.Printf("[СЕССИЯ #%d] Завершена", sessionID)
	select {
	case sessionErr := <-sessionErrCh:
		return configDelivered, sessionErr
	default:
	}
	return configDelivered, nil
}

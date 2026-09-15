package core

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
)

// Config — все параметры запуска (профиль + runtime).
type Config struct {
	PeerAddr      string   // -peer
	Password      string   // -password
	Hashes        []string // -vk (уже распарсенные)
	Listen        string   // -listen, default "127.0.0.1:9000"
	TurnHost      string   // -turn
	TurnPort      string   // -port
	DeviceID      string   // -device-id
	Workers       int      // -n
	CaptchaMode   string   // -captcha-mode
	VKAuthMode    string   // vkcalls | legacy
	ObfsMode      string   // audio | video
	MTU           int      // 0 = default 1240
	GoDNS         string   // DNS для VK API: yandex|google|cloudflare|doh-yandex|doh-google|doh-cloudflare|custom:IP|doh:URL
	TurnTransport string   // udp (default) | tcp
	RawMode       bool     // сырые IP без WireGuard/DTLS (сервер -listen-raw)
	TunIface      string   // имя TUN в RAW-режиме, по умолчанию tun-wdtt
}

// EventType — тип события от ядра.
type EventType string

const (
	EventState EventType = "state"
	EventLog   EventType = "log"
	EventEvent EventType = "event"
	EventError EventType = "error"
	EventStats EventType = "stats"
)

// Event — событие от ядра к orchestrator.
type Event struct {
	Type EventType

	// state
	Status string

	// log
	Level   string
	Message string

	// event
	Name string
	Data string

	// stats
	RxBytes int64
	TxBytes int64
	Workers int32
}

// Core — runtime controller ядра.
type Core struct {
	cfg               Config
	cancel            context.CancelFunc
	pauseFlag         int32
	CaptchaResultChan chan string
	captchaMode       atomic.Value
	vkAuthMode        atomic.Value
	events            chan Event
	once              sync.Once
	turnIPsMu         sync.Mutex
	turnIPs           []string
}

// AddTurnIPs регистрирует TURN IP-адреса (без порта) для исключения из туннеля.
func (c *Core) AddTurnIPs(urls []string) {
	c.turnIPsMu.Lock()
	defer c.turnIPsMu.Unlock()
	seen := make(map[string]struct{}, len(c.turnIPs))
	for _, ip := range c.turnIPs {
		seen[ip] = struct{}{}
	}
	for _, u := range urls {
		host, _, _ := net.SplitHostPort(strings.TrimPrefix(u, "turn:"))
		if host == "" {
			host = u
		}
		if _, ok := seen[host]; !ok {
			seen[host] = struct{}{}
			c.turnIPs = append(c.turnIPs, host)
		}
	}
}

// GetTurnIPs возвращает все зарегистрированные TURN IP.
func (c *Core) GetTurnIPs() []string {
	c.turnIPsMu.Lock()
	defer c.turnIPsMu.Unlock()
	result := make([]string, len(c.turnIPs))
	copy(result, c.turnIPs)
	return result
}

// New создаёт Core. Start() запускает его.
func New(cfg Config) *Core {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:9000"
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID = "unknown"
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 9
	}
	c := &Core{
		cfg:               cfg,
		CaptchaResultChan: make(chan string, 1),
		events:            make(chan Event, 256),
	}
	c.captchaMode.Store(normalizeCaptchaMode(cfg.CaptchaMode))
	c.vkAuthMode.Store(normalizeVKAuthMode(cfg.VKAuthMode))
	return c
}

// Start запускает ядро. Возвращает канал событий (закрывается при завершении).
func (c *Core) Start() (<-chan Event, error) {
	setupGlobalResolver(c.cfg.GoDNS)

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	if c.cfg.PeerAddr == "" {
		cancel()
		return nil, fmt.Errorf("PeerAddr is required")
	}
	if len(c.cfg.Hashes) == 0 {
		cancel()
		return nil, fmt.Errorf("Hashes are required")
	}
	if c.cfg.Password == "" {
		cancel()
		return nil, fmt.Errorf("Password is required")
	}

	peer, err := net.ResolveUDPAddr("udp", c.cfg.PeerAddr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("resolve peer: %w", err)
	}

	wrapKey, err := deriveWrapKey(c.cfg.Password)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("derive wrap key: %w", err)
	}

	// Нормализуем количество воркеров
	maxWorkers := 108
	n := c.cfg.Workers
	if n > maxWorkers {
		n = maxWorkers
	}
	if n < workersPerGroup {
		n = workersPerGroup
	}
	n = (n / workersPerGroup) * workersPerGroup

	tp := &TurnParams{
		Host:         c.cfg.TurnHost,
		Port:         c.cfg.TurnPort,
		Hashes:       c.cfg.Hashes,
		WrapKey:      wrapKey,
		ObfsMode:     normalizeObfsMode(c.cfg.ObfsMode),
		TCPTransport: strings.EqualFold(strings.TrimSpace(c.cfg.TurnTransport), "tcp"),
		RawMode:      c.cfg.RawMode,
	}
	obfsLabel := "Аудиозвонок (OPUS)"
	if tp.ObfsMode == "video" {
		obfsLabel = "Видеозвонок (H264)"
	}
	dnsLabel := strings.TrimSpace(c.cfg.GoDNS)
	if dnsLabel == "" {
		dnsLabel = "doh-yandex"
	}
	log.Printf("[Основной] Хешей=%d, Потоков=%d", len(c.cfg.Hashes), n)
	// Группа берёт хеш по кругу: хешей меньше групп — ссылка переиспользуется,
	// больше — лишние простаивают (одна группа умеет только один хеш).
	switch groups := n / workersPerGroup; {
	case len(c.cfg.Hashes) > groups:
		log.Printf("[Основной] Используются только первые %d хеша(ей): при %d хешах поставьте Потоки=%d",
			groups, len(c.cfg.Hashes), len(c.cfg.Hashes)*workersPerGroup)
	case groups > len(c.cfg.Hashes):
		log.Printf("[Основной] %d ссылка(и) на %d групп(ы): группы делят ссылку, но каждая берёт свои креды VK под отдельным device_id",
			len(c.cfg.Hashes), groups)
	}
	if c.cfg.RawMode {
		log.Printf("[СЕТЬ] Режим: VPN (raw-IP, без WireGuard/DTLS)")
	} else {
		log.Printf("[СЕТЬ] Режим: VPN (WireGuard over VK TURN/DTLS)")
	}
	log.Printf("[СЕТЬ] DNS: %s", dnsLabel)
	log.Printf("[СЕТЬ] Маскировка: %s", obfsLabel)
	log.Printf("[КЛИЕНТ] Режим VK: %s", c.getVKAuthMode())
	log.Printf("[WRAP] Ключ выведен из пароля, RTP AEAD активен")
	if tp.TCPTransport {
		log.Printf("[ЯДРО] Транспорт TURN: TCP")
	}

	var localConn net.PacketConn
	if !c.cfg.RawMode {
		localConn, err = listenUDP(c.cfg.Listen)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("listen %s: %w", c.cfg.Listen, err)
		}
		if uc, ok := localConn.(*net.UDPConn); ok {
			_ = uc.SetReadBuffer(socketBufSize)
			_ = uc.SetWriteBuffer(socketBufSize)
		}
	}

	_, localPort, _ := net.SplitHostPort(c.cfg.Listen)
	if localPort == "" {
		localPort = "9000"
	}

	numGroups := n / workersPerGroup

	stats := NewStats()
	emitCaptchaRequest := func(mode, redirectURI, sessionToken string) {
		c.emit(Event{Type: EventEvent, Name: "captcha_required", Data: mode + "|" + redirectURI + "|" + sessionToken})
	}

	shutdownCh := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(shutdownCh)
	}()
	go stats.RunLoop(shutdownCh,
		func(level, msg string) {
			c.emit(Event{Type: EventLog, Level: level, Message: msg})
		},
		func(rx, tx int64, workers int32) {
			c.emit(Event{Type: EventStats, RxBytes: rx, TxBytes: tx, Workers: workers})
		},
	)

	var disp *Dispatcher
	if c.cfg.RawMode {
		disp = NewDispatcherPendingTUN(ctx, stats)
	} else {
		disp = NewDispatcher(ctx, localConn, stats)
	}

	configCh := make(chan string, 1)

	go func() {
		select {
		case rawConf, ok := <-configCh:
			if !ok || rawConf == "" {
				return
			}
			if strings.HasPrefix(rawConf, "RAWCONF:") {
				c.attachRawTUN(disp, rawConf)
				return
			}
			finalConf := patchWGConfig(rawConf, c.cfg.MTU)
			c.emit(Event{Type: EventEvent, Name: "wg_config", Data: finalConf})
		case <-ctx.Done():
		}
	}()

	c.emit(Event{Type: EventState, Status: "connecting"})

	go func() {
		defer close(c.events)
		defer disp.Shutdown()
		defer cancel()
		defer func() {
			if localConn != nil {
				_ = localConn.Close()
			}
		}()

		var wg sync.WaitGroup
		workerIDCounter := 1
		var prevWaitReady <-chan struct{}
		// Общие на все группы: конфиг у сервера просит любая живая группа,
		// но ровно один воркер за раз и ровно один раз всего.
		var configSent, configInFlight int32

		for g := 0; g < numGroups; g++ {
			var myWaitReady <-chan struct{}
			var mySignalReady chan<- struct{}

			if g > 0 {
				myWaitReady = prevWaitReady
			}
			if g < numGroups-1 {
				ch := make(chan struct{})
				mySignalReady = ch
				prevWaitReady = ch
			}

			ids := make([]int, workersPerGroup)
			for i := range ids {
				ids[i] = workerIDCounter
				workerIDCounter++
			}

			gID := g + 1

			wg.Add(1)
			go func(groupID int, workerIds []int, startHashIndex int, waitR <-chan struct{}, sigR chan<- struct{}) {
				defer wg.Done()
				WorkerGroup(ctx, groupID, startHashIndex, tp, peer, disp, localPort,
					configCh, &configSent, &configInFlight, workerIds, &c.pauseFlag,
					c.cfg.DeviceID, c.cfg.Password, stats, waitR, sigR,
					c.CaptchaResultChan, c.getCaptchaMode, c.getVKAuthMode, emitCaptchaRequest, c.AddTurnIPs)
			}(gID, ids, g, myWaitReady, mySignalReady)
		}

		wg.Wait()
		close(configCh)
		log.Println("[CORE] все воркеры завершены")
		if ctx.Err() == nil && atomic.LoadInt32(&stats.ActiveConnections) == 0 {
			msg := "Ни один поток не поднялся: все VK-хеши мертвы или недоступны. Создайте новый звонок VK и замените ссылку."
			log.Printf("[ЯДРО] Ошибка: %s", msg)
			c.emit(Event{Type: EventError, Message: msg})
			c.emit(Event{Type: EventState, Status: "error"})
		}
	}()

	return c.events, nil
}

// Stop останавливает ядро.
func (c *Core) Stop() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
}

// Pause приостанавливает воркеры.
func (c *Core) Pause() { atomic.StoreInt32(&c.pauseFlag, 1) }

// Resume возобновляет воркеры.
func (c *Core) Resume() { atomic.StoreInt32(&c.pauseFlag, 0) }

// SolveCaptcha передаёт токен капчи в ядро.
func (c *Core) SolveCaptcha(token string) {
	// Дренируем устаревший результат
	select {
	case <-c.CaptchaResultChan:
	default:
	}
	c.CaptchaResultChan <- token
}

func (c *Core) emit(ev Event) {
	select {
	case c.events <- ev:
	default:
		if ev.Type != EventLog {
			// Drain one stale entry to make room for important event
			select {
			case <-c.events:
			default:
			}
			select {
			case c.events <- ev:
			default:
			}
		}
	}
}

func (c *Core) getCaptchaMode() string {
	mode, _ := c.captchaMode.Load().(string)
	if mode == "" {
		return "auto"
	}
	return mode
}

func (c *Core) getVKAuthMode() string {
	mode, _ := c.vkAuthMode.Load().(string)
	if mode == "" {
		return "vkcalls"
	}
	return mode
}

func normalizeCaptchaMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "rjs", "wv":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "auto"
	}
}

func normalizeVKAuthMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "legacy":
		return "legacy"
	default:
		return "vkcalls"
	}
}

func normalizeObfsMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "video":
		return "video"
	default:
		return "audio"
	}
}

func (c *Core) attachRawTUN(disp *Dispatcher, rawConf string) {
	ip, dnsCSV, mtu, err := ParseRawConf(rawConf)
	if err != nil {
		log.Printf("[RAW] Некорректный RAWCONF: %v", err)
		c.emit(Event{Type: EventError, Message: err.Error()})
		return
	}
	if ip == "" {
		log.Printf("[RAW] Сервер ещё не назначил IP")
		return
	}
	iface := strings.TrimSpace(c.cfg.TunIface)
	if iface == "" {
		iface = "tun-wdtt"
	}
	if mtu <= 0 {
		mtu = c.cfg.MTU
	}
	if mtu <= 0 {
		mtu = 1240
	}
	f, err := openRawTUN(iface)
	if err != nil {
		log.Printf("[RAW] TUN: %v", err)
		c.emit(Event{Type: EventError, Message: err.Error()})
		return
	}
	if err := configureTUN(iface, ip, mtu); err != nil {
		_ = f.Close()
		log.Printf("[RAW] configure TUN: %v", err)
		c.emit(Event{Type: EventError, Message: err.Error()})
		return
	}
	disp.AttachTUN(f)
	log.Printf("[RAW] TUN %s подключён (ip=%s mtu=%d)", iface, ip, mtu)
	c.emit(Event{
		Type: EventEvent,
		Name: "raw_config",
		Data: fmt.Sprintf("%s|%s|%d|%s", ip, dnsCSV, mtu, iface),
	})
}

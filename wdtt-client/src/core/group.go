package core

import (
	"context"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const workersPerGroup = 9

// WorkersPerGroup — количество воркеров в одной группе (экспортировано для orchestrator).
const WorkersPerGroup = workersPerGroup

// allocateGateInterval — пауза между TURN Allocate внутри группы.
// qWDTT 1.4.3: 100 мс, чтобы не ловить 486 quota при одновременном старте.
const allocateGateInterval = 100 * time.Millisecond

// WorkerGroup:
// Запускает 9 потоков с одними кредами. Ротации нет — работает до смерти воркеров.
func WorkerGroup(
	ctx context.Context,
	groupID int,
	hashIndex int,
	tp *TurnParams,
	peer *net.UDPAddr,
	d *Dispatcher,
	localPort string,
	configCh chan<- string,
	configSent *int32,
	configInFlight *int32,
	workerIDs []int,
	pauseFlag *int32,
	deviceID, password string,
	stats *Stats,
	waitReady <-chan struct{},
	signalReady chan<- struct{},
	captchaResultChan chan string,
	getCaptchaMode func() string,
	getVKAuthMode func() string,
	emitCaptchaRequest func(mode, redirectURI, sessionToken string),
	onTurnURLs func(urls []string),
) {
	// Эстафету следующей группе обязаны передать на любом выходе, включая
	// мёртвый хеш и FATAL_AUTH: иначе группа со своим (возможно живым) хешем
	// навсегда останется в "Ожидание сигнала от предыдущей группы".
	var batonOnce sync.Once
	releaseBaton := func(msg string) {
		if signalReady == nil {
			return
		}
		batonOnce.Do(func() {
			close(signalReady)
			if msg != "" {
				log.Printf("[ГРУППА #%d] %s", groupID, msg)
			}
		})
	}
	defer releaseBaton("")

	// Каскадный запуск: ждем свою очередь
	if waitReady != nil {
		log.Printf("[ГРУППА #%d] Ожидание сигнала от предыдущей группы...", groupID)
		select {
		case <-waitReady:
		case <-ctx.Done():
			return
		}
	}

	// Право запросить конфиг у сервера общее для всех групп: раньше его имела
	// только группа #1, и её мёртвый хеш означал, что конфиг не придёт никогда —
	// остальные группы поднимали DTLS, но интерфейса не существовало.
	getConfig := configCh != nil && configSent != nil && configInFlight != nil

	// Doze-mode пауза
	for atomic.LoadInt32(pauseFlag) != 0 {
		if ctx.Err() != nil {
			return
		}
		time.Sleep(1 * time.Second)
	}

	hash := tp.Hashes[hashIndex%len(tp.Hashes)]
	shortHash := hash
	if len(shortHash) > 8 {
		shortHash = shortHash[:8]
	}
	log.Printf("[ГРУППА #%d] Запрос кредов (хеш: %s...)", groupID, shortHash)

	credStreamID := groupID * 100
	var creds *Credentials
	for {
		if ctx.Err() != nil {
			return
		}
		credsCtx, credsCancel := context.WithTimeout(context.Background(), 120*time.Second)
		go func() {
			select {
			case <-ctx.Done():
				credsCancel()
			case <-credsCtx.Done():
			}
		}()
		user, pass, turnURLs, err := GetCreds(credsCtx, hash, credStreamID, captchaResultChan, getCaptchaMode, getVKAuthMode, emitCaptchaRequest)
		credsCancel()
		if err == nil {
			creds = &Credentials{User: user, Pass: pass, TurnURLs: turnURLs, CacheStreamID: credStreamID}
			break
		}
		log.Printf("[ГРУППА #%d] Ошибка кредов: %v", groupID, err)
		if callErr, ok := asCallUnavailableError(err); ok {
			log.Printf("[ГРУППА #%d] Хеш %s... мёртв (%v) — замените ссылку звонка VK в LuCI", groupID, shortHash, callErr)
			releaseBaton("Передаём эстафету следующей группе (её хеш может быть живым)")
			return
		}
		if strings.Contains(err.Error(), "FATAL_AUTH") || strings.Contains(err.Error(), "context canceled") {
			return
		}
		wait := 15 * time.Second
		if strings.Contains(err.Error(), "CAPTCHA_WAIT_REQUIRED") {
			wait = 65 * time.Second
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}

	log.Printf("[ГРУППА #%d] Креды OK, TURN: %v, %d воркеров", groupID, creds.TurnURLs, len(workerIDs))

	if onTurnURLs != nil {
		onTurnURLs(creds.TurnURLs)
	}

	var wg sync.WaitGroup
	var credsMu sync.RWMutex
	var refreshMu sync.Mutex
	var lastCredRefresh atomic.Int64

	refreshCreds := func(reason string) bool {
		refreshMu.Lock()
		defer refreshMu.Unlock()

		now := time.Now().Unix()
		last := lastCredRefresh.Load()
		if last > 0 && now-last < 15 {
			log.Printf("[TURN] Креды уже обновлялись %d сек назад, ждём следующий retry (%s)", now-last, reason)
			return true
		}

		getStreamCache(credStreamID).invalidate(credStreamID)
		refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer refreshCancel()
		u, p, urls, refreshErr := GetCreds(refreshCtx, hash, credStreamID, captchaResultChan, getCaptchaMode, getVKAuthMode, emitCaptchaRequest)
		if refreshErr != nil {
			if callErr, ok := asCallUnavailableError(refreshErr); ok {
				log.Printf("[TURN] Звонок недоступен при обновлении кредов: %v", callErr)
			} else {
				log.Printf("[TURN] Не удалось обновить креды после %s: %v", reason, refreshErr)
			}
			return false
		}

		credsMu.Lock()
		creds = &Credentials{User: u, Pass: p, TurnURLs: urls, CacheStreamID: credStreamID}
		credsMu.Unlock()
		lastCredRefresh.Store(time.Now().Unix())
		log.Printf("[TURN] Креды обновлены после %s, TURN urls=%d", reason, len(urls))
		return true
	}

	// Сигнализируем следующей группе (креды получены + короткая фора).
	if signalReady != nil {
		go func() {
			delayMs := 500 + rand.Intn(250)
			select {
			case <-time.After(time.Duration(delayMs) * time.Millisecond):
				if ctx.Err() == nil {
					releaseBaton("Успешный старт! Передача эстафеты следующей группе...")
				}
			case <-ctx.Done():
			}
		}()
	}

	allocateTicker := time.NewTicker(allocateGateInterval)
	defer allocateTicker.Stop()

	for i, wid := range workerIDs {
		wg.Add(1)

		// Stagger: 75 мс между воркерами (qWDTT 1.4.3)
		workerDelay := time.Duration(i) * 75 * time.Millisecond

		go func(wid int, delay time.Duration) {
			defer wg.Done()

			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}

			shouldGetConfig := getConfig
			attempt := 0

			for {
				if ctx.Err() != nil {
					return
				}

				getConf := false
				if shouldGetConfig && atomic.LoadInt32(configSent) == 0 {
					getConf = atomic.CompareAndSwapInt32(configInFlight, 0, 1)
				}
				var cc chan<- string
				if getConf {
					cc = configCh
				}

				credsMu.RLock()
				credsSnapshot := *creds
				credsSnapshot.TurnURLs = cloneStringSlice(creds.TurnURLs)
				credsMu.RUnlock()

				configDelivered, sessErr := RunSession(ctx, tp, peer, d, localPort,
					getConf, cc, wid, &credsSnapshot, deviceID, password, stats, allocateTicker.C)

				if getConf {
					if configDelivered {
						atomic.StoreInt32(configSent, 1)
					} else {
						atomic.StoreInt32(configInFlight, 0)
					}
				}

				if sessErr == nil {
					continue
				}

				fastRetry := false
				quotaRetry := false
				if sessErr != nil {
					if ctx.Err() != nil {
						return
					}
					errStr := sessErr.Error()
					errStrLower := strings.ToLower(errStr)
					fastRetry = strings.Contains(errStrLower, "broken pipe") ||
						strings.Contains(errStrLower, "connection reset by peer") ||
						strings.Contains(errStrLower, "unexpected eof") ||
						errStrLower == "eof" || strings.HasSuffix(errStrLower, ": eof")
					quotaRetry = strings.Contains(errStrLower, "turn квота") ||
						strings.Contains(errStrLower, "quota") ||
						strings.Contains(errStrLower, "486")

					turnAllocAttrMissing := strings.Contains(errStrLower, "turn allocate") &&
						strings.Contains(errStrLower, "attribute not found")
					turnCredRefreshNeeded := turnAllocAttrMissing ||
						strings.Contains(errStrLower, "turn allocate auth") ||
						strings.Contains(errStrLower, "invalid credential") ||
						strings.Contains(errStrLower, "stale nonce") ||
						strings.Contains(errStrLower, "allocation mismatch") ||
						strings.Contains(errStrLower, "error 508") ||
						strings.Contains(errStrLower, "turn квота") ||
						strings.Contains(errStrLower, "quota")

					if strings.Contains(errStrLower, "rate limit") ||
						strings.Contains(errStrLower, "flood control") ||
						strings.Contains(errStrLower, "ip mismatch") ||
						strings.Contains(errStrLower, "error 29") {
						errStr += " (ошибка со стороны ВК)"
					}

					if strings.Contains(errStr, "хеш мёртв") ||
						strings.Contains(errStr, "FATAL_AUTH") {
						log.Printf("[ВОРКЕР #%d] Фатальная ошибка: %s", wid, errStr)
						return
					}

					attempt++
					if turnAllocAttrMissing {
						log.Printf("[ВОРКЕР #%d] [TURN] Allocate вернул неполный ответ, обновляем TURN-креды и повторяем (попытка %d): %s", wid, attempt, errStr)
						refreshCreds("TURN Allocate attribute-not-found")
					} else if turnCredRefreshNeeded {
						log.Printf("[ВОРКЕР #%d] [TURN] Ошибка allocation/кредов, обновляем TURN-креды и повторяем (попытка %d): %s", wid, attempt, errStr)
						refreshCreds("TURN allocation error")
					} else {
						log.Printf("[ВОРКЕР #%d] Ошибка (попытка %d): %s", wid, attempt, errStr)
					}

					// Если ошибка STUN (credentials invalid), воркер не сможет переподключиться. Завершаем.
					isStunDeath := strings.Contains(errStrLower, "error 29") ||
						strings.Contains(errStrLower, "cannot create socket")

					if isStunDeath {
						log.Printf("[ВОРКЕР #%d] Невосстановимая TURN/STUN ошибка, завершение: %s", wid, errStr)
						return
					}
				}

				if ctx.Err() != nil {
					return
				}

				retryDelay := time.Duration(min(2<<uint(attempt-1), 30)) * time.Second
				retryDelay += time.Duration(rand.Intn(3)) * time.Second
				if quotaRetry {
					retryDelay = time.Duration(30+rand.Intn(31)) * time.Second
				} else if fastRetry {
					retryDelay = time.Duration(1+rand.Intn(3)) * time.Second
				}
				select {
				case <-time.After(retryDelay):
				case <-ctx.Done():
					return
				}
			}
		}(wid, workerDelay)
	}

	wg.Wait()
	log.Printf("[ГРУППА #%d] Все воркеры группы завершились.", groupID)
}

// ParseHashes — парсит строку хешей
func ParseHashes(raw string) []string {
	var result []string
	seen := make(map[string]struct{})
	for _, h := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		h = normalizeVKJoinHash(h)
		if h != "" {
			if _, exists := seen[h]; exists {
				continue
			}
			seen[h] = struct{}{}
			result = append(result, h)
		}
	}
	return result
}

func normalizeVKJoinHash(input string) string {
	s := strings.Trim(strings.TrimSpace(input), "<>\"'")
	if s == "" {
		return ""
	}

	lower := strings.ToLower(s)
	if idx := strings.Index(lower, "/call/join/"); idx >= 0 {
		s = s[idx+len("/call/join/"):]
	} else if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return ""
	}

	if idx := strings.IndexAny(s, "?#/"); idx != -1 {
		s = s[:idx]
	}
	return strings.Trim(strings.TrimSpace(s), "/")
}

// TurnParams — конфигурация TURN
type TurnParams struct {
	Host         string
	Port         string
	Hashes       []string
	WrapKey      []byte // Password-derived WRAP key (32 bytes), nil = disabled
	ObfsMode     string // audio (default) | video
	TCPTransport bool   // true = TURN поверх TCP (когда UDP душат)
	RawMode      bool   // сырые IP без WireGuard/DTLS (сервер -listen-raw)
}

// Credentials — учетные данные TURN
type Credentials struct {
	User          string
	Pass          string
	TurnURLs      []string
	CacheStreamID int
}

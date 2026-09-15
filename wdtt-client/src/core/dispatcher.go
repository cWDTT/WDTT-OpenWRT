package core

import (
	"context"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var pktPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 2048)
	},
}

func getPktBuf(size int) []byte {
	b := pktPool.Get().([]byte)
	if cap(b) < size {
		b = make([]byte, size)
	}
	return b[:size]
}

func putPktBuf(b []byte) {
	if cap(b) < 2048 {
		return
	}
	pktPool.Put(b[:cap(b)])
}

const (
	// returnChBuf — глубина очереди пакетов, готовых к записи в локальный
	// WireGuard. При RTT ~50-60мс и 70-80 Мбит/с BDP ≈ 440-600КБ; при MTU~1300
	// это ~340-460 пакетов, поэтому 384 было впритык к потолку.
	returnChBuf = 512

	// maxDwellMS — сколько максимум миллисекунд подряд пакеты идут через один
	// worker, даже если chunk по счётчику ещё не закончился. Страховка на
	// случай, если конкретный relay начал тормозить.
	maxDwellMS = 15

	// prioThreshold — пакеты до этого размера (в первую очередь TCP ACK) идут
	// через отдельный приоритетный канал воркера, минуя chunk-очередь. Иначе
	// ACK застревает за большим chunk'ом данных на медленном relay и рост
	// TCP-окна тормозится.
	prioThreshold = 128

	// prioBuf — глубина приоритетного канала воркера.
	prioBuf = 32

	// idlePauseMS — пауза в трафике, после которой chunk-affinity уже не даёт
	// выгоды и можно начинать новый chunk на следующем воркере.
	idlePauseMS = 10
)

// chunkSizeFor — сколько подряд пакетов такого размера отправлять в один
// worker, прежде чем переключиться на следующий.
//
// Зачем chunk, а не round-robin по одному пакету: при round-robin каждый пакет
// летит через разный TURN relay с разным latency, что даёт reorder на другой
// стороне. TCP внутри туннеля читает reorder как потери → cwnd collapse →
// скорость single-flow падает до считанных KB/s.
//
// Почему размер зависит от размера пакета: крупные пакеты (объёмные данные)
// выгодно группировать покрупнее — меньше переключений relay на мегабайт
// трафика. Мелкие (ACK, keepalive) — переключать быстро или вовсе уводить в
// приоритетный канал (см. prioThreshold), чтобы не копить задержку на
// управляющем трафике.
func chunkSizeFor(pktSize int) int {
	switch {
	case pktSize > 1100:
		return 64
	case pktSize >= 701:
		return 24
	case pktSize >= 301:
		return 8
	case pktSize >= 101:
		return 3
	default:
		return 1
	}
}

type WorkerSlot struct {
	ID     int
	SendCh chan []byte
	PrioCh chan []byte
}

type Dispatcher struct {
	localConn    net.PacketConn
	tunFile      *os.File
	ready        chan struct{}
	clientAddr   atomic.Pointer[net.Addr]
	mu           sync.Mutex
	workers      []*WorkerSlot
	rrIndex      int
	rrCount      int   // сколько пакетов отправлено в текущий worker в рамках chunk'а
	lastPktTime  int64 // unix millis последнего пакета — сброс chunk'а после паузы
	chunkStartTs int64 // unix millis начала текущего chunk'а — для maxDwellMS
	ReturnCh     chan []byte
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	stats        *Stats
}

func NewDispatcher(ctx context.Context, localConn net.PacketConn, stats *Stats) *Dispatcher {
	dctx, dcancel := context.WithCancel(ctx)
	ready := make(chan struct{})
	close(ready)
	d := &Dispatcher{
		localConn: localConn,
		ready:     ready,
		ReturnCh:  make(chan []byte, returnChBuf),
		ctx:       dctx,
		cancel:    dcancel,
		stats:     stats,
	}

	d.wg.Add(2)
	go d.readLoop()
	go d.writeLoop()
	return d
}

// NewDispatcherPendingTUN ждёт AttachTUN — воркеры стартуют до RAWCONF.
func NewDispatcherPendingTUN(ctx context.Context, stats *Stats) *Dispatcher {
	dctx, dcancel := context.WithCancel(ctx)
	d := &Dispatcher{
		ready:    make(chan struct{}),
		ReturnCh: make(chan []byte, returnChBuf),
		ctx:      dctx,
		cancel:   dcancel,
		stats:    stats,
	}
	d.wg.Add(2)
	go d.readLoop()
	go d.writeLoop()
	return d
}

func (d *Dispatcher) AttachTUN(f *os.File) {
	d.tunFile = f
	select {
	case <-d.ready:
	default:
		close(d.ready)
	}
	log.Printf("[ДИСП] TUN подключён")
}

func (d *Dispatcher) Shutdown() {
	d.cancel()
	d.wg.Wait()
	if d.tunFile != nil {
		_ = d.tunFile.Close()
	}
}

func (d *Dispatcher) Register(w *WorkerSlot) {
	d.mu.Lock()
	d.workers = append(d.workers, w)
	count := len(d.workers)
	d.mu.Unlock()
	log.Printf("[ДИСП] Воркер #%d зарегистрирован (всего: %d)", w.ID, count)
}

func (d *Dispatcher) Unregister(slot *WorkerSlot) {
	d.mu.Lock()
	for i, w := range d.workers {
		if w == slot {
			d.workers = append(d.workers[:i], d.workers[i+1:]...)
			break
		}
	}
	remaining := len(d.workers)
	// Подстраховка: если текущий rrIndex вылез за границу после удаления
	if d.rrIndex >= remaining && remaining > 0 {
		d.rrIndex = d.rrIndex % remaining
	}
	d.rrCount = 0
	d.chunkStartTs = 0
	d.mu.Unlock()
	log.Printf("[ДИСП] Воркер #%d отключён (осталось: %d)", slot.ID, remaining)
}

// readLoop читает WireGuard-пакеты и распределяет по workers адаптивными
// chunk'ами.
//
// Логика: отправляем chunkSizeFor(размер) подряд пакетов в один worker, потом
// переходим к следующему. Если текущий worker перегружен (канал полный) —
// немедленно ищем свободный и начинаем новый chunk на нём. Мелкие пакеты
// (вероятно ACK, см. prioThreshold) уходят через отдельный приоритетный канал,
// минуя очередь данных. maxDwellMS — предохранитель: если текущий relay начал
// тормозить, не ждём весь chunk целиком. Это даёт:
//   - В рамках chunk пакеты идут через один TURN relay → in-order delivery
//   - Между chunks — разные relay → максимальная агрегатная скорость
//   - ACK не застревают за большими chunk'ами данных на медленном relay
//   - Нет блокировки, нет буферизации сверх необходимого
func (d *Dispatcher) readLoop() {
	defer d.wg.Done()

	select {
	case <-d.ctx.Done():
		return
	case <-d.ready:
	}

	buf := make([]byte, readBufSize)
	for {
		if err := d.ctx.Err(); err != nil {
			return
		}

		var n int
		var addr net.Addr
		var err error
		if d.tunFile != nil {
			n, err = d.tunFile.Read(buf)
		} else {
			n, addr, err = d.localConn.ReadFrom(buf)
		}
		if err != nil {
			if d.ctx.Err() != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}

		if d.tunFile == nil {
			d.clientAddr.Store(&addr)
		}
		atomic.AddInt64(&d.stats.TotalBytesUp, int64(n))

		pkt := getPktBuf(n)
		copy(pkt, buf[:n])
		pktSize := n

		d.mu.Lock()
		nw := len(d.workers)
		if nw == 0 {
			d.mu.Unlock()
			putPktBuf(pkt)
			continue
		}

		now := time.Now().UnixMilli()
		lastTime := d.lastPktTime
		d.lastPktTime = now
		if lastTime > 0 && now-lastTime > idlePauseMS {
			// Была пауза — предыдущий chunk уже не даёт выгоды от affinity,
			// начинаем новый со следующего воркера.
			d.rrIndex = (d.rrIndex + 1) % nw
			d.rrCount = 0
			d.chunkStartTs = now
		}

		// Мелкие пакеты (вероятно ACK) — приоритетный канал с фолбэком на
		// другие воркеры, чтобы не застревать за большим chunk'ом данных.
		if pktSize <= prioThreshold {
			idx := d.rrIndex % nw
			sentPrio := false
			select {
			case d.workers[idx].PrioCh <- pkt:
				sentPrio = true
			default:
				for i := 1; i < nw; i++ {
					alt := (idx + i) % nw
					select {
					case d.workers[alt].PrioCh <- pkt:
						sentPrio = true
					default:
					}
					if sentPrio {
						break
					}
				}
			}
			if sentPrio {
				d.mu.Unlock()
				continue
			}
			// Все приоритетные каналы заняты — падаем в обычную очередь ниже.
		}

		chunk := chunkSizeFor(pktSize)

		if d.chunkStartTs == 0 {
			d.chunkStartTs = now
		} else if now-d.chunkStartTs >= maxDwellMS {
			// Текущий relay слишком долго держит chunk — переключаемся, не
			// дожидаясь конца chunk'а по счётчику.
			d.rrIndex = (d.rrIndex + 1) % nw
			d.rrCount = 0
			d.chunkStartTs = now
		}

		sent := false
		idx := d.rrIndex % nw

		// Пробуем текущий worker (chunk affinity)
		w := d.workers[idx]
		select {
		case w.SendCh <- pkt:
			sent = true
			d.rrCount++
			if d.rrCount >= chunk {
				d.rrIndex = (idx + 1) % nw
				d.rrCount = 0
				d.chunkStartTs = now
			}
		default:
			// Текущий worker перегружен — ищем свободный, начинаем новый chunk
			for i := 1; i < nw; i++ {
				altIdx := (idx + i) % nw
				select {
				case d.workers[altIdx].SendCh <- pkt:
					sent = true
					d.rrIndex = altIdx
					d.rrCount = 1 // первый пакет нового chunk'а уже отправлен
					d.chunkStartTs = now
				default:
				}
				if sent {
					break
				}
			}
		}

		if !sent {
			// Все workers перегружены — сдвигаем указатель, пакет дропается
			d.rrIndex = (idx + 1) % nw
			d.rrCount = 0
			putPktBuf(pkt)
		}
		d.mu.Unlock()
	}
}

func (d *Dispatcher) writeLoop() {
	defer d.wg.Done()

	select {
	case <-d.ctx.Done():
		return
	case <-d.ready:
	}

	for {
		select {
		case <-d.ctx.Done():
			return
		case pkt := <-d.ReturnCh:
			if d.tunFile != nil {
				if _, err := d.tunFile.Write(pkt); err != nil && d.ctx.Err() != nil {
					putPktBuf(pkt)
					return
				}
				atomic.AddInt64(&d.stats.TotalBytesDown, int64(len(pkt)))
				putPktBuf(pkt)
				continue
			}
			addrPtr := d.clientAddr.Load()
			if addrPtr == nil {
				putPktBuf(pkt)
				continue
			}
			addr := *addrPtr
			if _, err := d.localConn.WriteTo(pkt, addr); err != nil {
				if d.ctx.Err() != nil {
					putPktBuf(pkt)
					return
				}
			}
			atomic.AddInt64(&d.stats.TotalBytesDown, int64(len(pkt)))
			putPktBuf(pkt)
		}
	}
}

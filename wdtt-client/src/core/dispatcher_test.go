package core

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type fakeConn struct {
	pkts   chan []byte
	closed chan struct{}
	wrote  atomic.Int64
}

func (f *fakeConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case pkt, ok := <-f.pkts:
		if !ok {
			return 0, nil, net.ErrClosed
		}
		n := copy(p, pkt)
		return n, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}, nil
	case <-f.closed:
		return 0, nil, net.ErrClosed
	}
}
func (f *fakeConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	f.wrote.Add(int64(len(p)))
	return len(p), nil
}
func (f *fakeConn) Close() error                       { close(f.closed); return nil }
func (f *fakeConn) LocalAddr() net.Addr                { return &net.UDPAddr{} }
func (f *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

func TestChunkSizeFor(t *testing.T) {
	cases := map[int]int{40: 1, 120: 3, 500: 8, 900: 24, 1300: 64}
	for size, want := range cases {
		if got := chunkSizeFor(size); got != want {
			t.Errorf("chunkSizeFor(%d) = %d, want %d", size, got, want)
		}
	}
}

func TestDispatcherChunkAffinityAndPriority(t *testing.T) {
	fc := &fakeConn{pkts: make(chan []byte, 512), closed: make(chan struct{})}
	d := NewDispatcher(context.Background(), fc, &Stats{})
	defer func() {
		_ = fc.Close()
		d.Shutdown()
	}()

	const nw = 3
	slots := make([]*WorkerSlot, nw)
	for i := range slots {
		slots[i] = &WorkerSlot{ID: i, SendCh: make(chan []byte, 256), PrioCh: make(chan []byte, prioBuf)}
		d.Register(slots[i])
	}

	// 64 крупных пакета должны уйти в один worker (chunkSizeFor(1300) == 64).
	big := make([]byte, 1300)
	for i := 0; i < 64; i++ {
		fc.pkts <- big
	}
	// Мелкий пакет обязан оказаться в приоритетном канале.
	fc.pkts <- make([]byte, 40)

	deadline := time.Now().Add(3 * time.Second)
	total := 0
	for time.Now().Before(deadline) {
		total = 0
		for _, s := range slots {
			total += len(s.SendCh) + len(s.PrioCh)
		}
		if total >= 65 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if total < 65 {
		t.Fatalf("доставлено %d из 65 пакетов", total)
	}

	prio := 0
	for _, s := range slots {
		prio += len(s.PrioCh)
	}
	if prio != 1 {
		t.Errorf("в приоритетных каналах %d пакетов, ожидался 1", prio)
	}

	busiest := 0
	for _, s := range slots {
		if len(s.SendCh) > busiest {
			busiest = len(s.SendCh)
		}
	}
	if busiest < 32 {
		t.Errorf("chunk affinity не работает: максимум %d пакетов в одном воркере из 64", busiest)
	}
}

func TestBlacklistBanAndExpiry(t *testing.T) {
	b := &TurnBlacklist{banned: make(map[string]time.Time)}
	urls := []string{"a:1", "b:2", "c:3"}

	b.Ban("b:2")
	got := b.Available(urls)
	if len(got) != 2 || got[0] != "a:1" || got[1] != "c:3" {
		t.Errorf("Available = %v, want [a:1 c:3]", got)
	}

	// Забанены все → возвращаем исходный список, чтобы не остаться без relay.
	b.Ban("a:1")
	b.Ban("c:3")
	if len(b.Available(urls)) != 3 {
		t.Errorf("при полном бане ожидался исходный список, получено %v", b.Available(urls))
	}

	// Истёкший бан снимается.
	b.banned["b:2"] = time.Now().Add(-time.Second)
	if b.IsBanned("b:2") {
		t.Error("истёкший бан должен сниматься")
	}
}

package core

import (
	"log"
	"sync"
	"time"
)

// turnBanDuration — на сколько адрес TURN-relay считается мёртвым после
// unreachable/quota/timeout. VK выдаёт пул relay, часть которых с конкретной
// сети недоступна; без бана воркеры бесконечно долбят один и тот же адрес.
const turnBanDuration = 5 * time.Minute

// TurnBlacklist — временный бан TURN-адресов.
type TurnBlacklist struct {
	mu     sync.Mutex
	banned map[string]time.Time // адрес → время окончания бана
}

// GlobalBlacklist — общий на процесс список мёртвых TURN-адресов.
var GlobalBlacklist = &TurnBlacklist{banned: make(map[string]time.Time)}

// Ban помечает адрес мёртвым на turnBanDuration.
func (b *TurnBlacklist) Ban(addr string) {
	if addr == "" {
		return
	}
	b.mu.Lock()
	_, already := b.banned[addr]
	b.banned[addr] = time.Now().Add(turnBanDuration)
	b.mu.Unlock()
	if !already {
		log.Printf("[TURN] Адрес %s забанен на %v", addr, turnBanDuration)
	}
}

// IsBanned сообщает, забанен ли адрес; истёкшие записи удаляет.
func (b *TurnBlacklist) IsBanned(addr string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.banned[addr]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(b.banned, addr)
		return false
	}
	return true
}

// Available возвращает адреса без бана. Если забанены все — отдаёт исходный
// список: лучше пробовать хоть что-то, чем остаться совсем без relay.
func (b *TurnBlacklist) Available(urls []string) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if !b.IsBanned(u) {
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return urls
	}
	return out
}

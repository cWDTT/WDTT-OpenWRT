package core

import (
	"context"
	"testing"
	"time"
)

// Группа обязана отдать эстафету на любом выходе. Иначе следующая группа со
// своим (живым) хешем навсегда висит в "Ожидание сигнала от предыдущей группы".
func TestWorkerGroupReleasesBatonOnEarlyExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	waitReady := make(chan struct{})   // предыдущая группа эстафету не передала
	signalReady := make(chan struct{}) // нашу эстафету ждёт следующая группа

	var pauseFlag int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		WorkerGroup(ctx, 1, 0, &TurnParams{Hashes: []string{"deadhash"}}, nil, nil, "9000",
			nil, nil, nil, []int{1}, &pauseFlag, "dev", "pass", NewStats(),
			waitReady, signalReady, nil, nil, nil, nil, nil)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("WorkerGroup не завершилась после отмены контекста")
	}

	select {
	case <-signalReady:
	default:
		t.Fatal("эстафета не передана: следующая группа осталась бы в ожидании навсегда")
	}
}

func TestNormalizeVKJoinHash(t *testing.T) {
	cases := map[string]string{
		"https://vk.com/call/join/AbC123-xyz": "AbC123-xyz",
		"vk.com/call/join/AbC123?from=share":  "AbC123",
		"  AbC123  ":                          "AbC123",
		"https://vk.com/im?sel=123":           "",
		"https://vk.me/join/AbC123":           "",
		"":                                    "",
	}
	for in, want := range cases {
		if got := normalizeVKJoinHash(in); got != want {
			t.Errorf("normalizeVKJoinHash(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

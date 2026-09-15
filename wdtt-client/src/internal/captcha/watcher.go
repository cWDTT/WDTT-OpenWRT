package captcha

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const TokenFile = "captcha.token"

// Watch читает токены капчи из файла, который создаёт LuCI или CLI.
func Watch(ctx context.Context, dir string, onToken func(string)) {
	if dir == "" {
		dir = "/var/run/wdtt"
	}
	path := filepath.Join(dir, TokenFile)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data, err := os.ReadFile(path)
			if err != nil || len(data) == 0 {
				continue
			}
			token := strings.TrimSpace(string(data))
			_ = os.Remove(path)
			if token != "" {
				onToken(token)
			}
		}
	}
}

// Submit записывает токен для работающего демона.
func Submit(dir, token string) error {
	if dir == "" {
		dir = "/var/run/wdtt"
	}
	_ = os.MkdirAll(dir, 0o755)
	token = strings.TrimSpace(token)
	if token == "" {
		return os.ErrInvalid
	}
	return os.WriteFile(filepath.Join(dir, TokenFile), []byte(token), 0o600)
}

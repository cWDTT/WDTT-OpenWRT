package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultDir  = "/var/run/wdtt"
	StatusFile  = "status.json"
	LogFile     = "wdtt.log"
	MaxLogLines = 500
	MaxSteps    = 40
)

// Snapshot — состояние демона для LuCI / ubus.
type Snapshot struct {
	Running   bool      `json:"running"`
	State     string    `json:"state"`
	RxBytes   int64     `json:"rx_bytes"`
	TxBytes   int64     `json:"tx_bytes"`
	Workers   int32     `json:"workers"`
	WGApplied bool      `json:"wg_applied"`
	Captcha   *Captcha  `json:"captcha,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	UptimeSec int64     `json:"uptime_sec"`
	Version   string    `json:"version"`
	Steps     []string  `json:"steps,omitempty"`
}

type Captcha struct {
	Required    bool   `json:"required"`
	Mode        string `json:"mode,omitempty"`
	RedirectURI string `json:"redirect_uri,omitempty"`
	Session     string `json:"session,omitempty"`
}

type Manager struct {
	mu        sync.RWMutex
	dir       string
	version   string
	startedAt time.Time
	snap      Snapshot
	logLines  []string
}

func New(dir, version string) *Manager {
	if dir == "" {
		dir = DefaultDir
	}
	_ = os.MkdirAll(dir, 0o755)
	return &Manager{
		dir:     dir,
		version: version,
		snap: Snapshot{
			State:   "stopped",
			Version: version,
		},
	}
}

func (m *Manager) SetRunning(running bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap.Running = running
	if running {
		m.startedAt = time.Now()
		m.snap.State = "starting"
		m.snap.Steps = nil
	} else {
		m.snap.State = "stopped"
		m.snap.Workers = 0
		m.snap.WGApplied = false
		m.snap.Captcha = nil
	}
	m.persistLocked()
}

func (m *Manager) SetState(state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap.State = state
	m.persistLocked()
}

func (m *Manager) SetStats(rx, tx int64, workers int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap.RxBytes = rx
	m.snap.TxBytes = tx
	m.snap.Workers = workers
	m.persistLocked()
}

func (m *Manager) SetWGApplied(applied bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap.WGApplied = applied
	m.persistLocked()
}

func (m *Manager) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.snap.LastError = err.Error()
		m.snap.State = "error"
	} else {
		m.snap.LastError = ""
	}
	m.persistLocked()
}

func (m *Manager) SetCaptcha(c *Captcha) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap.Captcha = c
	m.persistLocked()
}

func (m *Manager) AppendStep(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := len(m.snap.Steps); n > 0 && m.snap.Steps[n-1] == line {
		return
	}
	m.snap.Steps = append(m.snap.Steps, line)
	if len(m.snap.Steps) > MaxSteps {
		m.snap.Steps = m.snap.Steps[len(m.snap.Steps)-MaxSteps:]
	}
	m.persistLocked()
}

func (m *Manager) AppendLog(line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logLines = append(m.logLines, line)
	if len(m.logLines) > MaxLogLines {
		m.logLines = m.logLines[len(m.logLines)-MaxLogLines:]
	}
	_ = os.WriteFile(filepath.Join(m.dir, LogFile), []byte(joinLines(m.logLines)), 0o644)
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.snap
	if m.snap.Running && !m.startedAt.IsZero() {
		s.UptimeSec = int64(time.Since(m.startedAt).Seconds())
	}
	s.UpdatedAt = time.Now()
	return s
}

func (m *Manager) Logs(tail int) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if tail <= 0 || tail >= len(m.logLines) {
		out := make([]string, len(m.logLines))
		copy(out, m.logLines)
		return out
	}
	out := make([]string, tail)
	copy(out, m.logLines[len(m.logLines)-tail:])
	return out
}

func (m *Manager) persistLocked() {
	m.snap.UpdatedAt = time.Now()
	if m.snap.Running && !m.startedAt.IsZero() {
		m.snap.UptimeSec = int64(time.Since(m.startedAt).Seconds())
	}
	data, err := json.MarshalIndent(m.snap, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(m.dir, StatusFile), data, 0o644)
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := lines[0]
	for _, l := range lines[1:] {
		out += "\n" + l
	}
	return out
}

func ReadSnapshot(dir string) (*Snapshot, error) {
	if dir == "" {
		dir = DefaultDir
	}
	data, err := os.ReadFile(filepath.Join(dir, StatusFile))
	if err != nil {
		return nil, fmt.Errorf("read status: %w", err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse status: %w", err)
	}
	return &s, nil
}

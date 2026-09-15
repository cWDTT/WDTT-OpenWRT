package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeDeviceID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"openwrt-a1b2c3d4e5f6\n", "openwrt-a1b2c3d4e5f6"},
		{"  router.home_1  ", "router.home_1"},
		// '|' разделяет поля в GETCONF/AUTH — символ обязан вырезаться
		{"dev|ice", "device"},
		{"пусто", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeDeviceID(c.in); got != c.want {
			t.Errorf("sanitizeDeviceID(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeDeviceIDLimit(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	if got := len(sanitizeDeviceID(long)); got != 64 {
		t.Errorf("длина = %d, ожидалось 64", got)
	}
}

func TestDeriveDeviceIDNotEmpty(t *testing.T) {
	// На любой машине должен получиться непустой стабильный ID: MAC, machine-id
	// или фоллбэк openwrt-wdtt.
	if id := deriveDeviceID(); id == "" {
		t.Fatal("deriveDeviceID вернул пустую строку")
	}
}

func TestCollectHashesSlotsAndLegacy(t *testing.T) {
	got := collectHashes(map[string]string{
		"hash1":  "https://vk.com/call/join/aaa111",
		"hash2":  "bbb222",
		"hash3":  "",
		"hashes": "aaa111,ccc333",
	})
	if len(got) != 3 {
		t.Fatalf("ожидалось 3 хеша, получили %v", got)
	}
	if got[0] != "aaa111" || got[1] != "bbb222" || got[2] != "ccc333" {
		t.Fatalf("порядок/нормализация: %v", got)
	}
}

func TestLoadTunnelModeRawRemapsIface(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wdtt")
	src := `config globals 'globals'
	option peer '203.0.113.10:56000'
	option password 'secret'
	option hash1 'aaa111'
	option tunnel_mode 'raw'
	option iface 'wg-wdtt'
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsRaw() {
		t.Fatalf("ожидался RAW, получили %q", s.TunnelMode)
	}
	if s.Iface != "tun-wdtt" {
		t.Fatalf("iface=%q, ожидалось tun-wdtt", s.Iface)
	}
}

func TestLoadTunnelModeWGDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wdtt")
	src := `config globals 'globals'
	option peer '203.0.113.10:56000'
	option password 'secret'
	option hash1 'aaa111'
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.IsRaw() {
		t.Fatal("без tunnel_mode должен быть WireGuard")
	}
	if s.Iface != "wg-wdtt" {
		t.Fatalf("iface=%q", s.Iface)
	}
}

func TestDeriveDeviceIDStable(t *testing.T) {
	if first, second := deriveDeviceID(), deriveDeviceID(); first != second {
		t.Errorf("ID нестабилен: %q != %q", first, second)
	}
}

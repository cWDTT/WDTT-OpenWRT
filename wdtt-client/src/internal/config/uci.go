package config

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

const DefaultConfigPath = "/etc/config/wdtt"

// RoutingMode — режим маршрутизации трафика.
type RoutingMode string

const (
	RoutingFull      RoutingMode = "full"
	RoutingSelective RoutingMode = "selective"
	// RoutingExternal — только туннель wg-wdtt + firewall; маршруты — Podkop/PBR.
	RoutingExternal RoutingMode = "external"
)

// Rule — секция правил маршрутизации (UCI rule).
type Rule struct {
	Name      string
	Enabled   bool
	Type      string // route | exclusion
	Domains   []string
	Subnets   []string
	SourceIPs []string // fully_routed_ips — весь трафик устройства
	ListURL   string   // URL списка доменов (по одному на строку)
}

// Settings — параметры из UCI /etc/config/wdtt.
type Settings struct {
	Enabled            bool
	Peer               string
	Password           string
	Hashes             []string
	Workers            int
	MTU                int
	Listen             string
	TurnHost           string
	TurnPort           string
	CaptchaMode        string
	VKAuthMode         string
	ObfsMode           string // audio | video
	GoDNS              string // DNS для VK API: yandex|doh-yandex|doh-cloudflare|...
	TurnTransport      string // udp | tcp
	DeviceID           string
	Iface              string
	UplinkIface        string // auto | wan | wwan | network section / device
	RoutingMode        RoutingMode
	TunnelMode         TunnelMode
	RoutingExcludedIPs []string
	Rules              []Rule
}

// TunnelMode — транспорт до VPS: WireGuard+DTLS или сырой IP (qWDTT -listen-raw).
type TunnelMode string

const (
	TunnelWG  TunnelMode = "wg"
	TunnelRaw TunnelMode = "raw"
)

type uciSection struct {
	typ     string
	name    string
	options map[string]string
	lists   map[string][]string
}

func Load(path string) (*Settings, error) {
	if path == "" {
		path = DefaultConfigPath
	}
	sections, err := parseUCI(path)
	if err != nil {
		return nil, err
	}

	var globals *uciSection
	var rules []Rule

	for _, sec := range sections {
		switch sec.typ {
		case "globals":
			if sec.name == "globals" || globals == nil {
				globals = sec
			}
		case "rule":
			rules = append(rules, parseRule(sec))
		}
	}

	if globals == nil {
		return nil, fmt.Errorf("section globals not found in %s", path)
	}

	g := globals.options
	s := &Settings{
		Enabled:       g["enabled"] == "1",
		Peer:          strings.TrimSpace(g["peer"]),
		Password:      g["password"],
		Workers:       atoiDefault(g["workers"], 12),
		MTU:           atoiDefault(g["mtu"], 1240),
		Listen:        defaultString(g["listen"], "127.0.0.1:9000"),
		TurnHost:      strings.TrimSpace(g["turn_host"]),
		TurnPort:      strings.TrimSpace(g["turn_port"]),
		CaptchaMode:   defaultString(g["captcha_mode"], "wv"),
		VKAuthMode:    defaultString(g["vk_auth_mode"], "vkcalls"),
		ObfsMode:      normalizeObfsMode(g["obfs_mode"]),
		GoDNS:         defaultString(g["go_dns"], "doh-yandex"),
		TurnTransport: normalizeTurnTransport(g["turn_transport"]),
		DeviceID:      defaultString(g["device_id"], ""),
		Iface:         defaultString(g["iface"], "wg-wdtt"),
		UplinkIface:   defaultString(g["uplink_iface"], "auto"),
		TunnelMode:    TunnelWG,
		Rules:         rules,
	}
	if strings.EqualFold(strings.TrimSpace(g["tunnel_mode"]), "raw") {
		s.TunnelMode = TunnelRaw
		if s.Iface == "" || s.Iface == "wg-wdtt" {
			s.Iface = "tun-wdtt"
		}
	}

	// routing_mode: selective | full | external (Podkop/PBR)
	switch strings.ToLower(strings.TrimSpace(g["routing_mode"])) {
	case "full":
		s.RoutingMode = RoutingFull
	case "external", "podkop", "tunnel", "tunnel_only":
		s.RoutingMode = RoutingExternal
	default:
		s.RoutingMode = RoutingSelective
	}

	// legacy full_tunnel option (не трогает external)
	if g["full_tunnel"] == "1" {
		s.RoutingMode = RoutingFull
	} else if g["full_tunnel"] == "0" && s.RoutingMode != RoutingExternal {
		s.RoutingMode = RoutingSelective
	}

	s.RoutingExcludedIPs = append(s.RoutingExcludedIPs, globals.lists["routing_excluded_ip"]...)
	s.RoutingExcludedIPs = append(s.RoutingExcludedIPs, globals.lists["routing_excluded_ips"]...)

	s.Hashes = collectHashes(g)

	if s.DeviceID = sanitizeDeviceID(s.DeviceID); s.DeviceID == "" {
		s.DeviceID = resolveDeviceID()
	}

	return s, nil
}

func parseRule(sec *uciSection) Rule {
	r := Rule{
		Name:    sec.name,
		Enabled: sec.options["enabled"] != "0",
		Type:    defaultString(sec.options["type"], "route"),
		ListURL: strings.TrimSpace(sec.options["list_url"]),
	}
	r.Domains = append(r.Domains, sec.lists["domain"]...)
	r.Domains = append(r.Domains, sec.lists["domains"]...)
	r.Subnets = append(r.Subnets, sec.lists["subnet"]...)
	r.Subnets = append(r.Subnets, sec.lists["subnets"]...)
	r.SourceIPs = append(r.SourceIPs, sec.lists["source_ip"]...)
	r.SourceIPs = append(r.SourceIPs, sec.lists["fully_routed_ip"]...)
	return r
}

func parseUCI(path string) ([]*uciSection, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open uci config: %w", err)
	}
	defer f.Close()

	var sections []*uciSection
	var current *uciSection

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "config ") {
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			typ := strings.Trim(parts[1], "'\"")
			name := typ
			if len(parts) >= 3 {
				name = strings.Trim(parts[2], "'\"")
			}
			current = &uciSection{
				typ:     typ,
				name:    name,
				options: make(map[string]string),
				lists:   make(map[string][]string),
			}
			sections = append(sections, current)
			continue
		}

		if current == nil {
			continue
		}

		if strings.HasPrefix(line, "option ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				key := parts[1]
				val := strings.TrimSpace(strings.Join(parts[2:], " "))
				val = strings.Trim(val, "'\"")
				current.options[key] = val
			}
			continue
		}

		if strings.HasPrefix(line, "list ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				key := parts[1]
				val := strings.TrimSpace(strings.Join(parts[2:], " "))
				val = strings.Trim(val, "'\"")
				current.lists[key] = append(current.lists[key], val)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read uci config: %w", err)
	}
	return sections, nil
}

func (s *Settings) Validate() error {
	if s.Peer == "" {
		return fmt.Errorf("peer is required")
	}
	if s.Password == "" {
		return fmt.Errorf("password is required")
	}
	if len(s.Hashes) == 0 {
		return fmt.Errorf("at least one VK hash is required")
	}
	if s.Workers < 3 {
		s.Workers = 3
	}
	if s.Workers > 108 {
		s.Workers = 108
	}
	if s.MTU <= 0 {
		s.MTU = 1240
	}
	if s.Iface == "" {
		if s.TunnelMode == TunnelRaw {
			s.Iface = "tun-wdtt"
		} else {
			s.Iface = "wg-wdtt"
		}
	}
	return nil
}

func (s *Settings) IsRaw() bool {
	return s.TunnelMode == TunnelRaw
}

func (s *Settings) IsSelective() bool {
	return s.RoutingMode == RoutingSelective
}

func (s *Settings) IsFull() bool {
	return s.RoutingMode == RoutingFull
}

func (s *Settings) IsExternal() bool {
	return s.RoutingMode == RoutingExternal
}

// collectHashes собирает до 4 отдельных hashN и общий список hashes/hash.
func collectHashes(g map[string]string) []string {
	var out []string
	seen := make(map[string]struct{})
	add := func(raw string) {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == '|'
		}) {
			h := normalizeHash(part)
			if h == "" {
				continue
			}
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
			out = append(out, h)
		}
	}
	for i := 1; i <= 4; i++ {
		add(g[fmt.Sprintf("hash%d", i)])
	}
	add(g["hashes"])
	add(g["hash"])
	return out
}

func normalizeHash(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if idx := strings.LastIndex(raw, "/join/"); idx >= 0 {
		raw = raw[idx+len("/join/"):]
	}
	if q := strings.IndexByte(raw, '?'); q >= 0 {
		raw = raw[:q]
	}
	return strings.Trim(raw, "/ ")
}

func defaultString(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func normalizeObfsMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "video":
		return "video"
	default:
		return "audio"
	}
}

func normalizeTurnTransport(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "tcp":
		return "tcp"
	default:
		return "udp"
	}
}

func atoiDefault(v string, def int) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	var n int
	_, err := fmt.Sscanf(v, "%d", &n)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// deviceIDDir/deviceIDFile — постоянное хранилище ID устройства на флеше.
// Сервер привязывает пароль к device_id и при лимите в одно устройство
// отказывает любому другому (DENIED:device_mismatch), поэтому ID обязан быть
// одинаковым между перезагрузками. /var на OpenWrt — симлинк в tmpfs, так что
// /var/lib/dbus/machine-id для этого не годится: он новый после каждой загрузки.
const (
	deviceIDDir  = "/etc/wdtt"
	deviceIDFile = deviceIDDir + "/device_id"
)

// macCandidates — интерфейсы, MAC которых берём как основу ID. Порядок важен:
// br-lan есть почти всегда и не меняется, eth* — фоллбэк для нестандартных сборок.
var macCandidates = []string{"br-lan", "eth0", "eth1", "lan", "br-wan", "wan"}

// resolveDeviceID возвращает стабильный между перезагрузками ID устройства.
func resolveDeviceID() string {
	if id := readDeviceIDFile(); id != "" {
		return id
	}
	id := deriveDeviceID()
	persistDeviceID(id)
	return id
}

func readDeviceIDFile() string {
	data, err := os.ReadFile(deviceIDFile)
	if err != nil {
		return ""
	}
	return sanitizeDeviceID(string(data))
}

// persistDeviceID сохраняет ID, чтобы он не зависел от порядка интерфейсов и
// переименований в будущих прошивках. Ошибки игнорируем: при read-only rootfs
// ID всё равно будет выведен из MAC и останется тем же.
func persistDeviceID(id string) {
	if id == "" {
		return
	}
	if err := os.MkdirAll(deviceIDDir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(deviceIDFile, []byte(id+"\n"), 0o644)
}

func deriveDeviceID() string {
	for _, iface := range macCandidates {
		if mac := readIfaceMAC(iface); mac != "" {
			return "openwrt-" + mac
		}
	}
	if mac := anyPhysicalMAC(); mac != "" {
		return "openwrt-" + mac
	}
	// /etc/machine-id лежит на флеше (в отличие от /var/lib/dbus/machine-id),
	// поэтому как фоллбэк подходит, если он вообще есть.
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		if id := sanitizeDeviceID(string(data)); id != "" {
			return id
		}
	}
	return "openwrt-wdtt"
}

func readIfaceMAC(iface string) string {
	data, err := os.ReadFile("/sys/class/net/" + iface + "/address")
	if err != nil {
		return ""
	}
	mac := strings.ToLower(strings.TrimSpace(string(data)))
	mac = strings.ReplaceAll(mac, ":", "")
	if len(mac) != 12 || mac == "000000000000" {
		return ""
	}
	return mac
}

// anyPhysicalMAC перебирает интерфейсы по алфавиту и берёт первый физический
// (наличие symlink device отсекает bridge, wg, tun и прочие виртуальные).
func anyPhysicalMAC() string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "lo" {
			continue
		}
		if _, err := os.Stat("/sys/class/net/" + name + "/device"); err != nil {
			continue
		}
		if mac := readIfaceMAC(name); mac != "" {
			return mac
		}
	}
	return ""
}

// sanitizeDeviceID оставляет только безопасные символы: ID уходит на сервер в
// GETCONF/AUTH, где поля разделяются '|'.
func sanitizeDeviceID(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}

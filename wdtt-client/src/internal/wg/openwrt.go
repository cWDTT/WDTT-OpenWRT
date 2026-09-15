package wg

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// RoutingMode определяет схему маршрутизации.
type RoutingMode string

const (
	ModeFull      RoutingMode = "full"
	ModeSelective RoutingMode = "selective"
	ModeExternal  RoutingMode = "external" // туннель без маршрутов WDTT
)

// vkExcludeCIDRs — подсети VK/TURN/DNS, которые должны идти мимо туннеля.
var vkExcludeCIDRs = []string{
	"87.240.128.0/18",
	"87.240.192.0/19",
	"90.156.0.0/16",
	"93.186.224.0/21",
	"95.142.192.0/21",
	"95.163.0.0/16",
	"95.213.0.0/18",
	"155.212.192.0/20",
	"185.16.28.0/22",
	"194.67.64.0/18",
	"195.82.146.0/23",
	"213.180.193.0/24",
	"77.88.0.0/18",
	"91.231.0.0/16",
	"8.8.8.0/24",
	"1.1.1.0/24",
}

var wgQuickOnlyFields = map[string]bool{
	"address": true, "dns": true, "mtu": true,
	"preup": true, "postup": true, "predown": true, "postdown": true,
	"saveconfig": true,
}

// Manager управляет WireGuard-интерфейсом на OpenWRT.
type Manager struct {
	iface  string
	mode   RoutingMode
	mu     sync.Mutex
	routes []string
}

func New(iface string) *Manager {
	if iface == "" {
		iface = "wg-wdtt"
	}
	return &Manager{iface: iface, mode: ModeSelective}
}

func (m *Manager) SetMode(mode RoutingMode) {
	m.mu.Lock()
	m.mode = mode
	m.mu.Unlock()
}

func (m *Manager) Apply(conf string, turnIPs []string) error {
	m.mu.Lock()
	mode := m.mode
	m.mu.Unlock()
	return m.ApplyWithMode(conf, turnIPs, mode, "", "")
}

func (m *Manager) ApplyWithMode(conf string, turnIPs []string, mode RoutingMode, uplinkIface, peerAddr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mode = mode

	m.teardownLocked()

	addr, mtu, allowedIPs, wgConf := parseWGConfig(conf)
	if addr == "" {
		return fmt.Errorf("Address not found in WireGuard config")
	}

	tmp, err := os.CreateTemp("/tmp", "wdtt-wg-*.conf")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(wgConf); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	_ = os.Chmod(tmpName, 0o600)

	if err := run("ip", "link", "add", m.iface, "type", "wireguard"); err != nil {
		return fmt.Errorf("ip link add: %w", err)
	}
	if err := run("wg", "setconf", m.iface, tmpName); err != nil {
		_ = run("ip", "link", "del", m.iface)
		return fmt.Errorf("wg setconf: %w", err)
	}

	_ = run("ip", "addr", "flush", "dev", m.iface)
	if err := run("ip", "addr", "add", addr, "dev", m.iface); err != nil {
		m.teardownLocked()
		return fmt.Errorf("ip addr add: %w", err)
	}
	if mtu != "" {
		_ = run("ip", "link", "set", m.iface, "mtu", mtu)
	}
	if err := run("ip", "link", "set", m.iface, "up"); err != nil {
		m.teardownLocked()
		return fmt.Errorf("ip link set up: %w", err)
	}

	var routes []string
	gw, uplinkDev := gatewayForUplink(uplinkIface)
	for _, ip := range turnIPs {
		cidr := ip + "/32"
		if addBypassRoute(cidr, gw, uplinkDev) {
			routes = append(routes, cidr)
		}
	}
	for _, cidr := range vkExcludeCIDRs {
		if addBypassRoute(cidr, gw, uplinkDev) {
			routes = append(routes, cidr)
		}
	}
	for _, dns := range localDNSServers() {
		cidr := dns + "/32"
		if addBypassRoute(cidr, gw, uplinkDev) {
			routes = append(routes, cidr)
		}
	}
	if peerHost := parsePeerHost(peerAddr); peerHost != "" {
		cidr := peerHost + "/32"
		if addBypassRoute(cidr, gw, uplinkDev) {
			routes = append(routes, cidr)
		}
	}

	// full — весь трафик через WG.
	// selective — /usr/libexec/wdtt/routing (policy routing).
	// external — только iface; маршруты у Podkop/PBR (route_allowed_ips=0).
	if mode == ModeFull {
		if len(allowedIPs) == 0 {
			allowedIPs = []string{"0.0.0.0/1", "128.0.0.0/1"}
		}
		for _, cidr := range allowedIPs {
			if run("ip", "route", "replace", cidr, "dev", m.iface) == nil {
				routes = append(routes, "dev:"+cidr)
			} else if run("ip", "route", "add", cidr, "dev", m.iface) == nil {
				routes = append(routes, "dev:"+cidr)
			}
		}
	}

	m.routes = routes
	return nil
}

// ApplyRawOverlay ставит обходные маршруты (TURN/VK/DNS/peer) и, в режиме
// full, default через уже поднятый TUN. Интерфейс создаёт ядро, не WireGuard.
func (m *Manager) ApplyRawOverlay(turnIPs []string, mode RoutingMode, uplinkIface, peerAddr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mode = mode

	for _, entry := range m.routes {
		if strings.HasPrefix(entry, "dev:") {
			_ = run("ip", "route", "del", strings.TrimPrefix(entry, "dev:"), "dev", m.iface)
		} else {
			_ = run("ip", "route", "del", entry)
		}
	}
	m.routes = nil

	var routes []string
	gw, uplinkDev := gatewayForUplink(uplinkIface)
	for _, ip := range turnIPs {
		cidr := ip + "/32"
		if addBypassRoute(cidr, gw, uplinkDev) {
			routes = append(routes, cidr)
		}
	}
	for _, cidr := range vkExcludeCIDRs {
		if addBypassRoute(cidr, gw, uplinkDev) {
			routes = append(routes, cidr)
		}
	}
	for _, dns := range localDNSServers() {
		cidr := dns + "/32"
		if addBypassRoute(cidr, gw, uplinkDev) {
			routes = append(routes, cidr)
		}
	}
	if peerHost := parsePeerHost(peerAddr); peerHost != "" {
		cidr := peerHost + "/32"
		if addBypassRoute(cidr, gw, uplinkDev) {
			routes = append(routes, cidr)
		}
	}
	if mode == ModeFull {
		for _, cidr := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
			if run("ip", "route", "replace", cidr, "dev", m.iface) == nil {
				routes = append(routes, "dev:"+cidr)
			} else if run("ip", "route", "add", cidr, "dev", m.iface) == nil {
				routes = append(routes, "dev:"+cidr)
			}
		}
	}
	m.routes = routes
	return nil
}

func (m *Manager) Teardown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.teardownLocked()
}

func (m *Manager) teardownLocked() {
	for _, entry := range m.routes {
		if strings.HasPrefix(entry, "dev:") {
			cidr := strings.TrimPrefix(entry, "dev:")
			_ = run("ip", "route", "del", cidr, "dev", m.iface)
		} else {
			_ = run("ip", "route", "del", entry)
		}
	}
	m.routes = nil
	_ = run("ip", "link", "del", m.iface)
}

func (m *Manager) Iface() string { return m.iface }

func (m *Manager) Mode() RoutingMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

func parseWGConfig(conf string) (addr, mtu string, allowedIPs []string, wgConf string) {
	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(conf))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) == 2 {
			key := strings.ToLower(strings.TrimSpace(parts[0]))
			val := strings.TrimSpace(parts[1])
			switch key {
			case "address":
				addr = val
				continue
			case "mtu":
				mtu = val
				continue
			case "allowedips":
				for _, cidr := range strings.Split(val, ",") {
					if c := strings.TrimSpace(cidr); c != "" {
						allowedIPs = append(allowedIPs, c)
					}
				}
			default:
				if wgQuickOnlyFields[key] {
					continue
				}
			}
		}
		out.WriteString(line + "\n")
	}
	wgConf = out.String()
	return
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w — %s", name, args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func defaultGateway() string {
	gw, _ := gatewayForUplink("auto")
	return gw
}

func gatewayForUplink(uplink string) (gw string, dev string) {
	uplink = strings.TrimSpace(uplink)
	if uplink == "" {
		uplink = "auto"
	}

	if _, err := exec.LookPath("/usr/libexec/wdtt/uplink"); err == nil {
		if out, err := exec.Command("/usr/libexec/wdtt/uplink", "device", uplink).Output(); err == nil {
			if d := strings.TrimSpace(string(out)); d != "" && !strings.HasPrefix(d, "/sys") {
				dev = d
			}
		}
		if out, err := exec.Command("/usr/libexec/wdtt/uplink", "gateway", uplink).Output(); err == nil {
			gw = strings.TrimSpace(string(out))
		}
	}

	if dev == "" {
		dev = resolveUplinkDevice(uplink)
	}
	if gw == "" && dev != "" {
		gw = gatewayOnDevice(dev)
	}
	if gw == "" {
		gw = gatewayOnDevice("")
	}
	return gw, dev
}

func addBypassRoute(cidr, gw, dev string) bool {
	if gw != "" && dev != "" && gw != dev {
		if run("ip", "route", "replace", cidr, "via", gw, "dev", dev) == nil {
			return true
		}
		return run("ip", "route", "add", cidr, "via", gw, "dev", dev) == nil
	}
	if gw != "" && !strings.HasPrefix(gw, "/sys") {
		if run("ip", "route", "replace", cidr, "via", gw) == nil {
			return true
		}
		return run("ip", "route", "add", cidr, "via", gw) == nil
	}
	if dev != "" {
		if run("ip", "route", "replace", cidr, "dev", dev) == nil {
			return true
		}
		return run("ip", "route", "add", cidr, "dev", dev) == nil
	}
	return false
}

func resolveUplinkDevice(uplink string) string {
	if uplink == "" || uplink == "auto" {
		return ""
	}
	out, err := exec.Command("uci", "-q", "get", "network."+uplink+".device").Output()
	if err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
			return d
		}
	}
	out, err = exec.Command("uci", "-q", "get", "network."+uplink+".ifname").Output()
	if err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
			return d
		}
	}
	return uplink
}

func gatewayOnDevice(dev string) string {
	args := []string{"route", "show", "default"}
	if dev != "" {
		args = append(args, "dev", dev)
	}
	cmd := exec.Command("ip", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "via" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func parsePeerHost(peerAddr string) string {
	peerAddr = strings.TrimSpace(peerAddr)
	if peerAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(peerAddr)
	if err != nil {
		if net.ParseIP(peerAddr) != nil {
			return peerAddr
		}
		return ""
	}
	if net.ParseIP(host) == nil {
		return ""
	}
	return host
}

func localDNSServers() []string {
	data, err := os.ReadFile("/tmp/resolv.conf.d/resolv.conf.auto")
	if err != nil {
		data, err = os.ReadFile("/etc/resolv.conf")
	}
	if err != nil {
		return nil
	}
	var result []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "nameserver" {
			continue
		}
		ip := net.ParseIP(fields[1])
		if ip == nil || ip.IsLoopback() {
			continue
		}
		result = append(result, fields[1])
	}
	return result
}

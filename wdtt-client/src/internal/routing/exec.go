package routing

import (
	"fmt"
	"os/exec"

	"github.com/wdtt-openwrt/wdtt-client/internal/config"
)

const (
	routingScript  = "/usr/libexec/wdtt/routing"
	datapathScript = "/usr/libexec/wdtt/datapath"
)

// Ensure поднимает datapath по UCI (selective | full | external) — единая точка после WG up.
func Ensure(iface string) error {
	if iface == "" {
		iface = "wg-wdtt"
	}
	if _, err := exec.LookPath(datapathScript); err == nil {
		cmd := exec.Command(datapathScript, "ensure", iface)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("datapath ensure: %w — %s", err, string(out))
		}
		return nil
	}
	// fallback без datapath
	return Start(iface, &config.Settings{RoutingMode: config.RoutingSelective})
}

// EnsureWithConfig — как Ensure, но учитывает режим из cfg.
func EnsureWithConfig(iface string, cfg *config.Settings) error {
	if iface == "" {
		iface = "wg-wdtt"
	}
	if _, err := exec.LookPath(datapathScript); err == nil {
		cmd := exec.Command(datapathScript, "ensure", iface)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("datapath ensure: %w — %s", err, string(out))
		}
		return nil
	}
	if cfg != nil && cfg.IsExternal() {
		_ = Stop()
		return RefreshFirewall(iface)
	}
	if cfg != nil && cfg.IsFull() {
		_ = Stop()
		if err := RefreshFirewall(iface); err != nil {
			return err
		}
		return ApplyFullTunnel(iface)
	}
	return Start(iface, cfg)
}

// Start применяет selective routing (nft sets + dnsmasq nftset + policy routing).
func Start(iface string, cfg *config.Settings) error {
	if cfg != nil && !cfg.IsSelective() {
		return Stop()
	}
	cmd := exec.Command(routingScript, "start", iface)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("routing start: %w — %s", err, string(out))
	}
	return nil
}

// Stop снимает selective routing (и full через datapath, если есть).
func Stop() error {
	if _, err := exec.LookPath(datapathScript); err == nil {
		cmd := exec.Command(datapathScript, "stop")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("datapath stop: %w — %s", err, string(out))
		}
		return nil
	}
	cmd := exec.Command(routingScript, "stop")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("routing stop: %w — %s", err, string(out))
	}
	_ = exec.Command("/usr/libexec/wdtt/full-tunnel", "stop").Run()
	return nil
}

// Reload перечитывает UCI и обновляет правила.
func Reload(iface string) error {
	return Ensure(iface)
}

// RefreshFirewall поднимает zone/NAT/forward для wg-wdtt (оба режима).
func RefreshFirewall(iface string) error {
	if iface == "" {
		iface = "wg-wdtt"
	}
	script := "/usr/libexec/wdtt/firewall-refresh"
	if _, err := exec.LookPath(script); err != nil {
		_ = exec.Command("/etc/firewall.wdtt").Run()
		return nil
	}
	cmd := exec.Command(script, iface)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("firewall-refresh: %w — %s", err, string(out))
	}
	return nil
}

// ApplyFullTunnel настраивает default routes и NAT для режима full (LAN → wg).
func ApplyFullTunnel(iface string) error {
	if iface == "" {
		iface = "wg-wdtt"
	}
	cmd := exec.Command("/usr/libexec/wdtt/full-tunnel", "apply")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("full-tunnel apply: %w — %s", err, string(out))
	}
	return nil
}

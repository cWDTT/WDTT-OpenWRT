package core

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func openRawTUN(name string) (*os.File, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("ifreq %s: %w", name, err)
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("TUNSETIFF %s: %w", name, err)
	}
	return os.NewFile(uintptr(fd), "/dev/net/tun"), nil
}

func configureTUN(name, cidr string, mtu int) error {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" {
		return fmt.Errorf("пустой адрес TUN")
	}
	if !strings.Contains(cidr, "/") {
		cidr += "/32"
	}
	run := func(args ...string) error {
		out, err := exec.Command("ip", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("ip %s: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	_ = run("addr", "flush", "dev", name)
	if err := run("addr", "add", cidr, "dev", name); err != nil {
		return err
	}
	if mtu > 0 {
		_ = run("link", "set", "dev", name, "mtu", strconv.Itoa(mtu))
	}
	return run("link", "set", "dev", name, "up")
}

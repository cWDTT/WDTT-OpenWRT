//go:build !linux

package core

import (
	"fmt"
	"os"
)

func openRawTUN(name string) (*os.File, error) {
	return nil, fmt.Errorf("RAW TUN только на Linux (%s)", name)
}

func configureTUN(name, cidr string, mtu int) error {
	return fmt.Errorf("RAW TUN только на Linux (%s %s mtu=%d)", name, cidr, mtu)
}

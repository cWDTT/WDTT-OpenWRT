package core

import (
	"strings"
	"testing"
)

func TestParseDenied(t *testing.T) {
	if err := parseDenied("OK"); err != nil {
		t.Fatalf("не DENIED не должен давать ошибку: %v", err)
	}
	err := parseDenied("DENIED:wrong_password")
	if err == nil || !strings.Contains(err.Error(), "FATAL_AUTH") {
		t.Fatalf("wrong_password: %v", err)
	}
	err = parseDenied("DENIED:device_mismatch")
	if err == nil || !strings.Contains(err.Error(), "FATAL_AUTH") {
		t.Fatalf("device_mismatch: %v", err)
	}
}

func TestParseRawConf(t *testing.T) {
	ip, dns, mtu, err := ParseRawConf("RAWCONF:10.8.0.2/32|1.1.1.1,8.8.8.8|1280")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "10.8.0.2/32" || dns != "1.1.1.1,8.8.8.8" || mtu != 1280 {
		t.Fatalf("ip=%q dns=%q mtu=%d", ip, dns, mtu)
	}

	ip, _, _, err = ParseRawConf("RAWCONF:||1240")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "" {
		t.Fatalf("пустой IP: %q", ip)
	}

	if _, _, _, err = ParseRawConf("WGCONF:nope"); err == nil {
		t.Fatal("ожидалась ошибка на не-RAWCONF")
	}
	if _, _, _, err = ParseRawConf("RAWCONF:only|two"); err == nil {
		t.Fatal("ожидалась ошибка на коротком RAWCONF")
	}
}

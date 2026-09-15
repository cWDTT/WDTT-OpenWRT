package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wdtt-openwrt/wdtt-client/internal/captcha"
	"github.com/wdtt-openwrt/wdtt-client/internal/daemon"
	"github.com/wdtt-openwrt/wdtt-client/internal/status"
)

var version = "1.0.0-dev"

func main() {
	captchaToken := flag.String("captcha", "", "submit VK captcha token and exit")
	showStatus := flag.Bool("status", false, "print JSON status and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if *showStatus {
		snap, err := status.ReadSnapshot("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf(`{"running":%t,"state":"%s","rx_bytes":%d,"tx_bytes":%d,"workers":%d,"wg_applied":%t,"last_error":"%s"}`,
			snap.Running, snap.State, snap.RxBytes, snap.TxBytes, snap.Workers, snap.WGApplied, snap.LastError)
		return
	}

	if *captchaToken != "" {
		if err := captcha.Submit(status.DefaultDir, *captchaToken); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	os.Exit(daemon.RunForeground(version))
}

package core

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

func parseDenied(resp string) error {
	if !strings.HasPrefix(resp, "DENIED:") {
		return nil
	}
	reason := strings.TrimPrefix(resp, "DENIED:")
	switch reason {
	case "wrong_password":
		return fmt.Errorf("FATAL_AUTH: неверный пароль подключения")
	case "expired":
		return fmt.Errorf("FATAL_AUTH: срок действия пароля истёк")
	case "device_mismatch":
		return fmt.Errorf("FATAL_AUTH: пароль привязан к другому устройству")
	default:
		return fmt.Errorf("FATAL_AUTH: доступ запрещён (%s)", reason)
	}
}

// RequestConfig запрашивает WireGuard конфиг через DTLS-соединение.
func RequestConfig(conn net.Conn, localPort, deviceID, password string) (string, error) {
	payload := fmt.Sprintf("GETCONF:%s|%s|%s", localPort, deviceID, password)
	if _, err := conn.Write([]byte(payload)); err != nil {
		return "", fmt.Errorf("отправка GETCONF: %w", err)
	}

	b := make([]byte, 4096)
	if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return "", fmt.Errorf("установка дедлайна: %w", err)
	}
	n, err := conn.Read(b)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return "", fmt.Errorf("чтение ответа конфига: %w", err)
	}

	resp := string(b[:n])
	if resp == "NOCONF" {
		return "", nil
	}
	if err := parseDenied(resp); err != nil {
		return "", err
	}
	return resp, nil
}

// SendAuth представляет сессию серверу. Сервер закрывает любое соединение,
// которое не прислало GETCONF/GETCONF_RAW или AUTH первым пакетом.
func SendAuth(conn net.Conn, deviceID, password string) error {
	payload := fmt.Sprintf("AUTH:%s|%s", deviceID, password)
	if _, err := conn.Write([]byte(payload)); err != nil {
		return fmt.Errorf("отправка AUTH: %w", err)
	}
	return nil
}

// RequestRawConfig запрашивает IP/DNS/MTU у сервера с -listen-raw.
// Ответ: RAWCONF:ip|dns|mtu. Пустой ip — сервер ещё не назначил адрес.
func RequestRawConfig(conn net.Conn, deviceID, password string) (ip, dnsCSV string, mtu int, err error) {
	payload := fmt.Sprintf("GETCONF_RAW:%s|%s", deviceID, password)
	if _, err = conn.Write([]byte(payload)); err != nil {
		return "", "", 0, fmt.Errorf("отправка GETCONF_RAW: %w", err)
	}

	b := make([]byte, 4096)
	if err = conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return "", "", 0, fmt.Errorf("установка дедлайна: %w", err)
	}
	n, readErr := conn.Read(b)
	_ = conn.SetReadDeadline(time.Time{})
	if readErr != nil {
		return "", "", 0, fmt.Errorf("чтение ответа RAWCONF: %w", readErr)
	}

	resp := string(b[:n])
	if resp == "NOCONF" {
		return "", "", 0, nil
	}
	if err = parseDenied(resp); err != nil {
		return "", "", 0, err
	}
	return ParseRawConf(resp)
}

// ParseRawConf разбирает RAWCONF:ip|dns|mtu. Пустой ip — адрес ещё не выдан.
func ParseRawConf(resp string) (ip, dnsCSV string, mtu int, err error) {
	if !strings.HasPrefix(resp, "RAWCONF:") {
		return "", "", 0, fmt.Errorf("неожиданный ответ RAWCONF: %q", resp)
	}
	parts := strings.Split(strings.TrimPrefix(resp, "RAWCONF:"), "|")
	if len(parts) != 3 {
		return "", "", 0, fmt.Errorf("некорректный формат RAWCONF: %q", resp)
	}
	mtuVal, convErr := strconv.Atoi(strings.TrimSpace(parts[2]))
	if convErr != nil {
		return "", "", 0, fmt.Errorf("некорректный MTU в RAWCONF: %q", parts[2])
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), mtuVal, nil
}

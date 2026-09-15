package core

import (
	"crypto/cipher"
	"net"
	"time"
)

// obfsDirectConn — RTP-obfs AEAD прямо поверх TURN relay, без DTLS.
// Нужен сервер с -listen-raw / -listen-direct (qWDTT).
type obfsDirectConn struct {
	relay      net.PacketConn
	peer       net.Addr
	wrapKey    []byte
	aead       cipher.AEAD
	cfg        *ObfsConfig
	writeState *ObfsState
}

func newObfsDirectConn(relay net.PacketConn, peer net.Addr, wrapKey []byte, mode string) (*obfsDirectConn, error) {
	aead, err := getAEAD(wrapKey)
	if err != nil {
		return nil, err
	}
	return &obfsDirectConn{
		relay:      relay,
		peer:       peer,
		wrapKey:    wrapKey,
		aead:       aead,
		cfg:        NewObfsConfig(mode),
		writeState: NewObfsState(),
	}, nil
}

func (c *obfsDirectConn) Read(b []byte) (int, error) {
	wire := make([]byte, len(b)+120)
	for {
		n, _, err := c.relay.ReadFrom(wire)
		if err != nil {
			return 0, err
		}
		if !obfsIsRTPPacket(wire[:n]) {
			continue
		}
		m, unwrapErr := obfsUnwrapPacketAEAD(c.aead, wire[:n], b)
		if unwrapErr != nil {
			continue
		}
		return m, nil
	}
}

func (c *obfsDirectConn) Write(b []byte) (int, error) {
	wrapped, err := obfsWrapPacket(c.wrapKey, b, c.cfg, c.writeState)
	if err != nil {
		return 0, err
	}
	if _, err := c.relay.WriteTo(wrapped, c.peer); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *obfsDirectConn) Close() error                       { return nil }
func (c *obfsDirectConn) LocalAddr() net.Addr                { return c.relay.LocalAddr() }
func (c *obfsDirectConn) RemoteAddr() net.Addr               { return c.peer }
func (c *obfsDirectConn) SetDeadline(t time.Time) error      { return c.relay.SetDeadline(t) }
func (c *obfsDirectConn) SetReadDeadline(t time.Time) error  { return c.relay.SetReadDeadline(t) }
func (c *obfsDirectConn) SetWriteDeadline(t time.Time) error { return c.relay.SetWriteDeadline(t) }

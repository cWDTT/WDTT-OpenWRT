// SPDX-License-Identifier: MIT
// obfs.go — WebRTC SRTP-like obfuscation for DTLS traffic
// Each UDP packet is wrapped in an RTP header making it indistinguishable
// from a real WebRTC OPUS audio (PT 111) or video (PT 96) stream to DPI systems.
//
// Packet format:
//   [RTP Header 12 bytes][ChaCha20-Poly1305 payload+tag][Padding 0-N bytes][PadLen 1 byte]
//
// The RTP header fields (SSRC + SeqNum + Timestamp) form the 12-byte AEAD nonce,
// so no separate nonce prefix is needed.

package core

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

var aeadCache sync.Map

func getAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != wrapKeyLen {
		return nil, fmt.Errorf("obfs: key must be %d bytes", wrapKeyLen)
	}
	keyStr := string(key)
	if val, ok := aeadCache.Load(keyStr); ok {
		return val.(cipher.AEAD), nil
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	aeadCache.Store(keyStr, aead)
	return aead, nil
}

// ─── Configuration ───

// ObfsConfig holds per-session obfuscation parameters.
type ObfsConfig struct {
	SSRC        uint32 // Synchronization Source — random per session
	PayloadType uint8  // RTP payload type (111 = OPUS audio, 96 = video)
	PaddingMax  int    // Max random padding bytes appended
}

// NewObfsConfig creates a config with random SSRC.
// mode: "audio" (default, PT 111) or "video" (PT 96, larger padding).
func NewObfsConfig(mode string) *ObfsConfig {
	var buf [4]byte
	rand.Read(buf[:])

	pt := uint8(111)
	pad := 24
	if strings.EqualFold(strings.TrimSpace(mode), "video") {
		pt = 96
		pad = 60
	}

	return &ObfsConfig{
		SSRC:        binary.BigEndian.Uint32(buf[:]),
		PayloadType: pt,
		PaddingMax:  pad,
	}
}

// ─── Per-direction state (sequence + timestamp counters) ───

// ObfsState tracks monotonically increasing RTP sequence number and timestamp using a 48-bit packet counter.
type ObfsState struct {
	mu      sync.Mutex
	initSeq uint16
	initTs  uint32
	count   uint64
}

// NewObfsState creates a state with random initial seq/ts and count=0.
func NewObfsState() *ObfsState {
	var buf [6]byte
	rand.Read(buf[:])
	return &ObfsState{
		initSeq: binary.BigEndian.Uint16(buf[0:2]),
		initTs:  binary.BigEndian.Uint32(buf[2:6]),
		count:   0,
	}
}

// ─── Nonce derivation ───

func obfsBuildNonceInto(dst *[12]byte, ssrc uint32, seq uint16, ts uint32) {
	binary.BigEndian.PutUint32(dst[0:4], ssrc)
	binary.BigEndian.PutUint16(dst[4:6], seq)
	dst[6] = 0
	dst[7] = 0
	binary.BigEndian.PutUint32(dst[8:12], ts)
}

// obfsBuildNonce deterministically builds a 12-byte AEAD nonce from RTP fields.
//
//	[SSRC 4B][SeqNum 2B][0x00 0x00][Timestamp 4B]
func obfsBuildNonce(ssrc uint32, seq uint16, ts uint32) []byte {
	n := make([]byte, 12)
	var tmp [12]byte
	obfsBuildNonceInto(&tmp, ssrc, seq, ts)
	copy(n, tmp[:])
	return n
}

// obfsWrapWireLen returns the maximum wire size for a given plaintext length.
func obfsWrapWireLen(payloadLen int, cfg *ObfsConfig) int {
	pad := 1
	if cfg != nil && cfg.PaddingMax > 0 {
		pad = cfg.PaddingMax
	}
	return 12 + payloadLen + chacha20poly1305.Overhead + pad
}

// ─── Wrap (encrypt + add RTP header) ───

// obfsWrapPacketInto wraps payload into dst without allocating (caller provides buffer).
// Returns number of bytes written to dst.
func obfsWrapPacketInto(dst []byte, aead cipher.AEAD, payload []byte, cfg *ObfsConfig, state *ObfsState) (int, error) {
	if len(payload) == 0 {
		return 0, errors.New("obfs: empty payload")
	}
	state.mu.Lock()
	c := state.count
	state.count++
	state.mu.Unlock()

	seq := state.initSeq + uint16(c)
	ts := state.initTs + uint32(c)*960 + uint32(c>>16)

	padRand := 0
	if cfg.PaddingMax > 0 {
		var rndBuf [1]byte
		rand.Read(rndBuf[:])
		padRand = int(rndBuf[0]) % cfg.PaddingMax
	}
	padTotal := padRand + 1
	outLen := 12 + len(payload) + chacha20poly1305.Overhead + padTotal
	if outLen > len(dst) {
		return 0, fmt.Errorf("obfs: dst too small (%d > %d)", outLen, len(dst))
	}

	dst[0] = 0x80 | 0x20 // V=2, P=1
	dst[1] = cfg.PayloadType & 0x7F
	binary.BigEndian.PutUint16(dst[2:4], seq)
	binary.BigEndian.PutUint32(dst[4:8], ts)
	binary.BigEndian.PutUint32(dst[8:12], cfg.SSRC)

	var nonce [12]byte
	obfsBuildNonceInto(&nonce, cfg.SSRC, seq, ts)
	sealed := aead.Seal(dst[12:12], nonce[:], payload, dst[:12])
	padStart := 12 + len(sealed)
	if padRand > 0 {
		rand.Read(dst[padStart : padStart+padRand])
	}
	dst[outLen-1] = byte(padTotal)
	return outLen, nil
}

// obfsWrapPacket wraps a plaintext payload into an RTP-like packet with authenticated encryption.
func obfsWrapPacket(key, payload []byte, cfg *ObfsConfig, state *ObfsState) ([]byte, error) {
	if len(key) != wrapKeyLen {
		return nil, fmt.Errorf("obfs: key must be %d bytes (got %d)", wrapKeyLen, len(key))
	}
	aead, err := getAEAD(key)
	if err != nil {
		return nil, fmt.Errorf("obfs: cipher init: %w", err)
	}
	out := make([]byte, obfsWrapWireLen(len(payload), cfg))
	n, err := obfsWrapPacketInto(out, aead, payload, cfg, state)
	if err != nil {
		return nil, err
	}
	return out[:n], nil
}

// ─── Unwrap (strip RTP header + decrypt) ───

// obfsUnwrapPacket strips the RTP header, removes padding, and decrypts the payload.
// Returns number of plaintext bytes written to dst.
func obfsUnwrapPacket(key, wire, dst []byte) (int, error) {
	if len(key) != wrapKeyLen {
		return 0, fmt.Errorf("obfs: key must be %d bytes (got %d)", wrapKeyLen, len(key))
	}
	aead, err := getAEAD(key)
	if err != nil {
		return 0, fmt.Errorf("obfs: cipher init: %w", err)
	}
	return obfsUnwrapPacketAEAD(aead, wire, dst)
}

func obfsUnwrapPacketAEAD(aead cipher.AEAD, wire, dst []byte) (int, error) {
	if len(wire) < 13 {
		return 0, errors.New("obfs: packet too short")
	}
	if (wire[0] >> 6) != 2 {
		return 0, errors.New("obfs: not RTP v2")
	}

	seq := binary.BigEndian.Uint16(wire[2:4])
	ts := binary.BigEndian.Uint32(wire[4:8])
	ssrc := binary.BigEndian.Uint32(wire[8:12])

	payloadEnd := len(wire)
	if wire[0]&0x20 != 0 {
		padLen := int(wire[len(wire)-1])
		if padLen == 0 || padLen > payloadEnd-12 {
			return 0, fmt.Errorf("obfs: invalid padding length %d", padLen)
		}
		payloadEnd -= padLen
	}

	ciphertextLen := payloadEnd - 12
	if ciphertextLen <= chacha20poly1305.Overhead {
		return 0, errors.New("obfs: no payload after stripping header/padding")
	}
	if ciphertextLen-chacha20poly1305.Overhead > len(dst) {
		return 0, errors.New("obfs: dst buffer too small")
	}

	var nonce [12]byte
	obfsBuildNonceInto(&nonce, ssrc, seq, ts)
	plain, err := aead.Open(dst[:0], nonce[:], wire[12:payloadEnd], wire[:12])
	if err != nil {
		return 0, fmt.Errorf("obfs: auth: %w", err)
	}
	return len(plain), nil
}

// ─── Detection ───

// obfsIsRTPPacket checks if a raw UDP packet looks like our obfuscated RTP.
func obfsIsRTPPacket(wire []byte) bool {
	if len(wire) < 13 {
		return false
	}
	if (wire[0] >> 6) != 2 {
		return false
	}
	pt := wire[1] & 0x7F
	return pt == 111 || pt == 96
}

package core

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestObfsWrapUnwrapRoundTrip(t *testing.T) {
	key := make([]byte, wrapKeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 1200)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"audio", "video", ""} {
		cfg := NewObfsConfig(mode)
		state := NewObfsState()
		wrapped, err := obfsWrapPacket(key, payload, cfg, state)
		if err != nil {
			t.Fatalf("mode=%q wrap: %v", mode, err)
		}
		if !obfsIsRTPPacket(wrapped) {
			t.Fatalf("mode=%q: wrapped packet not detected as RTP", mode)
		}
		wantPT := uint8(111)
		if mode == "video" {
			wantPT = 96
		}
		if pt := wrapped[1] & 0x7F; pt != wantPT {
			t.Fatalf("mode=%q: PT=%d want %d", mode, pt, wantPT)
		}

		dst := make([]byte, len(payload)+16)
		n, err := obfsUnwrapPacket(key, wrapped, dst)
		if err != nil {
			t.Fatalf("mode=%q unwrap: %v", mode, err)
		}
		if n != len(payload) || !bytes.Equal(dst[:n], payload) {
			t.Fatalf("mode=%q: plaintext mismatch (n=%d)", mode, n)
		}
	}
}

func TestObfsWrapPacketIntoReuse(t *testing.T) {
	key := make([]byte, wrapKeyLen)
	rand.Read(key)
	aead, err := getAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := NewObfsConfig("audio")
	state := NewObfsState()
	payload := []byte("hello-wdtt-obfs")
	dst := make([]byte, obfsWrapWireLen(len(payload), cfg))

	n, err := obfsWrapPacketInto(dst, aead, payload, cfg, state)
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, len(payload))
	m, err := obfsUnwrapPacketAEAD(aead, dst[:n], plain)
	if err != nil {
		t.Fatal(err)
	}
	if m != len(payload) || !bytes.Equal(plain[:m], payload) {
		t.Fatalf("into round-trip failed")
	}
}

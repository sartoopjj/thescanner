package protocol

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func makeToken(t *testing.T) []byte {
	t.Helper()
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return tok
}

func mkQuery(t *testing.T, auth Auth, queryLen int, respLen uint16) *QueryPlaintext {
	t.Helper()
	hdr := auth.headerLen()
	pad := make([]byte, queryLen-hdr)
	if _, err := rand.Read(pad); err != nil {
		t.Fatalf("rand: %v", err)
	}
	nonce, err := NewQueryNonce()
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	return &QueryPlaintext{
		Version: Version1,
		Nonce:   nonce,
		RespLen: respLen,
		Padding: pad,
	}
}

func TestQueryRoundTrip_Short(t *testing.T) {
	tok := makeToken(t)
	q := mkQuery(t, AuthShort, 40, 800)
	wire, err := EncodeQuery(tok, AuthShort, q)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, idx, err := DecodeQuery([][]byte{tok}, AuthShort, wire)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if idx != 0 {
		t.Fatalf("idx = %d, want 0", idx)
	}
	if got.Version != q.Version || got.RespLen != q.RespLen || got.Nonce != q.Nonce {
		t.Fatalf("header mismatch: %+v vs %+v", got, q)
	}
	if !bytes.Equal(got.Padding, q.Padding) {
		t.Fatalf("padding mismatch")
	}
}

func TestQueryRoundTrip_Strong(t *testing.T) {
	tok := makeToken(t)
	q := mkQuery(t, AuthStrong, 60, 1200)
	wire, err := EncodeQuery(tok, AuthStrong, q)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, idx, err := DecodeQuery([][]byte{tok}, AuthStrong, wire)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if idx != 0 {
		t.Fatalf("idx = %d", idx)
	}
	if got.Nonce != q.Nonce || got.RespLen != q.RespLen {
		t.Fatalf("header mismatch")
	}
}

func TestQueryTamper_EveryByteFlipDetected(t *testing.T) {
	tok := makeToken(t)
	q := mkQuery(t, AuthShort, 40, 800)
	wire, err := EncodeQuery(tok, AuthShort, q)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i := range wire {
		// Flipping the nonce bytes changes the keystream input, which causes
		// the entire decryption to differ — HMAC will fail. Flipping any
		// other byte changes one plaintext byte → HMAC fail.
		tampered := append([]byte(nil), wire...)
		tampered[i] ^= 0x01
		if _, _, err := DecodeQuery([][]byte{tok}, AuthShort, tampered); err == nil {
			t.Fatalf("byte %d tamper not detected", i)
		}
	}
}

func TestMultiToken_OnlyMatchingDecodes(t *testing.T) {
	alice := makeToken(t)
	bob := makeToken(t)
	carol := makeToken(t)

	q := mkQuery(t, AuthShort, 50, 600)
	wire, err := EncodeQuery(bob, AuthShort, q)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	tokens := [][]byte{alice, bob, carol}
	_, idx, err := DecodeQuery(tokens, AuthShort, wire)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if idx != 1 {
		t.Fatalf("idx = %d, want 1 (bob)", idx)
	}

	// With only alice + carol, decode must fail.
	if _, _, err := DecodeQuery([][]byte{alice, carol}, AuthShort, wire); err == nil {
		t.Fatalf("expected hmac mismatch with non-matching tokens")
	}
}

func TestResponseRoundTrip(t *testing.T) {
	tok := makeToken(t)
	nonce, _ := NewQueryNonce()
	pad := make([]byte, 200)
	rand.Read(pad)

	wire, err := EncodeResponse(tok, AuthShort, nonce, pad)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	r, err := DecodeResponse(tok, AuthShort, nonce, wire)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.NonceEcho != nonce {
		t.Fatalf("nonce echo mismatch")
	}
	if !bytes.Equal(r.Padding, pad) {
		t.Fatalf("padding mismatch")
	}
}

func TestResponseReplay_WrongNonceRejected(t *testing.T) {
	tok := makeToken(t)
	nonce, _ := NewQueryNonce()
	other, _ := NewQueryNonce()
	pad := make([]byte, 100)
	rand.Read(pad)

	wire, err := EncodeResponse(tok, AuthShort, nonce, pad)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := DecodeResponse(tok, AuthShort, other, wire); err == nil {
		t.Fatalf("expected nonce mismatch error")
	}
}

func TestResponseHMACTamper(t *testing.T) {
	tok := makeToken(t)
	nonce, _ := NewQueryNonce()
	pad := make([]byte, 100)
	rand.Read(pad)
	wire, _ := EncodeResponse(tok, AuthShort, nonce, pad)

	for i := NonceLen; i < len(wire); i++ {
		tampered := append([]byte(nil), wire...)
		tampered[i] ^= 0x80
		if _, err := DecodeResponse(tok, AuthShort, nonce, tampered); err == nil {
			t.Fatalf("byte %d tamper not detected", i)
		}
	}
}

func TestEncodeLabels_SingleAndDouble(t *testing.T) {
	short := make([]byte, 20) // base32 ≈ 32 chars → single
	long := make([]byte, 60)  // base32 ≈ 96 chars → split
	rand.Read(short)
	rand.Read(long)

	if l, err := EncodeLabels(short); err != nil || len(l) != 1 {
		t.Fatalf("short: %v %d", err, len(l))
	}
	l2, err := EncodeLabels(long)
	if err != nil || len(l2) != 2 {
		t.Fatalf("long: %v %d", err, len(l2))
	}
	for _, lab := range l2 {
		if len(lab) > 63 {
			t.Fatalf("label too long: %d", len(lab))
		}
	}

	got, err := DecodeLabels(l2)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, long) {
		t.Fatalf("label round-trip mismatch")
	}
}

func TestLengthConformance_ResponseMatchesRequest(t *testing.T) {
	tok := makeToken(t)
	for _, n := range []int{50, 200, 500, 1000, 1400} {
		nonce, _ := NewQueryNonce()
		pad := make([]byte, n-RespHeaderShort)
		wire, err := EncodeResponse(tok, AuthShort, nonce, pad)
		if err != nil {
			t.Fatalf("n=%d encode: %v", n, err)
		}
		if len(wire) != n {
			t.Fatalf("n=%d: wire len %d", n, len(wire))
		}
	}
}

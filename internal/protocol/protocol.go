// Package protocol implements the wire format described in PROTOCOL.md §3.
package protocol

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"sync"
)

// hmacPool keeps a sync.Pool per token. Each entry is a primed
// hmac.New(sha256.New, key) — the ipad/opad derivation runs once when
// the entry is created, then Reset+Write are cheap. Without pooling
// we redo the ipad/opad work on every HMAC call.
var hmacPool sync.Map // map[*byte]*sync.Pool — keyed by &token[0]

func getHMAC(token []byte) hash.Hash {
	if len(token) == 0 {
		return hmac.New(sha256.New, token)
	}
	key := &token[0]
	p, ok := hmacPool.Load(key)
	if !ok {
		np := &sync.Pool{New: func() any {
			return hmac.New(sha256.New, token)
		}}
		p, _ = hmacPool.LoadOrStore(key, np)
	}
	h := p.(*sync.Pool).Get().(hash.Hash)
	h.Reset()
	return h
}

func putHMAC(token []byte, h hash.Hash) {
	if len(token) == 0 {
		return
	}
	if p, ok := hmacPool.Load(&token[0]); ok {
		p.(*sync.Pool).Put(h)
	}
}

const (
	Version1 byte = 0x01

	NonceLen   = 4
	RespLenLen = 2
	HMACShort  = 4
	HMACStrong = 8

	headerNoHMAC = 1 + NonceLen + RespLenLen // 7
	HeaderShort  = headerNoHMAC + HMACShort  // 11
	HeaderStrong = headerNoHMAC + HMACStrong // 15

	MinPlaintextShort  = HeaderShort + 1  // 12
	MinPlaintextStrong = HeaderStrong + 1 // 16
	MaxPlaintext       = 250              // DNS label encoding cap (see §3.1)

	RespHeaderShort  = NonceLen + HMACShort  // 8
	RespHeaderStrong = NonceLen + HMACStrong // 12

	CtxQuery    = "q"
	CtxResponse = "r"
)

type Auth int

const (
	AuthShort Auth = iota
	AuthStrong
)

func (a Auth) HMACLen() int {
	if a == AuthStrong {
		return HMACStrong
	}
	return HMACShort
}

func (a Auth) HeaderLen() int {
	if a == AuthStrong {
		return HeaderStrong
	}
	return HeaderShort
}

func (a Auth) MinPlaintext() int {
	if a == AuthStrong {
		return MinPlaintextStrong
	}
	return MinPlaintextShort
}

// RespHeaderMin is the minimum response header size (nonce + hmac).
func (a Auth) RespHeaderMin() int {
	return NonceLen + a.HMACLen()
}

// internal aliases used inside this package
func (a Auth) hmacLen() int      { return a.HMACLen() }
func (a Auth) headerLen() int    { return a.HeaderLen() }
func (a Auth) minPlaintext() int { return a.MinPlaintext() }

// QueryPlaintext is the cleartext query before XOR encryption.
type QueryPlaintext struct {
	Version byte
	Nonce   [NonceLen]byte
	RespLen uint16
	Padding []byte
}

// ResponsePlaintext is the cleartext response before XOR encryption.
type ResponsePlaintext struct {
	NonceEcho [NonceLen]byte
	Padding   []byte
}

// keystream derives a length-preserving XOR stream from token, nonce,
// and a per-direction context tag. Implements §3.2. Uses the pooled
// HMAC for the token so we don't re-derive ipad/opad per block.
func keystream(token, nonce []byte, ctx string, length int) []byte {
	out := make([]byte, 0, length+sha256.Size)
	var counter uint32
	var cb [4]byte
	ctxBytes := []byte(ctx)
	for len(out) < length {
		h := getHMAC(token)
		h.Write(nonce)
		h.Write(ctxBytes)
		binary.BigEndian.PutUint32(cb[:], counter)
		h.Write(cb[:])
		out = h.Sum(out)
		putHMAC(token, h)
		counter++
	}
	return out[:length]
}

func xorInto(dst, src, ks []byte) {
	for i := range src {
		dst[i] = src[i] ^ ks[i]
	}
}

// computeHMAC computes HMAC-SHA256 over buf (tag region pre-zeroed by
// the caller), returns the first auth.hmacLen() bytes. Pooled mac.
func computeHMAC(token, buf []byte, tagOff int, auth Auth) []byte {
	_ = tagOff
	n := auth.hmacLen()
	mac := getHMAC(token)
	mac.Write(buf)
	full := mac.Sum(nil)
	putHMAC(token, mac)
	return full[:n]
}

// EncodeQuery serializes and encrypts a query plaintext. The returned wire
// bytes are what get base32-encoded into DNS labels.
func EncodeQuery(token []byte, auth Auth, q *QueryPlaintext) ([]byte, error) {
	hdr := auth.headerLen()
	total := hdr + len(q.Padding)
	if total < auth.minPlaintext() {
		return nil, fmt.Errorf("plaintext too short: %d < %d", total, auth.minPlaintext())
	}
	if total > MaxPlaintext {
		return nil, fmt.Errorf("plaintext too long: %d > %d", total, MaxPlaintext)
	}

	ks := keystream(token, q.Nonce[:], CtxQuery, total)

	// Build the canonical plaintext (what both sides will HMAC over):
	//   [0]                       version
	//   [1..1+NonceLen)           Nonce XOR ks[1..1+NonceLen)  (so on-wire == Nonce)
	//   [1+NonceLen..1+NonceLen+2)  RespLen BE
	//   [hmacOff..hmacOff+hmacLen)  zeroed (HMAC slot)
	//   [hdr..)                   padding (random)
	pt := make([]byte, total)
	pt[0] = q.Version
	for i := 0; i < NonceLen; i++ {
		pt[1+i] = q.Nonce[i] ^ ks[1+i]
	}
	binary.BigEndian.PutUint16(pt[1+NonceLen:], q.RespLen)
	hmacOff := 1 + NonceLen + RespLenLen
	// HMAC tag region already zero from make().
	copy(pt[hdr:], q.Padding)

	tag := computeHMAC(token, pt, hmacOff, auth)
	copy(pt[hmacOff:], tag)

	// Encrypt: ciphertext = plaintext XOR keystream.
	ct := make([]byte, total)
	xorInto(ct, pt, ks)
	// Confirm: ct[1..1+NonceLen) == Nonce (because pt there is Nonce^ks).
	return ct, nil
}

// DecodeQuery decrypts and authenticates an incoming query. Tries each token
// in order; returns the index of the matching token, or -1 if none match.
func DecodeQuery(tokens [][]byte, auth Auth, wire []byte) (q *QueryPlaintext, tokenIdx int, err error) {
	if len(wire) < auth.minPlaintext() {
		return nil, -1, fmt.Errorf("query too short: %d", len(wire))
	}
	if len(wire) > MaxPlaintext {
		return nil, -1, fmt.Errorf("query too long: %d", len(wire))
	}

	var nonce [NonceLen]byte
	copy(nonce[:], wire[1:1+NonceLen])

	hdr := auth.headerLen()
	hmacOff := 1 + NonceLen + RespLenLen

	for idx, tok := range tokens {
		ks := keystream(tok, nonce[:], CtxQuery, len(wire))
		pt := make([]byte, len(wire))
		xorInto(pt, wire, ks)

		// Pull out the candidate HMAC, zero its region, recompute, compare.
		gotTag := make([]byte, auth.hmacLen())
		copy(gotTag, pt[hmacOff:hmacOff+auth.hmacLen()])
		for i := 0; i < auth.hmacLen(); i++ {
			pt[hmacOff+i] = 0
		}
		want := computeHMAC(tok, pt, hmacOff, auth)
		if hmac.Equal(gotTag, want) {
			out := &QueryPlaintext{
				Version: pt[0],
				RespLen: binary.BigEndian.Uint16(pt[1+NonceLen:]),
				Padding: append([]byte(nil), pt[hdr:]...),
			}
			out.Nonce = nonce
			return out, idx, nil
		}
	}
	return nil, -1, errors.New("hmac mismatch (no token matched)")
}

// EncodeResponse serializes and encrypts a response plaintext.
// `nonce` is the on-wire query nonce we're echoing back.
func EncodeResponse(token []byte, auth Auth, nonce [NonceLen]byte, padding []byte) ([]byte, error) {
	hdr := NonceLen + auth.hmacLen()
	total := hdr + len(padding)
	if total < hdr+1 {
		return nil, fmt.Errorf("response too short: %d", total)
	}

	pt := make([]byte, total)
	copy(pt[:NonceLen], nonce[:])
	hmacOff := NonceLen
	// HMAC slot zero, then padding.
	copy(pt[hdr:], padding)

	tag := computeHMAC(token, pt, hmacOff, auth)
	copy(pt[hmacOff:], tag)

	ks := keystream(token, nonce[:], CtxResponse, total)
	ct := make([]byte, total)
	xorInto(ct, pt, ks)
	return ct, nil
}

// DecodeResponse decrypts and authenticates a response. `nonce` is what we
// sent. Returns the parsed response or an error categorizing the failure.
func DecodeResponse(token []byte, auth Auth, nonce [NonceLen]byte, wire []byte) (*ResponsePlaintext, error) {
	hdr := NonceLen + auth.hmacLen()
	if len(wire) < hdr+1 {
		return nil, fmt.Errorf("response too short: %d", len(wire))
	}
	ks := keystream(token, nonce[:], CtxResponse, len(wire))
	pt := make([]byte, len(wire))
	xorInto(pt, wire, ks)

	var echo [NonceLen]byte
	copy(echo[:], pt[:NonceLen])
	if echo != nonce {
		return nil, ErrNonceMismatch
	}

	hmacOff := NonceLen
	gotTag := make([]byte, auth.hmacLen())
	copy(gotTag, pt[hmacOff:hmacOff+auth.hmacLen()])
	for i := 0; i < auth.hmacLen(); i++ {
		pt[hmacOff+i] = 0
	}
	want := computeHMAC(token, pt, hmacOff, auth)
	if !hmac.Equal(gotTag, want) {
		return nil, ErrHMACMismatch
	}

	return &ResponsePlaintext{
		NonceEcho: echo,
		Padding:   append([]byte(nil), pt[hdr:]...),
	}, nil
}

// Categorized errors so the client can label failures (§5).
var (
	ErrNonceMismatch = errors.New("nonce_echo does not match query nonce")
	ErrHMACMismatch  = errors.New("response hmac mismatch")
)

// ---- DNS label encoding (§3.4) ----

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// EncodeLabels turns ciphertext into one or more DNS labels (lowercase
// base32, no padding). Splits into ceil(len/63) labels of ≤ 63 chars each.
// A full DNS name has a 255-byte cap including the suffix; the caller is
// responsible for keeping total length within that.
func EncodeLabels(ct []byte) ([]string, error) {
	enc := strings.ToLower(b32.EncodeToString(ct))
	if len(enc) == 0 {
		return nil, fmt.Errorf("empty ciphertext")
	}
	// Use up to 4 labels (= 252 chars = 157 raw bytes) for v1. Beyond that
	// we'd run into the 255-byte total-name cap anyway.
	const maxLabels = 4
	if len(enc) > 63*maxLabels {
		return nil, fmt.Errorf("encoded length %d exceeds %d-label cap", len(enc), maxLabels)
	}
	n := (len(enc) + 62) / 63
	labels := make([]string, 0, n)
	chunk := (len(enc) + n - 1) / n
	for i := 0; i < len(enc); i += chunk {
		end := i + chunk
		if end > len(enc) {
			end = len(enc)
		}
		labels = append(labels, enc[i:end])
	}
	return labels, nil
}

// DecodeLabels joins query labels (everything before the server suffix),
// uppercases, base32-decodes back to ciphertext.
func DecodeLabels(labels []string) ([]byte, error) {
	joined := strings.ToUpper(strings.Join(labels, ""))
	return b32.DecodeString(joined)
}

// EncodeBase32 returns lowercase base32 with no padding. Used for responses
// going into TXT records — no label-length cap.
func EncodeBase32(b []byte) string {
	return strings.ToLower(b32.EncodeToString(b))
}

// DecodeBase32 decodes a string produced by EncodeBase32 (or any equivalent
// no-padding base32). Case-insensitive.
func DecodeBase32(s string) ([]byte, error) {
	return b32.DecodeString(strings.ToUpper(s))
}

// RandomBytes is a small convenience around crypto/rand.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

// NewQueryNonce returns a random 4-byte nonce.
func NewQueryNonce() ([NonceLen]byte, error) {
	var n [NonceLen]byte
	b, err := RandomBytes(NonceLen)
	if err != nil {
		return n, err
	}
	copy(n[:], b)
	return n, nil
}

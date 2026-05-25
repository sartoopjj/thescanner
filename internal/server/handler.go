package server

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	mathrand "math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/miekg/dns"
	"github.com/sartoopjj/thescanner/internal/protocol"
)

// handlerView is the hot-path state, swapped atomically when the
// admin panel saves new tokens/domains. The Handler keeps an
// atomic.Pointer to one of these and the per-query path does a single
// .Load() — same cost as reading a field, no lock, no race with the
// next reload.
type handlerView struct {
	tokens [][]byte
	names  []string // parallel to tokens, for stats.RecordIdx mapping
	suffs  []string // pre-dotted ".v.example.com."
}

type Handler struct {
	stats *Stats
	auth  protocol.Auth
	view  atomic.Pointer[handlerView]
	// padRng pool: independently-seeded ChaCha8 PRNGs for response
	// padding. ~5 ns/call from a per-P pool slot. Padding doesn't need
	// cryptographic guarantees — the cipher (keystream XOR) hides
	// structure. Just needs to be non-constant across queries.
	padRng sync.Pool
}

func NewHandler(cfg *Config, stats *Stats) *Handler {
	h := &Handler{stats: stats, auth: cfg.AuthMode()}
	h.padRng.New = func() any { return newPadRng() }
	h.Reload(cfg)
	return h
}

// Reload swaps the live tokens/domains atomically. The next ServeDNS
// call sees the new view; in-flight calls finish on the old view (no
// race). Also rewires stats.SetTokenNames so RecordIdx keeps mapping
// indices to the right names.
func (h *Handler) Reload(cfg *Config) {
	v := &handlerView{
		tokens: cfg.TokenBytes(),
		names:  make([]string, len(cfg.Tokens)),
	}
	for i, t := range cfg.Tokens {
		v.names[i] = t.Name
	}
	for _, d := range cfg.Domains {
		v.suffs = append(v.suffs, "."+strings.ToLower(d.Name)+".")
	}
	h.view.Store(v)
	h.stats.SetTokenNames(v.names)
}

// newPadRng builds a fresh ChaCha8 source seeded from crypto/rand.
// Each Pool entry has its own state, so the hot path is lock-free.
func newPadRng() *mathrand.ChaCha8 {
	var seed [32]byte
	if _, err := cryptorand.Read(seed[:]); err != nil {
		// crypto/rand failing is catastrophic; fall back to a
		// pid+counter-ish seed rather than crashing the server.
		binary.LittleEndian.PutUint64(seed[:8], 0xC0FFEE)
	}
	return mathrand.NewChaCha8(seed)
}

// fillPad fills b with non-constant bytes via a pooled ChaCha8.
// See padRng comment in Handler for the rationale.
func (h *Handler) fillPad(b []byte) {
	rng := h.padRng.Get().(*mathrand.ChaCha8)
	_, _ = rng.Read(b)
	h.padRng.Put(rng)
}

// ServeDNS implements dns.Handler. See §4 for the algorithm.
func (h *Handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	if r == nil || len(r.Question) == 0 {
		h.nxdomain(w, r)
		return
	}
	q := r.Question[0]
	if q.Qtype != dns.TypeTXT {
		// v1 supports TXT only. Anything else gets NXDOMAIN.
		h.nxdomain(w, r)
		return
	}

	// One atomic load gives us a consistent (tokens, suffs) snapshot
	// for the rest of this query. A concurrent Reload swap doesn't
	// disturb us.
	view := h.view.Load()

	qname := strings.ToLower(q.Name)
	matched := ""
	for _, s := range view.suffs {
		if strings.HasSuffix(qname, s) {
			matched = s
			break
		}
	}
	if matched == "" {
		h.nxdomain(w, r)
		return
	}

	// `matched` carries its leading dot; TrimSuffix peels off both dots
	// plus the suffix, leaving just the label-content prefix.
	content := strings.TrimSuffix(qname, matched)
	if content == "" {
		h.nxdomain(w, r)
		return
	}
	labels := strings.Split(content, ".")
	ct, err := protocol.DecodeLabels(labels)
	if err != nil || len(ct) < h.auth.MinPlaintext() {
		h.stats.Invalid()
		h.nxdomain(w, r)
		return
	}

	plain, tokenIdx, err := protocol.DecodeQuery(view.tokens, h.auth, ct)
	if err != nil {
		h.stats.Invalid()
		h.nxdomain(w, r)
		return
	}
	if plain.Version != protocol.Version1 {
		h.stats.Invalid()
		h.nxdomain(w, r)
		return
	}

	// Determine max response size (EDNS0 budget).
	maxResp := 512
	if opt := r.IsEdns0(); opt != nil {
		if bs := int(opt.UDPSize()); bs > maxResp {
			maxResp = bs
		}
	}
	// Cap at 4096 per §4.
	if maxResp > 4096 {
		maxResp = 4096
	}
	// DNS framing overhead — leave headroom for TXT encoding + RR header.
	// A generous bound: subtract 80 bytes for DNS wire framing.
	respCap := maxResp - 80
	if respCap < h.auth.RespHeaderMin() {
		h.nxdomain(w, r)
		return
	}

	wantLen := int(plain.RespLen)
	if wantLen < h.auth.RespHeaderMin()+1 {
		wantLen = h.auth.RespHeaderMin() + 1
	}
	if wantLen > respCap {
		wantLen = respCap
	}
	// Response wire bytes must fit into base32-encoded TXT segments → leave
	// some headroom for the base32 expansion (8/5 ratio). Cap wantLen so the
	// encoded form stays within respCap.
	maxRaw := (respCap * 5) / 8
	if wantLen > maxRaw {
		wantLen = maxRaw
	}

	padLen := wantLen - (protocol.NonceLen + h.auth.HMACLen())
	if padLen < 0 {
		h.nxdomain(w, r)
		return
	}
	pad := make([]byte, padLen)
	h.fillPad(pad)

	wire, err := protocol.EncodeResponse(view.tokens[tokenIdx], h.auth, plain.Nonce, pad)
	if err != nil {
		h.nxdomain(w, r)
		return
	}

	// Responses go into TXT records (no DNS-label-length cap). Use the
	// plain base32 encoder, then split into ≤ 255-byte TXT segments.
	encoded := protocol.EncodeBase32(wire)
	txt := splitTXT(encoded, 255)

	resp := new(dns.Msg)
	resp.SetReply(r)
	resp.Authoritative = true
	resp.Answer = append(resp.Answer, &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   q.Name,
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    0,
		},
		Txt: txt,
	})

	// Per-token counter — lock-free atomic on a pre-allocated slot.
	h.stats.RecordIdx(tokenIdx)

	_ = w.WriteMsg(resp)
}

func (h *Handler) nxdomain(w dns.ResponseWriter, r *dns.Msg) {
	if r == nil {
		return
	}
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = dns.RcodeNameError
	_ = w.WriteMsg(m)
}

func splitTXT(s string, n int) []string {
	if len(s) <= n {
		return []string{s}
	}
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

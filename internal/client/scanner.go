package client

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/sartoopjj/thescanner/internal/protocol"
)

// Tester runs queries against one scanner-server endpoint. The same Tester
// is shared across all Level-1/Level-2 workers, so the round-robin domain
// cursor (held here, NOT on ServerEntry) advances globally across them.
type Tester struct {
	Server ServerEntry
	Scan   ScanCfg
	Log    *LogBus // optional; when set, each QueryOnce emits an event
	ListID string  // forwarded onto LogEvent so the UI can filter
	cursor atomic.Uint64
}

// NewTester takes the ServerEntry by value — ServerEntry is plain data,
// safe to copy. The cursor on Tester is what makes the rotation stateful.
func NewTester(s ServerEntry, sc ScanCfg) *Tester {
	return &Tester{Server: s, Scan: sc}
}

// pickDomain returns the next domain to use, advancing the rotation cursor.
func (t *Tester) pickDomain() string {
	i := t.cursor.Add(1) - 1
	return t.Server.PickDomainAt(i)
}

// QueryOnce runs a single query against `resolverIP` (port 53) and returns
// the round-trip outcome.
//
// A LogEvent is emitted to t.Log (if set) when the function returns — so
// callers don't have to wire log calls into every error path.
func (t *Tester) QueryOnce(ctx context.Context, resolverIP string) (status Status, reason FailReason, rtt time.Duration, err error) {
	var domain, qname string
	var qLen, respLen int
	defer func() {
		if t.Log == nil {
			return
		}
		ev := LogEvent{
			Kind:    "query",
			ListID:  t.ListID,
			IP:      resolverIP,
			Domain:  domain,
			QName:   qname,
			Status:  string(status),
			RTTMs:   rtt.Milliseconds(),
			QLen:    qLen,
			RespLen: respLen,
		}
		if status != StatusOK {
			ev.Reason = string(reason)
		}
		t.Log.Publish(ev)
	}()

	auth := protocol.AuthShort
	qlen := randInt(t.Scan.MinQuery, t.Scan.MaxQuery)
	rlen := randInt(t.Scan.MinResponse, t.Scan.MaxResponse)

	if qlen < auth.MinPlaintext() {
		qlen = auth.MinPlaintext()
	}
	if qlen > protocol.MaxPlaintext {
		qlen = protocol.MaxPlaintext
	}
	if rlen < auth.RespHeaderMin()+1 {
		rlen = auth.RespHeaderMin() + 1
	}

	nonce, err := protocol.NewQueryNonce()
	if err != nil {
		return StatusFail, FailNetwork, 0, err
	}
	pad := make([]byte, qlen-auth.HeaderLen())
	if _, err := rand.Read(pad); err != nil {
		return StatusFail, FailNetwork, 0, err
	}
	q := &protocol.QueryPlaintext{
		Version: protocol.Version1,
		Nonce:   nonce,
		RespLen: uint16(rlen),
		Padding: pad,
	}
	tok := []byte(t.Server.Token)
	ct, err := protocol.EncodeQuery(tok, auth, q)
	if err != nil {
		return StatusFail, FailDecode, 0, err
	}
	qLen = len(ct)
	labels, err := protocol.EncodeLabels(ct)
	if err != nil {
		return StatusFail, FailDecode, 0, err
	}
	domain = t.pickDomain()
	if domain == "" {
		return StatusFail, FailDecode, 0, fmt.Errorf("server %q has no domains configured", t.Server.Name)
	}
	qname = strings.Join(labels, ".") + "." + domain + "."

	m := new(dns.Msg)
	m.SetQuestion(qname, dns.TypeTXT)
	if t.Scan.EDNS0 {
		m.SetEdns0(4096, false)
	}

	c := &dns.Client{
		Net:     "udp",
		Timeout: time.Duration(t.Scan.TimeoutSeconds) * time.Second,
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}

	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		if d := time.Until(deadline); d > 0 && d < c.Timeout {
			c.Timeout = d
		}
	}

	addr := net.JoinHostPort(resolverIP, "53")
	start := time.Now()
	resp, rttDur, err := c.ExchangeContext(ctx, m, addr)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return StatusFail, FailTimeout, rttDur, err
		}
		return StatusFail, FailNetwork, rttDur, err
	}
	if rttDur == 0 {
		rttDur = time.Since(start)
	}
	if resp == nil || len(resp.Answer) == 0 {
		return StatusFail, FailNetwork, rttDur, fmt.Errorf("no answer")
	}
	var txt *dns.TXT
	for _, a := range resp.Answer {
		if t, ok := a.(*dns.TXT); ok {
			txt = t
			break
		}
	}
	if txt == nil {
		return StatusFail, FailNetwork, rttDur, fmt.Errorf("no TXT in answer")
	}
	enc := strings.Join(txt.Txt, "")
	respCT, err := protocol.DecodeBase32(enc)
	if err != nil {
		return StatusFail, FailDecode, rttDur, err
	}
	respLen = len(respCT)
	got, err := protocol.DecodeResponse(tok, auth, q.Nonce, respCT)
	if err != nil {
		switch {
		case errors.Is(err, protocol.ErrNonceMismatch):
			return StatusFail, FailStale, rttDur, err
		case errors.Is(err, protocol.ErrHMACMismatch):
			return StatusFail, FailBadAuth, rttDur, err
		default:
			return StatusFail, FailDecode, rttDur, err
		}
	}
	if len(respCT) != rlen {
		// The server may cap response sizes; this is informational only.
		_ = got
	}
	return StatusOK, "", rttDur, nil
}

// noiseDomains is the pool of well-known public hostnames we lookup as
// cover traffic. The list intentionally spans CDN / search / social /
// dev to avoid a single-vertical fingerprint.
var noiseDomains = []string{
	"www.google.com", "www.youtube.com", "www.facebook.com", "www.wikipedia.org",
	"www.amazon.com", "www.microsoft.com", "www.apple.com", "www.cloudflare.com",
	"www.github.com", "www.reddit.com", "www.netflix.com", "www.bing.com",
	"www.yahoo.com", "www.instagram.com", "twitter.com", "www.office.com",
	"www.linkedin.com", "www.zoom.us", "www.dropbox.com", "www.stackoverflow.com",
}

var noiseTypes = []uint16{dns.TypeA, dns.TypeAAAA}

// shouldNoise rolls the per-item dice. Two passes so the actual rate is
// roughly 1/(2*every) — that keeps the decoy load well under the real
// query rate even at low NoiseEvery values, and the doubled randomness
// means the cadence isn't a clean periodic pattern.
func shouldNoise(every int) bool {
	if every <= 0 {
		return false
	}
	if rand.Intn(every) != 0 {
		return false
	}
	// Coin-flip on top of the bucket to spread it out further.
	return rand.Intn(2) == 0
}

// NoiseQueryOnce fires a single "looks normal" DNS lookup at the given
// resolver and discards the result. Used as cover traffic when
// ScanCfg.NoiseEnabled is on.
func (t *Tester) NoiseQueryOnce(ctx context.Context, resolverIP string) {
	domain := noiseDomains[rand.Intn(len(noiseDomains))]
	qtype := noiseTypes[rand.Intn(len(noiseTypes))]

	m := new(dns.Msg)
	m.SetQuestion(domain+".", qtype)
	if t.Scan.EDNS0 {
		m.SetEdns0(4096, false)
	}
	c := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		if d := time.Until(deadline); d > 0 && d < c.Timeout {
			c.Timeout = d
		}
	}
	_, _, _ = c.ExchangeContext(ctx, m, net.JoinHostPort(resolverIP, "53"))

	if t.Log != nil {
		t.Log.Publish(LogEvent{
			Kind:    "noise",
			ListID:  t.ListID,
			IP:      resolverIP,
			Domain:  domain,
			Message: "decoy",
		})
	}
}

func randInt(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + rand.Intn(hi-lo+1)
}

func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}

package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sartoopjj/thescanner/internal/client"
)

// validateConfig: hand-build a Config snapshot, feed it to POST
// /api/config, assert the response code + (when 400) the error
// substring so a copy-paste regression in the message text is loud.
func TestValidateConfig_TableDriven(t *testing.T) {
	good := client.ConfigData{
		Servers: []client.ServerEntry{{
			Name: "s", Domains: []string{"v.example.com"}, Token: "T",
		}},
		Scan: client.ScanCfg{
			MinQuery: 33, MaxQuery: 80,
			MinResponse: 200, MaxResponse: 1200,
			EDNS0: true, Parallel: 500, Duplicate: 1,
			TimeoutSeconds: 10,
			Retries:        3,
			SubnetExpand:   false,
			SubnetMask:     24,
			NoiseEnabled:   true,
			NoiseEvery:     30,
		},
		Level2: client.Level2Cfg{QueriesPerResolver: 100, Parallel: 50},
		UI:     client.UICfg{Listen: "127.0.0.1:8080", Language: "en"},
	}

	cases := []struct {
		name string
		// mutate is applied to a copy of `good` before sending.
		mutate func(*client.ConfigData)
		status int
		errSub string
	}{
		{"baseline", func(*client.ConfigData) {}, 200, ""},
		{"min>max query", func(c *client.ConfigData) {
			c.Scan.MinQuery, c.Scan.MaxQuery = 100, 50
		}, 400, "min_query > max_query"},
		{"min>max response", func(c *client.ConfigData) {
			c.Scan.MinResponse, c.Scan.MaxResponse = 1200, 100
		}, 400, "min_response > max_response"},
		{"parallel zero", func(c *client.ConfigData) { c.Scan.Parallel = 0 }, 400, "parallel"},
		{"retries zero", func(c *client.ConfigData) { c.Scan.Retries = 0 }, 400, "retries"},
		{"subnet mask out of range", func(c *client.ConfigData) {
			c.Scan.SubnetMask = 8
		}, 400, "subnet_mask"},
		{"noise without rate", func(c *client.ConfigData) {
			c.Scan.NoiseEnabled, c.Scan.NoiseEvery = true, 0
		}, 400, "noise_every"},
		{"max_response > 480 without EDNS0", func(c *client.ConfigData) {
			c.Scan.EDNS0, c.Scan.MaxResponse = false, 1200
		}, 400, "EDNS0"},
		{"max_response 480 without EDNS0 ok", func(c *client.ConfigData) {
			c.Scan.EDNS0, c.Scan.MaxResponse = false, 480
		}, 200, ""},
		{"server missing name", func(c *client.ConfigData) {
			c.Servers[0].Name = ""
		}, 400, "name is empty"},
		{"server no domains", func(c *client.ConfigData) {
			c.Servers[0].Domains = nil
		}, 400, "domains"},
		{"server no token", func(c *client.ConfigData) {
			c.Servers[0].Token = ""
		}, 400, "token is empty"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := good
			body.Servers = append([]client.ServerEntry(nil), good.Servers...)
			tc.mutate(&body)

			s, _, _ := newTestServer(t)
			mux := http.NewServeMux()
			s.routes(mux)

			b, _ := json.Marshal(body)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/config", bytes.NewReader(b)))
			if w.Code != tc.status {
				t.Fatalf("status: got %d, want %d, body=%s", w.Code, tc.status, w.Body.String())
			}
			if tc.errSub != "" && !strings.Contains(strings.ToLower(w.Body.String()), strings.ToLower(tc.errSub)) {
				t.Fatalf("body %q doesn't contain %q", w.Body.String(), tc.errSub)
			}
		})
	}
}

// parseResolverList is the workhorse for every resolver-input path
// (UI textarea + file import + JSON API). The regex/parser ate a
// bug once; lock it down with edge-case coverage.
func TestParseResolverList_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			"plain single",
			"8.8.8.8",
			[]string{"8.8.8.8"},
		},
		{
			"dedups",
			"1.1.1.1\n1.1.1.1\n",
			[]string{"1.1.1.1"},
		},
		{
			"comments stripped",
			"# my list\n1.1.1.1  # primary\n2.2.2.2\n",
			[]string{"1.1.1.1", "2.2.2.2"},
		},
		{
			"comma-separated on one line",
			"8.8.8.8, 1.1.1.1, 9.9.9.9",
			[]string{"8.8.8.8", "1.1.1.1", "9.9.9.9"},
		},
		{
			"json-shaped garbage",
			`{"servers":["8.8.8.8","1.1.1.1"]}`,
			[]string{"8.8.8.8", "1.1.1.1"},
		},
		{
			"out-of-range octet rejected",
			"999.1.2.3\n1.1.1.1",
			[]string{"1.1.1.1"},
		},
		{
			"out-of-range cidr rejected",
			"1.1.1.0/40\n1.1.1.1",
			[]string{"1.1.1.1"},
		},
		{
			"cidr /30 expansion",
			"192.0.2.4/30",
			[]string{"192.0.2.4", "192.0.2.5", "192.0.2.6", "192.0.2.7"},
		},
		{
			"empty input",
			"",
			nil,
		},
		{
			"only whitespace + comments",
			"\n  # comment\n\n# another\n   \n",
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseResolverList(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("[%d]: got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

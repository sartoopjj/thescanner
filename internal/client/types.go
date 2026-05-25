package client

import "time"

// Per-IP status during a scan.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusOK         Status = "ok"
	StatusFail       Status = "fail"
)

// Categorised failure reasons surfaced in Result.Reason.
type FailReason string

const (
	FailTimeout     FailReason = "timeout"
	FailBadAuth     FailReason = "bad-auth"
	FailStale       FailReason = "stale"
	FailWrongLength FailReason = "wrong-length"
	FailNetwork     FailReason = "network"
	FailDecode      FailReason = "decode"
)

// Result is the per-IP record. Shallow-scan fields (Status/Reason/RTTMs/
// Attempts) come from the first pass; deep-scan fields (L2*) get filled
// in only when a deep scan runs over this IP.
type Result struct {
	IP        string     `json:"ip"`
	Status    Status     `json:"status"`
	Reason    FailReason `json:"reason,omitempty"`
	RTTMs     int64      `json:"rtt_ms,omitempty"`
	Attempts  int        `json:"attempts,omitempty"`
	Source    string     `json:"source,omitempty"` // "initial" | "subnet" | "manual"
	L2Total   int        `json:"l2_total,omitempty"`
	L2OK      int        `json:"l2_ok,omitempty"`
	L2P95Ms   int64      `json:"l2_p95_ms,omitempty"`
	L2Score   float64    `json:"l2_score,omitempty"`
	UpdatedAt time.Time  `json:"updated_at,omitempty"`
}

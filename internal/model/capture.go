package model

import "time"

// AnalyzerVersion identifies interpretation rules independently from binary and
// wire schema versions. Change it when identical evidence may yield different
// normalized findings or coverage outcomes.
const AnalyzerVersion = "1"

const (
	KubeSystemUIDProvenance = "kube-system-namespace-uid-sha256"
	NamespaceUIDProvenance  = "scoped-namespace-uid-sha256"
)

// SourceTiming describes an observation interval within a non-atomic capture.
type SourceTiming struct {
	Source     Source    `json:"source"`
	Resource   string    `json:"resource,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

package model

import "time"

const HistorySchemaVersion = "1"

type HistoryStatus string

const (
	HistoryComplete     HistoryStatus = "complete"
	HistoryPartial      HistoryStatus = "partial"
	HistoryUnavailable  HistoryStatus = "unavailable"
	HistoryPermission   HistoryStatus = "permission_denied"
	HistoryFailed       HistoryStatus = "failed"
	HistoryTruncated    HistoryStatus = "truncated"
	HistoryInsufficient HistoryStatus = "insufficient"
)

// HistoryReason values classify outcomes without retaining provider text.
type HistoryReason string

const (
	HistoryNoTargets        HistoryReason = "no_eligible_targets"
	HistoryTargetsLimited   HistoryReason = "target_budget_exceeded"
	HistoryRequestFailed    HistoryReason = "request_failed"
	HistoryRequestDenied    HistoryReason = "request_denied"
	HistoryRequestTimeout   HistoryReason = "request_timeout_or_canceled"
	HistoryBadResponse      HistoryReason = "unexpected_response"
	HistoryResponseLimited  HistoryReason = "response_budget_exceeded"
	HistoryResponseWarnings HistoryReason = "provider_annotations"
	HistoryMetricAbsent     HistoryReason = "metric_absent"
	HistoryAmbiguousSeries  HistoryReason = "ambiguous_series"
	HistoryBadSamples       HistoryReason = "invalid_or_unpaired_samples"
	HistorySparseSamples    HistoryReason = "insufficient_scrape_coverage"
	HistoryCounterReset     HistoryReason = "counter_reset"
)

type HistoryTarget struct {
	Pod         Resource `json:"pod"`
	Workload    Resource `json:"workload"`
	Container   string   `json:"container"`
	VPADisabled bool     `json:"vpa_disabled"`
}

// HistoryPoint carries a timestamp and a value. The series specifies the unit.
type HistoryPoint struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

type HistorySeries struct {
	Status HistoryStatus  `json:"status"`
	Reason HistoryReason  `json:"reason,omitempty"`
	Points []HistoryPoint `json:"points"`
}

type GrafanaDashboard struct {
	Origin string `json:"origin"`
	UID    string `json:"uid"`
	Slug   string `json:"slug"`
}

type HistoryTargetCapture struct {
	Target HistoryTarget `json:"target"`
	// CPUCounter is cumulative CPU seconds. Memory is working-set bytes.
	CPUCounter HistorySeries `json:"cpu_counter"`
	Memory     HistorySeries `json:"memory"`
}

type HistoryCapture struct {
	SchemaVersion string                 `json:"schema_version"`
	Status        HistoryStatus          `json:"status"`
	Reason        HistoryReason          `json:"reason,omitempty"`
	StartedAt     time.Time              `json:"started_at"`
	FinishedAt    time.Time              `json:"finished_at"`
	WindowStart   time.Time              `json:"window_start"`
	WindowEnd     time.Time              `json:"window_end"`
	StepSeconds   int64                  `json:"step_seconds"`
	ScrapeSeconds int64                  `json:"scrape_seconds"`
	Targets       []HistoryTargetCapture `json:"targets"`
	Grafana       *GrafanaDashboard      `json:"grafana,omitempty"`
}

type HistoryAdvice struct {
	CurrentRequest *float64 `json:"current_request,omitempty"`
	CurrentLimit   *float64 `json:"current_limit,omitempty"`
	Request        *float64 `json:"request,omitempty"`
	Reason         string   `json:"reason"`
}

type HistoryMetricReport struct {
	Status          HistoryStatus  `json:"status"`
	Reason          HistoryReason  `json:"reason,omitempty"`
	Unit            string         `json:"unit"`
	DistinctScrapes int            `json:"distinct_scrapes"`
	ExpectedScrapes int            `json:"expected_scrapes"`
	Samples         []HistoryPoint `json:"samples"`
	P50             *float64       `json:"p50,omitempty"`
	P95             *float64       `json:"p95,omitempty"`
	Peak            *float64       `json:"peak,omitempty"`
	Advice          HistoryAdvice  `json:"advice"`
}

type HistoryTargetReport struct {
	Target     HistoryTarget       `json:"target"`
	CPU        HistoryMetricReport `json:"cpu"`
	Memory     HistoryMetricReport `json:"memory"`
	GrafanaURL string              `json:"grafana_url,omitempty"`
}

type HistoryReport struct {
	SchemaVersion string                `json:"schema_version"`
	Status        HistoryStatus         `json:"status"`
	Reason        HistoryReason         `json:"reason,omitempty"`
	StartedAt     time.Time             `json:"started_at"`
	FinishedAt    time.Time             `json:"finished_at"`
	WindowStart   time.Time             `json:"window_start"`
	WindowEnd     time.Time             `json:"window_end"`
	StepSeconds   int64                 `json:"step_seconds"`
	ScrapeSeconds int64                 `json:"scrape_seconds"`
	Targets       []HistoryTargetReport `json:"targets"`
}

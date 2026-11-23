// Package investigation builds and compares deterministic views of normalized evidence.
package investigation

import (
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

const (
	SchemaVersion           = "1"
	ComparisonSchemaVersion = "1"
)

// Investigation is the shared view for CLI, JSON, HTML and MCP. Findings and
// their evidence are retained in Report. Other sections refer to them by index.
type Investigation struct {
	SchemaVersion      string                `json:"schema_version"`
	RuleVersion        string                `json:"rule_version"`
	CaptureRuleVersion string                `json:"capture_rule_version"`
	AnalysisMode       string                `json:"analysis_mode"`
	ClusterID          string                `json:"cluster_id,omitempty"`
	ClusterProvenance  string                `json:"cluster_provenance,omitempty"`
	Scope              model.Scope           `json:"scope"`
	CaptureStartedAt   time.Time             `json:"capture_started_at"`
	CaptureCompletedAt time.Time             `json:"capture_completed_at"`
	EvaluatedAt        time.Time             `json:"evaluated_at"`
	SourceTimings      []model.SourceTiming  `json:"source_timings"`
	Report             model.Report          `json:"report"`
	Relationships      []Relationship        `json:"relationships"`
	Incidents          []Incident            `json:"incidents"`
	Timeline           []TimelineEntry       `json:"timeline"`
	Resources          []ResourceObservation `json:"resources"`
	Limitations        []string              `json:"limitations"`
	Truncated          bool                  `json:"truncated"`
	History            *model.HistoryReport  `json:"history,omitempty"`
}

// ResourceObservation describes an observed object instance and its strongest
// UID-validated workload assignment. Declared but unobserved targets remain
// available as relationship endpoints.
type ResourceObservation struct {
	Resource     model.Resource      `json:"resource"`
	Workload     *model.Resource     `json:"workload,omitempty"`
	Source       model.Source        `json:"source"`
	CreatedAt    *time.Time          `json:"created_at,omitempty"`
	Active       bool                `json:"active"`
	StatusFresh  bool                `json:"status_fresh"`
	Ready        bool                `json:"ready"`
	ExpectedPods *int32              `json:"expected_pods,omitempty"`
	Service      *ServiceObservation `json:"service,omitempty"`
}

type ServiceObservation struct {
	BackendMode     string                 `json:"backend_mode"`
	Headless        bool                   `json:"headless"`
	PublishNotReady bool                   `json:"publish_not_ready"`
	DeclaredPorts   []ServicePortReference `json:"declared_ports"`
}

type ServicePortReference struct {
	Name     string `json:"name,omitempty"`
	Number   int32  `json:"number"`
	Protocol string `json:"protocol"`
}

type RelationshipKind string

const (
	Owns              RelationshipKind = "owned_by"
	ServiceBackend    RelationshipKind = "service_backend"
	EndpointTarget    RelationshipKind = "endpoint_target"
	ServiceSelectsPod RelationshipKind = "service_selects_pod"
	IngressService    RelationshipKind = "ingress_service"
	PodConfig         RelationshipKind = "pod_configuration"
	PodStorage        RelationshipKind = "pod_storage"
	PodNode           RelationshipKind = "pod_node"
	AutoscalingTarget RelationshipKind = "autoscaling_target"
	DisruptionTarget  RelationshipKind = "disruption_target"
	CronJobActiveJob  RelationshipKind = "cronjob_active_job"
)

type MatchBasis string

const (
	MatchUID      MatchBasis = "declared_uid"
	MatchName     MatchBasis = "declared_name"
	MatchSelector MatchBasis = "inferred_selector"
	MatchLabel    MatchBasis = "inferred_label"
)

type Relationship struct {
	Kind           RelationshipKind `json:"kind"`
	From           model.Resource   `json:"from"`
	To             model.Resource   `json:"to"`
	Source         model.Source     `json:"source"`
	Basis          MatchBasis       `json:"basis"`
	TargetObserved bool             `json:"target_observed"`
	Evidence       []model.Evidence `json:"evidence"`
}

type TimelineTimeKind string

const (
	OccurrenceTime  TimelineTimeKind = "occurrence"
	TransitionTime  TimelineTimeKind = "transition"
	ObservationTime TimelineTimeKind = "observation"
	CollectionTime  TimelineTimeKind = "collection"
)

type TimelineEntry struct {
	Time      time.Time        `json:"time"`
	TimeKind  TimelineTimeKind `json:"time_kind"`
	Resource  *model.Resource  `json:"resource,omitempty"`
	Source    model.Source     `json:"source"`
	Summary   string           `json:"summary"`
	Evidence  []model.Evidence `json:"evidence"`
	Uncertain bool             `json:"uncertain,omitempty"`
}

type Explanation struct {
	Summary        string           `json:"summary"`
	Confidence     model.Confidence `json:"confidence"`
	FindingIndexes []int            `json:"finding_indexes"`
}

type Incident struct {
	ID                  string           `json:"id"`
	Anchor              model.Resource   `json:"anchor"`
	Severity            model.Severity   `json:"severity"`
	Members             []model.Resource `json:"members"`
	FindingIndexes      []int            `json:"finding_indexes"`
	RelationshipIndexes []int            `json:"relationship_indexes"`
	TimelineIndexes     []int            `json:"timeline_indexes"`
	Observations        []Explanation    `json:"observations"`
	Corroboration       []Explanation    `json:"corroboration"`
	Candidates          []Explanation    `json:"candidate_explanations"`
	Alternatives        []Explanation    `json:"alternative_explanations"`
	MissingEvidence     []string         `json:"missing_evidence"`
	NextSteps           []string         `json:"next_steps"`
	RecoveryChecks      []Explanation    `json:"recovery_checks"`
}

type CaptureRef struct {
	ClusterID          string      `json:"cluster_id,omitempty"`
	ClusterProvenance  string      `json:"cluster_provenance,omitempty"`
	Scope              model.Scope `json:"scope"`
	SchemaVersion      string      `json:"investigation_schema_version"`
	RuleVersion        string      `json:"rule_version"`
	CaptureRuleVersion string      `json:"capture_rule_version"`
	AnalysisMode       string      `json:"analysis_mode"`
	CaptureStartedAt   time.Time   `json:"capture_started_at"`
	CaptureCompletedAt time.Time   `json:"capture_completed_at"`
	EvaluatedAt        time.Time   `json:"evaluated_at"`
}

type Classification string

const (
	New                Classification = "new"
	Persistent         Classification = "persistent"
	Changed            Classification = "changed"
	Resolved           Classification = "resolved"
	NoLongerObservable Classification = "no_longer_observable"
	NotComparable      Classification = "not_comparable"
)

type FindingOccurrence struct {
	Index   int           `json:"index"`
	Finding model.Finding `json:"finding"`
}

type Change struct {
	RuleID         string              `json:"rule_id"`
	Subject        model.Resource      `json:"subject"`
	Condition      string              `json:"condition"`
	Classification Classification      `json:"classification"`
	Reason         string              `json:"reason"`
	Before         []FindingOccurrence `json:"before"`
	After          []FindingOccurrence `json:"after"`
	Differences    []string            `json:"differences"`
}

type CoverageChange struct {
	Source   model.Source   `json:"source"`
	Resource string         `json:"resource,omitempty"`
	Before   model.Coverage `json:"before"`
	After    model.Coverage `json:"after"`
}

type Comparison struct {
	SchemaVersion   string             `json:"schema_version"`
	Before          CaptureRef         `json:"before"`
	After           CaptureRef         `json:"after"`
	Compatible      bool               `json:"compatible"`
	Complete        bool               `json:"complete"`
	Limitations     []string           `json:"limitations"`
	CoverageChanges []CoverageChange   `json:"coverage_changes"`
	ResourceChanges []Change           `json:"resource_changes"`
	WorkloadChanges []Change           `json:"workload_changes"`
	History         *HistoryComparison `json:"history,omitempty"`
}

type HistoryComparison struct {
	BeforeStatus      string `json:"before_status"`
	AfterStatus       string `json:"after_status"`
	BeforeTargetCount int    `json:"before_target_count"`
	AfterTargetCount  int    `json:"after_target_count"`
	ComparableTargets int    `json:"comparable_targets"`
	LostTargets       int    `json:"lost_targets"`
	NewTargets        int    `json:"new_targets"`
	Complete          bool   `json:"complete"`
}

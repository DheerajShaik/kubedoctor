package model

import "time"

const (
	SourceStatefulSets  Source = "statefulsets"
	SourceJobs          Source = "jobs"
	SourceCronJobs      Source = "cronjobs"
	SourceHPAs          Source = "horizontalpodautoscalers"
	SourceQuotas        Source = "resourcequotas"
	SourceLimitRanges   Source = "limitranges"
	SourceNodeLocalDNS  Source = "node_local_dns"
	SourceNodeLocalPods Source = "node_local_dns_pods"
	SourceProvenance    Source = "cluster_provenance"
)

// ExpansionEvidence contains only reviewed observations used by the additional
// workload rules. It is independent of Kubernetes API object serialization.
type ExpansionEvidence struct {
	StatefulSets    []StatefulSetEvidence    `json:"stateful_sets,omitempty"`
	Jobs            []JobEvidence            `json:"jobs,omitempty"`
	CronJobs        []CronJobEvidence        `json:"cron_jobs,omitempty"`
	HPAs            []HPAEvidence            `json:"horizontal_pod_autoscalers,omitempty"`
	Quotas          []QuotaEvidence          `json:"resource_quotas,omitempty"`
	LimitRanges     []LimitRangeEvidence     `json:"limit_ranges,omitempty"`
	Probes          []PodProbeEvidence       `json:"pod_probes,omitempty"`
	AdmissionEvents []AdmissionEventEvidence `json:"admission_events,omitempty"`
	ProbeEvents     []ProbeEventEvidence     `json:"probe_events,omitempty"`
	NodeLocalDNS    *NodeLocalDNSEvidence    `json:"node_local_dns,omitempty"`
	NodeLocalPods   []NodeLocalPodEvidence   `json:"node_local_pods,omitempty"`
}

type BriefCondition struct {
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	Transition time.Time `json:"transition"`
}

type StatefulSetEvidence struct {
	Resource             Resource  `json:"resource"`
	Created              time.Time `json:"created"`
	Deleting             bool      `json:"deleting,omitempty"`
	Generation           int64     `json:"generation"`
	ObservedGeneration   int64     `json:"observed_generation"`
	Desired              int32     `json:"desired"`
	Ready                int32     `json:"ready"`
	Available            int32     `json:"available"`
	Updated              int32     `json:"updated"`
	StartOrdinal         int32     `json:"start_ordinal"`
	Partition            int32     `json:"partition"`
	MinReadySeconds      int32     `json:"min_ready_seconds"`
	Management           string    `json:"management"`
	Strategy             string    `json:"strategy"`
	RevisionState        string    `json:"revision_state,omitempty"`
	VolumeClaimTemplates []string  `json:"volume_claim_templates,omitempty"`
	TemplatesTruncated   bool      `json:"templates_truncated,omitempty"`
}

type JobEvidence struct {
	Resource        Resource         `json:"resource"`
	Owner           *Resource        `json:"owner,omitempty"`
	Created         time.Time        `json:"created"`
	Deleting        bool             `json:"deleting,omitempty"`
	Started         time.Time        `json:"started"`
	Completed       time.Time        `json:"completed"`
	Suspended       bool             `json:"suspended,omitempty"`
	DeadlineSeconds *int64           `json:"deadline_seconds,omitempty"`
	BackoffLimit    *int32           `json:"backoff_limit,omitempty"`
	Parallelism     *int32           `json:"parallelism,omitempty"`
	Active          int32            `json:"active"`
	Failed          int32            `json:"failed"`
	Succeeded       int32            `json:"succeeded"`
	Conditions      []BriefCondition `json:"conditions,omitempty"`
}

type CronJobEvidence struct {
	Resource        Resource   `json:"resource"`
	Created         time.Time  `json:"created"`
	Deleting        bool       `json:"deleting,omitempty"`
	Suspended       bool       `json:"suspended,omitempty"`
	Concurrency     string     `json:"concurrency"`
	DeadlineSeconds *int64     `json:"starting_deadline_seconds,omitempty"`
	LastScheduled   time.Time  `json:"last_scheduled"`
	LastSuccessful  time.Time  `json:"last_successful"`
	ActiveJobs      []Resource `json:"active_jobs,omitempty"`
	ActiveTruncated bool       `json:"active_truncated,omitempty"`
}

type HPAEvidence struct {
	Resource           Resource               `json:"resource"`
	Created            time.Time              `json:"created"`
	Deleting           bool                   `json:"deleting,omitempty"`
	Target             Resource               `json:"target"`
	TargetAPIVersion   string                 `json:"target_api_version,omitempty"`
	Conditions         []BriefCondition       `json:"conditions,omitempty"`
	Generation         int64                  `json:"generation"`
	ObservedGeneration int64                  `json:"observed_generation"`
	CurrentReplicas    int32                  `json:"current_replicas"`
	DesiredReplicas    int32                  `json:"desired_replicas"`
	MinReplicas        int32                  `json:"min_replicas"`
	MaxReplicas        int32                  `json:"max_replicas"`
	UtilizationTargets []HPAUtilizationTarget `json:"utilization_targets,omitempty"`
	TargetsTruncated   bool                   `json:"targets_truncated,omitempty"`
}

type HPAUtilizationTarget struct {
	Resource  string `json:"resource"`
	Container string `json:"container,omitempty"`
	Percent   int32  `json:"percent"`
}

func HPAWarningReason(reason string) bool {
	switch reason {
	case "FailedGetScale", "FailedUpdateScale", "FailedRescale", "FailedGetResourceMetric", "FailedGetContainerResourceMetric",
		"FailedGetPodsMetric", "FailedGetObjectMetric", "FailedGetExternalMetric", "FailedComputeMetricsReplicas", "InvalidMetricSourceType":
		return true
	default:
		return false
	}
}

type QuotaEvidence struct {
	Resource   Resource        `json:"resource"`
	Created    time.Time       `json:"created"`
	Capacities []QuotaCapacity `json:"capacities,omitempty"`
}

type QuotaCapacity struct {
	Resource string `json:"resource"`
	Hard     string `json:"hard,omitempty"`
	Used     string `json:"used,omitempty"`
	Current  bool   `json:"current"`
}

type LimitRangeEvidence struct {
	Resource    Resource          `json:"resource"`
	Created     time.Time         `json:"created"`
	Constraints []LimitConstraint `json:"constraints,omitempty"`
}

type LimitConstraint struct {
	Scope    string `json:"scope"`
	Resource string `json:"resource"`
	Type     string `json:"type"`
}

type PodProbeEvidence struct {
	Pod        Resource         `json:"pod"`
	Containers []ContainerProbe `json:"containers,omitempty"`
	Truncated  bool             `json:"truncated,omitempty"`
}

type ContainerProbe struct {
	Name  string   `json:"name"`
	Role  string   `json:"role"`
	Kinds []string `json:"kinds"`
}

type TimedReference struct {
	Resource Resource  `json:"resource"`
	Time     time.Time `json:"time"`
	Count    int32     `json:"count"`
}

type AdmissionEventEvidence struct {
	Event        TimedReference `json:"event"`
	Constraint   string         `json:"constraint"`
	Quota        string         `json:"quota,omitempty"`
	ResourceName string         `json:"resource,omitempty"`
	LimitScope   string         `json:"limit_scope,omitempty"`
}

type ProbeEventEvidence struct {
	Event     TimedReference `json:"event"`
	Container string         `json:"container"`
	Role      string         `json:"role"`
	Kind      string         `json:"kind"`
}

type NodeLocalDNSEvidence struct {
	Resource           Resource  `json:"resource"`
	Created            time.Time `json:"created"`
	Generation         int64     `json:"generation"`
	ObservedGeneration int64     `json:"observed_generation"`
	Desired            int32     `json:"desired"`
	Ready              int32     `json:"ready"`
}

type NodeLocalPodEvidence struct {
	Pod      Resource  `json:"pod"`
	Owner    *Resource `json:"owner,omitempty"`
	Node     string    `json:"node,omitempty"`
	Ready    string    `json:"ready,omitempty"`
	Deleting bool      `json:"deleting,omitempty"`
}

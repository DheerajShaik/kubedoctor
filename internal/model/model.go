// Package model defines the diagnostic data shared by collectors and interfaces.
package model

import (
	"time"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	policy "k8s.io/api/policy/v1"
	storage "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const SchemaVersion = "1"

type ScopeKind string

const (
	NamespaceScope ScopeKind = "namespace"
	ClusterScope   ScopeKind = "cluster"
)

type Scope struct {
	Kind      ScopeKind `json:"kind"`
	Namespace string    `json:"namespace,omitempty"`
	Context   string    `json:"context"`
}

type ScanRequest struct {
	Scope             Scope
	IncludeProvenance bool
}

type Severity string

const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

func (s Severity) Rank() int {
	switch s {
	case Info:
		return 1
	case Warning:
		return 2
	case Critical:
		return 3
	default:
		return 0
	}
}

type Confidence string

const (
	Observed   Confidence = "observed"
	Likely     Confidence = "likely"
	Hypothesis Confidence = "hypothesis"
)

type Resource struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

func Identity(kind string, obj metav1.Object) Resource {
	return Resource{Kind: kind, Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: string(obj.GetUID())}
}

type Evidence struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

type Finding struct {
	RuleID        string     `json:"rule_id"`
	Resource      Resource   `json:"resource"`
	Severity      Severity   `json:"severity"`
	Symptom       string     `json:"symptom"`
	Evidence      []Evidence `json:"evidence"`
	Confidence    Confidence `json:"confidence"`
	ProbableCause string     `json:"probable_cause,omitempty"`
	NextSteps     []string   `json:"next_steps"`
	Incident      *Resource  `json:"incident,omitempty"`
}

type CheckDefinition struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type CheckStatus string

const (
	Checked      CheckStatus = "checked"
	Skipped      CheckStatus = "skipped"
	Inconclusive CheckStatus = "inconclusive"
)

type CheckResult struct {
	ID     string      `json:"id"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
}

type Source string

const (
	SourceNamespace          Source = "namespace"
	SourceNodes              Source = "nodes"
	SourcePods               Source = "pods"
	SourceReplicaSets        Source = "replicasets"
	SourceDeployments        Source = "deployments"
	SourceDaemonSets         Source = "daemonsets"
	SourceEvents             Source = "events"
	SourceConfigMaps         Source = "configmaps"
	SourcePVCs               Source = "persistentvolumeclaims"
	SourceStorageClasses     Source = "storageclasses"
	SourceServices           Source = "services"
	SourceEndpointSlices     Source = "endpointslices"
	SourceLegacyEndpoints    Source = "endpoints"
	SourceIngresses          Source = "ingresses"
	SourceDNSPods            Source = "dns_pods"
	SourceDNSServices        Source = "dns_services"
	SourceDNSConfig          Source = "dns_configuration"
	SourceDNSEndpointSlices  Source = "dns_endpointslices"
	SourceDNSLegacyEndpoints Source = "dns_endpoints"
	SourceNetworkPolicies    Source = "networkpolicies"
	SourcePDBs               Source = "poddisruptionbudgets"
	SourceMetrics            Source = "pod_metrics"
)

type Coverage string

const (
	Complete         Coverage = "complete"
	NotApplicable    Coverage = "not_applicable"
	PermissionDenied Coverage = "permission_denied"
	Unavailable      Coverage = "unavailable"
	Failed           Coverage = "failed"
	Truncated        Coverage = "truncated"
	NotInspected     Coverage = "not_inspected"
)

type SourceResult struct {
	Source   Source   `json:"source"`
	Status   Coverage `json:"status"`
	Required bool     `json:"required"`
	Resource string   `json:"resource,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

type Event struct {
	Resource  Resource
	Reason    string
	Time      time.Time
	TimeBasis string
	Count     int32
	Reference *ConfigIssue
}

type ConfigIssue struct {
	Kind    string
	Name    string
	Key     string
	Missing string // object or key
}

type ConfigMap struct {
	Namespace  string
	Name       string
	Keys       []string
	BinaryKeys []string
}

type DNSConfig struct {
	Name        string
	Namespace   string
	HasCorefile bool
}

type PodUsage struct {
	Namespace  string
	Name       string
	Timestamp  time.Time
	Window     time.Duration
	Containers []ContainerUsage
}

type ContainerUsage struct {
	Name   string
	CPU    resource.Quantity
	Memory resource.Quantity
}

// Snapshot stays internal. The collector projects metadata and only fields
// needed by rules. Exported evidence is separately projected by the bundle package.
type Snapshot struct {
	Scope              Scope
	CollectedAt        time.Time
	StartedAt          time.Time
	FinishedAt         time.Time
	ClusterID          string
	ClusterProvenance  string
	SourceTimings      []SourceTiming
	Sources            []SourceResult
	Nodes              []core.Node
	Pods               []core.Pod
	ReplicaSets        []apps.ReplicaSet
	Deployments        []apps.Deployment
	DaemonSets         []apps.DaemonSet
	Events             []Event
	ConfigMaps         []ConfigMap
	PVCs               []core.PersistentVolumeClaim
	StorageClasses     []storage.StorageClass
	Services           []core.Service
	EndpointSlices     []discovery.EndpointSlice
	LegacyEndpoints    []core.Endpoints
	Ingresses          []networking.Ingress
	DNSPods            []core.Pod
	DNSServices        []core.Service
	DNSConfig          []DNSConfig
	DNSEndpointSlices  []discovery.EndpointSlice
	DNSLegacyEndpoints []core.Endpoints
	NetworkPolicies    []networking.NetworkPolicy
	PDBs               []policy.PodDisruptionBudget
	Metrics            []PodUsage
	Expansion          ExpansionEvidence
	History            *HistoryCapture
}

// Coverage describes whether negative conclusions may rely on a source.
// Complete named observations can coexist with an incomplete inventory.
func (s Snapshot) Coverage(source Source) Coverage {
	status := NotInspected
	for _, observation := range s.Sources {
		if observation.Source != source {
			continue
		}
		if observation.Status != Complete && observation.Status != NotApplicable {
			return observation.Status
		}
		if observation.Status == Complete || status == NotInspected {
			status = observation.Status
		}
	}
	return status
}

type Report struct {
	SchemaVersion    string         `json:"schema_version"`
	Scope            Scope          `json:"scope"`
	CollectedAt      time.Time      `json:"collected_at"`
	Complete         bool           `json:"complete"`
	Findings         []Finding      `json:"findings"`
	CollectionErrors []SourceResult `json:"collection_errors"`
	Sources          []SourceResult `json:"sources"`
	Checks           []CheckResult  `json:"checks"`
}

// Package bundle persists bounded, reviewed diagnostic observations for offline analysis.
package bundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/artifact"
	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
)

const SchemaVersion = "1"
const MaxBytes = 32 << 20

var (
	ErrFormat          = errors.New("invalid incident bundle")
	ErrVersion         = errors.New("unsupported incident bundle schema version")
	ErrIntegrity       = errors.New("incident bundle integrity check failed")
	ErrLimit           = errors.New("incident bundle exceeds a processing limit")
	ErrAnalyzerVersion = errors.New("bundle uses a different analyzer version; use stored results or explicitly reevaluate with current rules")
	ErrReplayMismatch  = errors.New("stored analysis differs from replayed evidence under its claimed analyzer version")
	ErrMode            = errors.New("invalid bundle analysis mode")
)

type Mode string

const (
	Stored     Mode = "stored"
	Replay     Mode = "replay"
	Reevaluate Mode = "reevaluate"
)

type Analysis struct {
	Mode                  Mode         `json:"mode"`
	BundleAnalyzerVersion string       `json:"bundle_analyzer_version"`
	AnalyzerVersion       string       `json:"analyzer_version"`
	InterpretationChanged bool         `json:"interpretation_changed"`
	Report                model.Report `json:"report"`
}

type Bundle struct {
	data    *payload
	encoded []byte
	digest  string
}

type envelope struct {
	SchemaVersion string          `json:"schema_version"`
	Integrity     string          `json:"sha256"`
	Payload       json.RawMessage `json:"payload"`
}

type payload struct {
	Scope             model.Scope          `json:"scope"`
	StartedAt         time.Time            `json:"started_at"`
	FinishedAt        time.Time            `json:"finished_at"`
	EvaluatedAt       time.Time            `json:"evaluated_at"`
	ClusterID         string               `json:"cluster_id,omitempty"`
	ClusterProvenance string               `json:"cluster_provenance,omitempty"`
	Producer          string               `json:"producer"`
	AnalyzerVersion   string               `json:"analyzer_version"`
	Sources           []model.SourceResult `json:"sources"`
	SourceTimings     []model.SourceTiming `json:"source_timings"`
	Evidence          evidence             `json:"observations"`
	Stored            model.Report         `json:"stored_report"`
}

type evidence struct {
	Nodes              []node                  `json:"nodes,omitempty"`
	Pods               []pod                   `json:"pods,omitempty"`
	ReplicaSets        []replicaSet            `json:"replica_sets,omitempty"`
	Deployments        []deployment            `json:"deployments,omitempty"`
	DaemonSets         []daemonSet             `json:"daemon_sets,omitempty"`
	Events             []classifiedEvent       `json:"events,omitempty"`
	ConfigMaps         []configMap             `json:"config_maps,omitempty"`
	Claims             []claim                 `json:"claims,omitempty"`
	StorageClasses     []storageClass          `json:"storage_classes,omitempty"`
	Services           []service               `json:"services,omitempty"`
	EndpointSlices     []endpointSlice         `json:"endpoint_slices,omitempty"`
	LegacyEndpoints    []legacyEndpoints       `json:"legacy_endpoints,omitempty"`
	Ingresses          []ingress               `json:"ingresses,omitempty"`
	DNSPods            []pod                   `json:"dns_pods,omitempty"`
	DNSServices        []service               `json:"dns_services,omitempty"`
	DNSConfig          []dnsConfig             `json:"dns_configuration,omitempty"`
	DNSEndpointSlices  []endpointSlice         `json:"dns_endpoint_slices,omitempty"`
	DNSLegacyEndpoints []legacyEndpoints       `json:"dns_legacy_endpoints,omitempty"`
	NetworkPolicies    []networkPolicy         `json:"network_policies,omitempty"`
	PDBs               []pdb                   `json:"pod_disruption_budgets,omitempty"`
	Metrics            []currentUsage          `json:"current_metrics,omitempty"`
	Expansion          model.ExpansionEvidence `json:"workload_observations"`
	History            *model.HistoryCapture   `json:"historical_observations,omitempty"`
}

type Inventory struct {
	SchemaVersion     string               `json:"schema_version"`
	AnalyzerVersion   string               `json:"analyzer_version"`
	Producer          string               `json:"producer"`
	Integrity         string               `json:"sha256"`
	Scope             model.Scope          `json:"scope"`
	ClusterID         string               `json:"cluster_id,omitempty"`
	ClusterProvenance string               `json:"cluster_provenance,omitempty"`
	StartedAt         time.Time            `json:"started_at"`
	FinishedAt        time.Time            `json:"finished_at"`
	EvaluatedAt       time.Time            `json:"evaluated_at"`
	Duration          time.Duration        `json:"capture_duration_nanoseconds"`
	Sources           []model.SourceResult `json:"sources"`
	SourceTimings     []model.SourceTiming `json:"source_timings"`
	Resources         map[string]int       `json:"resources"`
}

// New projects and validates the diagnostic evidence. The stored report is
// generated from the same minimized observations used for later replay.
func New(snapshot model.Snapshot, producer string) (*Bundle, error) {
	if snapshot.StartedAt.IsZero() {
		snapshot.StartedAt = snapshot.CollectedAt
	}
	if snapshot.FinishedAt.IsZero() {
		snapshot.FinishedAt = snapshot.CollectedAt
	}
	p := &payload{
		Scope: snapshot.Scope, StartedAt: snapshot.StartedAt.UTC(), FinishedAt: snapshot.FinishedAt.UTC(),
		EvaluatedAt: snapshot.CollectedAt.UTC(), ClusterID: snapshot.ClusterID, ClusterProvenance: snapshot.ClusterProvenance,
		Producer: producer, AnalyzerVersion: model.AnalyzerVersion, Sources: snapshot.Sources,
		SourceTimings: snapshot.SourceTimings, Evidence: project(snapshot),
	}
	p.Evidence.minimize(p.Scope)
	if err := validateEvidence(p); err != nil {
		return nil, err
	}
	p.Stored = engine.Analyze(p.observations())
	if err := validateReport(p); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, ErrFormat
	}
	digest := digestOf(raw)
	if len(raw)+len(envelopePrefix)+len(digest)+len(envelopeSeparator)+2 > MaxBytes {
		return nil, ErrLimit
	}
	if err := validateJSONStructure(raw); err != nil {
		return nil, err
	}
	// Own the payload rather than retaining references to the caller's snapshot.
	return decodePayload(raw, digest)
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Decode bounds and validates one uncompressed incident bundle. The digest
// detects changes and makes no assertion about its author's identity.
func Decode(reader io.Reader) (*Bundle, error) {
	if reader == nil {
		return nil, ErrFormat
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxBytes+1))
	if err != nil {
		return nil, ErrFormat
	}
	if len(raw) > MaxBytes {
		return nil, ErrLimit
	}
	if err := validateJSONStructure(raw); err != nil {
		return nil, err
	}
	var input envelope
	if err := decodeStrict(raw, &input); err != nil {
		return nil, err
	}
	if input.SchemaVersion != SchemaVersion {
		return nil, ErrVersion
	}
	if input.Integrity == "" || !bytes.Equal([]byte(input.Integrity), []byte(digestOf(input.Payload))) {
		return nil, ErrIntegrity
	}
	return decodePayload(input.Payload, input.Integrity)
}

func decodePayload(raw []byte, digest string) (*Bundle, error) {
	p := new(payload)
	if err := decodeStrict(raw, p); err != nil {
		return nil, err
	}
	if err := validateEvidence(p); err != nil {
		return nil, err
	}
	if err := validateReport(p); err != nil {
		return nil, err
	}
	return &Bundle{data: p, encoded: raw, digest: digest}, nil
}

const envelopePrefix = `{"schema_version":"1","sha256":"`
const envelopeSeparator = `","payload":`

func (b *Bundle) Encode(writer io.Writer) error {
	if b == nil || b.data == nil || writer == nil {
		return ErrFormat
	}
	// Keep the exact payload bytes covered by the digest, including valid
	// payload whitespace introduced by tools outside KubeDoctor.
	if _, err := io.WriteString(writer, envelopePrefix+b.digest+envelopeSeparator); err != nil {
		return err
	}
	if n, err := writer.Write(b.encoded); err != nil {
		return err
	} else if n != len(b.encoded) {
		return io.ErrShortWrite
	}
	_, err := io.WriteString(writer, "}\n")
	return err
}

func Open(path string) (*Bundle, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("could not inspect the incident bundle")
	}
	if !before.Mode().IsRegular() {
		return nil, ErrFormat
	}
	if before.Size() > MaxBytes {
		return nil, ErrLimit
	}
	file, err := openBundleFile(path)
	if err != nil {
		return nil, errors.New("could not open the incident bundle")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, ErrFormat
	}
	return Decode(file)
}

func (b *Bundle) Save(path string) error {
	return artifact.WriteNew(path, b.Encode)
}

func (b *Bundle) Inventory() Inventory {
	p := b.data
	return Inventory{
		SchemaVersion: SchemaVersion, AnalyzerVersion: p.AnalyzerVersion, Producer: p.Producer, Integrity: b.digest,
		Scope: p.Scope, ClusterID: p.ClusterID, ClusterProvenance: p.ClusterProvenance,
		StartedAt: p.StartedAt, FinishedAt: p.FinishedAt, EvaluatedAt: p.EvaluatedAt,
		Duration: p.FinishedAt.Sub(p.StartedAt), Sources: slices.Clone(p.Sources), SourceTimings: slices.Clone(p.SourceTimings),
		Resources: p.Evidence.counts(),
	}
}

func (b *Bundle) Snapshot() (model.Snapshot, error) {
	if b == nil || b.data == nil {
		return model.Snapshot{}, ErrFormat
	}
	// Components may sort or enrich their copy of the model. Rebuild from
	// the owned payload so no copies share mutable maps or nested slices.
	var p payload
	if err := decodeStrict(b.encoded, &p); err != nil {
		return model.Snapshot{}, err
	}
	return p.observations(), nil
}

func (b *Bundle) Analyze(mode Mode) (Analysis, error) {
	if b == nil || b.data == nil {
		return Analysis{}, ErrFormat
	}
	current := model.AnalyzerVersion
	out := Analysis{Mode: mode, BundleAnalyzerVersion: b.data.AnalyzerVersion, AnalyzerVersion: current}
	switch mode {
	case Stored:
		out.AnalyzerVersion = b.data.AnalyzerVersion
		var p payload
		if err := decodeStrict(b.encoded, &p); err != nil {
			return Analysis{}, err
		}
		out.Report = p.Stored
	case Replay, Reevaluate:
		if mode == Replay && b.data.AnalyzerVersion != current {
			return Analysis{}, ErrAnalyzerVersion
		}
		snapshot, err := b.Snapshot()
		if err != nil {
			return Analysis{}, err
		}
		out.InterpretationChanged = b.data.AnalyzerVersion != current
		out.Report = engine.Analyze(snapshot)
		if mode == Replay && !sameReport(out.Report, b.data.Stored) {
			return Analysis{}, ErrReplayMismatch
		}
	default:
		return Analysis{}, ErrMode
	}
	return out, nil
}

func project(s model.Snapshot) evidence {
	return evidence{
		Nodes: mapItems(s.Nodes, describeNode), Pods: mapItems(s.Pods, describePod),
		ReplicaSets: mapItems(s.ReplicaSets, describeReplicaSet), Deployments: mapItems(s.Deployments, describeDeployment),
		DaemonSets: mapItems(s.DaemonSets, describeDaemonSet), Events: mapItems(s.Events, describeEvent),
		ConfigMaps: mapItems(s.ConfigMaps, describeConfigMap), Claims: mapItems(s.PVCs, describeClaim),
		StorageClasses: mapItems(s.StorageClasses, describeClass), Services: mapItems(s.Services, describeService),
		EndpointSlices: mapItems(s.EndpointSlices, describeEndpointSlice), LegacyEndpoints: mapItems(s.LegacyEndpoints, describeLegacy),
		Ingresses: mapItems(s.Ingresses, describeIngress), DNSPods: mapItems(s.DNSPods, describePod),
		DNSServices: mapItems(s.DNSServices, describeService), DNSConfig: mapItems(s.DNSConfig, describeDNSConfig),
		DNSEndpointSlices:  mapItems(s.DNSEndpointSlices, describeEndpointSlice),
		DNSLegacyEndpoints: mapItems(s.DNSLegacyEndpoints, describeLegacy),
		NetworkPolicies:    mapItems(s.NetworkPolicies, describeNetworkPolicy), PDBs: mapItems(s.PDBs, describePDB),
		Metrics: mapItems(s.Metrics, describeUsage), Expansion: s.Expansion, History: s.History,
	}
}

func (p *payload) observations() model.Snapshot {
	e := p.Evidence
	return model.Snapshot{
		Scope: p.Scope, StartedAt: p.StartedAt, FinishedAt: p.FinishedAt, CollectedAt: p.EvaluatedAt,
		ClusterID: p.ClusterID, ClusterProvenance: p.ClusterProvenance, Sources: p.Sources, SourceTimings: p.SourceTimings,
		Nodes: mapItems(e.Nodes, node.observation), Pods: mapItems(e.Pods, pod.observation),
		ReplicaSets: mapItems(e.ReplicaSets, replicaSet.observation), Deployments: mapItems(e.Deployments, deployment.observation),
		DaemonSets: mapItems(e.DaemonSets, daemonSet.observation), Events: mapItems(e.Events, classifiedEvent.observation),
		ConfigMaps: mapItems(e.ConfigMaps, configMap.observation), PVCs: mapItems(e.Claims, claim.observation),
		StorageClasses: mapItems(e.StorageClasses, storageClass.observation), Services: mapItems(e.Services, service.observation),
		EndpointSlices:  mapItems(e.EndpointSlices, endpointSlice.observation),
		LegacyEndpoints: mapItems(e.LegacyEndpoints, legacyEndpoints.observation), Ingresses: mapItems(e.Ingresses, ingress.observation),
		DNSPods: mapItems(e.DNSPods, pod.observation), DNSServices: mapItems(e.DNSServices, service.observation),
		DNSConfig:          mapItems(e.DNSConfig, dnsConfig.observation),
		DNSEndpointSlices:  mapItems(e.DNSEndpointSlices, endpointSlice.observation),
		DNSLegacyEndpoints: mapItems(e.DNSLegacyEndpoints, legacyEndpoints.observation),
		NetworkPolicies:    mapItems(e.NetworkPolicies, networkPolicy.observation), PDBs: mapItems(e.PDBs, pdb.observation),
		Metrics: mapItems(e.Metrics, currentUsage.observation), Expansion: e.Expansion, History: e.History,
	}
}

func (e evidence) counts() map[string]int {
	counts := map[string]int{
		"nodes": len(e.Nodes), "pods": len(e.Pods), "replica_sets": len(e.ReplicaSets), "deployments": len(e.Deployments),
		"daemon_sets": len(e.DaemonSets), "events": len(e.Events), "config_maps": len(e.ConfigMaps), "claims": len(e.Claims),
		"storage_classes": len(e.StorageClasses), "services": len(e.Services), "endpoint_slices": len(e.EndpointSlices),
		"legacy_endpoints": len(e.LegacyEndpoints), "ingresses": len(e.Ingresses), "dns_pods": len(e.DNSPods),
		"dns_services": len(e.DNSServices), "dns_configuration": len(e.DNSConfig), "dns_endpoint_slices": len(e.DNSEndpointSlices),
		"dns_legacy_endpoints": len(e.DNSLegacyEndpoints), "network_policies": len(e.NetworkPolicies), "pod_disruption_budgets": len(e.PDBs),
		"pod_metrics": len(e.Metrics), "stateful_sets": len(e.Expansion.StatefulSets), "jobs": len(e.Expansion.Jobs),
		"cron_jobs": len(e.Expansion.CronJobs), "horizontal_pod_autoscalers": len(e.Expansion.HPAs),
		"resource_quotas": len(e.Expansion.Quotas), "limit_ranges": len(e.Expansion.LimitRanges), "pod_probes": len(e.Expansion.Probes),
		"admission_events": len(e.Expansion.AdmissionEvents), "probe_events": len(e.Expansion.ProbeEvents),
		"node_local_dns_pods": len(e.Expansion.NodeLocalPods),
	}
	if e.Expansion.NodeLocalDNS != nil {
		counts["node_local_dns"] = 1
	}
	if e.History != nil {
		counts["historical_targets"] = len(e.History.Targets)
	}
	return counts
}

func sameReport(a, b model.Report) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

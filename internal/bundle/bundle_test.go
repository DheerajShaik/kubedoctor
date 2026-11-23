package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	policy "k8s.io/api/policy/v1"
	storage "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const marker = "KUBEDOCTOR_private_SENTINEL_21bc"

func sampleSnapshot() model.Snapshot {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	before := metav1.NewTime(now.Add(-20 * time.Minute))
	metadata := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Namespace: "payments", Name: name, UID: types.UID(name + "-uid"), CreationTimestamp: before}
	}
	scope := model.Scope{Kind: model.NamespaceScope, Namespace: "payments", Context: "disposable"}
	s := model.Snapshot{
		Scope: scope, StartedAt: now.Add(-2 * time.Second), FinishedAt: now, CollectedAt: now,
		ClusterID: digestOf([]byte("synthetic namespace UID")), ClusterProvenance: model.NamespaceUIDProvenance,
	}
	for _, source := range []model.Source{
		model.SourceNamespace, model.SourcePods, model.SourceNodes, model.SourceReplicaSets,
		model.SourceDeployments, model.SourceDaemonSets, model.SourcePDBs, model.SourcePVCs,
		model.SourceStorageClasses, model.SourceServices, model.SourceEndpointSlices, model.SourceNetworkPolicies,
		model.SourceIngresses, model.SourceEvents, model.SourceConfigMaps, model.SourceMetrics,
		model.SourceDNSPods, model.SourceDNSServices, model.SourceDNSEndpointSlices, model.SourceDNSConfig,
		model.SourceStatefulSets, model.SourceJobs, model.SourceCronJobs, model.SourceHPAs, model.SourceQuotas,
		model.SourceLimitRanges, model.SourceProvenance,
	} {
		s.Sources = append(s.Sources, model.SourceResult{Source: source, Status: model.Complete, Required: source == model.SourcePods})
	}
	s.Sources = append(s.Sources, model.SourceResult{Source: model.SourceNodeLocalDNS, Status: model.Unavailable})
	s.SourceTimings = []model.SourceTiming{
		{Source: model.SourcePods, StartedAt: now.Add(-time.Second), FinishedAt: now},
		{Source: model.SourceStorageClasses, Resource: "waiting", StartedAt: now.Add(-time.Second), FinishedAt: now},
	}
	pod := core.Pod{ObjectMeta: metadata("web")}
	pod.Labels = map[string]string{"app": marker, "APP": "a valid case-distinct label value", "unrelated": marker, marker: marker}
	pod.Labels["APP"] = "case-distinct"
	pod.Annotations = map[string]string{marker: marker}
	pod.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "frontend-1", UID: "frontend-1-uid", Controller: new(true)}}
	pod.Spec.NodeName = "hosting"
	pod.Spec.Containers = []core.Container{{
		Name: "server", Image: marker, Command: []string{marker}, Args: []string{marker},
		Env: []core.EnvVar{
			{Name: "UNSAFE_LITERAL", Value: marker},
			{Name: "CONFIG", ValueFrom: &core.EnvVarSource{ConfigMapKeyRef: &core.ConfigMapKeySelector{
				LocalObjectReference: core.LocalObjectReference{Name: "settings"}, Key: "flag",
			}}},
			{Name: "KEY", ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{
				LocalObjectReference: core.LocalObjectReference{Name: "missing"}, Key: "username",
			}}},
		},
		Ports: []core.ContainerPort{{Name: "web", ContainerPort: 8080, Protocol: core.ProtocolTCP}},
		Resources: core.ResourceRequirements{Requests: core.ResourceList{
			core.ResourceCPU: resource.MustParse("100m"), core.ResourceMemory: resource.MustParse("100Mi"),
		}},
	}}
	pod.Spec.Volumes = []core.Volume{
		{Name: "work-dir", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "db-volume"}}},
		{Name: "config", VolumeSource: core.VolumeSource{Projected: &core.ProjectedVolumeSource{Sources: []core.VolumeProjection{{
			ConfigMap: &core.ConfigMapProjection{LocalObjectReference: core.LocalObjectReference{Name: "settings"},
				Items: []core.KeyToPath{{Key: "document", Path: marker}}},
		}}}}},
	}
	pod.Status.Phase = core.PodRunning
	pod.Status.ContainerStatuses = []core.ContainerStatus{{
		Name: "server", RestartCount: 5, ContainerID: "containerd://" + strings.Repeat("a", 64),
		State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: marker}},
		LastTerminationState: core.ContainerState{Terminated: &core.ContainerStateTerminated{
			ExitCode: 137, Reason: "OOMKilled", Message: marker, FinishedAt: metav1.NewTime(now.Add(-time.Minute)),
		}},
	}}
	s.Pods = []core.Pod{pod}
	s.Nodes = []core.Node{{ObjectMeta: metav1.ObjectMeta{Name: "hosting", UID: "host-uid"},
		Status: core.NodeStatus{Conditions: []core.NodeCondition{{Type: core.NodeMemoryPressure, Status: core.ConditionTrue,
			Reason: "KubeletHasInsufficientMemory", Message: marker, LastTransitionTime: before}}}}}
	s.ReplicaSets = []apps.ReplicaSet{{ObjectMeta: metadata("frontend-1")}}
	s.ReplicaSets[0].OwnerReferences = []metav1.OwnerReference{{Kind: "Deployment", Name: "frontend", UID: "frontend-uid", Controller: new(true)}}
	s.Deployments = []apps.Deployment{{ObjectMeta: metadata("frontend")}}
	s.Deployments[0].Spec.Replicas = new(int32(1))
	s.Deployments[0].Spec.Template.Labels = map[string]string{"app": marker}
	s.DaemonSets = []apps.DaemonSet{{ObjectMeta: metadata("networking")}}
	s.ConfigMaps = []model.ConfigMap{{Name: "settings", Namespace: "payments", Keys: []string{"flag", marker}, BinaryKeys: []string{"document", marker}}}
	s.PVCs = []core.PersistentVolumeClaim{{ObjectMeta: metadata("db-volume"),
		Spec:   core.PersistentVolumeClaimSpec{StorageClassName: new("waiting")},
		Status: core.PersistentVolumeClaimStatus{Phase: core.ClaimPending}}}
	s.StorageClasses = []storage.StorageClass{{ObjectMeta: metav1.ObjectMeta{Name: "waiting"},
		VolumeBindingMode: new(storage.VolumeBindingWaitForFirstConsumer)}}
	s.Services = []core.Service{{ObjectMeta: metadata("web-svc"), Spec: core.ServiceSpec{
		ClusterIP: "192.0.2.5", Selector: map[string]string{"app": marker, "APP": "case-distinct"},
		Ports: []core.ServicePort{{Name: "web", Port: 80, TargetPort: intstr.FromString("web"), Protocol: core.ProtocolTCP}},
	}}}
	s.EndpointSlices = []discovery.EndpointSlice{{
		ObjectMeta: metadata("web-slice"), AddressType: discovery.AddressTypeIPv4,
		Ports: []discovery.EndpointPort{{Name: new("web"), Port: new(int32(8080)), Protocol: new(core.ProtocolTCP)}},
		Endpoints: []discovery.Endpoint{{Addresses: []string{"192.0.2.6"}, Conditions: discovery.EndpointConditions{Ready: new(true)},
			TargetRef: &core.ObjectReference{Namespace: "payments", Kind: "Pod", Name: pod.Name, UID: pod.UID}}},
	}}
	s.EndpointSlices[0].Labels = map[string]string{
		discovery.LabelServiceName: "web-svc", discovery.LabelManagedBy: builtInEndpointController, "private": marker,
	}
	s.EndpointSlices[0].OwnerReferences = []metav1.OwnerReference{{Kind: "Service", Name: "web-svc", UID: "web-svc-uid"}}
	s.LegacyEndpoints = []core.Endpoints{{ObjectMeta: metadata("legacy"), Subsets: []core.EndpointSubset{{
		Addresses: []core.EndpointAddress{{IP: "192.0.2.7"}},
	}}}}
	s.Ingresses = []networking.Ingress{{ObjectMeta: metadata("web-ingress"), Spec: networking.IngressSpec{
		DefaultBackend: &networking.IngressBackend{Service: &networking.IngressServiceBackend{
			Name: "web-svc", Port: networking.ServiceBackendPort{Name: "missing"},
		}},
		Rules: []networking.IngressRule{{Host: "private.example", IngressRuleValue: networking.IngressRuleValue{
			HTTP: &networking.HTTPIngressRuleValue{Paths: []networking.HTTPIngressPath{{Path: "/" + marker}}},
		}}},
	}}}
	podSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app": marker},
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "APP", Operator: metav1.LabelSelectorOpIn, Values: []string{"case-distinct"}}}}
	s.NetworkPolicies = []networking.NetworkPolicy{{ObjectMeta: metadata("web-policy"), Spec: networking.NetworkPolicySpec{
		PodSelector: podSelector,
		Ingress: []networking.NetworkPolicyIngressRule{{
			From:  []networking.NetworkPolicyPeer{{IPBlock: &networking.IPBlock{CIDR: "192.0.2.80/32"}}},
			Ports: []networking.NetworkPolicyPort{{Port: new(intstr.FromString("web")), Protocol: new(core.ProtocolTCP)}},
		}},
	}}}
	s.PDBs = []policy.PodDisruptionBudget{{ObjectMeta: metadata("web-pdb"), Spec: policy.PodDisruptionBudgetSpec{Selector: &podSelector},
		Status: policy.PodDisruptionBudgetStatus{CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1}}}
	s.Events = []model.Event{{
		Resource: model.Identity("Pod", &pod), Reason: "Failed", Time: now.Add(-time.Second), TimeBasis: "series_last_observed", Count: 3,
		Reference: &model.ConfigIssue{Kind: "Secret", Name: "missing", Key: "username", Missing: "key"},
	}}
	s.Metrics = []model.PodUsage{{
		Namespace: "payments", Name: pod.Name, Timestamp: now.Add(-time.Second), Window: time.Minute,
		Containers: []model.ContainerUsage{{Name: "server", CPU: resource.MustParse("150m"), Memory: resource.MustParse("240Mi")}},
	}}
	dnsMeta := metav1.ObjectMeta{Namespace: "kube-system", Name: "kube-dns", UID: "dns-uid", CreationTimestamp: before}
	s.DNSServices = []core.Service{{ObjectMeta: dnsMeta, Spec: core.ServiceSpec{
		Selector: map[string]string{"k8s-app": "kube-dns"}, Ports: []core.ServicePort{{Port: 53, Protocol: core.ProtocolUDP}},
		PublishNotReadyAddresses: true,
	}}}
	dnsPodMeta := dnsMeta
	dnsPodMeta.Name, dnsPodMeta.UID = "coredns-1", "coredns-1-uid"
	dnsPodMeta.Labels = map[string]string{"k8s-app": "kube-dns", "private": marker}
	s.DNSPods = []core.Pod{{ObjectMeta: dnsPodMeta, Status: core.PodStatus{
		Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}},
	}}}
	s.DNSConfig = []model.DNSConfig{{Namespace: "kube-system", Name: "coredns", HasCorefile: true}}
	s.DNSEndpointSlices = []discovery.EndpointSlice{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "kube-dns-1", UID: "dns-slice-uid",
			Labels: map[string]string{discovery.LabelServiceName: "kube-dns"}},
		AddressType: discovery.AddressTypeIPv4,
		Ports:       []discovery.EndpointPort{{Port: new(int32(53)), Protocol: new(core.ProtocolUDP)}},
		Endpoints:   []discovery.Endpoint{{Addresses: []string{"192.0.2.81"}, Conditions: discovery.EndpointConditions{Ready: new(true)}}},
	}}
	s.Expansion = model.ExpansionEvidence{
		StatefulSets: []model.StatefulSetEvidence{{Resource: model.Resource{Kind: "StatefulSet", Namespace: "payments", Name: "database"},
			Created: before.Time, Management: "OrderedReady", Strategy: "RollingUpdate", Desired: 2, Ready: 1,
			VolumeClaimTemplates: []string{"db"}}},
		Jobs: []model.JobEvidence{{Resource: model.Resource{Kind: "Job", Namespace: "payments", Name: "sync-job"},
			Created: before.Time, Started: before.Time, Failed: 1, Conditions: []model.BriefCondition{{Type: "Failed", Status: "True", Reason: "BackoffLimitExceeded"}}}},
		CronJobs: []model.CronJobEvidence{{Resource: model.Resource{Kind: "CronJob", Namespace: "payments", Name: "sync"},
			Created: before.Time, Concurrency: "Forbid", Suspended: true}},
		HPAs: []model.HPAEvidence{{Resource: model.Resource{Kind: "HorizontalPodAutoscaler", Namespace: "payments", Name: "frontend"},
			Target: model.Resource{Kind: "Deployment", Namespace: "payments", Name: "frontend"}, TargetAPIVersion: "apps/v1",
			MinReplicas: 1, MaxReplicas: 10, UtilizationTargets: []model.HPAUtilizationTarget{{Resource: "cpu", Percent: 80}}}},
		Quotas: []model.QuotaEvidence{{Resource: model.Resource{Kind: "ResourceQuota", Namespace: "payments", Name: "bounded"},
			Capacities: []model.QuotaCapacity{{Resource: "pods", Hard: "10", Used: "10", Current: true}}}},
		LimitRanges: []model.LimitRangeEvidence{{Resource: model.Resource{Kind: "LimitRange", Namespace: "payments", Name: "standard"},
			Constraints: []model.LimitConstraint{{Scope: "Container", Resource: "cpu", Type: "maximum"}}}},
		Probes: []model.PodProbeEvidence{{Pod: model.Identity("Pod", &pod),
			Containers: []model.ContainerProbe{{Name: "server", Role: "application", Kinds: []string{"readiness"}}}}},
	}
	return s
}

func encodedBundle(t testing.TB) (*Bundle, []byte) {
	t.Helper()
	b, err := New(sampleSnapshot(), "v0.2.0-test")
	if err != nil {
		t.Fatalf("create fixture bundle: %v", err)
	}
	var raw bytes.Buffer
	if err := b.Encode(&raw); err != nil {
		t.Fatal(err)
	}
	return b, raw.Bytes()
}

func TestCaptureUsesOnlyReviewedEvidenceAndReplaysLiveReport(t *testing.T) {
	live := engine.Analyze(sampleSnapshot())
	_, raw := encodedBundle(t)
	for _, forbidden := range []string{
		marker, "UNSAFE_LITERAL", "private.example", "192.0.2.", "containerd://",
		"\"apiVersion\"", "\"metadata\"", "\"spec\"", "\"image\"", "\"message\"", "\"annotations\"",
	} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("unnecessary fact %q persisted in bundle", forbidden)
		}
	}
	for _, retained := range []string{"username", "document", "front", "series_last_observed", "sha256-", "dns_endpoint_slices"} {
		if !bytes.Contains(raw, []byte(retained)) {
			t.Errorf("necessary fact %q missing in bundle", retained)
		}
	}
	b, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	info := b.Inventory()
	if info.Duration != 2*time.Second || len(info.SourceTimings) != 2 || info.Resources["pods"] != 1 ||
		info.Resources["stateful_sets"] != 1 || info.ClusterID == "" || !info.EvaluatedAt.Equal(live.CollectedAt) {
		t.Fatalf("incomplete inventory: %+v", info)
	}
	for _, mode := range []Mode{Stored, Replay, Reevaluate} {
		result, err := b.Analyze(mode)
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if !sameReport(live, result.Report) {
			before, _ := json.MarshalIndent(live, "", " ")
			after, _ := json.MarshalIndent(result.Report, "", " ")
			t.Fatalf("mode %s changed live output\nlive %s\nreplay %s", mode, before, after)
		}
	}
	first, _ := b.Snapshot()
	first.Pods[0].Labels["app"] = "modified"
	first.ConfigMaps[0].Keys[0] = "modified"
	second, err := b.Snapshot()
	if err != nil || second.Pods[0].Labels["app"] == "modified" || second.ConfigMaps[0].Keys[0] != "flag" {
		t.Fatal("an offline reader mutated other consumers' evidence")
	}
	secondapp := second.Pods[0].Labels["app"]
	if !validLabelFingerprint("app", secondapp) || second.Pods[0].Labels["APP"] == "case-distinct" ||
		second.Pods[0].Labels["unrelated"] != "" || len(second.ConfigMaps[0].BinaryKeys) != 1 {
		t.Fatal("label filtering or key membership did not round-trip correctly")
	}
}

func writeEnvelope(raw []byte) []byte {
	return slices.Concat([]byte(envelopePrefix+digestOf(raw)+envelopeSeparator), raw, []byte("}\n"))
}

func rewritePayload(t testing.TB, b *Bundle, modify func(*payload)) []byte {
	t.Helper()
	var p payload
	if err := decodeStrict(b.encoded, &p); err != nil {
		t.Fatal(err)
	}
	modify(&p)
	wire, err := json.Marshal(&p)
	if err != nil {
		t.Fatal(err)
	}
	return writeEnvelope(wire)
}

func TestReplayRequiresCapturedRuleVersionAndDetectsMismatch(t *testing.T) {
	b, _ := encodedBundle(t)
	old, err := Decode(bytes.NewReader(rewritePayload(t, b, func(p *payload) {
		p.AnalyzerVersion = "old"
	})))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{Stored, Reevaluate} {
		got, err := old.Analyze(mode)
		if err != nil || got.InterpretationChanged != (mode == Reevaluate) {
			t.Fatalf("analysis versions not explicit for %s: %+v %v", mode, got, err)
		}
	}
	if _, err := old.Analyze(Replay); !errors.Is(err, ErrAnalyzerVersion) {
		t.Fatalf("incompatible rules were replayed: %v", err)
	}
	storedTampered, err := Decode(bytes.NewReader(rewritePayload(t, b, func(p *payload) {
		p.Stored.Findings[0].Symptom = "Claimed analysis does not match its observations."
	})))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storedTampered.Analyze(Replay); !errors.Is(err, ErrReplayMismatch) {
		t.Fatalf("mismatched stored result was reported as reproduced: %v", err)
	}
}

func TestDecodeRejectsInvalidUntrustedBundles(t *testing.T) {
	b, raw := encodedBundle(t)
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"modified digest", bytes.Replace(raw, []byte("v0.2.0-test"), []byte("v0.2.0-fake"), 1), ErrIntegrity},
		{"bad version", bytes.Replace(raw, []byte(`"schema_version":"1"`), []byte(`"schema_version":"2"`), 1), ErrVersion},
		{"trailing value", append(bytes.Clone(raw), []byte("{}")...), ErrFormat},
		{"duplicate key", bytes.Replace(raw, []byte(envelopePrefix), []byte(`{"schema_version":"1","SCHEMA_VERSION":"1","sha256":"`), 1), ErrFormat},
		{"unknown payload field", writeEnvelope(bytes.Replace(b.encoded, []byte(`"scope":`), []byte(`"raw_cluster_dump":{},"scope":`), 1)), ErrFormat},
		{"deep", []byte(strings.Repeat("[", maxDepth+1) + "0" + strings.Repeat("]", maxDepth+1)), ErrLimit},
		{"array budget", []byte("[" + strings.Repeat("0,", maxMembers) + "0]"), ErrLimit},
		{"invalid utf8", []byte{0xff, 0xfe}, ErrFormat},
		{"cross-scope pod", rewritePayload(t, b, func(p *payload) {
			p.Evidence.Pods[0].Identity.Namespace = "unrelated"
		}), ErrFormat},
		{"cross-scope endpoint target", rewritePayload(t, b, func(p *payload) {
			p.Evidence.EndpointSlices[0].Endpoints[0].Target.Namespace = "unrelated"
		}), ErrFormat},
		{"irrelevant Node", rewritePayload(t, b, func(p *payload) {
			p.Evidence.Nodes[0].Identity.Name = "elsewhere"
		}), ErrFormat},
		{"irrelevant config key", rewritePayload(t, b, func(p *payload) {
			p.Evidence.ConfigMaps[0].Keys = append(p.Evidence.ConfigMaps[0].Keys, "another-token")
		}), ErrFormat},
		{"unsupported alias protocol", rewritePayload(t, b, func(p *payload) {
			p.Evidence.Services[0].Ports[0].Port.Protocol = "internet"
		}), ErrFormat},
		{"wrong scope provenance", rewritePayload(t, b, func(p *payload) {
			p.Scope.Kind, p.Scope.Namespace = model.ClusterScope, ""
		}), ErrFormat},
		{"oversized numeric amplification", rewritePayload(t, b, func(p *payload) {
			p.Evidence.EndpointSlices[0].Endpoints[0].AddressCount = 2000000
		}), ErrFormat},
		{"unparsed regex quantity", rewritePayload(t, b, func(p *payload) {
			p.Evidence.Pods[0].Containers[0].Resources.Requests.CPU = "1e2147483645"
		}), ErrFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(bytes.NewReader(tc.data))
			if !errors.Is(err, tc.want) {
				t.Fatalf("decode=%v want %v", err, tc.want)
			}
		})
	}
	if _, err := Decode(io.LimitReader(repeatingReader(' '), MaxBytes+1)); !errors.Is(err, ErrLimit) {
		t.Fatalf("byte budget was not applied: %v", err)
	}
}

type repeatingReader byte

func (r repeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestProtectedSaveAndOpen(t *testing.T) {
	b, _ := encodedBundle(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "incident.kdb.json")
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(path); err == nil {
		t.Fatal("overwrote an existing bundle file")
	}
	state, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && state.Mode().Perm() != 0600 {
		t.Fatalf("bundle was exposed with file mode %o", state.Mode().Perm())
	}
	out, err := Open(path)
	if err != nil || out.Inventory().Integrity != b.Inventory().Integrity {
		t.Fatalf("published artifact lost integrity: %v", err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(path, link); err == nil {
		if _, err := Open(link); !errors.Is(err, ErrFormat) {
			t.Fatalf("bundle open accepted a symbolic link: %v", err)
		}
		if err := b.Save(link); err == nil {
			t.Fatal("bundle save replaced a symbolic link")
		}
	}
	if _, err := Open(dir); !errors.Is(err, ErrFormat) {
		t.Fatalf("bundle open accepted a directory: %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	_, example := encodedBundle(f)
	for _, seed := range [][]byte{example, []byte(`{}`), []byte(`{"schema_version":"1","payload":{},"sha256":"invalid"}`)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		b, err := Decode(bytes.NewReader(raw))
		if err != nil {
			return
		}
		b.Inventory()
		if _, err := b.Snapshot(); err != nil {
			t.Fatalf("validated bundle could not build its observations: %v", err)
		}
		if _, err := b.Analyze(Stored); err != nil {
			t.Fatalf("validated bundle could not read its stored analysis: %v", err)
		}
	})
}

func BenchmarkReplay1000Pods(b *testing.B) {
	s := sampleSnapshot()
	pods := make([]core.Pod, 0, 1000)
	for i := range 1000 {
		pod := s.Pods[0].DeepCopy()
		pod.Name, pod.UID = fmt.Sprintf("web-%d", i), types.UID(fmt.Sprintf("web-%d-uid", i))
		pods = append(pods, *pod)
	}
	s.Pods = pods
	artifact, err := New(s, "benchmark")
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(artifact.encoded)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := artifact.Analyze(Replay); err != nil {
			b.Fatal(err)
		}
	}
}

package investigation

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	policy "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var baselineTime = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func fixtureMeta(name, uid string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: "demo", UID: types.UID(uid), Generation: 1,
		CreationTimestamp: metav1.NewTime(baselineTime.Add(-time.Hour)), Labels: map[string]string{"app": "checkout"}}
}

func owner(kind string, object metav1.Object) metav1.OwnerReference {
	return metav1.OwnerReference{Kind: kind, Name: object.GetName(), UID: object.GetUID(), Controller: new(true)}
}

func fixtureSnapshot(completed time.Time, ready bool, podUID string) (model.Snapshot, model.Report) {
	started := completed.Add(-20 * time.Second)
	scope := model.Scope{Kind: model.NamespaceScope, Namespace: "demo", Context: "kind-test"}
	snapshot := model.Snapshot{
		Scope: scope, StartedAt: started, FinishedAt: completed, CollectedAt: completed,
		ClusterID: strings.Repeat("a", 64), ClusterProvenance: model.NamespaceUIDProvenance,
	}
	report := model.Report{SchemaVersion: model.SchemaVersion, Scope: scope, CollectedAt: completed, Complete: true, Findings: []model.Finding{}}
	for _, source := range []model.Source{
		model.SourcePods, model.SourceReplicaSets, model.SourceDeployments, model.SourceEvents, model.SourceConfigMaps,
		model.SourceServices, model.SourceEndpointSlices, model.SourceIngresses, model.SourcePDBs, model.SourcePVCs,
	} {
		snapshot.SourceTimings = append(snapshot.SourceTimings, model.SourceTiming{Source: source, StartedAt: started, FinishedAt: completed})
		sourceResult := model.SourceResult{Source: source, Status: model.Complete, Required: true}
		snapshot.Sources, report.Sources = append(snapshot.Sources, sourceResult), append(report.Sources, sourceResult)
	}
	for _, rule := range []string{"configuration-references", "pod-containers", "deployment-rollout", "recent-warning-events"} {
		report.Checks = append(report.Checks, model.CheckResult{ID: rule, Status: model.Checked})
	}
	deployment := apps.Deployment{ObjectMeta: fixtureMeta("checkout", "deployment-uid"), Status: apps.DeploymentStatus{ObservedGeneration: 1}}
	rs := apps.ReplicaSet{ObjectMeta: fixtureMeta("checkout-rs", "rs-uid"), Status: apps.ReplicaSetStatus{ObservedGeneration: 1}}
	rs.OwnerReferences = []metav1.OwnerReference{owner("Deployment", &deployment)}
	pod := core.Pod{ObjectMeta: fixtureMeta("checkout-pod", podUID), Spec: core.PodSpec{NodeName: "test-node",
		Containers: []core.Container{{Name: "api", Env: []core.EnvVar{
			{Name: "SECRET_LITERAL", Value: "KUBEDOCTOR_SENTINEL_SHOULD_NOT_APPEAR"},
			{Name: "DEMO", ValueFrom: &core.EnvVarSource{ConfigMapKeyRef: &core.ConfigMapKeySelector{LocalObjectReference: core.LocalObjectReference{Name: "checkout-dependency"}, Key: "setting"}}},
		}}},
		Volumes: []core.Volume{{Name: "data", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "checkout-data"}}}},
	}, Status: core.PodStatus{Phase: core.PodPending}}
	pod.OwnerReferences = []metav1.OwnerReference{owner("ReplicaSet", &rs)}
	if ready {
		deployment.Status.AvailableReplicas = 1
		rs.Status.ReadyReplicas = 1
		pod.Status.Phase = core.PodRunning
		pod.Status.Conditions = []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue, LastTransitionTime: metav1.NewTime(completed.Add(-time.Second))}}
		snapshot.ConfigMaps = []model.ConfigMap{{Namespace: "demo", Name: "checkout-dependency", Keys: []string{"setting"}}}
	} else {
		podID := model.Identity("Pod", &pod)
		report.Findings = append(report.Findings,
			model.Finding{Resource: podID, RuleID: "configuration-references", Severity: model.Warning, Confidence: model.Observed,
				Symptom: "A required configuration object is missing", Evidence: []model.Evidence{evidence("reference_kind", "ConfigMap"), evidence("reference_name", "checkout-dependency")}},
			model.Finding{Resource: podID, RuleID: "pod-containers", Severity: model.Critical, Confidence: model.Observed,
				Symptom: "The container is waiting because its configuration could not be applied", Evidence: []model.Evidence{evidence("container", "api"), evidence("waiting_reason", "CreateContainerConfigError")}},
			model.Finding{Resource: model.Identity("Deployment", &deployment), RuleID: "deployment-rollout", Severity: model.Warning, Confidence: model.Observed,
				Symptom: "Deployment is below the desired replica count"})
		snapshot.Events = append(snapshot.Events, model.Event{Resource: podID, Reason: "Failed", Time: completed.Add(-time.Minute), Count: 5, TimeBasis: "series_last_observed"})
	}
	snapshot.Deployments, snapshot.ReplicaSets, snapshot.Pods = []apps.Deployment{deployment}, []apps.ReplicaSet{rs}, []core.Pod{pod}
	return snapshot, report
}

func TestBuildPreservesLinkedIncidentAndEvidence(t *testing.T) {
	snapshot, report := fixtureSnapshot(baselineTime, false, "pod-uid-a")
	service := core.Service{ObjectMeta: fixtureMeta("checkout", "service-uid"), Spec: core.ServiceSpec{Selector: map[string]string{"app": "checkout"}}}
	endpointSlice := discovery.EndpointSlice{ObjectMeta: fixtureMeta("checkout-slice", "slice-uid"), Endpoints: []discovery.Endpoint{{
		TargetRef:  &core.ObjectReference{Kind: "Pod", Namespace: "demo", Name: snapshot.Pods[0].Name, UID: "pod-uid-a"},
		Conditions: discovery.EndpointConditions{Ready: new(false)},
	}}}
	endpointSlice.OwnerReferences = []metav1.OwnerReference{owner("Service", &service)}
	backend := networking.IngressBackend{Service: &networking.IngressServiceBackend{Name: service.Name, Port: networking.ServiceBackendPort{Number: 80}}}
	snapshot.Services = []core.Service{service}
	snapshot.EndpointSlices = []discovery.EndpointSlice{endpointSlice}
	snapshot.Ingresses = []networking.Ingress{{ObjectMeta: fixtureMeta("checkout", "ingress-uid"), Spec: networking.IngressSpec{DefaultBackend: &backend}}}
	snapshot.Nodes = []core.Node{{ObjectMeta: metav1.ObjectMeta{Name: "test-node", UID: "node-uid"}}}
	snapshot.PVCs = []core.PersistentVolumeClaim{{ObjectMeta: fixtureMeta("checkout-data", "claim-uid")}}
	snapshot.PDBs = []policy.PodDisruptionBudget{{ObjectMeta: fixtureMeta("checkout", "pdb-uid"), Spec: policy.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}}}}}
	snapshot.Expansion.HPAs = []model.HPAEvidence{{Resource: model.Resource{Kind: "HorizontalPodAutoscaler", Namespace: "demo", Name: "checkout", UID: "hpa-uid"},
		Target: model.Resource{Kind: "Deployment", Namespace: "demo", Name: "checkout"}}}
	investigation := Build(snapshot, report)
	if len(investigation.Incidents) != 1 {
		t.Fatalf("expected one grouped investigation: %#v", investigation.Incidents)
	}
	incident := investigation.Incidents[0]
	if incident.Anchor.UID != "deployment-uid" || !slices.Equal(incident.FindingIndexes, []int{0, 1, 2}) ||
		incident.Severity != model.Critical || len(incident.Candidates) != 2 || len(incident.Alternatives) != 2 || len(incident.Corroboration) == 0 || len(incident.RecoveryChecks) == 0 {
		t.Fatalf("unexpected incident: %#v", incident)
	}
	if len(incident.RelationshipIndexes) == 0 || len(incident.TimelineIndexes) == 0 {
		t.Fatalf("incident evidence is not navigable: %#v", incident)
	}
	for _, expect := range []struct {
		kind     RelationshipKind
		basis    MatchBasis
		observed bool
	}{{Owns, MatchUID, true}, {ServiceBackend, MatchUID, true}, {EndpointTarget, MatchUID, true},
		{IngressService, MatchName, true}, {PodConfig, MatchName, false}, {PodStorage, MatchName, true},
		{PodNode, MatchName, true}, {AutoscalingTarget, MatchName, true}, {DisruptionTarget, MatchSelector, true}} {
		if !slices.ContainsFunc(investigation.Relationships, func(edge Relationship) bool {
			return edge.Kind == expect.kind && edge.Basis == expect.basis && edge.TargetObserved == expect.observed && len(edge.Evidence) > 0
		}) {
			t.Fatalf("missing %s relationship with matching provenance", expect.kind)
		}
	}
	if !slices.ContainsFunc(investigation.Timeline, func(entry TimelineEntry) bool {
		return entry.TimeKind == ObservationTime && entry.Source == model.SourceEvents && !entry.Uncertain && entry.Resource != nil && entry.Resource.UID == "pod-uid-a"
	}) {
		t.Fatal("bounded timeline lost latest aggregate event with an exact subject")
	}
	data, err := json.Marshal(investigation)
	if err != nil || strings.Contains(string(data), "KUBEDOCTOR_SENTINEL") || strings.Contains(string(data), "SECRET_LITERAL") {
		t.Fatalf("unsafe investigation serialization, marshal error: %v", err)
	}
	if !reflect.DeepEqual(investigation, Build(snapshot, report)) {
		t.Fatal("equivalent observations did not build an equivalent investigation")
	}
}

func TestInvestigationTreatsSameNameReplacedOwnerAsUnobserved(t *testing.T) {
	snapshot, report := fixtureSnapshot(baselineTime, false, "pod-uid-a")
	snapshot.Deployments[0].UID = "uid-new-deployment"
	inv := Build(snapshot, report)
	for _, edge := range inv.Relationships {
		if edge.From.Kind == "ReplicaSet" && edge.Kind == Owns && edge.To.Kind == "Deployment" && (edge.TargetObserved || edge.To.UID != "deployment-uid") {
			t.Fatalf("old typed owner reference joined to a same-name replacement: %#v", edge)
		}
	}
	for _, item := range inv.Resources {
		if item.Resource.Kind == "Pod" && item.Workload != nil && item.Workload.UID == "uid-new-deployment" {
			t.Fatal("assigned Pod to replaced controller")
		}
	}
}

func TestInvestigationBoundsSelectorsAndTimeline(t *testing.T) {
	snapshot, report := fixtureSnapshot(baselineTime, false, "pod-uid-a")
	for i := range 600 {
		pod := core.Pod{ObjectMeta: fixtureMeta(fmt.Sprintf("pod-%d", i), fmt.Sprintf("pod-uid-%d", i))}
		snapshot.Pods = append(snapshot.Pods, pod)
		service := core.Service{ObjectMeta: fixtureMeta(fmt.Sprintf("service-%d", i), fmt.Sprintf("service-uid-%d", i)),
			Spec: core.ServiceSpec{Selector: map[string]string{"app": "no-pod-matches-this"}}}
		snapshot.Services = append(snapshot.Services, service)
	}
	for i := range 3_000 {
		snapshot.Events = append(snapshot.Events, model.Event{Resource: model.Identity("Pod", &snapshot.Pods[0]),
			Reason: "Failed", Time: baselineTime.Add(-time.Duration(i) * time.Second), Count: 1, TimeBasis: "event_time"})
	}
	inv := Build(snapshot, report)
	if !inv.Truncated || len(inv.Timeline) != maxTimelineEntries || !slices.ContainsFunc(inv.Limitations, func(s string) bool { return strings.Contains(s, "selector") }) {
		t.Fatalf("expected visible selector and timeline budgets, timeline length %d, limitations %v", len(inv.Timeline), inv.Limitations)
	}
	if !slices.ContainsFunc(inv.Timeline, func(entry TimelineEntry) bool {
		return entry.Source == "capture" && entry.Summary == "Capture started."
	}) {
		t.Fatal("bounded timeline removed its capture markers")
	}
	if len(inv.Report.Findings) != len(report.Findings) {
		t.Fatal("budget removed underlying report findings")
	}
}

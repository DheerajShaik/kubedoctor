package analyze

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func networkFixture() model.Snapshot {
	snapshot := model.Snapshot{
		Scope:       model.Scope{Kind: model.NamespaceScope, Namespace: "payments", Context: "test"},
		CollectedAt: time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC),
	}
	for _, source := range []model.Source{
		model.SourcePods, model.SourceDeployments, model.SourceDaemonSets,
		model.SourceServices, model.SourceEndpointSlices, model.SourceIngresses,
		model.SourceDNSPods, model.SourceDNSServices, model.SourceDNSConfig, model.SourceDNSEndpointSlices,
		model.SourceNetworkPolicies, model.SourcePDBs,
	} {
		snapshot.Sources = append(snapshot.Sources, model.SourceResult{Source: source, Status: model.Complete})
	}
	return snapshot
}

func networkSetCoverage(snapshot *model.Snapshot, source model.Source, coverage model.Coverage) {
	for i := range snapshot.Sources {
		if snapshot.Sources[i].Source == source {
			snapshot.Sources[i].Status = coverage
			return
		}
	}
	snapshot.Sources = append(snapshot.Sources, model.SourceResult{Source: source, Status: coverage})
}

func networkTestObject(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: "payments", UID: types.UID("uid-" + name)}
}

func networkTestService() core.Service {
	return core.Service{
		ObjectMeta: networkTestObject("api"),
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "api"},
			Ports:    []core.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}
}

func networkTestPod(phase core.PodPhase) core.Pod {
	meta := networkTestObject("api-pod")
	meta.Labels = map[string]string{"app": "api"}
	return core.Pod{ObjectMeta: meta, Status: core.PodStatus{Phase: phase}}
}

func networkTestSlice(service core.Service, suffix string, port int32, conditions discovery.EndpointConditions) discovery.EndpointSlice {
	meta := metav1.ObjectMeta{
		Namespace: service.Namespace, Name: service.Name + "-" + suffix,
		Labels:          map[string]string{discovery.LabelServiceName: service.Name},
		OwnerReferences: []metav1.OwnerReference{{Kind: "Service", Name: service.Name, UID: service.UID}},
	}
	return discovery.EndpointSlice{
		ObjectMeta:  meta,
		AddressType: discovery.AddressTypeIPv4,
		Ports:       []discovery.EndpointPort{{Name: new("http"), Port: &port}},
		Endpoints:   []discovery.Endpoint{{Addresses: []string{""}, Conditions: conditions}},
	}
}

func networkCheckCase(t *testing.T, snapshot model.Snapshot, id string, status model.CheckStatus, count int) []model.Finding {
	t.Helper()
	findings, checks := analyzeNetwork(snapshot)
	checkIndex := slices.IndexFunc(checks, func(result model.CheckResult) bool { return result.ID == id })
	if checkIndex < 0 {
		t.Fatalf("missing check %q", id)
	}
	if got := checks[checkIndex]; got.Status != status {
		t.Fatalf("check %q = %q, expected %q (%s)", id, got.Status, status, got.Detail)
	}
	selected := slices.DeleteFunc(findings, func(finding model.Finding) bool { return finding.RuleID != id })
	if len(selected) != count {
		t.Fatalf("check %q produced %d findings, expected %d: %#v", id, len(selected), count, selected)
	}
	return selected
}

func TestServiceBackendCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*model.Snapshot)
		status model.CheckStatus
		count  int
		phrase string
	}{
		{
			name: "no Pods matched by selector", count: 1, status: model.Checked, phrase: "selector matches no active Pods",
			setup: func(s *model.Snapshot) {
				pod := networkTestPod(core.PodRunning)
				pod.Namespace = "other"
				s.Pods = append(s.Pods, pod)
			},
		},
		{
			name: "EndpointSlices aggregate and unspecified readiness is ready", status: model.Checked,
			setup: func(s *model.Snapshot) {
				service := s.Services[0]
				s.EndpointSlices = []discovery.EndpointSlice{
					networkTestSlice(service, "bad", 8080, discovery.EndpointConditions{Ready: new(false)}),
					networkTestSlice(service, "good", 8080, discovery.EndpointConditions{}),
				}
			},
		},
		{
			name: "terminating serving endpoint is a drain advisory", count: 1, status: model.Checked, phrase: "Only terminating backend",
			setup: func(s *model.Snapshot) {
				s.EndpointSlices = []discovery.EndpointSlice{networkTestSlice(s.Services[0], "drain", 8080, discovery.EndpointConditions{
					Ready: new(false), Terminating: new(true), Serving: new(true),
				})}
			},
		},
		{
			name: "terminating published endpoint retains ready signal", count: 1, status: model.Checked, phrase: "Only terminating backend",
			setup: func(s *model.Snapshot) {
				s.Services[0].Spec.PublishNotReadyAddresses = true
				s.EndpointSlices = []discovery.EndpointSlice{networkTestSlice(s.Services[0], "published-terminating", 8080, discovery.EndpointConditions{
					Ready: new(true), Terminating: new(true), Serving: new(false),
				})}
			},
		},
		{
			name: "terminating endpoint that has stopped serving", count: 1, status: model.Checked, phrase: "No ready or draining",
			setup: func(s *model.Snapshot) {
				s.Pods = []core.Pod{networkTestPod(core.PodRunning)}
				s.EndpointSlices = []discovery.EndpointSlice{networkTestSlice(s.Services[0], "stopped", 8080, discovery.EndpointConditions{
					Ready: new(false), Terminating: new(true), Serving: new(false),
				})}
			},
		},
		{
			name: "slice belongs to an old Service UID", count: 1, status: model.Checked, phrase: "selector matches no active Pods",
			setup: func(s *model.Snapshot) {
				prior := s.Services[0]
				prior.UID = "prior-service"
				s.EndpointSlices = []discovery.EndpointSlice{networkTestSlice(prior, "old", 8080, discovery.EndpointConditions{})}
			},
		},
		{
			name: "intentional published unready addresses", status: model.Checked,
			setup: func(s *model.Snapshot) {
				s.Services[0].Spec.PublishNotReadyAddresses = true
				s.EndpointSlices = []discovery.EndpointSlice{networkTestSlice(s.Services[0], "published", 8080, discovery.EndpointConditions{
					Ready: new(true), Serving: new(false),
				})}
			},
		},
		{
			name: "EndpointSlice partial list cannot prove absence", status: model.Inconclusive,
			setup: func(s *model.Snapshot) { networkSetCoverage(s, model.SourceEndpointSlices, model.Truncated) },
		},
		{
			name: "Pod access loss preserves direct endpoint evidence", count: 1, status: model.Inconclusive, phrase: "No ready or draining",
			setup: func(s *model.Snapshot) { networkSetCoverage(s, model.SourcePods, model.PermissionDenied) },
		},
		{
			name: "reconciling new Service", status: model.Inconclusive,
			setup: func(s *model.Snapshot) {
				s.Services[0].CreationTimestamp = metav1.NewTime(s.CollectedAt.Add(-15 * time.Second))
			},
		},
		{
			name: "selectorless headless Service has manually managed address", status: model.Checked,
			setup: func(s *model.Snapshot) {
				s.Services[0].Spec = core.ServiceSpec{ClusterIP: core.ClusterIPNone}
				backend := networkTestSlice(s.Services[0], "manual", 8080, discovery.EndpointConditions{})
				backend.OwnerReferences = nil
				backend.Ports = nil
				s.EndpointSlices = []discovery.EndpointSlice{backend}
			},
		},
		{
			name: "selectorless Service empty states are ambiguous", status: model.Inconclusive,
			setup: func(s *model.Snapshot) { s.Services[0].Spec.Selector = nil },
		},
		{
			name: "ExternalName has no Kubernetes backends", status: model.Skipped,
			setup: func(s *model.Snapshot) { s.Services[0].Spec.Type = core.ServiceTypeExternalName },
		},
		{
			name: "completed Job with no remaining consumers", status: model.Checked,
			setup: func(s *model.Snapshot) {
				pod := networkTestPod(core.PodSucceeded)
				pod.OwnerReferences = []metav1.OwnerReference{{Kind: "Job", UID: "job-uid", Name: "api-job"}}
				s.Pods = []core.Pod{pod}
			},
		},
		{
			name: "desired zero is intentional", status: model.Checked,
			setup: func(s *model.Snapshot) {
				s.Deployments = []apps.Deployment{{ObjectMeta: networkTestObject("api"), Spec: apps.DeploymentSpec{
					Replicas: new(int32(0)), Template: core.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}}},
				}}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := networkFixture()
			snapshot.Services = []core.Service{networkTestService()}
			tc.setup(&snapshot)
			findings := networkCheckCase(t, snapshot, serviceBackendsID, tc.status, tc.count)
			if tc.phrase != "" && !strings.Contains(findings[0].Symptom, tc.phrase) {
				t.Fatalf("unexpected evidence: %s", findings[0].Symptom)
			}
		})
	}
}

func TestServiceSelectorDisagreementUsesControllerAndPodUID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		managedBy string
		samePod   bool
		count     int
	}{
		{"controller and anchored UID", endpointController, true, 1},
		{"same Pod name with replaced UID", endpointController, false, 0},
		{"manual slice leaves selector relation uncertain", "example-endpoint-controller", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := networkFixture()
			service := networkTestService()
			pod := networkTestPod(core.PodRunning)
			pod.Labels["app"] = "old-label"
			snapshot.Services = []core.Service{service}
			snapshot.Pods = []core.Pod{pod}
			slice := networkTestSlice(service, "published", 8080, discovery.EndpointConditions{})
			slice.Labels[discovery.LabelManagedBy] = tc.managedBy
			slice.Endpoints[0].TargetRef = &core.ObjectReference{Kind: "Pod", Name: pod.Name, Namespace: pod.Namespace, UID: pod.UID}
			if !tc.samePod {
				slice.Endpoints[0].TargetRef.UID = "old-pod"
			}
			snapshot.EndpointSlices = []discovery.EndpointSlice{slice, slice}
			findings := networkCheckCase(t, snapshot, serviceBackendsID, model.Checked, tc.count)
			if tc.count > 0 && (findings[0].Severity != model.Info || findings[0].Evidence[0].Value != "1") {
				t.Fatalf("expected one advisory for the unique Pod, got %#v", findings[0])
			}
		})
	}
}

func TestServiceBackendFindingReflectsKnownControllerIntent(t *testing.T) {
	for _, tc := range []struct {
		name         string
		controller   bool
		wantSeverity model.Severity
	}{
		{"unverified headless workload may be intentionally scaled down", false, model.Info},
		{"matching Deployment expects an active backend", true, model.Warning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := networkFixture()
			service := networkTestService()
			service.Spec.ClusterIP = core.ClusterIPNone
			snapshot.Services = []core.Service{service}
			if tc.controller {
				snapshot.Deployments = []apps.Deployment{{ObjectMeta: networkTestObject("api"), Spec: apps.DeploymentSpec{
					Replicas: new(int32(1)), Template: core.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}}},
				}}}
			}
			findings := networkCheckCase(t, snapshot, serviceBackendsID, model.Checked, 1)
			if findings[0].Severity != tc.wantSeverity {
				t.Fatalf("backend severity %q, expected %q", findings[0].Severity, tc.wantSeverity)
			}
		})
	}
}

func TestServiceLegacyFallbackRequiresUnavailableEndpointSlices(t *testing.T) {
	for _, tc := range []struct {
		slices model.Coverage
		status model.CheckStatus
		count  int
	}{
		{model.Unavailable, model.Checked, 0},
		{model.PermissionDenied, model.Inconclusive, 0},
		{model.Complete, model.Checked, 1},
	} {
		t.Run(string(tc.slices), func(t *testing.T) {
			snapshot := networkFixture()
			service := networkTestService()
			snapshot.Services = []core.Service{service}
			snapshot.LegacyEndpoints = []core.Endpoints{{ObjectMeta: service.ObjectMeta, Subsets: []core.EndpointSubset{{
				Addresses: []core.EndpointAddress{{}}, Ports: []core.EndpointPort{{Name: "http", Port: 8080}},
			}}}}
			networkSetCoverage(&snapshot, model.SourceEndpointSlices, tc.slices)
			networkSetCoverage(&snapshot, model.SourceLegacyEndpoints, model.Complete)
			networkCheckCase(t, snapshot, serviceBackendsID, tc.status, tc.count)
		})
	}
}

func TestServicePortMappings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*model.Snapshot)
		status model.CheckStatus
		count  int
	}{
		{
			name: "published port uses targetPort", status: model.Checked, count: 1,
			setup: func(s *model.Snapshot) { s.EndpointSlices[0].Ports[0].Port = new(int32(9090)) },
		},
		{
			name: "published port needs matching protocol", status: model.Checked, count: 1,
			setup: func(s *model.Snapshot) { s.EndpointSlices[0].Ports[0].Protocol = new(core.ProtocolUDP) },
		},
		{
			name: "matching slice and alternate named target number", status: model.Checked,
			setup: func(s *model.Snapshot) {
				s.Services[0].Spec.Ports[0].TargetPort = intstr.FromString("http-port")
				s.EndpointSlices = append(s.EndpointSlices, networkTestSlice(s.Services[0], "alt", 9090, discovery.EndpointConditions{}))
			},
		},
		{
			name: "unspecified endpoint port is indeterminate", status: model.Inconclusive,
			setup: func(s *model.Snapshot) { s.EndpointSlices[0].Ports[0].Port = nil },
		},
		{
			name: "headless Service targetPort is ignored", status: model.Checked,
			setup: func(s *model.Snapshot) {
				s.Services[0].Spec.ClusterIP = core.ClusterIPNone
				s.EndpointSlices[0].Ports[0].Port = new(int32(9090))
			},
		},
		{
			name: "missing named target in selected Pods survives incomplete port inspection", status: model.Inconclusive, count: 1,
			setup: func(s *model.Snapshot) {
				s.Services[0].Spec.Ports[0].TargetPort = intstr.FromString("http-port")
				s.EndpointSlices = nil
				s.Pods = []core.Pod{networkTestPod(core.PodRunning)}
			},
		},
		{
			name: "restartable init container satisfies named target", status: model.Inconclusive,
			setup: func(s *model.Snapshot) {
				s.Services[0].Spec.Ports[0].TargetPort = intstr.FromString("http-port")
				s.EndpointSlices = nil
				pod := networkTestPod(core.PodRunning)
				pod.Spec.InitContainers = []core.Container{{RestartPolicy: new(core.ContainerRestartPolicyAlways), Ports: []core.ContainerPort{{Name: "http-port", ContainerPort: 8080}}}}
				s.Pods = []core.Pod{pod}
			},
		},
		{
			name: "selectorless Service without published ports has no mapping evidence", status: model.Inconclusive,
			setup: func(s *model.Snapshot) {
				s.Services[0].Spec.Selector = nil
				s.EndpointSlices = nil
			},
		},
		{
			name: "scaled to zero Service has no applicable mapping", status: model.Skipped,
			setup: func(s *model.Snapshot) {
				s.EndpointSlices = nil
				s.Deployments = []apps.Deployment{{ObjectMeta: networkTestObject("api"), Spec: apps.DeploymentSpec{
					Replicas: new(int32(0)), Template: core.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}}},
				}}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := networkFixture()
			snapshot.Services = []core.Service{networkTestService()}
			snapshot.EndpointSlices = []discovery.EndpointSlice{networkTestSlice(snapshot.Services[0], "regular", 8080, discovery.EndpointConditions{})}
			tc.setup(&snapshot)
			networkCheckCase(t, snapshot, servicePortsID, tc.status, tc.count)
		})
	}
}

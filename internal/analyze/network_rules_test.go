package analyze

import (
	"testing"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	policy "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIngressReferences(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*model.Snapshot)
		status model.CheckStatus
		count  int
	}{
		{name: "healthy names and numbers", setup: func(*model.Snapshot) {}, status: model.Checked},
		{
			name: "Service exists in another namespace", status: model.Checked, count: 1,
			setup: func(s *model.Snapshot) { s.Services[0].Namespace = "another" },
		},
		{
			name: "targetPort is distinct from Service port", status: model.Checked, count: 1,
			setup: func(s *model.Snapshot) { s.Ingresses[0].Spec.DefaultBackend.Service.Port.Number = 8080 },
		},
		{
			name: "port configured for UDP", status: model.Checked, count: 2,
			setup: func(s *model.Snapshot) { s.Services[0].Spec.Ports[0].Protocol = core.ProtocolUDP },
		},
		{
			name: "Service list denied", status: model.Inconclusive,
			setup: func(s *model.Snapshot) {
				s.Services = nil
				networkSetCoverage(s, model.SourceServices, model.PermissionDenied)
			},
		},
		{
			name: "observed port discrepancy survives partial Service list", status: model.Inconclusive, count: 1,
			setup: func(s *model.Snapshot) {
				s.Ingresses[0].Spec.DefaultBackend.Service.Port.Number = 8080
				networkSetCoverage(s, model.SourceServices, model.Truncated)
			},
		},
		{
			name: "resource backend requires additional inspection", status: model.Inconclusive,
			setup: func(s *model.Snapshot) {
				s.Ingresses[0].Spec.DefaultBackend = &networking.IngressBackend{Resource: &core.TypedLocalObjectReference{Kind: "Bucket", Name: "files"}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := networkFixture()
			snapshot.Services = []core.Service{networkTestService()}
			defaultBackend := networking.IngressBackend{Service: &networking.IngressServiceBackend{Name: "api", Port: networking.ServiceBackendPort{Number: 80}}}
			pathBackend := networking.IngressBackend{Service: &networking.IngressServiceBackend{Name: "api", Port: networking.ServiceBackendPort{Name: "http"}}}
			snapshot.Ingresses = []networking.Ingress{{
				ObjectMeta: networkTestObject("api-ingress"),
				Spec: networking.IngressSpec{DefaultBackend: &defaultBackend, Rules: []networking.IngressRule{{
					IngressRuleValue: networking.IngressRuleValue{HTTP: &networking.HTTPIngressRuleValue{
						Paths: []networking.HTTPIngressPath{{Backend: pathBackend}, {Backend: pathBackend}},
					}},
				}}},
			}}
			tc.setup(&snapshot)
			networkCheckCase(t, snapshot, ingressBackendsID, tc.status, tc.count)
		})
	}
}

func networkDNSFixture() model.Snapshot {
	snapshot := networkFixture()
	service := networkTestService()
	service.Name, service.Namespace = dnsService, dnsNamespace
	service.Spec.Ports = []core.ServicePort{{Name: "dns", Port: 53, Protocol: core.ProtocolUDP}, {Name: "dns-tcp", Port: 53}}
	snapshot.DNSServices = []core.Service{service}
	slice := networkTestSlice(service, "ready", 53, discovery.EndpointConditions{Ready: new(true)})
	slice.Ports = []discovery.EndpointPort{{Name: new("dns"), Port: new(int32(53)), Protocol: new(core.ProtocolUDP)}, {Name: new("dns-tcp"), Port: new(int32(53))}}
	snapshot.DNSEndpointSlices = []discovery.EndpointSlice{slice}
	snapshot.DNSConfig = []model.DNSConfig{{Name: "coredns", Namespace: dnsNamespace, HasCorefile: true}}
	pod := networkTestPod(core.PodRunning)
	pod.Namespace = dnsNamespace
	pod.Status.Conditions = []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}
	snapshot.DNSPods = []core.Pod{pod}
	return snapshot
}

func TestDNSInfrastructure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*model.Snapshot)
		status model.CheckStatus
		count  int
	}{
		{name: "standard component and DNS port observations", setup: func(*model.Snapshot) {}, status: model.Checked},
		{
			name: "nonstandard DNS is outside observation", status: model.Inconclusive,
			setup: func(s *model.Snapshot) { s.DNSServices[0].Name = "custom-dns" },
		},
		{
			name: "DNS Service is missing TCP", status: model.Checked, count: 1,
			setup: func(s *model.Snapshot) { s.DNSServices[0].Spec.Ports = s.DNSServices[0].Spec.Ports[:1] },
		},
		{
			name: "DNS backend is missing UDP target", status: model.Checked, count: 1,
			setup: func(s *model.Snapshot) { s.DNSEndpointSlices[0].Ports = s.DNSEndpointSlices[0].Ports[1:] },
		},
		{
			name: "DNS backends are unavailable", status: model.Checked, count: 1,
			setup: func(s *model.Snapshot) { s.DNSEndpointSlices[0].Endpoints[0].Conditions.Ready = new(false) },
		},
		{
			name: "DNS backend visibility is denied", status: model.Inconclusive,
			setup: func(s *model.Snapshot) {
				s.DNSEndpointSlices = nil
				networkSetCoverage(s, model.SourceDNSEndpointSlices, model.PermissionDenied)
			},
		},
		{
			name: "conventional Corefile key absent", status: model.Checked, count: 1,
			setup: func(s *model.Snapshot) { s.DNSConfig[0].HasCorefile = false },
		},
		{
			name: "known configuration observation survives missing Service visibility", status: model.Inconclusive, count: 1,
			setup: func(s *model.Snapshot) {
				s.DNSServices = nil
				s.DNSConfig[0].HasCorefile = false
				networkSetCoverage(s, model.SourceDNSServices, model.PermissionDenied)
			},
		},
		{
			name: "CoreDNS configuration access denied", status: model.Inconclusive,
			setup: func(s *model.Snapshot) {
				s.DNSConfig = nil
				networkSetCoverage(s, model.SourceDNSConfig, model.PermissionDenied)
			},
		},
		{
			name: "published unready addresses do not establish Pod readiness", status: model.Checked, count: 1,
			setup: func(s *model.Snapshot) {
				s.DNSServices[0].Spec.PublishNotReadyAddresses = true
				s.DNSEndpointSlices[0].Endpoints[0].Conditions.Serving = new(false)
				s.DNSPods[0].Status.Conditions[0].Status = core.ConditionFalse
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := networkDNSFixture()
			tc.setup(&snapshot)
			networkCheckCase(t, snapshot, dnsID, tc.status, tc.count)
		})
	}
}

func TestNetworkPolicyAdditiveSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		setup     func(*model.Snapshot)
		status    model.CheckStatus
		direction string
	}{
		{name: "default policy type selects ingress only", setup: func(*model.Snapshot) {}, status: model.Checked, direction: "ingress"},
		{
			name: "explicit egress-only default deny", status: model.Checked, direction: "egress",
			setup: func(s *model.Snapshot) {
				s.NetworkPolicies[0].Spec.PolicyTypes = []networking.PolicyType{networking.PolicyTypeEgress}
			},
		},
		{
			name: "separate allow rule is additive", status: model.Checked,
			setup: func(s *model.Snapshot) {
				s.NetworkPolicies = append(s.NetworkPolicies, networking.NetworkPolicy{
					ObjectMeta: networkTestObject("allow"), Spec: networking.NetworkPolicySpec{Ingress: []networking.NetworkPolicyIngressRule{{}}},
				})
			},
		},
		{
			name: "another namespace cannot supply an allow rule", status: model.Checked, direction: "ingress",
			setup: func(s *model.Snapshot) {
				s.NetworkPolicies = append(s.NetworkPolicies, networking.NetworkPolicy{
					ObjectMeta: metav1.ObjectMeta{Namespace: "another", Name: "allow"}, Spec: networking.NetworkPolicySpec{Ingress: []networking.NetworkPolicyIngressRule{{}}},
				})
			},
		},
		{
			name: "selected hostNetwork implementation varies", status: model.Checked,
			setup: func(s *model.Snapshot) { s.Pods[0].Spec.HostNetwork = true },
		},
		{
			name: "select active Pods only", status: model.Checked,
			setup: func(s *model.Snapshot) { s.Pods[0].Status.Phase = core.PodSucceeded },
		},
		{
			name: "missing policies could contain allow rules", status: model.Inconclusive,
			setup: func(s *model.Snapshot) { networkSetCoverage(s, model.SourceNetworkPolicies, model.Truncated) },
		},
		{
			name: "invalid selector makes policy addition unknown", status: model.Inconclusive,
			setup: func(s *model.Snapshot) {
				s.NetworkPolicies[0].Spec.PodSelector.MatchExpressions[0].Operator = "unknown"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := networkFixture()
			snapshot.Pods = []core.Pod{networkTestPod(core.PodRunning)}
			snapshot.NetworkPolicies = []networking.NetworkPolicy{{
				ObjectMeta: networkTestObject("deny"),
				Spec: networking.NetworkPolicySpec{PodSelector: metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"api"}}},
				}},
			}}
			tc.setup(&snapshot)
			count := 0
			if tc.direction != "" {
				count = 1
			}
			findings := networkCheckCase(t, snapshot, networkPolicyID, tc.status, count)
			if count > 0 && (findings[0].Confidence != model.Hypothesis || findings[0].Severity != model.Info || findings[0].Evidence[0].Value != tc.direction) {
				t.Fatalf("expected an uncertainty-labeled %s observation: %#v", tc.direction, findings[0])
			}
		})
	}
}

func TestPDBDisruptions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*model.Snapshot)
		status model.CheckStatus
		count  int
	}{
		{name: "zero can be intentional", setup: func(*model.Snapshot) {}, status: model.Checked, count: 1},
		{
			name: "positive allowance", status: model.Checked,
			setup: func(s *model.Snapshot) { s.PDBs[0].Status.DisruptionsAllowed = 1 },
		},
		{
			name: "zero scaled to zero workload", status: model.Checked,
			setup: func(s *model.Snapshot) {
				s.PDBs[0].Status.ExpectedPods, s.PDBs[0].Status.CurrentHealthy, s.PDBs[0].Status.DesiredHealthy = 0, 0, 0
			},
		},
		{
			name: "stale generation", status: model.Inconclusive,
			setup: func(s *model.Snapshot) { s.PDBs[0].Generation++ },
		},
		{
			name: "sync failed", status: model.Inconclusive,
			setup: func(s *model.Snapshot) {
				s.PDBs[0].Status.Conditions = []metav1.Condition{{Type: policy.DisruptionAllowedCondition, Status: metav1.ConditionFalse, Reason: policy.SyncFailedReason}}
			},
		},
		{
			name: "complete status observed before torn list", status: model.Inconclusive, count: 1,
			setup: func(s *model.Snapshot) { networkSetCoverage(s, model.SourcePDBs, model.Truncated) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := networkFixture()
			meta := networkTestObject("api-disruptions")
			meta.Generation = 2
			snapshot.PDBs = []policy.PodDisruptionBudget{{
				ObjectMeta: meta,
				Status: policy.PodDisruptionBudgetStatus{
					ObservedGeneration: 2, CurrentHealthy: 2, DesiredHealthy: 2, ExpectedPods: 2, DisruptionsAllowed: 0,
				},
			}}
			tc.setup(&snapshot)
			findings := networkCheckCase(t, snapshot, pdbID, tc.status, tc.count)
			if tc.count > 0 && (findings[0].Severity != model.Info || findings[0].Confidence != model.Observed) {
				t.Fatalf("expected informational eviction constraint: %#v", findings[0])
			}
		})
	}
}

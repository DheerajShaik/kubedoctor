package investigation

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/bundle"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func policyPortSnapshot() model.Snapshot {
	s := eventComparisonSnapshot()
	addApplicableSources(&s, model.SourceNetworkPolicies)
	s.Pods = []core.Pod{{
		ObjectMeta: fixtureMeta("checkout", "pod-uid"),
		Spec:       core.PodSpec{Containers: []core.Container{{Name: "api"}}},
		Status:     core.PodStatus{Phase: core.PodRunning},
	}}
	s.NetworkPolicies = []networking.NetworkPolicy{{
		ObjectMeta: fixtureMeta("named-port", "policy-uid"),
		Spec: networking.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
			Ingress: []networking.NetworkPolicyIngressRule{{
				Ports: []networking.NetworkPolicyPort{{Port: new(intstr.FromString("api"))}},
			}},
		},
	}}
	return s
}

func TestPolicyNamedPortRecoveryNeedsSelectedDeclaration(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*model.Snapshot)
		want Classification
	}{
		{"selected workload now declares the named port", func(s *model.Snapshot) {
			s.Pods[0].Spec.Containers[0].Ports = []core.ContainerPort{{Name: "api", ContainerPort: 8080}}
		}, Resolved},
		{"selected restartable init container now declares the named port", func(s *model.Snapshot) {
			s.Pods[0].Spec.InitContainers = []core.Container{{
				Name: "sidecar", RestartPolicy: new(core.ContainerRestartPolicyAlways),
				Ports: []core.ContainerPort{{Name: "api", ContainerPort: 8080}},
			}}
		}, Resolved},
		{"a terminal Pod's named port is not current evidence", func(s *model.Snapshot) {
			s.Pods[0].Status.Phase = core.PodSucceeded
			s.Pods[0].Spec.Containers[0].Ports = []core.ContainerPort{{Name: "api", ContainerPort: 8080}}
		}, NoLongerObservable},
		{"all selected Pods disappeared", func(s *model.Snapshot) {
			s.Pods = nil
		}, NoLongerObservable},
		{"policy no longer selects Pods", func(s *model.Snapshot) {
			s.NetworkPolicies[0].Spec.PodSelector.MatchLabels = map[string]string{"app": "unknown"}
		}, NoLongerObservable},
		{"policy no longer applies to the named port", func(s *model.Snapshot) {
			s.NetworkPolicies[0].Spec.Ingress[0].Ports[0].Port = new(intstr.FromInt32(8080))
		}, NoLongerObservable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := policyPortSnapshot()
			before := replayNetworkContinuity(t, s)
			after := advanceObservation(s, time.Minute)
			tt.edit(&after)
			requireNetworkClassification(t, before, after, "networkpolicy-named-ports", "policy-uid", tt.want)
		})
	}
}

func TestPolicyNamedPortsAreComparedSeparately(t *testing.T) {
	s := policyPortSnapshot()
	s.NetworkPolicies[0].Spec.Ingress[0].Ports = append(s.NetworkPolicies[0].Spec.Ingress[0].Ports,
		networking.NetworkPolicyPort{Port: new(intstr.FromString("metrics"))})
	before := replayNetworkContinuity(t, s)
	after := advanceObservation(s, time.Minute)
	after.Pods[0].Spec.Containers[0].Ports = []core.ContainerPort{{Name: "api", ContainerPort: 8080}}
	comparison := Compare(before, replayNetworkContinuity(t, after))
	for port, want := range map[string]Classification{"api": Resolved, "metrics": Persistent} {
		if !slices.ContainsFunc(comparison.WorkloadChanges, func(change Change) bool {
			return change.RuleID == "networkpolicy-named-ports" && change.Subject.UID == "policy-uid" &&
				change.Classification == want && len(change.Before) == 1 && evidenceValue(change.Before[0].Finding, "named_port") == port
		}) {
			t.Fatalf("expected independent %s change for %s: %+v", want, port, comparison.WorkloadChanges)
		}
	}
}

func conventionalDNSSnapshot(publish, endpointReady bool) model.Snapshot {
	s := eventComparisonSnapshot()
	addApplicableSources(&s, model.SourceDNSServices, model.SourceDNSPods, model.SourceDNSConfig, model.SourceDNSEndpointSlices)
	service := core.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system", Name: "kube-dns", UID: "dns-uid",
			CreationTimestamp: metav1.NewTime(baselineTime.Add(-time.Hour)),
		},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"k8s-app": "kube-dns"}, PublishNotReadyAddresses: publish,
			Ports: []core.ServicePort{
				{Name: "dns", Protocol: core.ProtocolUDP, Port: 53, TargetPort: intstr.FromInt32(53)},
				{Name: "dns-tcp", Protocol: core.ProtocolTCP, Port: 53, TargetPort: intstr.FromInt32(53)},
			},
		},
	}
	s.DNSServices = []core.Service{service}
	s.DNSPods = []core.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system", Name: "coredns", UID: "coredns-uid", Labels: map[string]string{"k8s-app": "kube-dns"},
		}, Status: core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionFalse}}},
	}}
	s.DNSConfig = []model.DNSConfig{{Namespace: "kube-system", Name: "coredns", HasCorefile: true}}
	s.DNSEndpointSlices = []discovery.EndpointSlice{{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system", Name: "kube-dns-test", UID: "dns-slice-uid",
			Labels:          map[string]string{discovery.LabelServiceName: service.Name},
			OwnerReferences: []metav1.OwnerReference{owner("Service", &service)},
		},
		AddressType: discovery.AddressTypeIPv4,
		Ports: []discovery.EndpointPort{
			{Name: new("dns"), Protocol: new(core.ProtocolUDP), Port: new(int32(53))},
			{Name: new("dns-tcp"), Protocol: new(core.ProtocolTCP), Port: new(int32(53))},
		},
		Endpoints: []discovery.Endpoint{{
			Addresses: []string{"192.0.2.5"}, Conditions: discovery.EndpointConditions{Ready: &endpointReady},
		}},
	}}
	return s
}

func TestDNSReadinessRecoveryNeedsSelectedReadyPod(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*model.Snapshot)
		want Classification
	}{
		{"turning off publication hides unchanged unready status", func(s *model.Snapshot) {
			s.DNSServices[0].Spec.PublishNotReadyAddresses = false
		}, NoLongerObservable},
		{"turning off publication with observed Pod recovery", func(s *model.Snapshot) {
			s.DNSServices[0].Spec.PublishNotReadyAddresses = false
			s.DNSPods[0].Status.Conditions[0].Status = core.ConditionTrue
		}, Resolved},
		{"actual Pod recovery without a Service change", func(s *model.Snapshot) {
			s.DNSPods[0].Status.Conditions[0].Status = core.ConditionTrue
		}, Resolved},
		{"new selector hides the previously unready cohort", func(s *model.Snapshot) {
			s.DNSServices[0].Spec.Selector = map[string]string{"provider": "different"}
			s.DNSPods[0].Labels = map[string]string{"k8s-app": "kube-dns", "provider": "different"}
			s.DNSPods[0].Status.Conditions[0].Status = core.ConditionTrue
		}, NoLongerObservable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := conventionalDNSSnapshot(true, true)
			before := replayNetworkContinuity(t, s)
			after := advanceObservation(s, time.Minute)
			tt.edit(&after)
			requireNetworkClassification(t, before, after, "dns.infrastructure", "dns-uid", tt.want)
		})
	}
}

func TestDNSBackendRecoveryRequiresUsableEndpointAndPod(t *testing.T) {
	for _, tt := range []struct {
		name  string
		ready bool
		want  Classification
	}{
		{"published Ready without actual readiness", false, NoLongerObservable},
		{"serving endpoint and explicitly Ready Pod", true, Resolved},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := conventionalDNSSnapshot(false, false)
			before := replayNetworkContinuity(t, s)
			after := advanceObservation(s, time.Minute)
			after.DNSServices[0].Spec.PublishNotReadyAddresses = true
			after.DNSEndpointSlices[0].Endpoints[0].Conditions.Ready = new(true)
			if tt.ready {
				after.DNSEndpointSlices[0].Endpoints[0].Conditions.Serving = new(true)
				after.DNSPods[0].Status.Conditions[0].Status = core.ConditionTrue
			}
			comparison := Compare(before, replayNetworkContinuity(t, after))
			if !slices.ContainsFunc(comparison.WorkloadChanges, func(change Change) bool {
				return change.RuleID == "dns.infrastructure" && change.Subject.UID == "dns-uid" &&
					change.Classification == tt.want && len(change.Before) == 1 &&
					evidenceValue(change.Before[0].Finding, "usable_backend_entries") == "0"
			}) {
				t.Fatalf("expected DNS backend classification %s: %+v", tt.want, comparison.WorkloadChanges)
			}
		})
	}
}

func replayNetworkContinuity(t *testing.T, s model.Snapshot) Investigation {
	t.Helper()
	input, err := bundle.New(s, "network-test")
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := input.Encode(&encoded); err != nil {
		t.Fatal(err)
	}
	decoded, err := bundle.Decode(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := FromBundle(decoded, bundle.Replay)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func requireNetworkClassification(t *testing.T, before Investigation, after model.Snapshot, rule, uid string, want Classification) {
	t.Helper()
	comparison := Compare(before, replayNetworkContinuity(t, after))
	change := pickChange(t, comparison.WorkloadChanges, rule, uid)
	if change.Classification != want {
		t.Fatalf("expected %s, got %+v", want, change)
	}
	if want == NoLongerObservable && comparison.Complete {
		t.Fatal("a loss of recovery evidence passed the comparison gate")
	}
}

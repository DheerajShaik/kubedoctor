package investigation

import (
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func endpointIdentitySnapshot() model.Snapshot {
	s := eventComparisonSnapshot()
	s.Services = []core.Service{{
		ObjectMeta: fixtureMeta("frontend", "service-uid"),
		Spec:       core.ServiceSpec{Selector: map[string]string{"app": "checkout"}},
	}}
	s.Pods = []core.Pod{
		{
			ObjectMeta: fixtureMeta("replacement", "current-uid"),
			Status: core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{
				{Type: core.PodReady, Status: core.ConditionTrue},
			}},
		},
		{
			ObjectMeta: fixtureMeta("other", "other-uid"),
			Status: core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{
				{Type: core.PodReady, Status: core.ConditionTrue},
			}},
		},
	}
	target := endpointIdentitySlice("sibling", "slice-uid", discovery.AddressTypeIPv4, &s.Services[0], &s.Pods[0])
	target.Endpoints[0].TargetRef.UID = "stale-uid"
	other := endpointIdentitySlice("other", "other-slice-uid", discovery.AddressTypeIPv4, &s.Services[0], &s.Pods[1])
	s.EndpointSlices = []discovery.EndpointSlice{target, other}
	return s
}

func endpointIdentitySlice(name, uid string, family discovery.AddressType, service *core.Service, pod *core.Pod) discovery.EndpointSlice {
	meta := fixtureMeta(name, uid)
	meta.Labels = map[string]string{
		discovery.LabelServiceName: service.Name,
		discovery.LabelManagedBy:   "endpointslice-controller.k8s.io",
	}
	meta.OwnerReferences = []metav1.OwnerReference{owner("Service", service)}
	return discovery.EndpointSlice{
		ObjectMeta: meta, AddressType: family,
		Endpoints: []discovery.Endpoint{{
			Addresses:  []string{"address"},
			Conditions: discovery.EndpointConditions{Ready: new(true)},
			TargetRef:  &core.ObjectReference{Kind: "Pod", Name: pod.Name, Namespace: pod.Namespace, UID: pod.UID},
		}},
	}
}

func TestEndpointIdentityRecoveryRequiresImplicatedCurrentPod(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*model.Snapshot)
		want Classification
	}{
		{"same-name current Pod disappears while old target remains", func(s *model.Snapshot) {
			s.Pods = s.Pods[1:]
		}, NoLongerObservable},
		{"an unrelated ready endpoint survives after the implicated target disappears", func(s *model.Snapshot) {
			s.EndpointSlices = s.EndpointSlices[1:]
		}, NoLongerObservable},
		{"ownerless slice cannot prove a repaired target", func(s *model.Snapshot) {
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
			s.EndpointSlices[0].OwnerReferences = nil
		}, NoLongerObservable},
		{"foreign Service UID cannot prove a repaired target", func(s *model.Snapshot) {
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
			s.EndpointSlices[0].OwnerReferences[0].UID = "old-service-uid"
		}, NoLongerObservable},
		{"conflicting sibling Service owner keeps a current target unverified", func(s *model.Snapshot) {
			stale := s.EndpointSlices[0].DeepCopy()
			stale.Name, stale.UID = "foreign", "foreign-slice-uid"
			stale.OwnerReferences[0].UID = "old-service-uid"
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
			s.EndpointSlices = append(s.EndpointSlices, *stale)
		}, NoLongerObservable},
		{"unknown manager cannot prove a repaired target", func(s *model.Snapshot) {
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
			delete(s.EndpointSlices[0].Labels, discovery.LabelManagedBy)
		}, NoLongerObservable},
		{"another address family cannot prove a repaired target", func(s *model.Snapshot) {
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
			s.EndpointSlices[0].AddressType = discovery.AddressTypeIPv6
		}, NoLongerObservable},
		{"a terminating endpoint cannot prove a repaired target", func(s *model.Snapshot) {
			s.EndpointSlices[0].Endpoints[0].Conditions.Terminating = new(true)
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
		}, NoLongerObservable},
		{"endpoint Ready disagrees with the current Pod", func(s *model.Snapshot) {
			s.Pods[0].Status.Conditions[0].Status = core.ConditionFalse
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
		}, NoLongerObservable},
		{"an endpoint without a Pod target cannot prove a repaired target", func(s *model.Snapshot) {
			s.EndpointSlices[0].Endpoints[0].TargetRef = nil
		}, NoLongerObservable},
		{"a changed selector does not verify continuity", func(s *model.Snapshot) {
			s.Services[0].Spec.Selector = map[string]string{"app": "different"}
			s.Pods[0].Labels["app"] = "different"
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
		}, NoLongerObservable},
		{"an endpoint with missing UID cannot prove a repaired target", func(s *model.Snapshot) {
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = ""
		}, NoLongerObservable},
		{"related nonterminating target still has unknown UID", func(s *model.Snapshot) {
			other := s.EndpointSlices[0].Endpoints[0].DeepCopy()
			other.TargetRef.UID = ""
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
			s.EndpointSlices[0].Endpoints = append(s.EndpointSlices[0].Endpoints, *other)
		}, NoLongerObservable},
		{"same-family controller slice publishes correct current UID", func(s *model.Snapshot) {
			s.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
		}, Resolved},
		{"controller recreates the slice with an observed newer Pod", func(s *model.Snapshot) {
			s.Pods[0].UID = "later-pod-uid"
			s.EndpointSlices[0] = endpointIdentitySlice("new-slice", "new-slice-uid", discovery.AddressTypeIPv4, &s.Services[0], &s.Pods[0])
		}, Resolved},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := endpointIdentitySnapshot()
			before := replayNetworkContinuity(t, s)
			after := advanceObservation(s, time.Minute)
			tt.edit(&after)
			requireNetworkClassification(t, before, after, "service-endpoint-identity", "service-uid", tt.want)
		})
	}
}

func TestEndpointIdentityRecoveryCoversEveryAffectedNameAndFamily(t *testing.T) {
	for _, tt := range []struct {
		name string
		lost int
		want Classification
	}{
		{"entire affected set has current target evidence", -1, Resolved},
		{"one family of the same Pod disappeared", 2, NoLongerObservable},
		{"one affected Pod name disappeared from endpoints", 3, NoLongerObservable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := endpointIdentitySnapshot()
			secondFamily := endpointIdentitySlice("sibling-ipv6", "slice-ipv6-uid", discovery.AddressTypeIPv6, &s.Services[0], &s.Pods[0])
			secondName := endpointIdentitySlice("other-ipv6", "other-ipv6-uid", discovery.AddressTypeIPv6, &s.Services[0], &s.Pods[1])
			secondFamily.Endpoints[0].TargetRef.UID = "stale-uid"
			secondName.Endpoints[0].TargetRef.UID = "other-stale-uid"
			s.EndpointSlices = append(s.EndpointSlices, secondFamily, secondName)
			before := replayNetworkContinuity(t, s)
			after := advanceObservation(s, time.Minute)
			after.EndpointSlices[0].Endpoints[0].TargetRef.UID = "current-uid"
			after.EndpointSlices[2].Endpoints[0].TargetRef.UID = "current-uid"
			after.EndpointSlices[3].Endpoints[0].TargetRef.UID = "other-uid"
			if tt.lost >= 0 {
				after.EndpointSlices = append(after.EndpointSlices[:tt.lost], after.EndpointSlices[tt.lost+1:]...)
			}
			requireNetworkClassification(t, before, after, "service-endpoint-identity", "service-uid", tt.want)
		})
	}
}

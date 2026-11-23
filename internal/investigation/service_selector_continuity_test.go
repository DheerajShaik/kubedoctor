package investigation

import (
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func selectorMismatchSnapshot() model.Snapshot {
	snapshot := eventComparisonSnapshot()
	pod := core.Pod{ObjectMeta: fixtureMeta("checkout-pod", "old-selected-pod-uid"), Status: core.PodStatus{Phase: core.PodRunning}}
	pod.Labels["app"] = "different-workload"
	service := core.Service{ObjectMeta: fixtureMeta("checkout", "selector-service-uid"), Spec: core.ServiceSpec{
		Selector: map[string]string{"app": "checkout"}, Ports: []core.ServicePort{{Name: "http", Port: 80}},
	}}
	slice := discovery.EndpointSlice{ObjectMeta: fixtureMeta("checkout-managed", "managed-slice-uid"), AddressType: discovery.AddressTypeIPv4,
		Ports: []discovery.EndpointPort{{Name: new("http"), Port: new(int32(80))}}}
	slice.Labels = map[string]string{discovery.LabelServiceName: service.Name, discovery.LabelManagedBy: "endpointslice-controller.k8s.io"}
	slice.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: service.Name, UID: service.UID}}
	slice.Endpoints = []discovery.Endpoint{readySelectorEndpoint(pod)}
	snapshot.Pods = []core.Pod{pod}
	snapshot.Services = []core.Service{service}
	snapshot.EndpointSlices = []discovery.EndpointSlice{slice}
	return snapshot
}

func readySelectorEndpoint(pod core.Pod) discovery.Endpoint {
	return discovery.Endpoint{Addresses: []string{""}, Conditions: discovery.EndpointConditions{Ready: new(true)},
		TargetRef: &core.ObjectReference{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID}}
}

func addCurrentSelectorTarget(snapshot *model.Snapshot) {
	pod := core.Pod{ObjectMeta: fixtureMeta("other-checkout-pod", "selected-pod-uid"), Status: core.PodStatus{Phase: core.PodRunning}}
	snapshot.Pods = append(snapshot.Pods, pod)
	snapshot.EndpointSlices[0].Endpoints = append(snapshot.EndpointSlices[0].Endpoints, readySelectorEndpoint(pod))
}

func TestServiceSelectorRecoveryRequiresCurrentManagedTargetIdentities(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*model.Snapshot)
		want Classification
	}{
		{"old target Pod disappeared while slice remains Ready", func(s *model.Snapshot) {
			s.Pods = nil
		}, NoLongerObservable},
		{"unrelated Ready target cannot hide a dangling UID", func(s *model.Snapshot) {
			s.Pods = nil
			addCurrentSelectorTarget(s)
		}, NoLongerObservable},
		{"a nonready stale target is still present beside a Ready target", func(s *model.Snapshot) {
			s.Pods = nil
			s.EndpointSlices[0].Endpoints[0].Conditions.Ready = new(false)
			addCurrentSelectorTarget(s)
		}, NoLongerObservable},
		{"a draining stale target can still serve", func(s *model.Snapshot) {
			s.Pods = nil
			s.EndpointSlices[0].Endpoints[0].Conditions = discovery.EndpointConditions{
				Ready: new(false), Serving: new(true), Terminating: new(true),
			}
			addCurrentSelectorTarget(s)
		}, NoLongerObservable},
		{"same name with a different UID does not corroborate the old endpoint", func(s *model.Snapshot) {
			s.Pods[0].UID = "replacement-uid"
			s.Pods[0].Labels["app"] = "checkout"
		}, NoLongerObservable},
		{"a managed slice lost its verified owner", func(s *model.Snapshot) {
			s.Pods = nil
			s.EndpointSlices[0].OwnerReferences[0].UID = "other-service-uid"
			repaired := *s.EndpointSlices[0].DeepCopy()
			repaired.Name, repaired.UID = "checkout-current", "current-slice-uid"
			repaired.OwnerReferences[0].UID = s.Services[0].UID
			repaired.Endpoints = nil
			s.EndpointSlices = append([]discovery.EndpointSlice{repaired}, s.EndpointSlices...)
			addCurrentSelectorTarget(s)
		}, NoLongerObservable},
		{"endpoint with omitted Pod identity remains ambiguous", func(s *model.Snapshot) {
			s.Pods = nil
			s.EndpointSlices[0].Endpoints[0].TargetRef = nil
			addCurrentSelectorTarget(s)
		}, NoLongerObservable},
		{"Pod labels now match the same published UID", func(s *model.Snapshot) {
			s.Pods[0].Labels["app"] = "checkout"
		}, Resolved},
		{"the stale target was removed and the remaining target is verified", func(s *model.Snapshot) {
			s.Pods = nil
			s.EndpointSlices[0].Endpoints = nil
			addCurrentSelectorTarget(s)
		}, Resolved},
		{"a terminating target with no readiness or serving leaves no eligible stale target", func(s *model.Snapshot) {
			s.Pods = nil
			s.EndpointSlices[0].Endpoints[0].Conditions = discovery.EndpointConditions{
				Ready: new(false), Serving: new(false), Terminating: new(true),
			}
			addCurrentSelectorTarget(s)
		}, Resolved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := selectorMismatchSnapshot()
			before := replayNetworkContinuity(t, s)
			after := advanceObservation(s, time.Minute)
			tc.edit(&after)
			requireNetworkClassification(t, before, after, "service.backends", "selector-service-uid", tc.want)
		})
	}
}

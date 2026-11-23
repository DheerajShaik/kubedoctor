package collect

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/bundle"
	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/investigation"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestNamespaceDNSNamedTargetThroughCaptureReplayAndComparison(t *testing.T) {
	created := metav1.NewTime(time.Now().UTC().Add(-time.Hour))
	baselineOnline, baselineReplay := captureNamespaceDNS(t, created, false, "", 0)
	for _, scenario := range []struct {
		name, placement string
		port            int32
		want            investigation.Classification
		exit            int
	}{
		{"app port corroborates the repaired publication", "app", 5353, investigation.Resolved, 0},
		{"restartable init port corroborates the repaired publication", "sidecar", 5353, investigation.Resolved, 0},
		{"missing declaration leaves recovery unverified", "", 0, investigation.NoLongerObservable, 2},
		{"different declared number leaves recovery unverified", "app", 5354, investigation.NoLongerObservable, 2},
		{"different declared protocol leaves recovery unverified", "udp", 5353, investigation.NoLongerObservable, 2},
		{"ordinary init port cannot corroborate the repaired publication", "init", 5353, investigation.NoLongerObservable, 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			afterOnline, afterReplay := captureNamespaceDNS(t, created, true, scenario.placement, scenario.port)
			logicalOnline := investigation.Compare(baselineOnline, afterOnline)
			replayed := investigation.Compare(baselineReplay, afterReplay)
			checkDNSNamedPortGate(t, logicalOnline, scenario.want, scenario.exit)
			checkDNSNamedPortGate(t, replayed, scenario.want, scenario.exit)
		})
	}
}

func captureNamespaceDNS(t *testing.T, created metav1.Time, publishCorrectly bool, placement string, number int32) (investigation.Investigation, investigation.Investigation) {
	t.Helper()
	const rawAddress = "192.0.2.177"
	const dnsUID = "scope-dns-uid"
	labels := map[string]string{"k8s-app": "kube-dns"}
	dns := core.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system", Name: "kube-dns", UID: dnsUID, CreationTimestamp: created,
		},
		Spec: core.ServiceSpec{Selector: labels, Ports: []core.ServicePort{
			{Name: "dns-udp", Protocol: core.ProtocolUDP, Port: 53, TargetPort: intstr.FromInt32(53)},
			{Name: "dns-tcp", Protocol: core.ProtocolTCP, Port: 53, TargetPort: intstr.FromString("dns-listener")},
		}},
	}
	container := core.Container{
		Name: "resolver", Image: sensitive, Command: []string{sensitive},
		Env: []core.EnvVar{{Name: "FIXTURE_TOKEN", Value: sensitive}},
	}
	pod := core.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system", Name: "scope-coredns", UID: "scope-coredns-uid", CreationTimestamp: created,
			Labels:      map[string]string{"k8s-app": "kube-dns", "private": sensitive},
			Annotations: map[string]string{"private": sensitive},
		},
		Spec:   core.PodSpec{Containers: []core.Container{container}},
		Status: core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}},
	}
	declared := []core.ContainerPort{{Name: "dns-listener", ContainerPort: number, Protocol: core.ProtocolTCP}}
	switch placement {
	case "app", "udp":
		if placement == "udp" {
			declared[0].Protocol = core.ProtocolUDP
		}
		pod.Spec.Containers[0].Ports = declared
	case "init", "sidecar":
		container.Name, container.Ports = "dns-init", declared
		if placement == "sidecar" {
			container.RestartPolicy = new(core.ContainerRestartPolicyAlways)
		}
		pod.Spec.InitContainers = []core.Container{container}
	}
	tcpName := "stale-tcp-port"
	if publishCorrectly {
		tcpName = "dns-tcp"
	}
	published := discovery.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system", Name: "scope-dns-slice", UID: "scope-dns-slice-uid",
			Labels: map[string]string{discovery.LabelServiceName: dns.Name, discovery.LabelManagedBy: "endpointslice-controller.k8s.io"},
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "v1", Kind: "Service", Name: dns.Name, UID: dns.UID, Controller: new(true)},
			},
		},
		AddressType: discovery.AddressTypeIPv4,
		Ports: []discovery.EndpointPort{
			{Name: new("dns-udp"), Port: new(int32(53)), Protocol: new(core.ProtocolUDP)},
			{Name: &tcpName, Port: new(int32(5353)), Protocol: new(core.ProtocolTCP)},
		},
		Endpoints: []discovery.Endpoint{{
			Addresses:  []string{rawAddress},
			Conditions: discovery.EndpointConditions{Ready: new(true), Serving: new(true), Terminating: new(false)},
			TargetRef:  &core.ObjectReference{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID},
		}},
	}
	collector, wire := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
		var response any
		switch req.URL.Path {
		case "/api/v1/namespaces/payments":
			response = core.Namespace{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
				ObjectMeta: metav1.ObjectMeta{Name: "payments", UID: "payments-uid"},
			}
		case "/api/v1/namespaces/kube-system/services/kube-dns":
			response = dns
		case "/api/v1/namespaces/kube-system/pods":
			if req.URL.Query().Get("labelSelector") != dnsPodSelector {
				t.Errorf("DNS Pod read used an unexpected selector: %v", req.URL.Query())
			}
			response = core.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []core.Pod{pod}}
		case "/apis/discovery.k8s.io/v1/namespaces/kube-system/endpointslices":
			if req.URL.Query().Get("labelSelector") != dnsSliceSelector {
				t.Errorf("DNS slice read used an unexpected selector: %v", req.URL.Query())
			}
			response = discovery.EndpointSliceList{
				TypeMeta: metav1.TypeMeta{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSliceList"}, Items: []discovery.EndpointSlice{published},
			}
		default:
			return false
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode Kubernetes fixture: %v", err)
		}
		return true
	}, 0)
	scope := model.Scope{Kind: model.NamespaceScope, Namespace: "payments", Context: "fixture"}
	snapshot := engine.New(collector).Capture(t.Context(), model.ScanRequest{Scope: scope})
	if !wire.called("/api/v1/namespaces/kube-system/pods") || !wire.called("/apis/discovery.k8s.io/v1/namespaces/kube-system/endpointslices") ||
		wire.called("/api/v1/pods") || len(snapshot.Pods) != 0 || len(snapshot.DNSPods) != 1 {
		t.Fatalf("expected DNS evidence only from scoped supplements: %+v", snapshot.Sources)
	}
	online := investigation.Build(snapshot, engine.Analyze(snapshot))
	saved, err := bundle.New(snapshot, "dns-named-target-test")
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if err := saved.Encode(&raw); err != nil {
		t.Fatal(err)
	}
	decoded, err := bundle.Decode(bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := investigation.FromBundle(decoded, bundle.Replay)
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []investigation.Investigation{online, replayed} {
		rendered, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{sensitive, rawAddress, "FIXTURE_TOKEN"} {
			if bytes.Contains(raw.Bytes(), []byte(forbidden)) || bytes.Contains(rendered, []byte(forbidden)) {
				t.Fatal("DNS supplemental proof retained unrelated or sensitive fields")
			}
		}
	}
	return online, replayed
}

func checkDNSNamedPortGate(t *testing.T, result investigation.Comparison, expected investigation.Classification, exit int) {
	t.Helper()
	found := slices.ContainsFunc(result.WorkloadChanges, func(change investigation.Change) bool {
		return change.RuleID == "dns.infrastructure" && change.Subject.UID == "scope-dns-uid" &&
			change.Classification == expected && len(change.Before) == 1 && len(change.After) == 0 &&
			slices.Contains(change.Before[0].Finding.Evidence, model.Evidence{Field: "port_name", Value: "dns-tcp"})
	})
	if !result.Compatible || !found || investigation.ExitCode(result, model.Warning) != exit {
		t.Fatalf("expected named DNS port %s and gate exit %d, got %+v", expected, exit, result)
	}
	rendered, err := json.Marshal(result)
	if err != nil || bytes.Contains(rendered, []byte(sensitive)) || bytes.Contains(rendered, []byte("192.0.2.177")) ||
		bytes.Contains(rendered, []byte("FIXTURE_TOKEN")) {
		t.Fatal("comparison output was invalid or contained raw DNS source fields")
	}
}

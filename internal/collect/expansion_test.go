package collect

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
)

func TestCollectExpansionScopedEvidenceAndTimings(t *testing.T) {
	collector, fixture := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
		switch req.URL.Path {
		case "/api/v1/namespaces/payments":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"payments","uid":"payment-exclusive-namespace-uid"}}`)
		case "/api/v1/namespaces/payments/pods":
			item := fmt.Sprintf(`{"metadata":{"namespace":"payments","name":"app-0","uid":"uid-pod"},"spec":{"nodeName":"node-one","containers":[{"name":"app","env":[{"name":"PRIVATE","value":%q}],"readinessProbe":{"exec":{"command":[%q]}}}]},"status":{"phase":"Running","containerStatuses":[{"name":"app","containerID":%q}]} }`, sensitive, sensitive, "containerd://"+strings.Repeat("a", 64))
			list(w, "v1", "PodList", "", item)
		case "/api/v1/nodes/node-one":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-one","uid":"uid-node"}}`)
		case "/api/v1/namespaces/payments/events":
			probe := fmt.Sprintf(`{"metadata":{"namespace":"payments","name":"warning"},"involvedObject":{"kind":"Pod","namespace":"payments","name":"app-0","uid":"uid-pod","fieldPath":"spec.containers{app}"},"type":"Warning","reason":"Unhealthy","source":{"component":"kubelet"},"message":%q,"eventTime":%q}`, "Readiness probe failed: "+sensitive, time.Now().Add(-time.Second).UTC().Format(microTime))
			list(w, "v1", "EventList", "", probe)
		case "/apis/apps/v1/namespaces/payments/statefulsets":
			list(w, "apps/v1", "StatefulSetList", "", `{"metadata":{"namespace":"payments","name":"app","uid":"uid-set","generation":2},"spec":{"replicas":2,"ordinals":{"start":1},"volumeClaimTemplates":[{"metadata":{"name":"data"}}]},"status":{"observedGeneration":2,"readyReplicas":1}}`)
		case "/apis/batch/v1/namespaces/payments/jobs":
			status(w, http.StatusNotFound, "NotFound")
		case "/apis/batch/v1/namespaces/payments/cronjobs":
			status(w, http.StatusForbidden, "Forbidden")
		case "/apis/autoscaling/v2/namespaces/payments/horizontalpodautoscalers":
			list(w, "autoscaling/v2", "HorizontalPodAutoscalerList", "", fmt.Sprintf(`{"metadata":{"namespace":"payments","name":"app","uid":"uid-hpa","generation":1},"spec":{"scaleTargetRef":{"apiVersion":"apps/v1","kind":"StatefulSet","name":"app"},"maxReplicas":3,"metrics":[{"type":"Resource","resource":{"name":"cpu","target":{"type":"Utilization","averageUtilization":60}}}]},"status":{"conditions":[{"type":"ScalingActive","status":"False","reason":"FailedGetResourceMetric","message":%q}]}}`, sensitive))
		case "/apis/apps/v1/namespaces/kube-system/daemonsets/node-local-dns":
			fmt.Fprint(w, `{"apiVersion":"apps/v1","kind":"DaemonSet","metadata":{"namespace":"kube-system","name":"node-local-dns","uid":"cache-uid","generation":1},"status":{"observedGeneration":1,"desiredNumberScheduled":2,"numberReady":1}}`)
		case "/api/v1/namespaces/kube-system/pods":
			if req.URL.Query().Get("labelSelector") != nodeLocalSelector {
				return false
			}
			local := `{"metadata":{"namespace":"kube-system","name":"node-local-one","uid":"uid-cache-one","labels":{"k8s-app":"node-local-dns"},"ownerReferences":[{"kind":"DaemonSet","name":"node-local-dns","uid":"cache-uid","controller":true}]},"spec":{"nodeName":"node-one"},"status":{"conditions":[{"type":"Ready","status":"False"}]}}`
			other := `{"metadata":{"namespace":"kube-system","name":"node-local-two","uid":"uid-cache-two","labels":{"k8s-app":"node-local-dns"},"ownerReferences":[{"kind":"DaemonSet","name":"node-local-dns","uid":"cache-uid","controller":true}]},"spec":{"nodeName":"other-node"}}`
			list(w, "v1", "PodList", "", local+","+other)
		default:
			return false
		}
		return true
	}, time.Second)
	s := collector.Collect(t.Context(), model.ScanRequest{Scope: model.Scope{Kind: model.NamespaceScope, Namespace: "payments"}, IncludeProvenance: true})
	digest := sha256.Sum256([]byte("k8s-namespace-uid:payments:payment-exclusive-namespace-uid"))
	if s.ClusterID != fmt.Sprintf("%x", digest) || s.ClusterProvenance != model.NamespaceUIDProvenance || fixture.called("/api/v1/namespaces/kube-system") {
		t.Fatalf("unexpected scoped provenance: %s %s", s.ClusterID, s.ClusterProvenance)
	}
	if s.Coverage(model.SourceJobs) != model.Unavailable || s.Coverage(model.SourceCronJobs) != model.PermissionDenied {
		t.Fatalf("optional API coverage is incorrect: %+v", s.Sources)
	}
	for _, source := range []model.Source{model.SourceStatefulSets, model.SourceJobs, model.SourceCronJobs, model.SourceHPAs, model.SourceNodeLocalDNS, model.SourceNodeLocalPods} {
		if !slices.ContainsFunc(s.SourceTimings, func(window model.SourceTiming) bool {
			return window.Source == source && !window.StartedAt.Before(s.StartedAt) && !window.FinishedAt.Before(window.StartedAt) && !window.FinishedAt.After(s.FinishedAt)
		}) {
			t.Errorf("source %s has no capture-bounded timing", source)
		}
	}
	if !s.CollectedAt.Equal(s.FinishedAt) || len(s.Expansion.StatefulSets) != 1 || s.Expansion.StatefulSets[0].StartOrdinal != 1 ||
		len(s.Expansion.HPAs) != 1 || len(s.Expansion.HPAs[0].UtilizationTargets) != 1 || len(s.Expansion.Probes) != 1 ||
		len(s.Expansion.ProbeEvents) != 1 || s.Expansion.ProbeEvents[0].Kind != "readiness" || len(s.Expansion.NodeLocalPods) != 1 || s.Expansion.NodeLocalPods[0].Node != "node-one" {
		t.Fatalf("projected diagnostics were not scoped or retained: %+v", s.Expansion)
	}
	if id := s.Pods[0].Status.ContainerStatuses[0].ContainerID; id != "containerd://"+strings.Repeat("a", 64) {
		t.Fatalf("container runtime identity was not retained: %s", id)
	}
	retained, err := json.Marshal(s.Expansion)
	if err != nil || strings.Contains(string(retained), sensitive) || strings.Contains(string(retained), "other-node") {
		t.Fatalf("unexpected source data was retained: %s, error %v", retained, err)
	}
	for _, operation := range fixture.operations {
		if operation.method != http.MethodGet || slices.Contains([]string{"secrets", "exec", "logs", "scale", "proxy"}, lastSegment(operation.path)) {
			t.Fatalf("prohibited operation: %+v", operation)
		}
	}
}

func lastSegment(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

func TestClusterProvenanceRequiresExplicitCapture(t *testing.T) {
	collector, fixture := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path != "/api/v1/namespaces/kube-system" {
			return false
		}
		fmt.Fprint(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-system","uid":"disposable-system-identity"}}`)
		return true
	}, time.Second)
	request := model.ScanRequest{Scope: model.Scope{Kind: model.ClusterScope}}
	legacy := collector.Collect(t.Context(), request)
	if legacy.ClusterID != "" || fixture.called("/api/v1/namespaces/kube-system") {
		t.Fatal("legacy scan inspected capture provenance")
	}
	request.IncludeProvenance = true
	captured := collector.Collect(t.Context(), request)
	if !fixture.called("/api/v1/namespaces/kube-system") || captured.ClusterProvenance != model.KubeSystemUIDProvenance || captured.Coverage(model.SourceProvenance) != model.Complete {
		t.Fatalf("capture did not record the named kube-system UID proof: %+v", captured.Sources)
	}
	if strings.Contains(captured.ClusterID, "disposable-system-identity") || len(captured.ClusterID) != 64 {
		t.Fatalf("unexpected provenance token %q", captured.ClusterID)
	}
}

func TestClassifiedAdmissionDropsRawDetails(t *testing.T) {
	for _, tc := range []struct{ name, kind, message, constraint string }{
		{"quota", "ReplicaSet", "Error creating: pods \"worker\" is forbidden: exceeded quota: cap, requested: pods=1, used: pods=1, limited: pods=1", "quota_exceeded"},
		{"required", "ReplicaSet", "Error creating: pods \"worker\" is forbidden: failed quota: cap: must specify limits.cpu for: worker", "quota_required_resources"},
		{"limit", "ReplicaSet", "Error creating: pods \"worker\" is forbidden: minimum cpu usage per Container is 15m, but request is 1m", "minimum"},
		{"stateful", "StatefulSet", "create Claim data-worker-0 for Pod worker-0 in StatefulSet worker failed error: persistentvolumeclaims \"data-worker-0\" is forbidden: storage max limit to request ratio per PersistentVolumeClaim is 2", "maximum_ratio"},
		{"wrong stateful identity", "StatefulSet", "create Pod worker-0 in StatefulSet unrelated failed error: pods \"worker-0\" is forbidden: exceeded quota: cap, requested: pods=1", ""},
		{"unrelated", "ReplicaSet", "Error creating: pods \"worker\" is forbidden: failed quota: cap: quota cache unavailable", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := model.Event{Resource: model.Resource{Kind: tc.kind, Namespace: "payments", Name: "worker", UID: "uid-worker"}, Reason: "FailedCreate"}
			classified := projectAdmissionEvent(event, core.Event{Type: core.EventTypeWarning, Message: tc.message + " " + sensitive})
			if tc.constraint == "" {
				if classified != nil {
					t.Fatalf("unrecognized event synthesized evidence: %+v", classified)
				}
				return
			}
			if classified == nil || classified.Constraint != tc.constraint {
				t.Fatalf("missing expected admission class: %+v", classified)
			}
			data, err := json.Marshal(classified)
			if err != nil || strings.Contains(string(data), sensitive) || strings.Contains(string(data), "pods=1") {
				t.Fatalf("raw event contents leaked: %s, error %v", data, err)
			}
		})
	}
}

func TestHPAEventRequiresControllerSource(t *testing.T) {
	project := eventProjector(nil)
	base := core.Event{Type: core.EventTypeWarning, Reason: "FailedGetResourceMetric", Message: sensitive,
		InvolvedObject: core.ObjectReference{Kind: "HorizontalPodAutoscaler", Namespace: "payments", Name: "worker", UID: "hpa-uid"}}
	if _, keep := project(base); keep {
		t.Fatal("accepted HPA failure without controller attribution")
	}
	base.Source.Component = "horizontal-pod-autoscaler"
	event, keep := project(base)
	recorded, err := json.Marshal(event)
	if err != nil || !keep || event.Reason != base.Reason || strings.Contains(string(recorded), sensitive) {
		t.Fatalf("unexpected normalized HPA event: %s, error %v", recorded, err)
	}
}

func BenchmarkCollection250Pods(b *testing.B) {
	page := func(first, end int) string {
		items := make([]string, 0, end-first)
		for i := first; i < end; i++ {
			items = append(items, fmt.Sprintf(`{"metadata":{"namespace":"payments","name":"worker-%d","uid":"pod-%d"},"spec":{"containers":[{"name":"worker"}]}}`, i, i))
		}
		return strings.Join(items, ",")
	}
	first, next := page(0, 200), page(200, 250)
	var requests atomic.Uint64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if req.URL.Path == "/api/v1/namespaces/payments/pods" {
			if req.URL.Query().Get("continue") == "second" {
				list(w, "v1", "PodList", "", next)
				return
			}
			list(w, "v1", "PodList", "second", first)
			return
		}
		defaultWireResponse(w, req)
	}))
	defer server.Close()
	b.ReportAllocs()
	request := model.ScanRequest{Scope: model.Scope{Kind: model.NamespaceScope, Namespace: "payments"}}
	for b.Loop() {
		b.StopTimer()
		collector, err := New(&rest.Config{Host: server.URL})
		if err != nil {
			b.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(b.Context(), 10*time.Second)
		b.StartTimer()
		s := collector.Collect(ctx, request)
		b.StopTimer()
		cancel()
		if len(s.Pods) != 250 || s.Coverage(model.SourcePods) != model.Complete {
			b.Fatalf("capture incomplete: %d Pods, coverage %s", len(s.Pods), s.Coverage(model.SourcePods))
		}
		b.StartTimer()
	}
	b.ReportMetric(float64(requests.Load())/float64(b.N), "requests/op")
}

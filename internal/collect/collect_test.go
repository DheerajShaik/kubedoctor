package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
)

const sensitive = "SENTINEL_NEVER_RETAIN_5b260"
const microTime = "2006-01-02T15:04:05.000000Z07:00"

type wireOperation struct {
	method string
	path   string
	query  url.Values
}

type wireFixture struct {
	mu           sync.Mutex
	operations   []wireOperation
	inFlight     atomic.Int32
	peakInFlight atomic.Int32
}

type handlerOverride func(http.ResponseWriter, *http.Request) bool

func fixtureCollector(t *testing.T, override handlerOverride, timeout time.Duration) (*Collector, *wireFixture) {
	t.Helper()
	fixture := &wireFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		active := fixture.inFlight.Add(1)
		defer fixture.inFlight.Add(-1)
		for peak := fixture.peakInFlight.Load(); active > peak; peak = fixture.peakInFlight.Load() {
			if fixture.peakInFlight.CompareAndSwap(peak, active) {
				break
			}
		}
		fixture.mu.Lock()
		fixture.operations = append(fixture.operations, wireOperation{req.Method, req.URL.Path, req.URL.Query()})
		fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if override == nil || !override(w, req) {
			defaultWireResponse(w, req)
		}
	}))
	t.Cleanup(server.Close)
	collector, err := New(&rest.Config{Host: server.URL, Timeout: timeout, BearerToken: sensitive})
	if err != nil {
		t.Fatal(err)
	}
	return collector, fixture
}

func (f *wireFixture) called(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.operations, func(call wireOperation) bool { return call.path == path })
}

func list(w http.ResponseWriter, apiVersion, kind, next, items string) {
	fmt.Fprintf(w, `{"apiVersion":%q,"kind":%q,"metadata":{"continue":%q},"items":[%s]}`, apiVersion, kind, next, items)
}

func status(w http.ResponseWriter, code int, reason string) {
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":%q,"message":%q,"code":%d}`, reason, sensitive, code)
}

func defaultWireResponse(w http.ResponseWriter, req *http.Request) {
	p := req.URL.Path
	switch p {
	case "/apis/discovery.k8s.io/v1":
		fmt.Fprint(w, `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"discovery.k8s.io/v1","resources":[{"name":"endpointslices","namespaced":true,"kind":"EndpointSlice","verbs":["list"]}]}`)
		return
	case "/apis/metrics.k8s.io/v1beta1":
		status(w, http.StatusNotFound, "NotFound")
		return
	case "/api/v1/namespaces/payments":
		fmt.Fprint(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"payments"}}`)
		return
	case "/api/v1/namespaces/kube-system/services/kube-dns":
		fmt.Fprint(w, `{"apiVersion":"v1","kind":"Service","metadata":{"namespace":"kube-system","name":"kube-dns","uid":"dns-service"},"spec":{"ports":[{"name":"dns","port":53,"protocol":"UDP","targetPort":53}]}}`)
		return
	case "/api/v1/namespaces/kube-system/configmaps/coredns":
		fmt.Fprintf(w, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"namespace":"kube-system","name":"coredns"},"data":{"Corefile":%q}}`, sensitive)
		return
	case "/api/v1/namespaces/kube-system/configmaps/kube-dns":
		status(w, http.StatusNotFound, "NotFound")
		return
	}
	kinds := map[string]string{
		"pods": "PodList", "nodes": "NodeList", "events": "EventList", "configmaps": "ConfigMapList",
		"persistentvolumeclaims": "PersistentVolumeClaimList", "services": "ServiceList", "endpoints": "EndpointsList",
		"deployments": "DeploymentList", "daemonsets": "DaemonSetList", "replicasets": "ReplicaSetList",
		"endpointslices": "EndpointSliceList", "ingresses": "IngressList", "networkpolicies": "NetworkPolicyList",
		"poddisruptionbudgets": "PodDisruptionBudgetList", "storageclasses": "StorageClassList",
		"statefulsets": "StatefulSetList", "jobs": "JobList", "cronjobs": "CronJobList",
		"horizontalpodautoscalers": "HorizontalPodAutoscalerList", "resourcequotas": "ResourceQuotaList", "limitranges": "LimitRangeList",
	}
	segments := strings.Split(strings.TrimPrefix(p, "/"), "/")
	kind := kinds[segments[len(segments)-1]]
	if kind == "" {
		status(w, http.StatusNotFound, "NotFound")
		return
	}
	version := "v1"
	if segments[0] == "apis" {
		version = segments[1] + "/" + segments[2]
	}
	list(w, version, kind, "", "")
}

func paymentsScan(ctx context.Context, collector *Collector) model.Snapshot {
	return collector.Collect(ctx, model.ScanRequest{Scope: model.Scope{Kind: model.NamespaceScope, Namespace: "payments", Context: "test"}})
}

func TestNamespaceCollectorReadsScopedResourcesAndProjectsEvidence(t *testing.T) {
	collector, fixture := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
		if req.Header.Get("Authorization") != "Bearer "+sensitive {
			t.Errorf("request missing client-go authentication")
		}
		switch req.URL.Path {
		case "/api/v1/namespaces/payments/pods":
			if req.URL.Query().Get("continue") == "next-page" {
				list(w, "v1", "PodList", "", `{"metadata":{"namespace":"payments","name":"second","uid":"pod2"},"status":{"phase":"Succeeded"}}`)
				return true
			}
			item := fmt.Sprintf(`{"metadata":{"namespace":"payments","name":"frontend","uid":"pod1","annotations":{"note":%q}},"spec":{"nodeName":"node-a","containers":[{"name":"app","command":[%q],"env":[{"name":"TOKEN","value":%q},{"name":"USER","valueFrom":{"secretKeyRef":{"name":"db","key":"USER"}}}]}]},"status":{"phase":"Pending","containerStatuses":[{"name":"app","state":{"waiting":{"reason":"CreateContainerConfigError","message":%q}}}]}}`, sensitive, sensitive, sensitive, sensitive)
			list(w, "v1", "PodList", "next-page", item)
		case "/api/v1/namespaces/payments/events":
			item := fmt.Sprintf(`{"metadata":{"namespace":"payments","name":"frontend-warning"},"involvedObject":{"kind":"Pod","namespace":"payments","name":"frontend","uid":"pod1"},"type":"Warning","reason":"Failed","message":"Error: couldn't find key USER in Secret payments/db","eventTime":%q,"series":{"count":4,"lastObservedTime":%q}}`, time.Now().Add(-time.Minute).UTC().Format(microTime), time.Now().UTC().Format(microTime))
			list(w, "v1", "EventList", "", item)
		case "/api/v1/namespaces/payments/configmaps":
			list(w, "v1", "ConfigMapList", "", fmt.Sprintf(`{"metadata":{"namespace":"payments","name":"config"},"data":{"reason":%q},"binaryData":{"binary":"YWJj"}}`, sensitive))
		case "/api/v1/namespaces/payments/persistentvolumeclaims":
			list(w, "v1", "PersistentVolumeClaimList", "", `{"metadata":{"namespace":"payments","name":"data"},"spec":{"storageClassName":"wait"},"status":{"phase":"Pending"}}`)
		case "/api/v1/nodes/node-a":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a"},"status":{"conditions":[{"type":"Ready","status":"True","reason":"KubeletReady"}]}}`)
		case "/apis/storage.k8s.io/v1/storageclasses/wait":
			fmt.Fprint(w, `{"apiVersion":"storage.k8s.io/v1","kind":"StorageClass","metadata":{"name":"wait"},"volumeBindingMode":"WaitForFirstConsumer"}`)
		case "/apis/metrics.k8s.io/v1beta1":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"metrics.k8s.io/v1beta1","resources":[{"name":"pods","namespaced":true,"kind":"PodMetrics","verbs":["list"]}]}`)
		case "/apis/metrics.k8s.io/v1beta1/namespaces/payments/pods":
			item := fmt.Sprintf(`{"metadata":{"namespace":"payments","name":"frontend"},"timestamp":%q,"window":"30s","containers":[{"name":"app","usage":{"cpu":"50m","memory":"3Mi"}}]}`, time.Now().UTC().Format(time.RFC3339Nano))
			list(w, "metrics.k8s.io/v1beta1", "PodMetricsList", "", item)
		case "/api/v1/namespaces/kube-system/pods":
			if req.URL.Query().Get("labelSelector") != dnsPodSelector {
				t.Error("DNS Pod request was missing its restrictive label selector")
			}
			list(w, "v1", "PodList", "", `{"metadata":{"namespace":"kube-system","name":"coredns","labels":{"k8s-app":"kube-dns"}},"status":{"phase":"Running"}}`)
		case "/apis/discovery.k8s.io/v1/namespaces/kube-system/endpointslices":
			if req.URL.Query().Get("labelSelector") != dnsSliceSelector {
				t.Error("DNS EndpointSlice request was missing its restrictive label selector")
			}
			list(w, "discovery.k8s.io/v1", "EndpointSliceList", "", `{"metadata":{"namespace":"kube-system","name":"kube-dns-1","labels":{"kubernetes.io/service-name":"kube-dns"}},"addressType":"IPv4","endpoints":[{"addresses":["192.0.2.8"],"conditions":{"ready":true}}]}`)
		default:
			return false
		}
		return true
	}, 0)

	snapshot := paymentsScan(t.Context(), collector)
	for _, source := range snapshot.Sources {
		if source.Required && source.Status != model.Complete && source.Status != model.NotApplicable {
			t.Errorf("required source incomplete: %+v", source)
		}
	}
	for _, path := range []string{
		"/api/v1/nodes", "/apis/storage.k8s.io/v1/storageclasses", "/api/v1/pods",
		"/api/v1/namespaces/payments/endpoints", "/api/v1/namespaces/kube-system/endpoints/kube-dns",
	} {
		if fixture.called(path) {
			t.Errorf("unexpected request to %s", path)
		}
	}
	for _, call := range fixture.operations {
		if call.method != http.MethodGet || strings.Contains(call.path, "/secrets") ||
			strings.HasSuffix(call.path, "/log") || strings.HasSuffix(call.path, "/exec") {
			t.Errorf("unexpected wire operation: %+v", call)
		}
	}
	if len(snapshot.Nodes) != 1 || len(snapshot.StorageClasses) != 1 || len(snapshot.Pods) != 2 ||
		len(snapshot.DNSPods) != 1 || len(snapshot.DNSServices) != 1 || len(snapshot.DNSEndpointSlices) != 1 ||
		len(snapshot.DNSConfig) != 1 || len(snapshot.Metrics) != 1 || !snapshot.DNSConfig[0].HasCorefile {
		t.Fatalf("expected the visible inventory, DNS, and metrics, sources: %+v", snapshot.Sources)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0].Reference == nil ||
		*snapshot.Events[0].Reference != (model.ConfigIssue{Kind: "Secret", Name: "db", Key: "USER", Missing: "key"}) ||
		snapshot.Events[0].Count != 4 {
		t.Fatalf("expected classified key evidence and event-series count: %+v", snapshot.Events)
	}
	if len(snapshot.ConfigMaps) != 1 || !slices.Equal(snapshot.ConfigMaps[0].Keys, []string{"reason"}) ||
		!slices.Equal(snapshot.ConfigMaps[0].BinaryKeys, []string{"binary"}) {
		t.Fatalf("expected separate text and binary keys: %+v", snapshot.ConfigMaps)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), sensitive) || strings.Contains(string(payload), "192.0.2.8") {
		t.Error("collector retained sensitive or unnecessary API fields")
	}
	if fixture.peakInFlight.Load() > maxParallelSources {
		t.Errorf("concurrency exceeded source budget: %d", fixture.peakInFlight.Load())
	}
}

func TestForbiddenSecondPageRetainsEvidenceAndNeverFallsBack(t *testing.T) {
	collector, fixture := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path != "/apis/discovery.k8s.io/v1/namespaces/payments/endpointslices" {
			return false
		}
		if req.URL.Query().Get("continue") == "other-slice" {
			status(w, http.StatusForbidden, "Forbidden")
			return true
		}
		list(w, "discovery.k8s.io/v1", "EndpointSliceList", "other-slice",
			`{"metadata":{"namespace":"payments","name":"one","labels":{"kubernetes.io/service-name":"frontend"}},"addressType":"IPv4"}`)
		return true
	}, 0)
	snapshot := paymentsScan(t.Context(), collector)
	if snapshot.Coverage(model.SourceEndpointSlices) != model.PermissionDenied || len(snapshot.EndpointSlices) != 1 ||
		snapshot.Coverage(model.SourceMetrics) != model.Unavailable {
		t.Fatalf("partial visibility not preserved: %+v", snapshot.Sources)
	}
	if fixture.called("/api/v1/namespaces/payments/endpoints") {
		t.Error("permission denial triggered legacy Endpoints fallback")
	}
	payload, _ := json.Marshal(snapshot.Sources)
	if strings.Contains(string(payload), sensitive) {
		t.Error("raw access error was retained")
	}
}

func TestLegacyFallbackWhenDiscoveryConfirmsAbsence(t *testing.T) {
	collector, fixture := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
		switch req.URL.Path {
		case "/apis/discovery.k8s.io/v1":
			status(w, http.StatusNotFound, "NotFound")
		case "/api/v1/namespaces/payments/endpoints":
			list(w, "v1", "EndpointsList", "",
				`{"metadata":{"namespace":"payments","name":"web","annotations":{"endpoints.kubernetes.io/over-capacity":"truncated"}},"subsets":[{"addresses":[{"ip":"192.0.2.10"}]}]}`)
		case "/api/v1/namespaces/kube-system/endpoints/kube-dns":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"Endpoints","metadata":{"namespace":"kube-system","name":"kube-dns"}}`)
		default:
			return false
		}
		return true
	}, 0)
	snapshot := paymentsScan(t.Context(), collector)
	if snapshot.Coverage(model.SourceEndpointSlices) != model.Unavailable ||
		snapshot.Coverage(model.SourceLegacyEndpoints) != model.Truncated ||
		len(snapshot.LegacyEndpoints) != 1 || len(snapshot.DNSLegacyEndpoints) != 1 {
		t.Fatalf("unexpected fallback coverage: %+v", snapshot.Sources)
	}
	if fixture.called("/apis/discovery.k8s.io/v1/namespaces/payments/endpointslices") {
		t.Error("confirmed unavailable EndpointSlices were listed")
	}
}

func TestNamespaceAbsenceAndOutOfScopeObjectsAreVisible(t *testing.T) {
	collector, _ := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
		switch req.URL.Path {
		case "/api/v1/namespaces/payments":
			status(w, http.StatusNotFound, "NotFound")
		case "/api/v1/namespaces/payments/pods":
			list(w, "v1", "PodList", "", `{"metadata":{"namespace":"unrelated","name":"private"}}`)
		default:
			return false
		}
		return true
	}, 0)
	snapshot := paymentsScan(t.Context(), collector)
	if snapshot.Coverage(model.SourceNamespace) != model.Unavailable ||
		snapshot.Coverage(model.SourcePods) != model.Failed || len(snapshot.Pods) != 0 {
		t.Fatalf("namespace and object scope distinction was lost: %+v", snapshot.Sources)
	}
}

func TestClusterInventoryUsesClusterReads(t *testing.T) {
	collector, fixture := fixtureCollector(t, nil, 0)
	snapshot := collector.Collect(t.Context(), model.ScanRequest{Scope: model.Scope{Kind: model.ClusterScope}})
	for _, source := range snapshot.Sources {
		if source.Required && source.Status != model.Complete && source.Status != model.NotApplicable {
			t.Errorf("required source was incomplete: %+v", source)
		}
	}
	for _, p := range []string{"/api/v1/pods", "/api/v1/nodes", "/apis/storage.k8s.io/v1/storageclasses", "/apis/apps/v1/deployments"} {
		if !fixture.called(p) {
			t.Errorf("expected cluster read at %s", p)
		}
	}
	if fixture.called("/api/v1/namespaces/payments") {
		t.Error("cluster scan checked an unrelated namespace")
	}
}

func TestKubernetesTimeoutAndScanCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancel), func(t *testing.T) {
			started := make(chan struct{}, 1)
			collector, _ := fixtureCollector(t, func(_ http.ResponseWriter, req *http.Request) bool {
				if req.URL.Path != "/api/v1/namespaces/payments/pods" {
					return false
				}
				select {
				case started <- struct{}{}:
				default:
				}
				<-req.Context().Done()
				return true
			}, 200*time.Millisecond)
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			if cancel {
				go func() {
					<-started
					stop()
				}()
			}
			snapshot := paymentsScan(ctx, collector)
			expected := "collection timed out"
			if cancel {
				expected = "collection was canceled"
			}
			if !slices.ContainsFunc(snapshot.Sources, func(s model.SourceResult) bool {
				return s.Source == model.SourcePods && s.Status == model.Failed && s.Detail == expected
			}) {
				t.Fatalf("pods did not record %q: %+v", expected, snapshot.Sources)
			}
		})
	}
}

func TestEventProjectionRejectsUnmatchedOptionalAndUntrustedContent(t *testing.T) {
	pod := core.Pod{}
	pod.Namespace, pod.Name, pod.UID = "payments", "local", "new-uid"
	pod.Spec.Containers = []core.Container{{
		Name: "app",
		Env: []core.EnvVar{
			{ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{
				LocalObjectReference: core.LocalObjectReference{Name: "db"}, Key: "USER",
			}}},
			{ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{
				LocalObjectReference: core.LocalObjectReference{Name: "optional"}, Key: "KEY", Optional: new(true),
			}}},
		},
	}}
	pod.Spec.Volumes = []core.Volume{{
		Name: "settings", VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{
			SecretName: "volume-db", Items: []core.KeyToPath{{Key: "PASSWORD"}},
		}},
	}}
	for _, row := range []struct {
		message string
		kind    string
		object  string
		key     string
		missing string
	}{
		{"Error: secret \"db\" not found", "Secret", "db", "", "object"},
		{"Error: couldn't find key USER in Secret payments/db", "Secret", "db", "USER", "key"},
		{"MountVolume.SetUp failed for volume \"settings\" : references non-existent secret key: PASSWORD", "Secret", "volume-db", "PASSWORD", "key"},
		{"Error: secret \"unrelated\" not found", "", "", "", ""},
		{"Error: secret \"optional\" not found", "", "", "", ""},
		{"Error: couldn't find key USER in Secret other/db", "", "", "", ""},
		{"Error: secret \"db\" not found. " + sensitive, "", "", "", ""},
	} {
		found := classifyConfigFailure(row.message, pod)
		if row.kind == "" {
			if found != nil {
				t.Errorf("unexpected reference for %q: %+v", row.message, found)
			}
		} else if found == nil || *found != (model.ConfigIssue{Kind: row.kind, Name: row.object, Key: row.key, Missing: row.missing}) {
			t.Errorf("wrong classification for %q: %+v", row.message, found)
		}
	}
	event := core.Event{
		InvolvedObject: core.ObjectReference{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: "old-uid"},
		Type:           core.EventTypeWarning,
		Reason:         "Failed",
		Message:        "Error: secret \"db\" not found",
	}
	if found, ok := eventProjector([]core.Pod{pod})(event); !ok || found.Reference != nil {
		t.Errorf("event was incorrectly attributed to a replacement Pod: %+v", found)
	}
}

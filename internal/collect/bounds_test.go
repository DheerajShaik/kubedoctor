package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestTransportRejectsWritesSensitiveReadsAndScopeEscapes(t *testing.T) {
	server, err := url.Parse("https://kube.example/gate")
	if err != nil {
		t.Fatal(err)
	}
	var networkCalls int
	transport := diagnosticTransport{
		server: server,
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			networkCalls++
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
	}
	for _, tc := range []struct {
		method string
		suffix string
	}{
		{http.MethodPatch, "/api/v1/namespaces/payments/pods/app"},
		{http.MethodPost, "/api/v1/namespaces/payments/pods"},
		{http.MethodDelete, "/api/v1/namespaces/payments/pods"},
		{http.MethodPut, "/api/v1/namespaces/payments/pods/app"},
		{http.MethodGet, "/api/v1/namespaces/payments/secrets"},
		{http.MethodGet, "/api/v1/namespaces/payments/secrets/secret-name"},
		{http.MethodGet, "/api/v1/namespaces/payments/pods/app/log"},
		{http.MethodGet, "/api/v1/namespaces/payments/pods/app/exec"},
		{http.MethodGet, "/api/v1/namespaces/payments/pods/app/attach"},
		{http.MethodGet, "/api/v1/namespaces/payments/pods/app/portforward"},
		{http.MethodGet, "/api/v1/namespaces/payments/services/web/proxy"},
		{http.MethodGet, "/api/v1/namespaces/payments/pods?watch=1"},
		{http.MethodGet, "/api/v1/namespaces/payments/pods?limit=200&limit=1000"},
		{http.MethodGet, "/api/v1/namespaces/other/pods"},
		{http.MethodGet, "/api/v1/namespaces/kube-system/pods"},
		{http.MethodGet, "/api/v1/namespaces/kube-system/configmaps"},
		{http.MethodGet, "/api/v1/namespaces/kube-system/services/other"},
		{http.MethodGet, "/api/v1/namespaces/payments/pods%2fsecret"},
		{http.MethodGet, "/api/v1/pods"},
		{http.MethodGet, "/api/v1/nodes"},
		{http.MethodGet, "/apis/storage.k8s.io/v1/storageclasses"},
		{http.MethodGet, "/apis"},
	} {
		ctx := requestContext(withAccess(t.Context(), scanAccess{namespace: "payments"}))
		req, err := http.NewRequestWithContext(ctx, tc.method, server.String()+tc.suffix, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.RoundTrip(req); !errors.Is(err, errDisallowedRequest) {
			t.Errorf("request escaped guard: %s %s: %v", tc.method, tc.suffix, err)
		}
	}

	ctx := requestContext(withAccess(t.Context(), scanAccess{namespace: "payments"}))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://other.example/gate/api/v1/namespaces/payments/pods", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); !errors.Is(err, errDisallowedRequest) {
		t.Errorf("request escaped configured API origin: %v", err)
	}
	if networkCalls != 0 {
		t.Errorf("guard contacted the network %d times", networkCalls)
	}
	req.URL.Host = "kube.example"
	if _, err := transport.RoundTrip(req); err != nil || networkCalls != 1 {
		t.Errorf("scoped read failed: %v", err)
	}
}

func TestPaginationBounds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		continuation  func(int) string
		items         int
		limit         int
		truncatedText string
	}{
		{"repeated", func(int) string { return "constant" }, 1, 2, "API repeated a pagination token"},
		{"page count", func(index int) string { return fmt.Sprintf("page-%d", index) }, 0, maxPages, "API page budget reached"},
		{"item count", func(int) string { return "" }, maxItems + 1, 1, "API item budget reached"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var previous string
			get := func(_ context.Context, opts metav1.ListOptions) (*core.PodList, error) {
				calls++
				if opts.Limit != pageSize || opts.Continue != previous {
					t.Fatalf("unexpected pagination request: %+v", opts)
				}
				previous = tc.continuation(calls)
				return &core.PodList{
					ListMeta: metav1.ListMeta{Continue: previous},
					Items:    make([]core.Pod, tc.items),
				}, nil
			}
			items, result := inventory(t.Context(), model.SourcePods, get,
				func(list *core.PodList) []core.Pod { return list.Items }, projectPod)
			if calls != tc.limit || result.Status != model.Truncated || result.Detail != tc.truncatedText {
				t.Fatalf("incorrect or unbounded page behavior: calls=%d %+v", calls, result)
			}
			if len(items) > maxItems || (tc.name == "repeated" && len(items) != 1) {
				t.Fatalf("preserved invalid or duplicate pagination items: %d", len(items))
			}
		})
	}
}

func TestTransportCapsAttemptsAndResponseBytes(t *testing.T) {
	server, _ := url.Parse("http://kube.example")
	var seen int
	transport := diagnosticTransport{
		server: server,
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			seen++
			return &http.Response{
				StatusCode: http.StatusOK, ContentLength: maxResponseBytes + 1, Body: http.NoBody,
			}, nil
		}),
	}
	ctx := requestContext(withAccess(t.Context(), scanAccess{namespace: "payments"}))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.String()+"/api/v1/namespaces/payments/pods", nil)
	for i := 0; i < maxRequestAttempts; i++ {
		if _, err := transport.RoundTrip(req); !errors.Is(err, errResponseBudget) {
			t.Errorf("oversized response was accepted: %v", err)
		}
	}
	if _, err := transport.RoundTrip(req); !errors.Is(err, errRequestBudget) || seen != maxRequestAttempts {
		t.Fatalf("attempt budget was not enforced: requests=%d error=%v", seen, err)
	}
	stream := &boundedBody{ReadCloser: io.NopCloser(strings.NewReader("oversized")), remaining: 4}
	prefix, err := io.ReadAll(stream)
	if string(prefix) != "over" || !errors.Is(err, errResponseBudget) {
		t.Fatalf("streaming body budget was not enforced: %q %v", prefix, err)
	}
}

func TestCollectorDoesNotFollowRedirects(t *testing.T) {
	collector, fixture := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path != "/api/v1/namespaces/payments/pods" {
			return false
		}
		http.Redirect(w, req, "/api/v1/namespaces/kube-system/services/kube-dns?limit=22", http.StatusTemporaryRedirect)
		return true
	}, 0)
	snapshot := paymentsScan(t.Context(), collector)
	if snapshot.Coverage(model.SourcePods) != model.Failed {
		t.Errorf("redirected pods collection had wrong coverage: %+v", snapshot.Sources)
	}
	for _, call := range fixture.operations {
		if call.query.Get("limit") == "22" {
			t.Errorf("unexpected request to redirect destination: %+v", call)
		}
	}
}

type warningTracker struct {
	calls atomic.Int32
}

func (tracker *warningTracker) HandleWarningHeaderWithContext(_ context.Context, _ int, _, _ string) {
	tracker.calls.Add(1)
}

func TestCollectorDoesNotLogUntrustedWarningHeaders(t *testing.T) {
	tracker := &warningTracker{}
	rest.SetDefaultWarningHandlerWithContext(tracker)
	t.Cleanup(func() { rest.SetDefaultWarningHandlerWithContext(rest.WarningLogger{}) })
	collector, _ := fixtureCollector(t, func(w http.ResponseWriter, _ *http.Request) bool {
		w.Header().Set("Warning", `299 - "`+sensitive+`"`)
		return false
	}, 0)
	snapshot := paymentsScan(t.Context(), collector)
	if snapshot.Coverage(model.SourcePods) != model.Complete {
		t.Fatalf("unexpected Pods collection failure: %+v", snapshot.Sources)
	}
	if tracker.calls.Load() != 0 {
		t.Error("untrusted warnings reached client-go's application warning handler")
	}
}

func TestStorageClassObjectNotFoundIsDistinctFromMissingAPI(t *testing.T) {
	for _, objectStatus := range []bool{false, true} {
		t.Run(fmt.Sprintf("object-status=%t", objectStatus), func(t *testing.T) {
			collector, _ := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
				switch req.URL.Path {
				case "/api/v1/namespaces/payments/persistentvolumeclaims":
					list(w, "v1", "PersistentVolumeClaimList", "", `{"metadata":{"namespace":"payments","name":"data"},"spec":{"storageClassName":"wait"},"status":{"phase":"Pending"}}`)
				case "/apis/storage.k8s.io/v1/storageclasses/wait":
					if objectStatus {
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"NotFound","code":404,"details":{"group":"storage.k8s.io","kind":"storageclasses","name":"wait"}}`)
					} else {
						status(w, http.StatusNotFound, "NotFound")
					}
				default:
					return false
				}
				return true
			}, 0)
			want := model.Unavailable
			if objectStatus {
				want = model.Complete
			}
			snapshot := paymentsScan(t.Context(), collector)
			if snapshot.Coverage(model.SourceStorageClasses) != want {
				t.Errorf("storage lookup coverage=%s want=%s: %+v", snapshot.Coverage(model.SourceStorageClasses), want, snapshot.Sources)
			}
		})
	}
}

func TestCollectorSupportsKubernetesAPIServerPrefix(t *testing.T) {
	server := httptest.NewServer(http.StripPrefix("/gateway/cluster", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		defaultWireResponse(w, req)
	})))
	defer server.Close()
	collector, err := New(&rest.Config{Host: server.URL + "/gateway/cluster"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := paymentsScan(t.Context(), collector)
	for _, source := range snapshot.Sources {
		if source.Required && source.Status != model.Complete && source.Status != model.NotApplicable {
			t.Errorf("API gateway prefix broke required source: %+v", source)
		}
	}
}

package collect

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	dnsNamespace       = "kube-system"
	dnsService         = "kube-dns"
	dnsSliceSelector   = "kubernetes.io/service-name=kube-dns"
	maxResponseBytes   = 32 << 20
	maxRequestAttempts = 3
	dnsPodSelector     = "k8s-app=kube-dns"
)

var errDisallowedRequest = errors.New("Kubernetes diagnostic request is outside the read-only API surface")

var errRequestBudget = errors.New("Kubernetes API retry budget reached")
var errResponseBudget = errors.New("Kubernetes API response byte budget reached")

type scanAccessKey struct{}
type attemptBudgetKey struct{}

func requestContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, attemptBudgetKey{}, new(atomic.Int32))
}

type scanAccess struct {
	namespace string
	cluster   bool
}

func withAccess(ctx context.Context, access scanAccess) context.Context {
	return context.WithValue(ctx, scanAccessKey{}, access)
}

type diagnosticTransport struct {
	base   http.RoundTripper
	server *url.URL
}

func (t diagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	access, valid := req.Context().Value(scanAccessKey{}).(scanAccess)
	if !valid || !t.allows(req, access) {
		return nil, errDisallowedRequest
	}
	attempts, present := req.Context().Value(attemptBudgetKey{}).(*atomic.Int32)
	if !present || attempts.Add(1) > maxRequestAttempts {
		return nil, errRequestBudget
	}
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > maxResponseBytes {
		response.Body.Close()
		return nil, errResponseBudget
	}
	response.Body = &boundedBody{ReadCloser: response.Body, remaining: maxResponseBytes}
	return response, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int
}

func (body *boundedBody) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if body.remaining == 0 {
		var extra [1]byte
		n, err := body.ReadCloser.Read(extra[:])
		if n > 0 {
			return 0, errResponseBudget
		}
		return 0, err
	}
	n, err := body.ReadCloser.Read(destination[:min(len(destination), body.remaining)])
	body.remaining -= n
	return n, err
}

func (t diagnosticTransport) allows(req *http.Request, access scanAccess) bool {
	if req.Method != http.MethodGet || req.Body != nil || req.URL.Scheme != t.server.Scheme ||
		req.URL.Host != t.server.Host || req.URL.User != nil || req.URL.RawPath != "" ||
		req.URL.Path != path.Clean(req.URL.Path) {
		return false
	}
	prefix := strings.TrimSuffix(t.server.Path, "/")
	if !strings.HasPrefix(req.URL.Path, prefix+"/") {
		return false
	}
	p := strings.TrimPrefix(req.URL.Path, prefix)
	query, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return false
	}
	// All collector lists are paginated reads. In particular, watch and proxy
	// query parameters have no place in a diagnostic request.
	for key, values := range query {
		if len(values) != 1 {
			return false
		}
		switch key {
		case "limit", "continue", "timeout", "timeoutSeconds":
		case "labelSelector":
			if (p != "/apis/discovery.k8s.io/v1/namespaces/kube-system/endpointslices" || values[0] != dnsSliceSelector) &&
				(p != "/api/v1/namespaces/kube-system/pods" || values[0] != dnsPodSelector) {
				return false
			}
		default:
			return false
		}
	}

	switch p {
	case "/apis/discovery.k8s.io/v1", "/apis/metrics.k8s.io/v1beta1":
		return true
	case "/api/v1/namespaces/" + access.namespace:
		return !access.cluster && validNamespace(access.namespace)
	case "/api/v1/namespaces/kube-system/services/kube-dns",
		"/api/v1/namespaces/kube-system/endpoints/kube-dns",
		"/api/v1/namespaces/kube-system/configmaps/coredns",
		"/api/v1/namespaces/kube-system/configmaps/kube-dns":
		return true
	case "/api/v1/namespaces/kube-system/pods":
		if query.Get("labelSelector") == dnsPodSelector {
			return true
		}
	case "/apis/discovery.k8s.io/v1/namespaces/kube-system/endpointslices":
		if query.Get("labelSelector") == dnsSliceSelector {
			return true
		}
	}

	for _, root := range []string{"/api/v1/nodes", "/apis/storage.k8s.io/v1/storageclasses"} {
		if p == root {
			return access.cluster
		}
		if strings.HasPrefix(p, root+"/") {
			return !access.cluster && validResourceName(strings.TrimPrefix(p, root+"/"))
		}
	}

	api, suffix := splitResourcePath(p)
	if api == "" {
		return false
	}
	segments := strings.Split(suffix, "/")
	switch {
	case access.cluster && len(segments) == 1:
		return inventoryResource(api, segments[0])
	case !access.cluster && len(segments) == 3 && segments[0] == "namespaces":
		return segments[1] == access.namespace && validNamespace(segments[1]) && inventoryResource(api, segments[2])
	default:
		return false
	}
}

func splitResourcePath(p string) (string, string) {
	for _, api := range []string{
		"/api/v1", "/apis/apps/v1", "/apis/discovery.k8s.io/v1",
		"/apis/networking.k8s.io/v1", "/apis/policy/v1", "/apis/metrics.k8s.io/v1beta1",
	} {
		if suffix, found := strings.CutPrefix(p, api+"/"); found {
			return api, suffix
		}
	}
	return "", ""
}

func inventoryResource(api, resource string) bool {
	switch api {
	case "/api/v1":
		switch resource {
		case "pods", "events", "configmaps", "persistentvolumeclaims", "services", "endpoints":
			return true
		}
	case "/apis/apps/v1":
		return resource == "deployments" || resource == "daemonsets" || resource == "replicasets"
	case "/apis/discovery.k8s.io/v1":
		return resource == "endpointslices"
	case "/apis/networking.k8s.io/v1":
		return resource == "ingresses" || resource == "networkpolicies"
	case "/apis/policy/v1":
		return resource == "poddisruptionbudgets"
	case "/apis/metrics.k8s.io/v1beta1":
		return resource == "pods"
	}
	return false
}

func validNamespace(name string) bool {
	return len(validation.IsDNS1123Label(name)) == 0
}

func validResourceName(name string) bool {
	return len(validation.IsDNS1123Subdomain(name)) == 0
}

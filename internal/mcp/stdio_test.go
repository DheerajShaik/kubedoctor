package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/analyze"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	clientconfig "k8s.io/client-go/tools/clientcmd"
	configapi "k8s.io/client-go/tools/clientcmd/api"
)

// Exercise the installed command with the SDK's actual subprocess transport.
// The disposable HTTP fixture records requests without accessing a live cluster.
func TestStdioBinaryInterop(t *testing.T) {
	binary := buildTestBinary(t)
	var requests atomic.Int32
	var repaired atomic.Bool
	var blocking atomic.Pointer[blockedFixtureRequest]
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Upgrade") != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unsupported fixture operation", http.StatusMethodNotAllowed)
			return
		}
		if wait := blocking.Load(); wait != nil && r.URL.Path == "/api/v1/namespaces/payments/pods" &&
			blocking.CompareAndSwap(wait, nil) {
			close(wait.started)
			<-r.Context().Done()
			close(wait.canceled)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		response, code := fixtureResponse(t, r, repaired.Load())
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	t.Cleanup(api.Close)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientconfig.WriteToFile(configapi.Config{
		CurrentContext: "other",
		Clusters:       map[string]*configapi.Cluster{"fixture": {Server: api.URL}},
		Contexts: map[string]*configapi.Context{
			"fixture": {Cluster: "fixture", Namespace: "payments"},
			"other":   {Cluster: "fixture", Namespace: "default"},
		},
	}, kubeconfig); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}

	args := []string{"mcp", "serve", "--stdio", "--kubeconfig", kubeconfig, "--context", "fixture",
		"--allow-namespace", "payments", "--allow-namespace", "billing", "--timeout", "10s"}
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			var stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Stderr = &stderr
			client := protocol.NewClient(&protocol.Implementation{Name: "kubedoctor-interop-test"}, nil)
			session, err := client.Connect(ctx, &protocol.CommandTransport{Command: cmd, TerminateDuration: time.Second},
				&protocol.ClientSessionOptions{ProtocolVersion: version})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			if session.InitializeResult().ProtocolVersion != version {
				t.Fatalf("negotiated %q instead of %s", session.InitializeResult().ProtocolVersion, version)
			}
			atStart := requests.Load()
			list, err := session.ListTools(ctx, nil)
			if err != nil || len(list.Tools) != len(toolArguments) {
				t.Fatalf("list tools = %v, %v", list, err)
			}
			checks := callTool(t, session, checksTool, `{}`)
			var inventory checkInventory
			convertStructured(t, checks, &inventory)
			if checks.IsError || !slices.Equal(inventory.Checks, analyze.Checks()) {
				t.Fatalf("incorrect check inventory: %s", resultJSON(t, checks))
			}
			for _, call := range []struct{ name, args string }{
				{namespaceTool, `{"namespace":"default"}`},
				{namespaceTool, `{"namespace":"payments","host":"https://private-sentinel.invalid"}`},
				{clusterTool, `{}`},
			} {
				response := callTool(t, session, call.name, call.args)
				if !response.IsError || strings.Contains(string(resultJSON(t, response)), "private-sentinel") {
					t.Fatalf("unauthorized call returned %s", resultJSON(t, response))
				}
			}
			if requests.Load() != atStart {
				t.Fatal("metadata or rejected requests contacted Kubernetes")
			}

			report := decodeReport(t, callTool(t, session, namespaceTool, `{"namespace":"payments"}`))
			want := model.Scope{Kind: model.NamespaceScope, Namespace: "payments", Context: "fixture"}
			if !report.Complete || report.Scope != want {
				t.Fatalf("unexpected report scope or coverage: %+v", report)
			}
			if !slices.ContainsFunc(report.Findings, func(f model.Finding) bool {
				return f.RuleID == "pod-containers" && f.Resource.Name == "checkout"
			}) {
				t.Fatalf("image pull evidence missing: %+v", report.Findings)
			}
			if requests.Load() == atStart {
				t.Fatal("the authorized subprocess call did not reach the fixture")
			}
			if version == "2026-07-28" {
				assertStdioCancellation(t, session, &blocking)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("stdio shutdown: %v (%s)", err, stderr.String())
			}
		})
	}

	t.Run("oversized frame", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_checks","arguments":{"value":"` +
			strings.Repeat("x", maxInputBytes) + "private-sentinel\"}}}\n")
		atStart := requests.Load()
		err := cmd.Run()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 2 || ctx.Err() != nil || stdout.Len() != 0 || strings.Contains(stderr.String(), "private-sentinel") ||
			!strings.Contains(stderr.String(), "MCP stdio session") || requests.Load() != atStart {
			t.Fatalf("unexpected oversized frame behavior: err=%v stdout=%.256s stderr=%.256s", err, &stdout, &stderr)
		}
	})

	t.Run("authorized incident artifacts", func(t *testing.T) {
		checkStdioArtifacts(t, binary, kubeconfig, &requests, &repaired)
	})
	after, err := os.ReadFile(kubeconfig)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("subprocess changed its kubeconfig")
	}
}

func buildTestBinary(t *testing.T) string {
	t.Helper()
	name := "kubectl-doctor"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	goCommand := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goCommand += ".exe"
	}
	cmd := exec.CommandContext(ctx, goCommand, "build", "-mod=readonly", "-o", binary, "./cmd/kubectl-doctor")
	cmd.Dir = filepath.Join("..", "..")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test executable: %v\n%s", err, output)
	}
	return binary
}

func fixtureResponse(t *testing.T, request *http.Request, repaired bool) (map[string]any, int) {
	t.Helper()
	path := request.URL.Path
	switch path {
	case "/api/v1/namespaces/payments", "/api/v1/namespaces/kube-system":
		namespace := filepath.Base(path)
		return map[string]any{"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{"name": namespace, "uid": namespace + "-uid"}}, http.StatusOK
	case "/apis/discovery.k8s.io/v1":
		return map[string]any{
			"apiVersion": "v1", "kind": "APIResourceList", "groupVersion": "discovery.k8s.io/v1",
			"resources": []map[string]any{{"name": "endpointslices", "namespaced": true, "kind": "EndpointSlice"}},
		}, http.StatusOK
	case "/apis/metrics.k8s.io/v1beta1", "/api/v1/namespaces/kube-system/services/kube-dns",
		"/api/v1/namespaces/kube-system/configmaps/coredns", "/api/v1/namespaces/kube-system/configmaps/kube-dns",
		"/apis/apps/v1/namespaces/kube-system/daemonsets/node-local-dns":
		return map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404}, http.StatusNotFound
	case "/api/v1/namespaces/kube-system/pods":
		if request.URL.Query().Get("labelSelector") != "k8s-app=kube-dns" {
			t.Error("DNS inventory lacked its workload selector")
		}
		return listFixture("v1", "Pod"), http.StatusOK
	}
	group, resource, ok := strings.Cut(path, "/namespaces/payments/")
	if !ok {
		t.Errorf("request outside fixture namespace and dependencies: %s", path)
		return map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404}, http.StatusNotFound
	}
	kinds := map[string]string{
		"pods": "Pod", "configmaps": "ConfigMap", "events": "Event", "persistentvolumeclaims": "PersistentVolumeClaim",
		"services": "Service", "replicasets": "ReplicaSet", "deployments": "Deployment", "daemonsets": "DaemonSet",
		"endpointslices": "EndpointSlice", "ingresses": "Ingress", "networkpolicies": "NetworkPolicy",
		"poddisruptionbudgets": "PodDisruptionBudget",
		"statefulsets":         "StatefulSet", "jobs": "Job", "cronjobs": "CronJob", "resourcequotas": "ResourceQuota",
		"limitranges": "LimitRange", "horizontalpodautoscalers": "HorizontalPodAutoscaler",
	}
	kind, ok := kinds[resource]
	if !ok {
		t.Errorf("unexpected Kubernetes resource path %s", path)
		return map[string]any{"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404}, http.StatusNotFound
	}
	version := strings.TrimPrefix(strings.TrimPrefix(group, "/api/"), "/apis/")
	result := listFixture(version, kind)
	if resource == "pods" && repaired {
		result["items"] = []json.RawMessage{json.RawMessage(`{
			"metadata":{"name":"checkout","namespace":"payments","uid":"checkout-uid"},
			"spec":{"containers":[{"name":"app","image":"example.invalid/app"}]},
			"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],
				"containerStatuses":[{"name":"app","ready":true,"state":{"running":{}}}]}
		}`)}
	}
	if resource == "pods" && !repaired {
		result["items"] = []json.RawMessage{json.RawMessage(`{
			"metadata":{"name":"checkout","namespace":"payments","uid":"checkout-uid"},
			"spec":{"containers":[{"name":"app","image":"example.invalid/app"}]},
			"status":{"phase":"Pending","containerStatuses":[{"name":"app","state":{"waiting":{"reason":"ImagePullBackOff"}}}]}
		}`)}
	}
	return result, http.StatusOK
}

func listFixture(version, kind string) map[string]any {
	return map[string]any{"apiVersion": version, "kind": kind + "List",
		"items": []any{}, "metadata": map[string]string{"resourceVersion": "1"}}
}

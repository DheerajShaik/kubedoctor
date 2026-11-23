package history

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const fixtureRuntime = "containerd://0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func exampleConfig(providerURL string) Config {
	return Config{
		URL: providerURL, Cluster: "lab", Window: "15m", Step: "15s", ScrapeInterval: "30s",
		Targets: []TargetPolicy{{Namespace: "payments", Kind: "Deployment", Name: "checkout", Container: "app", VPADisabled: true}},
		Grafana: &model.GrafanaDashboard{Origin: "https://grafana.example", UID: "checkout_v1", Slug: "resource-usage"},
	}
}

func exampleSnapshot(end time.Time) model.Snapshot {
	isController := true
	return model.Snapshot{
		Scope: model.Scope{Kind: model.NamespaceScope, Namespace: "payments", Context: "lab"}, CollectedAt: end,
		Sources: []model.SourceResult{
			{Source: model.SourcePods, Status: model.Complete},
			{Source: model.SourceReplicaSets, Status: model.Complete},
			{Source: model.SourceHPAs, Status: model.Complete},
		},
		Pods: []core.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "payments", Name: "checkout-demo", UID: "pod-uid",
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "checkout-demo", UID: "rs-uid", Controller: &isController}},
			},
			Spec: core.PodSpec{Containers: []core.Container{{
				Name:      "app",
				Resources: core.ResourceRequirements{Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("400m"), core.ResourceMemory: resource.MustParse("128Mi")}},
			}}},
			Status: core.PodStatus{Phase: core.PodRunning, ContainerStatuses: []core.ContainerStatus{{
				Name: "app", ContainerID: fixtureRuntime,
				State: core.ContainerState{Running: &core.ContainerStateRunning{StartedAt: metav1.NewTime(end.Add(-time.Hour))}},
			}}},
		}},
		ReplicaSets: []apps.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{
			Namespace: "payments", Name: "checkout-demo", UID: "rs-uid",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "checkout", UID: "deployment-uid", Controller: &isController}},
		}}},
	}
}

func exampleCapture(snapshot model.Snapshot) *model.HistoryCapture {
	end := snapshot.CollectedAt
	start := end.Add(-15 * time.Minute)
	cpu := model.HistorySeries{Status: model.HistoryComplete}
	memory := model.HistorySeries{Status: model.HistoryComplete}
	for i := range 31 {
		at := start.Add(time.Duration(i) * 30 * time.Second)
		cpu.Points = append(cpu.Points, model.HistoryPoint{At: at, Value: 100 + float64(i)*9})
		memory.Points = append(memory.Points, model.HistoryPoint{At: at, Value: float64((64 + i) * mebibyte)})
	}
	return &model.HistoryCapture{
		SchemaVersion: model.HistorySchemaVersion, Status: model.HistoryComplete, WindowStart: start, WindowEnd: end,
		StartedAt: end.Add(time.Second), FinishedAt: end.Add(3 * time.Second), StepSeconds: 15, ScrapeSeconds: 30,
		Targets: []model.HistoryTargetCapture{{
			Target: model.HistoryTarget{
				Pod:       model.Identity("Pod", &snapshot.Pods[0]),
				Workload:  model.Resource{Kind: "Deployment", Name: "checkout", Namespace: "payments", UID: "deployment-uid"},
				Container: "app", VPADisabled: true,
			},
			CPUCounter: cpu, Memory: memory,
		}},
		Grafana: exampleConfig("").Grafana,
	}
}

func TestSizeWithCoverage(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	snapshot := exampleSnapshot(now)
	snapshot.History = exampleCapture(snapshot)
	item := assertAdequate(t, Analyze(snapshot))
	if !ValidGrafanaLink(item.GrafanaURL) || !strings.Contains(item.GrafanaURL, "var-workload=checkout") {
		t.Fatalf("invalid dashboard navigation: %q", item.GrafanaURL)
	}
}

func assertAdequate(t *testing.T, report *model.HistoryReport) model.HistoryTargetReport {
	t.Helper()
	if report == nil || report.Status != model.HistoryComplete || len(report.Targets) != 1 {
		t.Fatalf("expected complete history for one target: %+v", report)
	}
	item := report.Targets[0]
	if item.CPU.DistinctScrapes != 31 || len(item.CPU.Samples) != 30 || item.Memory.DistinctScrapes != 31 ||
		item.CPU.P95 == nil || *item.CPU.P95 != 300 || item.Memory.P95 == nil || *item.Memory.P95 != 93*mebibyte ||
		item.CPU.Advice.Request == nil || *item.CPU.Advice.Request != 380 ||
		item.Memory.Advice.Request == nil || *item.Memory.Advice.Request != 121*mebibyte {
		t.Fatalf("unexpected historical calculations: %+v", item)
	}
	return item
}

func TestSizingSuppressions(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name                    string
		change                  func(*model.Snapshot)
		cpuReason, memoryReason string
	}{
		{"sparse", func(s *model.Snapshot) {
			s.History.Targets[0].CPUCounter.Points = s.History.Targets[0].CPUCounter.Points[15:]
		}, "independent scrapes", ""},
		{"gap", func(s *model.Snapshot) {
			points := s.History.Targets[0].Memory.Points
			s.History.Targets[0].Memory.Points = append(points[:12], points[15:]...)
		}, "", "independent scrapes"},
		{"resets", func(s *model.Snapshot) {
			s.History.Targets[0].CPUCounter.Points[15].Value = 0
		}, "independent scrapes", ""},
		{"HPA utilization", func(s *model.Snapshot) {
			s.Expansion.HPAs = []model.HPAEvidence{{
				Target:             s.History.Targets[0].Target.Workload,
				UtilizationTargets: []model.HPAUtilizationTarget{{Resource: "cpu", Container: "app", Percent: 80}},
			}}
		}, "matching HPA", ""},
		{"unknown HPA visibility", func(s *model.Snapshot) {
			s.Sources = s.Sources[:2]
		}, "HPA inventory", "HPA inventory"},
		{"truncated matching HPA", func(s *model.Snapshot) {
			s.Expansion.HPAs = []model.HPAEvidence{{
				Target: s.History.Targets[0].Target.Workload, TargetsTruncated: true,
			}}
		}, "truncated metric observations", "truncated metric observations"},
		{"Pod-level request", func(s *model.Snapshot) {
			s.Pods[0].Spec.Resources = &core.ResourceRequirements{
				Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("400m")},
			}
		}, "shared budget", ""},
		{"VPA not excluded", func(s *model.Snapshot) {
			s.History.Targets[0].Target.VPADisabled = false
		}, "outside VPA management", "outside VPA management"},
		{"container restart near window", func(s *model.Snapshot) {
			s.Pods[0].Status.ContainerStatuses[0].State.Running.StartedAt = metav1.NewTime(now.Add(-time.Minute))
		}, "current container lifetime", "current container lifetime"},
		{"unplaced OOM", func(s *model.Snapshot) {
			s.Pods[0].Status.ContainerStatuses[0].LastTerminationState.Terminated = &core.ContainerStateTerminated{Reason: "OOMKilled"}
		}, "termination evidence", "termination evidence"},
		{"configured CPU limit", func(s *model.Snapshot) {
			s.Pods[0].Spec.Containers[0].Resources.Limits = core.ResourceList{core.ResourceCPU: resource.MustParse("800m")}
		}, "hide demand", ""},
		{"censored memory", func(s *model.Snapshot) {
			s.Pods[0].Spec.Containers[0].Resources.Limits = core.ResourceList{core.ResourceMemory: resource.MustParse("116Mi")}
		}, "", "exceeds the configured limit"},
		{"partial provider", func(s *model.Snapshot) {
			s.History.Status, s.History.Reason = model.HistoryPartial, model.HistoryResponseWarnings
		}, "incomplete provider", "incomplete provider"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := exampleSnapshot(now)
			snapshot.History = exampleCapture(snapshot)
			tt.change(&snapshot)
			report := Analyze(snapshot)
			if len(report.Targets) != 1 {
				t.Fatalf("expected stats and evidence for target: %+v", report)
			}
			for _, item := range []struct {
				want string
				got  model.HistoryAdvice
			}{{tt.cpuReason, report.Targets[0].CPU.Advice}, {tt.memoryReason, report.Targets[0].Memory.Advice}} {
				if item.want == "" && item.got.Request == nil || item.want != "" && (item.got.Request != nil || !strings.Contains(item.got.Reason, item.want)) {
					t.Errorf("expected advice reason %q, got %+v", item.want, item.got)
				}
			}
		})
	}
}

func TestProviderScopeFailuresAndBudgets(t *testing.T) {
	for _, tt := range []struct {
		name, body     string
		status         int
		expectedStatus model.HistoryStatus
		expectedReason model.HistoryReason
	}{
		{"denied", "provider-body-SENTINEL", 403, model.HistoryPermission, model.HistoryRequestDenied},
		{"redirect", "provider-body-SENTINEL", 307, model.HistoryFailed, model.HistoryRequestFailed},
		{"unknown response", `{"status":"success","data":{"resultType":"vector","result":[]}}`, 200, model.HistoryFailed, model.HistoryBadResponse},
		{"no metrics", `{"status":"success","data":{"resultType":"matrix","result":[]}}`, 200, model.HistoryUnavailable, model.HistoryMetricAbsent},
		{"warning", `{"status":"success","warnings":["provider-body-SENTINEL"],"data":{"resultType":"matrix","result":[]}}`, 200, model.HistoryPartial, model.HistoryResponseWarnings},
		{"oversized", strings.Repeat("x", maxResponseBytes+1), 200, model.HistoryTruncated, model.HistoryResponseLimited},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests.Add(1)
				if req.Method != "POST" || req.URL.Path != "/api/v1/query_range" || req.Header.Get("Authorization") != "" {
					t.Errorf("unexpected provider request: %s %s", req.Method, req.URL.Path)
				}
				if err := req.ParseForm(); err != nil {
					t.Error("could not parse query form")
				}
				query := req.Form.Get("query")
				for _, selector := range []string{`cluster="lab"`, `namespace="payments"`, `pod_uid="pod-uid"`, `container="app"`, `container_id="` + fixtureRuntime + `"`} {
					if !strings.Contains(query, selector) {
						t.Errorf("fixed selector missing %s", selector)
					}
				}
				w.Header().Set("Location", "/should-not-be-contacted")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			p, err := New(exampleConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			s := exampleSnapshot(time.Now().UTC().Truncate(time.Second))
			beforeEvaluation := s.CollectedAt
			p.Enrich(t.Context(), &s)
			report := Analyze(s)
			if s.Coverage(model.SourcePods) != model.Complete || report.Status != tt.expectedStatus || report.Reason != tt.expectedReason || requests.Load() > 2 {
				t.Fatalf("unexpected historical failure state with intact core coverage: %+v, %d requests", report, requests.Load())
			}
			if !s.FinishedAt.Equal(s.History.FinishedAt) || !s.CollectedAt.Equal(beforeEvaluation) {
				t.Fatal("historical capture timing modified the evaluation time or did not extend the envelope")
			}
			data, err := json.Marshal(s.History)
			if err != nil || strings.Contains(string(data), "SENTINEL") || strings.Contains(string(data), fixtureRuntime) || strings.Contains(string(data), server.URL) {
				t.Fatal("historical evidence retained remote input or runtime identity")
			}
			s.Scope.Namespace = "other"
			before := requests.Load()
			if got := p.Capture(t.Context(), s); got.Reason != model.HistoryNoTargets || requests.Load() != before {
				t.Fatalf("out-of-scope operation queried provider: %+v", got)
			}
		})
	}
}

func TestTLSVerificationAndNoRedirectCredential(t *testing.T) {
	var redirected atomic.Int32
	redirect := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer redirect.Close()
	var authorized atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer history-token-SENTINEL" {
			authorized.Add(1)
		}
		http.Redirect(w, r, redirect.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	t.Setenv("KUBEDOCTOR_TEST_TOKEN", "history-token-SENTINEL")
	cfg := exampleConfig(server.URL)
	cfg.BearerTokenEnv = "KUBEDOCTOR_TEST_TOKEN"
	s := exampleSnapshot(time.Now().UTC().Truncate(time.Second))
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result := p.Capture(t.Context(), s)
	if result.Status != model.HistoryFailed || authorized.Load() != 0 {
		t.Fatalf("untrusted peer was accepted: %+v", result)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.CAFile = ca
	p, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result = p.Capture(t.Context(), s)
	if result.Status != model.HistoryFailed || authorized.Load() != 2 || redirected.Load() != 0 {
		t.Fatalf("redirect followed or credential scope changed: %+v", result)
	}
	cfg.URL = redirect.URL
	if _, err := New(cfg); err == nil {
		t.Fatal("credentialed cleartext loopback accepted")
	}
}

func TestCanceledBeforeRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("canceled operation reached the provider")
	}))
	defer server.Close()
	p, err := New(exampleConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	capture := p.Capture(ctx, exampleSnapshot(time.Now().UTC().Truncate(time.Second)))
	if capture.Reason != model.HistoryRequestTimeout {
		t.Fatalf("unexpected cancellation status: %+v", capture)
	}
}

func TestHistoryValidationRejectsHostileEvidence(t *testing.T) {
	s := exampleSnapshot(time.Now().UTC().Truncate(time.Second))
	h := exampleCapture(s)
	for _, origin := range []string{
		"javascript:alert(1)", "https://grafana.example/?token=SENTINEL", "https://user:SENTINEL@grafana.example", "https://grafana.example/app",
	} {
		h.Grafana.Origin = origin
		if err := ValidateCapture(h, s.Scope); err == nil {
			t.Errorf("accepted hostile dashboard origin %q", origin)
		}
	}
	h.Grafana = nil
	h.Targets[0].Target.Pod.Namespace = "unallowed"
	h.Targets[0].Target.Workload.Namespace = "unallowed"
	if err := ValidateCapture(h, s.Scope); err == nil {
		t.Fatal("accepted history that escaped the capture scope")
	}
	if ValidGrafanaLink("https://example.org/d/abc/dash?from=1&to=2&token=SENTINEL") {
		t.Fatal("accepted arbitrary URL query")
	}
}

func TestHistoryOwnerValidation(t *testing.T) {
	s := exampleSnapshot(time.Now().UTC().Truncate(time.Second))
	s.History = exampleCapture(s)
	s.Pods[0].Status.ContainerStatuses[0].ContainerID = ""
	if err := ValidateTargetOwners(s); err != nil {
		t.Fatalf("normalized ownership unexpectedly required the runtime identity: %v", err)
	}
	s.History.Targets[0].Target.Workload.UID = "unobserved"
	if err := ValidateTargetOwners(s); err == nil {
		t.Fatal("accepted a history relationship absent from Kubernetes observations")
	}
}

func TestRejectsUntrustedConfigFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte(`{"query":"malicious-secret-SENTINEL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || strings.Contains(fmt.Sprint(err), "SENTINEL") {
		t.Fatalf("configuration accepted arbitrary query or echoed raw input: %v", err)
	}
}

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type capturedFixture struct{ coverage model.Coverage }

func (f capturedFixture) Collect(_ context.Context, req model.ScanRequest) model.Snapshot {
	source := model.SourceResult{Source: model.SourcePods, Required: true, Status: f.coverage}
	if f.coverage != model.Complete {
		source.Detail = "Kubernetes did not authorize this API read"
	}
	at := time.Date(2026, 1, 12, 14, 0, 0, 0, time.UTC)
	return model.Snapshot{Scope: req.Scope, CollectedAt: at, StartedAt: at, FinishedAt: at,
		Sources: []model.SourceResult{source}, SourceTimings: []model.SourceTiming{{Source: model.SourcePods, StartedAt: at, FinishedAt: at}}}
}

func TestCaptureOfflineReplayAndStoredContract(t *testing.T) {
	for _, coverage := range []model.Coverage{model.Complete, model.PermissionDenied} {
		t.Run(string(coverage), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capture.json")
			factory := func(*rest.Config) (engine.Scanner, error) { return engine.New(capturedFixture{coverage}), nil }
			code, live, stderr := run(t, []string{"capture", "namespace", "payments", "--bundle", path, "-o", "json", "--kubeconfig", configFile(t, fixtureConfig)}, factory)
			want := 0
			if coverage != model.Complete {
				want = 2
			}
			if code != want || !json.Valid([]byte(live)) {
				t.Fatalf("capture exit %d, stderr=%s, stdout=%s", code, stderr, live)
			}
			forbid := func(*rest.Config) (engine.Scanner, error) {
				t.Fatal("offline command initialized a live scanner")
				return nil, nil
			}
			badConfig := filepath.Join(t.TempDir(), "absent")
			for _, command := range []string{"replay", "investigate"} {
				code, out, stderr := run(t, []string{command, path, "-o", "json", "--kubeconfig", badConfig}, forbid)
				if code != want || out != live {
					t.Fatalf("%s returned %d: %s; replay diverged from live", command, code, stderr)
				}
			}
			code, stored, stderr := run(t, []string{"replay", path, "--mode", "stored", "-o", "json"}, forbid)
			var report model.Report
			if code != want || json.Unmarshal([]byte(stored), &report) != nil || report.SchemaVersion != "1" || report.Scope.Namespace != "payments" {
				t.Fatalf("stored report: %d %s %s", code, stderr, stored)
			}
			code, inventory, stderr := run(t, []string{"inspect", path, "--kubeconfig", badConfig}, forbid)
			if code != 0 || !json.Valid([]byte(inventory)) {
				t.Fatalf("inspect: %d %s %s", code, stderr, inventory)
			}
			code, _, stderr = run(t, []string{"replay", path, "-o", "html", "--file", path}, forbid)
			if code != 2 || !strings.Contains(stderr, "exists") {
				t.Fatalf("overwrite: %d %s", code, stderr)
			}
			if data, err := os.ReadFile(path); err != nil || !json.Valid(data) {
				t.Fatal("capture file was modified", err)
			}
		})
	}
}

func TestInvestigationInvalidOptionsAreOffline(t *testing.T) {
	for _, args := range [][]string{{"capture", "cluster"}, {"capture", "namespace", "INVALID", "--bundle", "new"},
		{"capture", "cluster", "--bundle", "file", "--timeout", "0s"}, {"replay", "empty", "--mode", "unrecognized"},
		{"replay", "empty", "--mode", "stored", "-o", "html"}, {"compare", "a", "b", "--fail-on", "invalid"}} {
		code, _, _ := run(t, args, func(*rest.Config) (engine.Scanner, error) {
			t.Fatal("invalid input started collection")
			return nil, nil
		})
		if code != 2 {
			t.Errorf("%v = %d", args, code)
		}
	}
}

type historyFixtureCollector struct{}

func (historyFixtureCollector) Collect(_ context.Context, req model.ScanRequest) model.Snapshot {
	at := time.Now().UTC()
	pod := core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: req.Scope.Namespace, UID: "fixture-pod"}}
	pod.Spec.Containers = []core.Container{{Name: "api"}}
	pod.Status.Phase = core.PodRunning
	pod.Status.ContainerStatuses = []core.ContainerStatus{{Name: "api", ContainerID: "containerd://" + strings.Repeat("a", 64), State: core.ContainerState{Running: &core.ContainerStateRunning{StartedAt: metav1.NewTime(at.Add(-time.Hour))}}}}
	return model.Snapshot{Scope: req.Scope, CollectedAt: at, StartedAt: at, FinishedAt: at, Pods: []core.Pod{pod}, Sources: []model.SourceResult{{Source: model.SourcePods, Status: model.Complete, Required: true}}}
}

func TestCapturePreservesCoreReportWhenRequestedHistoryFails(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "fixture-private-provider-message", 503)
	}))
	defer server.Close()
	cfg := configFile(t, `{"url":"`+server.URL+`","cluster":"lab","targets":[{"namespace":"payments","kind":"Pod","name":"shop","container":"api"}]}`)
	path := filepath.Join(t.TempDir(), "observation.json")
	code, out, stderr := run(t, []string{"capture", "namespace", "payments", "--bundle", path, "--history-config", cfg, "--kubeconfig", configFile(t, fixtureConfig), "-o", "json"}, func(*rest.Config) (engine.Scanner, error) { return engine.New(historyFixtureCollector{}), nil })
	var result struct {
		Report  model.Report
		History *model.HistoryReport
	}
	if code != 2 || calls.Load() == 0 || json.Unmarshal([]byte(out), &result) != nil || !result.Report.Complete || len(result.Report.Findings) == 0 || result.History == nil || result.History.Status == model.HistoryComplete {
		t.Fatalf("history/core outcome: %d; %d requests; %s %s", code, calls.Load(), out, stderr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data)+out+stderr, "fixture-private-provider-message") || strings.Contains(string(data), server.URL) {
		t.Fatal("provider metadata escaped into the evidence")
	}
	before := calls.Load()
	code, replay, stderr := run(t, []string{"replay", path, "-o", "json"}, func(*rest.Config) (engine.Scanner, error) {
		t.Fatal("offline replay configured a Kubernetes client")
		return nil, nil
	})
	if code != 0 || replay != out || calls.Load() != before {
		t.Fatalf("offline partial-provider replay: %d %s", code, stderr)
	}
}

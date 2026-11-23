package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	"k8s.io/client-go/rest"
)

type capturedFixture struct{ coverage model.Coverage }

func (f capturedFixture) Collect(_ context.Context, req model.ScanRequest) model.Snapshot {
	source := model.SourceResult{Source: model.SourcePods, Required: true, Status: f.coverage}
	if f.coverage != model.Complete {
		source.Detail = "Kubernetes RBAC denied access."
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

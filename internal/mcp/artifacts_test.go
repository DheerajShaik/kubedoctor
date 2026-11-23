package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/bundle"
	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/history"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var (
	fixtureTime = time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	fixtureID   = strings.Repeat("a", 64)
)

func snapshotFixture(scope model.Scope, at time.Time, broken bool) model.Snapshot {
	pod := core.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout", Namespace: scope.Namespace, UID: types.UID("checkout-uid"),
			CreationTimestamp: metav1.NewTime(at.Add(-time.Hour)),
		},
		Spec: core.PodSpec{Containers: []core.Container{{Name: "app"}}},
		Status: core.PodStatus{
			Phase:      core.PodRunning,
			Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}},
			ContainerStatuses: []core.ContainerStatus{{
				Name: "app", Ready: true,
				State: core.ContainerState{Running: &core.ContainerStateRunning{StartedAt: metav1.NewTime(at.Add(-time.Minute))}},
			}},
		},
	}
	if broken {
		pod.Status.Phase = core.PodPending
		pod.Status.Conditions = nil
		pod.Status.ContainerStatuses[0] = core.ContainerStatus{
			Name: "app", State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}
	}
	return model.Snapshot{
		Scope: scope, StartedAt: at.Add(-time.Second), FinishedAt: at, CollectedAt: at,
		ClusterID: fixtureID, ClusterProvenance: model.KubeSystemUIDProvenance,
		Sources: []model.SourceResult{
			{Source: model.SourcePods, Required: true, Status: model.Complete},
			{Source: model.SourceEvents, Required: true, Status: model.Complete},
		},
		SourceTimings: []model.SourceTiming{
			{Source: model.SourcePods, StartedAt: at.Add(-time.Second), FinishedAt: at},
			{Source: model.SourceEvents, StartedAt: at.Add(-time.Second), FinishedAt: at},
		},
		Pods: []core.Pod{pod},
	}
}

func namespaceScope(contextName, namespace string) model.Scope {
	return model.Scope{Kind: model.NamespaceScope, Context: contextName, Namespace: namespace}
}

func makeFixtureBundle(t *testing.T, snapshot model.Snapshot) *bundle.Bundle {
	t.Helper()
	evidence, err := bundle.New(snapshot, "v0.2.0-test")
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func saveFixtureBundle(t *testing.T, path string, snapshot model.Snapshot) {
	t.Helper()
	if err := makeFixtureBundle(t, snapshot).Save(path); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactStorePolicyAndLifetime(t *testing.T) {
	ctx := t.Context()
	snapshot := snapshotFixture(namespaceScope("fixture", "payments"), fixtureTime, true)
	evidence := makeFixtureBundle(t, snapshot)
	policy := scopePolicy{context: "fixture", namespaces: map[string]bool{"payments": true}, clusterID: fixtureID}
	store := newArtifactStore(policy)
	reference, err := store.register(ctx, evidence)
	if err != nil || !validArtifactID(reference.ArtifactID) {
		t.Fatalf("register = %+v, %v", reference, err)
	}
	if got := store.list().Artifacts; len(got) != 1 || got[0] != reference {
		t.Fatalf("artifact list = %v", got)
	}
	got, err := store.load(ctx, reference.ArtifactID)
	if err != nil || got.Inventory().Scope != snapshot.Scope {
		t.Fatalf("lookup = %v, %v", got, err)
	}
	newSession := newArtifactStore(policy)
	if _, err := newSession.load(ctx, reference.ArtifactID); !errors.Is(err, errArtifactMissing) {
		t.Fatalf("old session ID returned %v", err)
	}
	newReference, err := newSession.register(ctx, evidence)
	if err != nil || newReference.ArtifactID == reference.ArtifactID {
		t.Fatalf("new session returned %v, %v", newReference, err)
	}
	snapshot.ClusterProvenance = model.NamespaceUIDProvenance
	pinned, err := newSession.register(ctx, makeFixtureBundle(t, snapshot))
	if err != nil || pinned.Provenance != model.NamespaceUIDProvenance {
		t.Fatalf("namespace provenance pin was rejected: %v, %v", pinned, err)
	}
	for _, test := range []struct {
		name   string
		policy scopePolicy
	}{
		{"different context", scopePolicy{context: "other", namespaces: policy.namespaces}},
		{"denied namespace", scopePolicy{context: "fixture", namespaces: map[string]bool{"public": true}}},
		{"wrong cluster identity", scopePolicy{context: "fixture", namespaces: policy.namespaces, clusterID: strings.Repeat("b", 64)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newArtifactStore(test.policy)
			if _, err := store.register(ctx, evidence); !errors.Is(err, errArtifactScope) {
				t.Fatalf("register error = %v", err)
			}
			if got := store.list().Artifacts; len(got) != 0 {
				t.Fatalf("unauthorized artifact was retained: %+v", got)
			}
		})
	}
}

func TestArtifactStoreBudgets(t *testing.T) {
	ctx := t.Context()
	policy := scopePolicy{context: "fixture", namespaces: map[string]bool{"payments": true}}
	evidence := makeFixtureBundle(t, snapshotFixture(namespaceScope("fixture", "payments"), fixtureTime, true))
	store := newArtifactStore(policy)
	for range maxArtifacts {
		if _, err := store.register(ctx, evidence); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.register(ctx, evidence); !errors.Is(err, errArtifactLimit) || store.hasRoom() {
		t.Fatalf("count limit = %v, room = %t", err, store.hasRoom())
	}
	byteLimited := newArtifactStore(policy)
	byteLimited.bytes = maxArtifactStorage - 10
	if _, err := byteLimited.register(ctx, evidence); !errors.Is(err, errArtifactLimit) || len(byteLimited.list().Artifacts) != 0 {
		t.Fatalf("byte budget = %v", err)
	}
}

func TestStartupImportRejectsTraversalLinksDirectoriesAndForeignContent(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	snapshot := snapshotFixture(namespaceScope("fixture", "payments"), fixtureTime, true)
	saveFixtureBundle(t, filepath.Join(dir, "allowed.json"), snapshot)
	saveFixtureBundle(t, filepath.Join(outside, "outside.json"), snapshot)
	saveFixtureBundle(t, filepath.Join(dir, "wrong-context.json"), snapshotFixture(namespaceScope("foreign", "payments"), fixtureTime, true))
	saveFixtureBundle(t, filepath.Join(dir, "wrong-namespace.json"), snapshotFixture(namespaceScope("fixture", "other"), fixtureTime, true))
	cluster := snapshotFixture(model.Scope{Kind: model.ClusterScope, Context: "fixture"}, fixtureTime, true)
	cluster.Pods = nil
	saveFixtureBundle(t, filepath.Join(dir, "cluster.json"), cluster)

	if err := os.Mkdir(filepath.Join(dir, "subdirectory"), 0700); err != nil {
		t.Fatal(err)
	}
	sparse, err := os.Create(filepath.Join(dir, "oversized.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sparse.Truncate(maxArtifactBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := sparse.Close(); err != nil {
		t.Fatal(err)
	}
	closed := newArtifactStore(scopePolicy{context: "fixture", namespaces: map[string]bool{"payments": true}})
	for _, files := range [][]string{
		{"../outside.json"}, {filepath.Join(outside, "outside.json")}, {"subdirectory"},
		{makeTamperedBundle(t, dir, snapshot)}, {"wrong-context.json"}, {"wrong-namespace.json"},
		{"cluster.json"}, {"oversized.json"}, {"allowed.json", "allowed.json"}, {"allowed.json:stream"},
	} {
		if err := closed.importStartup(t.Context(), dir, files); err == nil || len(closed.list().Artifacts) != 0 {
			t.Fatalf("unsafe startup files %v returned %v", files, err)
		}
	}
	for name, target := range map[string]string{
		"inside-link.json":  "allowed.json",
		"outside-link.json": filepath.Join(outside, "outside.json"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Skipf("fixture symlink unavailable: %v", err)
			}
			if err := closed.importStartup(t.Context(), dir, []string{name}); err == nil || len(closed.list().Artifacts) != 0 {
				t.Fatalf("symlink was accepted: %v", err)
			}
		})
	}
	if err := closed.importStartup(t.Context(), dir, []string{"allowed.json"}); err != nil {
		t.Fatal(err)
	}
	view := closed.list().Artifacts
	if len(view) != 1 || view[0].Scope != snapshot.Scope {
		t.Fatalf("valid artifact was not authorized: %+v", view)
	}
	if err := os.WriteFile(filepath.Join(dir, "allowed.json"), []byte("fixture-private-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	evidence, err := closed.load(t.Context(), view[0].ArtifactID)
	if err != nil || evidence.Inventory().Scope != snapshot.Scope {
		t.Fatalf("registered artifact changed when its import file was replaced: %v", err)
	}
}

func makeTamperedBundle(t *testing.T, dir string, original model.Snapshot) string {
	t.Helper()
	return writeEditedBundle(t, dir, "misplaced.json", original, func(payload []byte) []byte {
		misplaced := bytes.Replace(payload, []byte(`"namespace":"payments"`), []byte(`"namespace":"foreign"`), 2)
		// Restore the header while leaving the Pod observation outside its scope.
		return bytes.Replace(misplaced, []byte(`"namespace":"foreign"`), []byte(`"namespace":"payments"`), 1)
	})
}

func writeEditedBundle(t *testing.T, dir, name string, original model.Snapshot, edit func([]byte) []byte) string {
	t.Helper()
	var encoded bytes.Buffer
	if err := makeFixtureBundle(t, original).Encode(&encoded); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Version string          `json:"schema_version"`
		Digest  string          `json:"sha256"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(encoded.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	edited := edit(env.Payload)
	if bytes.Equal(env.Payload, edited) {
		t.Fatal("unexpected unchanged test bundle payload")
	}
	env.Payload = edited
	digest := sha256.Sum256(env.Payload)
	env.Digest = hex.EncodeToString(digest[:])
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

type captureFixture func(context.Context, model.ScanRequest) model.Snapshot

func (f captureFixture) Capture(ctx context.Context, request model.ScanRequest) model.Snapshot {
	return f(ctx, request)
}

func (f captureFixture) Scan(ctx context.Context, request model.ScanRequest) model.Report {
	return engine.Analyze(f(ctx, request))
}

func TestCaptureToolsEnforceScopeAndOpaqueInput(t *testing.T) {
	capture := captureFixture(func(_ context.Context, request model.ScanRequest) model.Snapshot {
		return snapshotFixture(request.Scope, fixtureTime, true)
	})
	client := memoryClient(t, t.Context(), capture, Options{
		Context: "fixture", Timeout: time.Second, AllowNamespaces: []string{"payments"}, Version: "v0.2.0-test",
	})

	for _, denied := range []struct{ tool, args string }{
		{captureNamespace, `{"namespace":"foreign"}`},
		{captureNamespace, `{"namespace":"payments","context":"fixture-private-marker"}`},
		{captureCluster, `{}`},
		{inspectBundle, `{"artifact_id":"./fixture-private-marker"}`},
		{getEvidence, `{"artifact_id":"art_AAAAAAAAAAAAAAAAAAAAAAAAAA","path":"fixture-private-marker"}`},
		{compareCaptures, `{"before_id":"art_AAAAAAAAAAAAAAAAAAAAAAAAAA","before_id":"art_BBBBBBBBBBBBBBBBBBBBBBBBBB"}`},
		{historicalTool, `{"artifact_id":"art_AAAAAAAAAAAAAAAAAAAAAAAAAA","query":"fixture-private-marker"}`},
	} {
		result := callTool(t, client, denied.tool, denied.args)
		if !result.IsError || bytes.Contains(resultJSON(t, result), []byte("fixture-private-marker")) {
			t.Fatalf("unexpected result for %s: %s", denied.tool, resultJSON(t, result))
		}
	}

	result := callTool(t, client, captureNamespace, `{"namespace":"payments"}`)
	if result.IsError {
		t.Fatal(string(resultJSON(t, result)))
	}
	var reference artifactReference
	convertStructured(t, result, &reference)
	if !validArtifactID(reference.ArtifactID) || reference.Scope != namespaceScope("fixture", "payments") {
		t.Fatalf("capture reference = %+v", reference)
	}
	report := decodeReport(t, callTool(t, client, getEvidence, fmt.Sprintf(`{"artifact_id":%q}`, reference.ArtifactID)))
	if report.Scope != reference.Scope || len(report.Findings) == 0 {
		t.Fatalf("replayed report = %+v", report)
	}
}

func TestReplayToolsRejectDifferentAnalyzerVersion(t *testing.T) {
	dir := t.TempDir()
	snapshot := snapshotFixture(namespaceScope("fixture", "payments"), fixtureTime, false)
	snapshot.History = historyFixture(snapshot)
	name := writeEditedBundle(t, dir, "newer.json", snapshot, func(data []byte) []byte {
		expected := fmt.Sprintf(`"analyzer_version":%q`, model.AnalyzerVersion)
		return bytes.Replace(data, []byte(expected), []byte(`"analyzer_version":"98765"`), 1)
	})
	client := memoryClient(t, t.Context(), scannerFunc(func(_ context.Context, request model.ScanRequest) model.Report {
		return reportFor(request)
	}), Options{
		Context: "fixture", Timeout: time.Second, AllowNamespaces: []string{"payments"},
		ArtifactDirectory: dir, ImportBundles: []string{name}, Version: "v0.2.0-test",
	})
	var list artifactList
	convertStructured(t, callTool(t, client, listArtifacts, `{}`), &list)
	if len(list.Artifacts) != 1 {
		t.Fatalf("artifact inventory = %+v", list)
	}
	arguments := fmt.Sprintf(`{"artifact_id":%q}`, list.Artifacts[0].ArtifactID)
	inspect := callTool(t, client, inspectBundle, arguments)
	if inspect.IsError {
		t.Fatalf("inspection should remain compatible: %s", resultJSON(t, inspect))
	}
	for _, tool := range []string{analyzeBundle, getEvidence, getRelationships, historicalTool} {
		result := callTool(t, client, tool, arguments)
		if !result.IsError || result.StructuredContent != nil ||
			!bytes.Contains(resultJSON(t, result), []byte("requires a compatible analyzer version")) {
			t.Fatalf("unexpected %s response: %s", tool, resultJSON(t, result))
		}
	}
}

func TestConfiguredHistoryUsesAuthorizedCaptureScope(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the out-of-scope history target reached the provider")
		http.Error(w, "fixture sent an unexpected request", http.StatusBadRequest)
	}))
	t.Cleanup(endpoint.Close)
	provider, err := history.New(history.Config{
		URL: endpoint.URL, Cluster: "fixture-cluster",
		Targets: []history.TargetPolicy{{Namespace: "other", Kind: "Pod", Name: "checkout", Container: "app"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := captureFixture(func(_ context.Context, request model.ScanRequest) model.Snapshot {
		return snapshotFixture(request.Scope, time.Now().UTC(), false)
	})
	client := memoryClient(t, t.Context(), capture, Options{
		Context: "fixture", Timeout: time.Second, AllowNamespaces: []string{"payments"},
		Version: "v0.2.0-test", History: provider,
	})
	saved := callTool(t, client, captureNamespace, `{"namespace":"payments"}`)
	var artifact artifactReference
	convertStructured(t, saved, &artifact)
	if saved.IsError || !validArtifactID(artifact.ArtifactID) {
		t.Fatalf("capture failed: %s", resultJSON(t, saved))
	}
	result := callTool(t, client, historicalTool, fmt.Sprintf(`{"artifact_id":%q}`, artifact.ArtifactID))
	var output historicalResult
	convertStructured(t, result, &output)
	if result.IsError || output.Scope != artifact.Scope || output.Status != model.HistoryUnavailable ||
		output.History == nil || output.History.Reason != model.HistoryNoTargets || len(output.History.Targets) != 0 {
		t.Fatalf("unexpected history result: %s", resultJSON(t, result))
	}
}

func TestClusterCaptureWithExplicitEnablement(t *testing.T) {
	capture := captureFixture(func(_ context.Context, request model.ScanRequest) model.Snapshot {
		snapshot := snapshotFixture(request.Scope, fixtureTime, false)
		snapshot.Pods[0].Namespace = "payments"
		return snapshot
	})
	client := memoryClient(t, t.Context(), capture, Options{
		Context: "fixture", Timeout: time.Second, EnableCluster: true, ArtifactClusterID: fixtureID, Version: "v0.2.0-test",
	})
	saved := callTool(t, client, captureCluster, `{}`)
	var reference artifactReference
	convertStructured(t, saved, &reference)
	expected := model.Scope{Kind: model.ClusterScope, Context: "fixture"}
	if saved.IsError || !validArtifactID(reference.ArtifactID) || reference.Scope != expected {
		t.Fatalf("cluster capture = %s", resultJSON(t, saved))
	}
}

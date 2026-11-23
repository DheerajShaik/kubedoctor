package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/bundle"
	"github.com/DheerajShaik/kubedoctor/internal/investigation"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/types"
)

type blockedFixtureRequest struct {
	started  chan struct{}
	canceled chan struct{}
}

func assertStdioCancellation(t *testing.T, session *protocol.ClientSession, blocker *atomic.Pointer[blockedFixtureRequest]) {
	t.Helper()
	wait := &blockedFixtureRequest{started: make(chan struct{}), canceled: make(chan struct{})}
	blocker.Store(wait)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &protocol.CallToolParams{
			Name: namespaceTool, Arguments: map[string]any{"namespace": "payments"},
		})
		result <- err
	}()
	select {
	case <-wait.started:
	case <-ctx.Done():
		t.Fatal("stdio scan did not reach the blocked API endpoint")
	}
	busy := callTool(t, session, captureNamespace, `{"namespace":"payments"}`)
	if !busy.IsError || !bytes.Contains(resultJSON(t, busy), []byte(errBusy.Error())) {
		t.Fatalf("overlapping capture was not rejected: %s", resultJSON(t, busy))
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stdio cancellation returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled stdio call did not return")
	}
	select {
	case <-wait.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled stdio request did not interrupt the Kubernetes request")
	}
}

func checkStdioArtifacts(t *testing.T, binary, kubeconfig string, requests *atomic.Int32, repaired *atomic.Bool) {
	t.Helper()
	dir := t.TempDir()
	beforePath, afterPath := filepath.Join(dir, "before.json"), filepath.Join(dir, "after.json")
	before := captureFixtureFile(t, binary, kubeconfig, beforePath)
	repaired.Store(true)
	defer repaired.Store(false)
	after := captureFixtureFile(t, binary, kubeconfig, afterPath)

	large := snapshotFixture(namespaceScope("fixture", "payments"), fixtureTime, true)
	for i := 0; i < 1_000; i++ {
		pod := large.Pods[0].DeepCopy()
		pod.Name = fmt.Sprintf("checkout-%d", i)
		pod.UID = types.UID(pod.Name + "-uid")
		large.Pods = append(large.Pods, *pod)
	}
	saveFixtureBundle(t, filepath.Join(dir, "large.json"), large)
	historical := snapshotFixture(namespaceScope("fixture", "payments"), fixtureTime, false)
	historical.History = historyFixture(historical)
	saveFixtureBundle(t, filepath.Join(dir, "history.json"), historical)

	atStart := requests.Load()
	args := []string{"mcp", "serve", "--stdio", "--kubeconfig", kubeconfig, "--context", "fixture",
		"--allow-namespace", "payments", "--artifact-dir", dir,
		"--allow-bundle", "before.json", "--allow-bundle", "after.json", "--allow-bundle", "large.json", "--allow-bundle", "history.json"}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	session := startArtifactClient(t, ctx, binary, args)
	inventoryResponse := callTool(t, session, listArtifacts, `{}`)
	var artifacts artifactList
	convertStructured(t, inventoryResponse, &artifacts)
	if inventoryResponse.IsError || len(artifacts.Artifacts) != 4 ||
		bytes.Contains(resultJSON(t, inventoryResponse), []byte("before.json")) {
		t.Fatalf("unexpected artifact inventory: %s", resultJSON(t, inventoryResponse))
	}
	for _, a := range artifacts.Artifacts {
		if !validArtifactID(a.ArtifactID) || a.Scope != before.Scope {
			t.Fatalf("artifact authorization = %+v", a)
		}
	}
	beforeRef, afterRef := artifacts.Artifacts[0], artifacts.Artifacts[1]
	beforeArgs := fmt.Sprintf(`{"artifact_id":%q}`, beforeRef.ArtifactID)
	metadata := callTool(t, session, inspectBundle, beforeArgs)
	var bundleInventory bundle.Inventory
	convertStructured(t, metadata, &bundleInventory)
	if metadata.IsError || bundleInventory.Scope != before.Scope || bundleInventory.Integrity == "" {
		t.Fatalf("bundle metadata = %s", resultJSON(t, metadata))
	}
	for i, expected := range []investigation.Investigation{before, after} {
		response := callTool(t, session, analyzeBundle, fmt.Sprintf(`{"artifact_id":%q}`, artifacts.Artifacts[i].ArtifactID))
		var view investigation.Investigation
		convertStructured(t, response, &view)
		if response.IsError || !reflect.DeepEqual(view, expected) {
			t.Fatalf("MCP and CLI investigations differ for capture %d: %.1000s", i, resultJSON(t, response))
		}
	}
	if report := decodeReport(t, callTool(t, session, getEvidence, beforeArgs)); !reflect.DeepEqual(report, before.Report) {
		t.Fatal("MCP evidence differs from the CLI investigation report")
	}
	relationships := callTool(t, session, getRelationships, beforeArgs)
	var relationshipView relationshipResult
	convertStructured(t, relationships, &relationshipView)
	if relationships.IsError || !reflect.DeepEqual(relationshipView.Relationships, before.Relationships) {
		t.Fatalf("MCP relationships differ from the CLI investigation: %.1000s", resultJSON(t, relationships))
	}

	compareArgs := fmt.Sprintf(`{"before_id":%q,"after_id":%q}`, beforeRef.ArtifactID, afterRef.ArtifactID)
	result := callTool(t, session, compareCaptures, compareArgs)
	var comparison investigation.Comparison
	convertStructured(t, result, &comparison)
	if result.IsError || !reflect.DeepEqual(comparison, investigation.Compare(before, after)) {
		t.Fatalf("MCP comparison differs from the shared model: %.1000s", resultJSON(t, result))
	}
	if !comparison.Compatible || !slices.ContainsFunc(comparison.ResourceChanges, func(change investigation.Change) bool {
		return change.RuleID == "pod-containers" && change.Classification == investigation.Resolved
	}) {
		t.Fatalf("fixture recovery was not established: %+v", comparison.ResourceChanges)
	}

	historyResult := callTool(t, session, historicalTool, fmt.Sprintf(`{"artifact_id":%q}`, artifacts.Artifacts[3].ArtifactID))
	var history historicalResult
	convertStructured(t, historyResult, &history)
	if historyResult.IsError || history.History == nil || history.Scope != before.Scope ||
		len(history.History.Targets) != 1 || history.History.Targets[0].CPU.P95 == nil {
		t.Fatalf("MCP historical data was not retrievable: %.1000s", resultJSON(t, historyResult))
	}
	largeResult := callTool(t, session, analyzeBundle, fmt.Sprintf(`{"artifact_id":%q}`, artifacts.Artifacts[2].ArtifactID))
	if !largeResult.IsError || largeResult.StructuredContent != nil ||
		!bytes.Contains(resultJSON(t, largeResult), []byte("exceeds the MCP size limit")) {
		t.Fatalf("oversized artifact result = %.1000s", resultJSON(t, largeResult))
	}
	if requests.Load() != atStart {
		t.Fatal("offline artifact tools contacted Kubernetes")
	}

	live := callTool(t, session, captureNamespace, `{"namespace":"payments"}`)
	if live.IsError || requests.Load() == atStart {
		t.Fatalf("live capture did not collect evidence: %s", resultJSON(t, live))
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	newSession := startArtifactClient(t, ctx, binary, args)
	var fresh artifactList
	convertStructured(t, callTool(t, newSession, listArtifacts, `{}`), &fresh)
	if len(fresh.Artifacts) != 4 {
		t.Fatalf("new session artifact list = %+v", fresh)
	}
	for _, ids := range [][2]string{
		{beforeRef.ArtifactID, fresh.Artifacts[1].ArtifactID},
		{fresh.Artifacts[0].ArtifactID, afterRef.ArtifactID},
	} {
		stale := callTool(t, newSession, compareCaptures, fmt.Sprintf(`{"before_id":%q,"after_id":%q}`, ids[0], ids[1]))
		if !stale.IsError || stale.StructuredContent != nil {
			t.Fatalf("prior session handle remained usable: %s", resultJSON(t, stale))
		}
	}
	if err := newSession.Close(); err != nil {
		t.Fatal(err)
	}

	frozen := requests.Load()
	wrongContext := append(slices.Clone(args), "--context", "other")
	cmd := exec.CommandContext(ctx, binary, wrongContext...)
	output, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 || !bytes.Contains(output, []byte(errArtifactScope.Error())) ||
		bytes.Contains(output, []byte(dir)) || requests.Load() != frozen {
		t.Fatalf("wrong-context startup = %v, %.500s", err, output)
	}
}

func startArtifactClient(t *testing.T, ctx context.Context, binary string, args []string) *protocol.ClientSession {
	t.Helper()
	cmd := exec.CommandContext(ctx, binary, args...)
	client := protocol.NewClient(&protocol.Implementation{Name: "kubedoctor-artifact-test"}, nil)
	session, err := client.Connect(ctx, &protocol.CommandTransport{
		Command: cmd, TerminateDuration: time.Second,
	}, &protocol.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func captureFixtureFile(t *testing.T, binary, kubeconfig, path string) investigation.Investigation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "capture", "namespace", "payments", "--kubeconfig", kubeconfig,
		"--context", "fixture", "--bundle", path, "--output", "json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fixture capture: %v, %.500s, %.1000s", err, &stderr, &stdout)
	}
	var view investigation.Investigation
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func historyFixture(snapshot model.Snapshot) *model.HistoryCapture {
	end := snapshot.CollectedAt
	start := end.Add(-30 * time.Minute)
	cpu := model.HistorySeries{Status: model.HistoryComplete, Points: []model.HistoryPoint{}}
	memory := model.HistorySeries{Status: model.HistoryComplete, Points: []model.HistoryPoint{}}
	for i := range 31 {
		at := start.Add(time.Duration(i) * time.Minute)
		cpu.Points = append(cpu.Points, model.HistoryPoint{At: at, Value: float64(i * 12)})
		memory.Points = append(memory.Points, model.HistoryPoint{At: at, Value: 128 << 20})
	}
	pod := model.Identity("Pod", &snapshot.Pods[0])
	return &model.HistoryCapture{
		SchemaVersion: model.HistorySchemaVersion, Status: model.HistoryComplete,
		StartedAt: end, FinishedAt: end, WindowStart: start, WindowEnd: end, StepSeconds: 60, ScrapeSeconds: 60,
		Targets: []model.HistoryTargetCapture{{
			Target:     model.HistoryTarget{Pod: pod, Workload: pod, Container: "app", VPADisabled: true},
			CPUCounter: cpu, Memory: memory,
		}},
	}
}

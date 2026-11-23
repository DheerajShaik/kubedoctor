package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fixtureCollector model.Snapshot

func (f fixtureCollector) Collect(_ context.Context, _ model.ScanRequest) model.Snapshot {
	return model.Snapshot(f)
}

func TestReportKeepsPositiveEvidenceFromPartialResult(t *testing.T) {
	snapshot := syntheticCluster(4)
	snapshot.Sources = append(snapshot.Sources, model.SourceResult{
		Source: model.SourceServices, Status: model.PermissionDenied, Required: true, Detail: "Missing read permission.",
	})
	report := New(fixtureCollector(snapshot)).Scan(t.Context(), model.ScanRequest{Scope: snapshot.Scope})
	if report.Complete || ExitCode(report, model.Warning) != 2 || len(report.Findings) == 0 {
		t.Fatalf("expected findings and required permission failure, got %+v", report)
	}
	if len(report.CollectionErrors) != 2 {
		t.Fatalf("missing optional or required error: %+v", report.CollectionErrors)
	}
}

func TestDeterministicReportOrdering(t *testing.T) {
	snapshot := syntheticCluster(10)
	request := model.ScanRequest{Scope: snapshot.Scope}
	before := New(fixtureCollector(snapshot)).Scan(t.Context(), request)
	slices.Reverse(snapshot.Pods)
	slices.Reverse(snapshot.Sources)
	after := New(fixtureCollector(snapshot)).Scan(t.Context(), request)
	left, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("reports differ after reversing inventory\n%s\n%s", left, right)
	}
	if !before.Complete || len(before.CollectionErrors) != 1 ||
		before.CollectionErrors[0].Status != model.Unavailable {
		t.Fatalf("optional metrics visibility policy is wrong: %+v", before.CollectionErrors)
	}
}

func TestCanceledScanIsIncomplete(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	snapshot := syntheticCluster(4)
	report := New(fixtureCollector(snapshot)).Scan(ctx, model.ScanRequest{Scope: snapshot.Scope})
	if report.Complete || len(report.Findings) == 0 {
		t.Fatalf("expected canceled report with available findings, got %+v", report)
	}
}

func BenchmarkSyntheticCluster1000Pods(b *testing.B) {
	snapshot := syntheticCluster(1000)
	engine := New(fixtureCollector(snapshot))
	request := model.ScanRequest{Scope: snapshot.Scope}
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		engine.Scan(ctx, request)
	}
}

func syntheticCluster(podCount int) model.Snapshot {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	snapshot := model.Snapshot{
		Scope: model.Scope{Kind: model.ClusterScope, Context: "synthetic"}, CollectedAt: at,
		Sources: []model.SourceResult{
			{Source: model.SourcePods, Status: model.Complete, Required: true},
			{Source: model.SourceNodes, Status: model.Complete, Required: true},
			{Source: model.SourceMetrics, Status: model.Unavailable},
		},
	}
	for index := range podCount {
		pod := core.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "app-" + strconv.Itoa(index), Namespace: "ns-" + strconv.Itoa(index%20),
				CreationTimestamp: metav1.NewTime(at.Add(-15 * time.Minute)),
			},
			Spec:   core.PodSpec{Containers: []core.Container{{Name: "app"}}},
			Status: core.PodStatus{Phase: core.PodRunning},
		}
		if index%10 == 0 {
			pod.Status.ContainerStatuses = []core.ContainerStatus{{
				Name: "app", RestartCount: 5, State: core.ContainerState{
					Waiting: &core.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
			}}
		}
		snapshot.Pods = append(snapshot.Pods, pod)
	}
	return snapshot
}

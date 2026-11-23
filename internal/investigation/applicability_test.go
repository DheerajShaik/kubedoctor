package investigation

import (
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func addApplicableSources(snapshot *model.Snapshot, sources ...model.Source) {
	for _, source := range sources {
		if snapshot.Coverage(source) != model.Complete {
			snapshot.Sources = append(snapshot.Sources, model.SourceResult{Source: source, Status: model.Complete, Required: true})
			snapshot.SourceTimings = append(snapshot.SourceTimings, model.SourceTiming{
				Source: source, StartedAt: snapshot.StartedAt, FinishedAt: snapshot.FinishedAt,
			})
		}
	}
}

func assertApplicableChange(t *testing.T, before Investigation, after model.Snapshot, rule, uid string, want Classification) {
	t.Helper()
	comparison := Compare(before, Build(after, engine.Analyze(after)))
	change := pickChange(t, comparison.WorkloadChanges, rule, uid)
	if change.Classification != want {
		t.Fatalf("wanted %s, found %+v", want, change)
	}
	if want == NoLongerObservable && comparison.Complete {
		t.Fatal("the comparison gate treated a loss of current evidence as complete")
	}
}

func TestNodePressureNeedsSpecificCurrentConditions(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*core.Node)
		want Classification
	}{
		{"explicit pressure clearance", func(node *core.Node) {
			node.Status.Conditions[1].Status = core.ConditionFalse
		}, Resolved},
		{"duplicate status masks missing condition", func(node *core.Node) {
			node.Status.Conditions[1] = core.NodeCondition{Type: core.NodeDiskPressure, Status: core.ConditionFalse}
		}, NoLongerObservable},
		{"node deleting", func(node *core.Node) {
			node.Status.Conditions[1].Status = core.ConditionFalse
			node.DeletionTimestamp = new(metav1.NewTime(baselineTime))
		}, NoLongerObservable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := eventComparisonSnapshot()
			addApplicableSources(&snapshot, model.SourceNodes)
			snapshot.Nodes = []core.Node{{
				ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "node-uid"},
				Status: core.NodeStatus{Conditions: []core.NodeCondition{
					{Type: core.NodeReady, Status: core.ConditionTrue},
					{Type: core.NodeMemoryPressure, Status: core.ConditionTrue},
					{Type: core.NodeDiskPressure, Status: core.ConditionFalse},
					{Type: core.NodePIDPressure, Status: core.ConditionFalse},
				}},
			}}
			before := Build(snapshot, engine.Analyze(snapshot))
			after := advanceObservation(snapshot, time.Minute)
			tt.edit(&after.Nodes[0])
			assertApplicableChange(t, before, after, "node-pressure", "node-uid", tt.want)
		})
	}
}

func TestNodeLocalDNSNeedsSameCurrentHostingEvidence(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*model.Snapshot)
		want Classification
	}{
		{"cache now Ready on implicated host", func(*model.Snapshot) {}, Resolved},
		{"workloads moved to a different host", func(s *model.Snapshot) {
			s.Pods[0].Spec.NodeName, s.Expansion.NodeLocalPods[0].Node = "other", "other"
		}, NoLongerObservable},
		{"provider scaled to zero desired", func(s *model.Snapshot) {
			s.Expansion.NodeLocalDNS.Desired, s.Expansion.NodeLocalDNS.Ready = 0, 0
		}, NoLongerObservable},
		{"provider deleting", func(s *model.Snapshot) {
			s.Expansion.NodeLocalDNS.Deleting = true
		}, NoLongerObservable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := eventComparisonSnapshot()
			addApplicableSources(&snapshot, model.SourcePods, model.SourceNodeLocalDNS, model.SourceNodeLocalPods)
			snapshot.Pods = []core.Pod{{
				ObjectMeta: fixtureMeta("application", "app-uid"),
				Spec:       core.PodSpec{NodeName: "worker"},
				Status:     core.PodStatus{Phase: core.PodRunning},
			}}
			provider := model.Resource{Kind: "DaemonSet", Namespace: "kube-system", Name: "node-local-dns", UID: "dns-uid"}
			snapshot.Expansion.NodeLocalDNS = &model.NodeLocalDNSEvidence{
				Resource: provider, Generation: 1, ObservedGeneration: 1, Desired: 1,
			}
			snapshot.Expansion.NodeLocalPods = []model.NodeLocalPodEvidence{{
				Pod:   model.Resource{Kind: "Pod", Namespace: "kube-system", Name: "cache", UID: "cache-uid"},
				Owner: &provider, Node: "worker", Ready: "False",
			}}
			before := Build(snapshot, engine.Analyze(snapshot))
			after := advanceObservation(snapshot, time.Minute)
			after.Expansion.NodeLocalPods[0].Ready = "True"
			after.Expansion.NodeLocalDNS.Ready = 1
			tt.edit(&after)
			assertApplicableChange(t, before, after, "node-local-dns", "dns-uid", tt.want)
		})
	}
}

func TestHPAInapplicabilityCannotResolveScalingFailure(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*model.HPAEvidence)
		want Classification
	}{
		{"scaling becomes active", func(*model.HPAEvidence) {}, Resolved},
		{"deleted HPA still visible", func(hpa *model.HPAEvidence) { hpa.Deleting = true }, NoLongerObservable},
		{"scaling explicitly disabled", func(hpa *model.HPAEvidence) {
			hpa.Conditions[1].Status, hpa.Conditions[1].Reason = "False", "ScalingDisabled"
		}, NoLongerObservable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, _ := fixtureSnapshot(baselineTime, true, "pod-uid")
			addApplicableSources(&snapshot, model.SourceHPAs, model.SourceStatefulSets)
			snapshot.Expansion.HPAs = []model.HPAEvidence{{
				Resource: model.Resource{Kind: "HorizontalPodAutoscaler", Namespace: "demo", Name: "checkout", UID: "hpa-uid"},
				Target:   model.Resource{Kind: "Deployment", Namespace: "demo", Name: "checkout"}, TargetAPIVersion: "apps/v1",
				Generation: 1, ObservedGeneration: 1,
				Conditions: []model.BriefCondition{{Type: "AbleToScale", Status: "True"},
					{Type: "ScalingActive", Status: "False", Reason: "FailedGetResourceMetric"}},
			}}
			before := Build(snapshot, engine.Analyze(snapshot))
			after := advanceObservation(snapshot, time.Minute)
			after.Expansion.HPAs[0].Conditions[1].Status, after.Expansion.HPAs[0].Conditions[1].Reason = "True", "ValidMetricFound"
			tt.edit(&after.Expansion.HPAs[0])
			assertApplicableChange(t, before, after, "hpa-scaling", "hpa-uid", tt.want)
		})
	}
}

func TestDeletingCronJobIsNotResumed(t *testing.T) {
	snapshot := eventComparisonSnapshot()
	id := model.Resource{Kind: "CronJob", Namespace: "demo", Name: "nightly", UID: "cron-uid"}
	snapshot.Expansion.CronJobs = []model.CronJobEvidence{{Resource: id, Created: baselineTime.Add(-time.Hour), Suspended: true}}
	before := Build(snapshot, engine.Analyze(snapshot))
	after := advanceObservation(snapshot, time.Minute)
	after.Expansion.CronJobs[0].Deleting = true
	assertApplicableChange(t, before, after, "cronjob-scheduling", "cron-uid", NoLongerObservable)
}

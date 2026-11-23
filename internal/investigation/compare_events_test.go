package investigation

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func eventComparisonSnapshot() model.Snapshot {
	snapshot, _ := fixtureSnapshot(baselineTime, true, "pod-uid-a")
	snapshot.Pods, snapshot.Deployments, snapshot.ReplicaSets = nil, nil, nil
	for _, source := range []model.Source{model.SourceDaemonSets, model.SourceCronJobs, model.SourceJobs, model.SourceQuotas, model.SourceLimitRanges} {
		snapshot.Sources = append(snapshot.Sources, model.SourceResult{Source: source, Status: model.Complete})
		snapshot.SourceTimings = append(snapshot.SourceTimings, model.SourceTiming{Source: source, StartedAt: snapshot.StartedAt, FinishedAt: snapshot.FinishedAt})
	}
	return snapshot
}

func advanceObservation(snapshot model.Snapshot, elapsed time.Duration) model.Snapshot {
	snapshot.StartedAt = snapshot.StartedAt.Add(elapsed)
	snapshot.FinishedAt = snapshot.FinishedAt.Add(elapsed)
	snapshot.CollectedAt = snapshot.CollectedAt.Add(elapsed)
	snapshot.SourceTimings = slices.Clone(snapshot.SourceTimings)
	for i, timing := range snapshot.SourceTimings {
		snapshot.SourceTimings[i].StartedAt = timing.StartedAt.Add(elapsed)
		snapshot.SourceTimings[i].FinishedAt = timing.FinishedAt.Add(elapsed)
	}
	return snapshot
}

func compareAnalyzed(before, after model.Snapshot) Comparison {
	return Compare(Build(before, engine.Analyze(before)), Build(after, engine.Analyze(after)))
}

func requireHistoricalDisappearance(t *testing.T, result Comparison, rule, uid string) {
	t.Helper()
	if !result.Compatible || !result.Complete {
		t.Fatalf("ordinary observation expiry should retain comparable coverage: %+v", result.Limitations)
	}
	for _, changes := range [][]Change{result.ResourceChanges, result.WorkloadChanges} {
		change := pickChange(t, changes, rule, uid)
		if change.Classification != NoLongerObservable || !strings.Contains(change.Reason, "historical occurrence") || len(change.Before) != 1 || len(change.After) != 0 {
			t.Fatalf("expired observation must remain historical: %+v", change)
		}
	}
}

func quotaAtHardValue() model.QuotaEvidence {
	return model.QuotaEvidence{
		Resource:   model.Resource{Kind: "ResourceQuota", Namespace: "demo", Name: "pod-cap", UID: "quota-uid"},
		Created:    baselineTime.Add(-time.Hour),
		Capacities: []model.QuotaCapacity{{Resource: "pods", Hard: "2", Used: "2", Current: true}},
	}
}

func TestAdmissionEventExpiryDoesNotResolveDaemonSetDeficit(t *testing.T) {
	snapshot := eventComparisonSnapshot()
	daemon := apps.DaemonSet{ObjectMeta: fixtureMeta("node-agent", "daemon-uid"), Status: apps.DaemonSetStatus{
		DesiredNumberScheduled: 2, NumberAvailable: 1, NumberReady: 1, UpdatedNumberScheduled: 1, ObservedGeneration: 1,
	}}
	snapshot.DaemonSets = []apps.DaemonSet{daemon}
	quota := quotaAtHardValue()
	snapshot.Expansion.Quotas = []model.QuotaEvidence{quota}
	snapshot.Expansion.AdmissionEvents = []model.AdmissionEventEvidence{{
		Event:      model.TimedReference{Resource: model.Identity("DaemonSet", &daemon), Time: baselineTime.Add(-time.Minute), Count: 2},
		Constraint: "quota_exceeded", Quota: quota.Resource.Name,
	}}

	// The same UID, generation, deficit, quota, and event are still visible.
	// The reevaluation time alone moves the event outside the analyzer window.
	result := compareAnalyzed(snapshot, advanceObservation(snapshot, time.Hour))
	requireHistoricalDisappearance(t, result, "namespace-admission", "daemon-uid")
	for _, check := range []struct{ rule, uid string }{{"daemonset-rollout", "daemon-uid"}, {"namespace-admission", "quota-uid"}} {
		if change := pickChange(t, result.WorkloadChanges, check.rule, check.uid); change.Classification != Persistent {
			t.Fatalf("the ongoing current condition should persist: %+v", change)
		}
	}
}

func TestCronJobHistoryExpiryIsNotRecovery(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		setup func(*model.Snapshot, model.Resource)
	}{
		{"controller event", func(snapshot *model.Snapshot, cron model.Resource) {
			snapshot.Events = []model.Event{{Resource: cron, Reason: "MissSchedule", Time: baselineTime.Add(-time.Minute), Count: 1}}
		}},
		{"failed owned job", func(snapshot *model.Snapshot, cron model.Resource) {
			snapshot.Expansion.Jobs = []model.JobEvidence{{
				Resource: model.Resource{Kind: "Job", Namespace: "demo", Name: "nightly-01", UID: "job-uid"},
				Owner:    &cron, Created: baselineTime.Add(-20 * time.Minute),
				Conditions: []model.BriefCondition{{Type: "Failed", Status: "True", Transition: baselineTime.Add(-time.Minute)}},
			}}
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			snapshot := eventComparisonSnapshot()
			cron := model.Resource{Kind: "CronJob", Namespace: "demo", Name: "nightly", UID: "cron-uid"}
			snapshot.Expansion.CronJobs = []model.CronJobEvidence{{Resource: cron, Created: baselineTime.Add(-time.Hour), LastScheduled: baselineTime.Add(-20 * time.Minute)}}
			scenario.setup(&snapshot, cron)
			result := compareAnalyzed(snapshot, advanceObservation(snapshot, time.Hour))
			requireHistoricalDisappearance(t, result, "cronjob-scheduling", "cron-uid")
		})
	}
}

func TestProbeEventExpiryIsNotRecovery(t *testing.T) {
	snapshot := eventComparisonSnapshot()
	pod := core.Pod{ObjectMeta: fixtureMeta("probe-pod", "probe-pod-uid"), Status: core.PodStatus{
		Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionFalse}},
		ContainerStatuses: []core.ContainerStatus{{Name: "app", State: core.ContainerState{
			Running: &core.ContainerStateRunning{StartedAt: metav1.NewTime(baselineTime.Add(-20 * time.Minute))},
		}}},
	}}
	id := model.Identity("Pod", &pod)
	snapshot.Pods = []core.Pod{pod}
	snapshot.Expansion.Probes = []model.PodProbeEvidence{{Pod: id, Containers: []model.ContainerProbe{{Name: "app", Role: "application", Kinds: []string{"readiness"}}}}}
	snapshot.Expansion.ProbeEvents = []model.ProbeEventEvidence{{
		Event:     model.TimedReference{Resource: id, Time: baselineTime.Add(-time.Minute), Count: 1},
		Container: "app", Role: "application", Kind: "readiness",
	}}
	result := compareAnalyzed(snapshot, advanceObservation(snapshot, 15*time.Minute))
	requireHistoricalDisappearance(t, result, "rollout-probes", "probe-pod-uid")
}

func TestEventExpiryPolicyKeepsDirectCurrentConditionRecovery(t *testing.T) {
	snapshot := eventComparisonSnapshot()
	cron := model.Resource{Kind: "CronJob", Namespace: "demo", Name: "nightly", UID: "cron-uid"}
	snapshot.Expansion.CronJobs = []model.CronJobEvidence{{Resource: cron, Created: baselineTime.Add(-time.Hour), Suspended: true}}
	snapshot.Expansion.Quotas = []model.QuotaEvidence{quotaAtHardValue()}
	before := Build(snapshot, engine.Analyze(snapshot))
	snapshot = advanceObservation(snapshot, time.Hour)
	snapshot.Expansion.CronJobs[0].Suspended = false
	snapshot.Expansion.Quotas[0].Capacities[0].Used = "1"
	result := Compare(before, Build(snapshot, engine.Analyze(snapshot)))
	for _, check := range []struct{ rule, uid string }{{"cronjob-scheduling", "cron-uid"}, {"namespace-admission", "quota-uid"}} {
		if change := pickChange(t, result.WorkloadChanges, check.rule, check.uid); change.Classification != Resolved {
			t.Fatalf("a direct positive current observation should still establish recovery: %+v", change)
		}
	}
}

func TestComparisonSchedulingSeparatesEventsFromAnObservedCondition(t *testing.T) {
	for _, conditionRecorded := range []bool{false, true} {
		name, want := "event only", NoLongerObservable
		if conditionRecorded {
			name, want = "event corroborates current condition", Resolved
		}
		t.Run(name, func(t *testing.T) {
			snapshot := eventComparisonSnapshot()
			pod := core.Pod{ObjectMeta: fixtureMeta("api-pod", "pod-uid"), Status: core.PodStatus{Phase: core.PodPending}}
			if conditionRecorded {
				pod.Status.Conditions = []core.PodCondition{{Type: core.PodScheduled, Status: core.ConditionFalse, Reason: core.PodReasonUnschedulable}}
			}
			snapshot.Pods = []core.Pod{pod}
			snapshot.Events = []model.Event{{Resource: model.Identity("Pod", &pod), Reason: "FailedScheduling", Time: baselineTime.Add(-time.Minute)}}
			before := Build(snapshot, engine.Analyze(snapshot))
			snapshot = advanceObservation(snapshot, time.Hour)
			pod.Spec.NodeName = "fixture-worker"
			pod.Status = core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{
				{Type: core.PodScheduled, Status: core.ConditionTrue}, {Type: core.PodReady, Status: core.ConditionTrue},
			}}
			snapshot.Pods = []core.Pod{pod}
			result := Compare(before, Build(snapshot, engine.Analyze(snapshot)))
			change := pickChange(t, result.ResourceChanges, "pod-scheduling", "pod-uid")
			if !result.Complete || change.Classification != want {
				t.Fatalf("unexpected classification for scheduler observation: %+v", change)
			}
		})
	}
}

func TestComparisonCurrentTerminationIsSeparateFromEarlierOOM(t *testing.T) {
	snapshot := eventComparisonSnapshot()
	finished := metav1.NewTime(baselineTime.Add(-time.Minute))
	pod := core.Pod{ObjectMeta: fixtureMeta("api-pod", "pod-uid"), Status: core.PodStatus{
		Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionFalse}},
		ContainerStatuses: []core.ContainerStatus{{Name: "app",
			State: core.ContainerState{Terminated: &core.ContainerStateTerminated{ExitCode: 137, FinishedAt: finished}},
			LastTerminationState: core.ContainerState{Terminated: &core.ContainerStateTerminated{
				Reason: "OOMKilled", FinishedAt: metav1.NewTime(baselineTime.Add(-10 * time.Minute)),
			}},
		}},
	}}
	snapshot.Pods = []core.Pod{pod}
	before := Build(snapshot, engine.Analyze(snapshot))
	snapshot = advanceObservation(snapshot, time.Hour)
	pod.Status = core.PodStatus{Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}},
		ContainerStatuses: []core.ContainerStatus{{Name: "app", Ready: true, State: core.ContainerState{Running: &core.ContainerStateRunning{StartedAt: finished}}}},
	}
	snapshot.Pods = []core.Pod{pod}
	result := Compare(before, Build(snapshot, engine.Analyze(snapshot)))
	change := pickChange(t, result.WorkloadChanges, "pod-containers", "pod-uid")
	if !result.Complete || change.Classification != Resolved || evidenceValue(change.Before[0].Finding, "termination_reason") != "" {
		t.Fatalf("the named historical OOM must remain separate from the now-recovered current exit with an unknown cause: %+v", change)
	}
}

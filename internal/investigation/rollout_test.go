package investigation

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func requireRolloutComparison(t *testing.T, result Comparison, rule, uid, reason string) {
	t.Helper()
	if !result.Compatible {
		t.Fatalf("incompatible fixture: %+v", result.Limitations)
	}
	want, exit := Resolved, 0
	if reason != "" {
		want, exit = NoLongerObservable, 2
	}
	for _, changes := range [][]Change{result.ResourceChanges, result.WorkloadChanges} {
		change := pickChange(t, changes, rule, uid)
		if change.Classification != want || len(change.Before) != 1 || len(change.After) != 0 || !strings.Contains(change.Reason, reason) {
			t.Fatalf("unexpected %s change: %+v", rule, change)
		}
	}
	if code := ExitCode(result, model.Warning); code != exit {
		t.Fatalf("comparison gate returned %d, expected %d", code, exit)
	}
}

func statefulPods(set model.StatefulSetEvidence, ready int32) []core.Pod {
	pods := make([]core.Pod, set.Desired)
	for i := range pods {
		ordinal := strconv.Itoa(int(set.StartOrdinal) + i)
		desiredName := set.Resource.Name + "-" + ordinal
		meta := fixtureMeta(desiredName, "pod-uid-"+ordinal)
		meta.OwnerReferences = []metav1.OwnerReference{{Kind: "StatefulSet", Name: set.Resource.Name, UID: "set-uid", Controller: new(true)}}
		isReady := core.ConditionFalse
		if int32(i) < ready {
			isReady = core.ConditionTrue
		}
		pods[i] = core.Pod{ObjectMeta: meta, Spec: core.PodSpec{NodeName: "fixture-worker"}, Status: core.PodStatus{
			Phase: core.PodRunning, Conditions: []core.PodCondition{{Type: core.PodReady, Status: isReady}},
		}}
	}
	return pods
}

func TestStatefulSetComparisonRequiresCurrentStrategyAndOrdinalReadiness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strategy string
		modify   func(*model.StatefulSetEvidence, []core.Pod, time.Time)
		reason   string
	}{
		{"replacement ordinal persists in startup grace", "RollingUpdate", func(set *model.StatefulSetEvidence, pods []core.Pod, at time.Time) {
			set.Ready, set.Available, set.Updated = 1, 1, 1
			pods[1].UID = "replacement-uid"
			pods[1].CreationTimestamp = metav1.NewTime(at.Add(-time.Minute))
			pods[1].Status.Conditions[0].Status = core.ConditionFalse
			pods[2].Status.Conditions[0].Status = core.ConditionFalse
		}, "1 Ready and 1 available replicas out of 3"},
		{"complete partitioned rollout with nonzero start ordinal", "RollingUpdate", func(set *model.StatefulSetEvidence, _ []core.Pod, _ time.Time) {
			set.Updated = 2
		}, ""},
		{"partition exemption alone", "RollingUpdate", func(set *model.StatefulSetEvidence, _ []core.Pod, _ time.Time) {
			set.Partition, set.Updated = 3, 1
		}, "earlier rollout required 2 updated"},
		{"switching to OnDelete with outstanding updates", "RollingUpdate", func(set *model.StatefulSetEvidence, _ []core.Pod, _ time.Time) {
			set.Strategy, set.Updated = "OnDelete", 1
		}, "earlier rollout required 2 updated"},
		{"existing OnDelete availability repaired", "OnDelete", func(set *model.StatefulSetEvidence, _ []core.Pod, _ time.Time) {
			set.Updated = 0
		}, ""},
		{"unowned or out-of-range Ready pod cannot replace an expected ordinal", "RollingUpdate", func(_ *model.StatefulSetEvidence, pods []core.Pod, _ time.Time) {
			pods[2].Name = "db-10"
		}, "Only 2 of 3 expected StatefulSet ordinals"},
		{"scaled to zero", "RollingUpdate", func(set *model.StatefulSetEvidence, _ []core.Pod, _ time.Time) {
			set.Desired = 0
		}, "zero desired replicas"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := eventComparisonSnapshot()
			before.Sources = append(before.Sources, model.SourceResult{Source: model.SourceStatefulSets, Status: model.Complete})
			before.SourceTimings = append(before.SourceTimings, model.SourceTiming{Source: model.SourceStatefulSets, StartedAt: before.StartedAt, FinishedAt: before.FinishedAt})
			set := model.StatefulSetEvidence{
				Resource: model.Resource{Kind: "StatefulSet", Namespace: "demo", Name: "db", UID: "set-uid"},
				Created:  baselineTime.Add(-time.Hour), Generation: 1, ObservedGeneration: 1,
				Desired: 3, Ready: 1, Available: 1, Updated: 1, StartOrdinal: 7, Partition: 1,
				Management: "OrderedReady", Strategy: tc.strategy, RevisionState: "different",
			}
			before.Pods = statefulPods(set, 1)
			before.Expansion.StatefulSets = []model.StatefulSetEvidence{set}
			after := advanceObservation(before, time.Hour)
			set.Ready, set.Available, set.Updated = 3, 3, 3
			after.Pods = statefulPods(set, 3)
			tc.modify(&set, after.Pods, after.CollectedAt)
			after.Expansion.StatefulSets = []model.StatefulSetEvidence{set}
			requireRolloutComparison(t, compareAnalyzed(before, after), "statefulset-rollout", "set-uid", tc.reason)
		})
	}
}

func TestDeploymentComparisonRequiresUnpausedCompleteRollout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*apps.Deployment, time.Time)
		reason string
	}{
		{"paused with a partial rollout", func(deployment *apps.Deployment, _ time.Time) {
			deployment.Spec.Paused = true
			deployment.Status.ReadyReplicas, deployment.Status.AvailableReplicas = 1, 1
		}, "Deployment is paused"},
		{"controller is within progress deadline", func(deployment *apps.Deployment, at time.Time) {
			deployment.Status.AvailableReplicas = 1
			deployment.Status.Conditions[0].LastUpdateTime = metav1.NewTime(at.Add(-time.Minute))
		}, "3 Ready and 1 available replicas out of 3"},
		{"available count alone does not prove updates", func(deployment *apps.Deployment, at time.Time) {
			deployment.Status.UpdatedReplicas = 1
			deployment.Status.Conditions[0].LastUpdateTime = metav1.NewTime(at.Add(-time.Minute))
		}, "1 updated replicas against a rollout target of 3"},
		{"available and updated counts alone do not prove readiness", func(deployment *apps.Deployment, _ time.Time) {
			deployment.Status.ReadyReplicas = 1
		}, "1 Ready and 3 available replicas out of 3"},
		{"scaled to zero", func(deployment *apps.Deployment, _ time.Time) { deployment.Spec.Replicas = new(int32(0)) }, "zero desired replicas"},
		{"current generation fully rolled out", func(*apps.Deployment, time.Time) {}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := eventComparisonSnapshot()
			deployment := apps.Deployment{ObjectMeta: fixtureMeta("api", "deployment-uid"), Spec: apps.DeploymentSpec{Replicas: new(int32(3))}, Status: apps.DeploymentStatus{
				ObservedGeneration: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1,
				Conditions: []apps.DeploymentCondition{{Type: apps.DeploymentProgressing, Status: core.ConditionTrue, LastUpdateTime: metav1.NewTime(baselineTime.Add(-30 * time.Minute))}},
			}}
			before.Deployments = []apps.Deployment{*deployment.DeepCopy()}
			after := advanceObservation(before, time.Hour)
			deployment.Status.ReadyReplicas, deployment.Status.AvailableReplicas, deployment.Status.UpdatedReplicas = 3, 3, 3
			tc.modify(&deployment, after.CollectedAt)
			after.Deployments = []apps.Deployment{deployment}
			requireRolloutComparison(t, compareAnalyzed(before, after), "deployment-rollout", "deployment-uid", tc.reason)
		})
	}
}

func TestDeploymentComparisonRequiresEarlierProgressDeadlineConditionToClear(t *testing.T) {
	for _, clearDeadline := range []bool{false, true} {
		before := eventComparisonSnapshot()
		deployment := apps.Deployment{ObjectMeta: fixtureMeta("api", "deployment-uid"), Status: apps.DeploymentStatus{
			ObservedGeneration: 1, Conditions: []apps.DeploymentCondition{{
				Type: apps.DeploymentProgressing, Status: core.ConditionFalse, Reason: "ProgressDeadlineExceeded",
			}},
		}}
		before.Deployments = []apps.Deployment{*deployment.DeepCopy()}
		after := advanceObservation(before, time.Hour)
		deployment.Status.ReadyReplicas, deployment.Status.AvailableReplicas, deployment.Status.UpdatedReplicas = 1, 1, 1
		reason := "still reports ProgressDeadlineExceeded"
		if clearDeadline {
			deployment.Status.Conditions[0].Status, deployment.Status.Conditions[0].Reason = core.ConditionTrue, "NewReplicaSetAvailable"
			reason = ""
		}
		after.Deployments = []apps.Deployment{deployment}
		requireRolloutComparison(t, compareAnalyzed(before, after), "deployment-rollout", "deployment-uid", reason)
	}
}

func TestStatefulSetComparisonRequiresCitedClaimToBeBound(t *testing.T) {
	for _, bound := range []bool{false, true} {
		before := eventComparisonSnapshot()
		before.Sources = append(before.Sources, model.SourceResult{Source: model.SourceStatefulSets, Status: model.Complete})
		before.SourceTimings = append(before.SourceTimings, model.SourceTiming{Source: model.SourceStatefulSets, StartedAt: before.StartedAt, FinishedAt: before.FinishedAt})
		set := model.StatefulSetEvidence{
			Resource: model.Resource{Kind: "StatefulSet", Namespace: "demo", Name: "db", UID: "set-uid"},
			Created:  baselineTime.Add(-time.Hour), Generation: 1, ObservedGeneration: 1,
			Desired: 1, Management: "OrderedReady", Strategy: "OnDelete", VolumeClaimTemplates: []string{"data"},
		}
		claim := core.PersistentVolumeClaim{ObjectMeta: fixtureMeta("data-db-0", "claim-uid"), Status: core.PersistentVolumeClaimStatus{Phase: core.ClaimLost}}
		before.Pods = statefulPods(set, 0)
		before.Pods[0].Spec.Volumes = []core.Volume{{Name: "data", VolumeSource: core.VolumeSource{
			PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: claim.Name},
		}}}
		before.PVCs = []core.PersistentVolumeClaim{claim}
		before.Expansion.StatefulSets = []model.StatefulSetEvidence{set}
		after := advanceObservation(before, time.Hour)
		set.Ready, set.Available = 1, 1
		after.Expansion.StatefulSets = []model.StatefulSetEvidence{set}
		after.Pods = statefulPods(set, 1)
		after.Pods[0].Spec.Volumes = before.Pods[0].Spec.Volumes
		reason := "previously implicated claim data-db-0 lacks current Bound evidence"
		if bound {
			claim.Status.Phase = core.ClaimBound
			reason = ""
		}
		after.PVCs = []core.PersistentVolumeClaim{claim}
		requireRolloutComparison(t, compareAnalyzed(before, after), "statefulset-rollout", "set-uid", reason)
	}
}

func TestDaemonSetComparisonRequiresActiveAdequateCurrentRollout(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strategy apps.DaemonSetUpdateStrategyType
		modify   func(*apps.DaemonSet, time.Time)
		reason   string
	}{
		{"deleting controller", apps.RollingUpdateDaemonSetStrategyType, func(daemon *apps.DaemonSet, at time.Time) {
			daemon.DeletionTimestamp = new(metav1.NewTime(at.Add(-time.Minute)))
		}, "controller is being deleted"},
		{"one of three Pods ready", apps.RollingUpdateDaemonSetStrategyType, func(daemon *apps.DaemonSet, _ time.Time) {
			daemon.Status.NumberReady = 1
		}, "1 Ready and 3 available replicas out of 3"},
		{"OnDelete switched before finishing prior rollout", apps.RollingUpdateDaemonSetStrategyType, func(daemon *apps.DaemonSet, _ time.Time) {
			daemon.Spec.UpdateStrategy.Type, daemon.Status.UpdatedNumberScheduled = apps.OnDeleteDaemonSetStrategyType, 1
		}, "earlier rollout required 3 updated"},
		{"same OnDelete strategy remains applicable", apps.OnDeleteDaemonSetStrategyType, func(daemon *apps.DaemonSet, _ time.Time) {
			daemon.Status.UpdatedNumberScheduled = 0
		}, ""},
		{"current generation fully rolled out", apps.RollingUpdateDaemonSetStrategyType, func(*apps.DaemonSet, time.Time) {}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := eventComparisonSnapshot()
			daemon := apps.DaemonSet{ObjectMeta: fixtureMeta("agent", "daemon-uid"), Spec: apps.DaemonSetSpec{
				UpdateStrategy: apps.DaemonSetUpdateStrategy{Type: tc.strategy},
			}, Status: apps.DaemonSetStatus{
				ObservedGeneration: 1, DesiredNumberScheduled: 3, NumberReady: 1, NumberAvailable: 1, UpdatedNumberScheduled: 1,
			}}
			before.DaemonSets = []apps.DaemonSet{*daemon.DeepCopy()}
			after := advanceObservation(before, time.Hour)
			daemon.Status.NumberReady, daemon.Status.NumberAvailable, daemon.Status.UpdatedNumberScheduled = 3, 3, 3
			tc.modify(&daemon, after.CollectedAt)
			after.DaemonSets = []apps.DaemonSet{daemon}
			requireRolloutComparison(t, compareAnalyzed(before, after), "daemonset-rollout", "daemon-uid", tc.reason)
		})
	}
}

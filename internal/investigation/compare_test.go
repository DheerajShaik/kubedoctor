package investigation

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
)

func repairedFixture(beforeReady, afterReady bool) (Investigation, Investigation) {
	snapshot, report := fixtureSnapshot(baselineTime, beforeReady, "pod-uid-a")
	before := Build(snapshot, report)
	snapshot, report = fixtureSnapshot(baselineTime.Add(time.Minute), afterReady, "pod-uid-a")
	after := Build(snapshot, report)
	return before, after
}

func pickChange(t *testing.T, changes []Change, rule, uid string) Change {
	t.Helper()
	for _, change := range changes {
		if change.RuleID == rule && change.Subject.UID == uid {
			return change
		}
	}
	t.Fatalf("change for rule %s UID %s missing from %+v", rule, uid, changes)
	return Change{}
}

func TestComparisonVerifiedRepair(t *testing.T) {
	before, after := repairedFixture(false, true)
	after.Scope.Context, after.Report.Scope.Context = "renamed-context", "renamed-context"
	// History enrichment can finish after Kubernetes evaluation. It stays
	// within the capture interval and does not move age-sensitive rule time.
	after.CaptureCompletedAt = after.CaptureCompletedAt.Add(5 * time.Second)
	comparison := Compare(before, after)
	if !comparison.Compatible || !comparison.Complete || ExitCode(comparison, model.Warning) != 0 {
		t.Fatalf("expected a complete successful gate, got %+v", comparison)
	}
	for _, level := range []struct {
		changes []Change
		uid     string
	}{{comparison.ResourceChanges, "pod-uid-a"}, {comparison.WorkloadChanges, "deployment-uid"}} {
		for _, rule := range []string{"configuration-references", "pod-containers"} {
			change := pickChange(t, level.changes, rule, level.uid)
			if change.Classification != Resolved || len(change.Before) != 1 || len(change.After) != 0 {
				t.Fatalf("expected repaired %s: %+v", rule, change)
			}
		}
	}
	if change := pickChange(t, comparison.WorkloadChanges, "deployment-rollout", "deployment-uid"); change.Classification != Resolved {
		t.Fatalf("expected verified rollout recovery: %+v", change)
	}
}

func TestComparisonVisibilityLossNeverResolves(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		modify func(*Investigation)
	}{
		{"config forbidden", func(inv *Investigation) {
			for i := range inv.Report.Sources {
				if inv.Report.Sources[i].Source == model.SourceConfigMaps {
					inv.Report.Sources[i].Status = model.PermissionDenied
				}
			}
		}},
		{"check unknown", func(inv *Investigation) { inv.Report.Checks[0].Status = model.Inconclusive }},
		{"stale named evidence beside complete source", func(inv *Investigation) {
			inv.Report.Sources = append(inv.Report.Sources, model.SourceResult{
				Source: model.SourceConfigMaps, Resource: "checkout-dependency", Status: model.Coverage("stale"),
			})
		}},
		{"source timing absent", func(inv *Investigation) {
			inv.SourceTimings = slices.DeleteFunc(inv.SourceTimings, func(timing model.SourceTiming) bool { return timing.Source == model.SourceConfigMaps })
		}},
		{"bad timing interval", func(inv *Investigation) {
			for i := range inv.SourceTimings {
				if inv.SourceTimings[i].Source == model.SourceConfigMaps {
					inv.SourceTimings[i].FinishedAt = inv.CaptureCompletedAt.Add(time.Hour)
				}
			}
		}},
		{"pod status stale", func(inv *Investigation) {
			for i := range inv.Resources {
				if inv.Resources[i].Resource.Kind == "Pod" {
					inv.Resources[i].StatusFresh = false
				}
			}
		}},
		{"active population smaller than desired", func(inv *Investigation) {
			for i := range inv.Resources {
				if inv.Resources[i].Resource.Kind == "Deployment" {
					inv.Resources[i].ExpectedPods = new(int32(2))
				}
			}
		}},
		{"analysis truncated", func(inv *Investigation) { inv.Truncated = true }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			before, after := repairedFixture(false, true)
			scenario.modify(&after)
			comparison := Compare(before, after)
			if !comparison.Compatible || comparison.Complete || ExitCode(comparison, model.Warning) != 2 {
				t.Fatalf("visibility loss should gate as incomplete: %+v", comparison)
			}
			change := pickChange(t, comparison.WorkloadChanges, "configuration-references", "deployment-uid")
			if change.Classification != NoLongerObservable {
				t.Fatalf("visibility loss appeared to repair a finding: %+v", change)
			}
			if scenario.name == "config forbidden" && !slices.ContainsFunc(comparison.CoverageChanges, func(change CoverageChange) bool {
				return change.Source == model.SourceConfigMaps && change.Before == model.Complete && change.After == model.PermissionDenied
			}) {
				t.Fatal("missing explicit ConfigMap coverage change")
			}
		})
	}
}

func TestComparisonRejectsIncompatibleObservations(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		modify func(*Investigation)
	}{
		{"source context alone", func(inv *Investigation) { inv.ClusterID = "" }},
		{"different strong cluster", func(inv *Investigation) { inv.ClusterID = strings.Repeat("b", 64) }},
		{"different provenance methods", func(inv *Investigation) { inv.ClusterProvenance = model.KubeSystemUIDProvenance }},
		{"different scope", func(inv *Investigation) { inv.Scope.Namespace = "other" }},
		{"different rules", func(inv *Investigation) { inv.RuleVersion = "future" }},
		{"unknown schema", func(inv *Investigation) { inv.SchemaVersion = "future" }},
		{"overlapping captures", func(inv *Investigation) { inv.CaptureStartedAt = baselineTime.Add(-time.Second) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			before, after := repairedFixture(false, true)
			scenario.modify(&after)
			comparison := Compare(before, after)
			if comparison.Compatible || comparison.Complete || ExitCode(comparison, model.Warning) != 2 || len(comparison.Limitations) == 0 {
				t.Fatalf("incompatible captures appeared comparable: %+v", comparison)
			}
			for _, change := range comparison.WorkloadChanges {
				if change.Classification != NotComparable {
					t.Fatalf("unexpected classification for incompatible captures: %+v", change)
				}
			}
		})
	}
}

func TestComparisonKeepsReplicaOccurrencesSeparate(t *testing.T) {
	beforeSnapshot, beforeReport := fixtureSnapshot(baselineTime, false, "pod-uid-a")
	afterSnapshot, afterReport := fixtureSnapshot(baselineTime.Add(time.Minute), false, "pod-uid-b")
	for _, item := range []struct {
		snapshot *model.Snapshot
		report   *model.Report
		uid      string
	}{{&beforeSnapshot, &beforeReport, "second-before-uid"}, {&afterSnapshot, &afterReport, "second-after-uid"}} {
		pod := *item.snapshot.Pods[0].DeepCopy()
		pod.Name = "other-pod"
		pod.UID = fixtureMeta("other-pod", item.uid).UID
		item.snapshot.Pods = append(item.snapshot.Pods, pod)
		finding := item.report.Findings[0]
		finding.Resource = model.Identity("Pod", &pod)
		item.report.Findings = append(item.report.Findings, finding)
	}
	comparison := Compare(Build(beforeSnapshot, beforeReport), Build(afterSnapshot, afterReport))
	change := pickChange(t, comparison.WorkloadChanges, "configuration-references", "deployment-uid")
	if !comparison.Complete || change.Classification != Persistent || len(change.Before) != 2 || len(change.After) != 2 ||
		!slices.Equal(change.Differences, []string{"object_occurrences"}) || ExitCode(comparison, model.Warning) != 0 {
		t.Fatalf("normal replica churn appeared regressive: %+v", change)
	}
	if change := pickChange(t, comparison.ResourceChanges, "configuration-references", "pod-uid-a"); change.Classification != NoLongerObservable || !strings.Contains(change.Reason, "different UID") {
		t.Fatalf("previous object occurrence incorrectly treated as continuing: %+v", change)
	}
	if change := pickChange(t, comparison.ResourceChanges, "configuration-references", "pod-uid-b"); change.Classification != New {
		t.Fatalf("replacement object occurrence was not visible: %+v", change)
	}
}

func TestComparisonGateNewChangedAndHistoricalEvidence(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		before, after := repairedFixture(true, false)
		comparison := Compare(before, after)
		if !comparison.Complete || ExitCode(comparison, model.Warning) != 1 || ExitCode(comparison, "") != 0 {
			t.Fatalf("new severity did not trip gate: %+v", comparison)
		}
	})
	t.Run("severity and evidence changed", func(t *testing.T) {
		before, after := repairedFixture(false, false)
		after.Report.Findings[0].Severity, after.Report.Findings[0].Confidence = model.Critical, model.Likely
		after.Report.Findings[0].Evidence = append(after.Report.Findings[0].Evidence, evidence("witness", "linked"))
		comparison := Compare(before, after)
		change := pickChange(t, comparison.WorkloadChanges, "configuration-references", "deployment-uid")
		if !comparison.Complete || change.Classification != Changed || !slices.Equal(change.Differences, []string{"severity", "confidence", "evidence"}) || ExitCode(comparison, model.Warning) != 1 {
			t.Fatalf("expected material severity, confidence, and evidence regression: %+v", change)
		}
	})
	t.Run("historical observations age out", func(t *testing.T) {
		before, after := repairedFixture(false, true)
		old := before.Report.Findings[1]
		old.Symptom = "The container is running after a recent recorded OOM kill"
		old.Evidence = []model.Evidence{evidence("container", "api"), evidence("last_termination_reason", "OOMKilled")}
		before.Report.Findings = []model.Finding{old}
		comparison := Compare(before, after)
		change := pickChange(t, comparison.WorkloadChanges, "pod-containers", "deployment-uid")
		if !comparison.Complete || change.Classification != NoLongerObservable || ExitCode(comparison, model.Warning) != 0 {
			t.Fatalf("historical evidence should age out without a recovery claim: %+v", change)
		}
	})
}

func TestComparisonUnrelatedPodReadinessDoesNotRepairController(t *testing.T) {
	before, _ := repairedFixture(false, true)
	snapshot, report := fixtureSnapshot(baselineTime.Add(time.Minute), true, "pod-uid-b")
	// An unrelated Ready Pod with the same labels supplies no owner continuity.
	snapshot.Pods[0].OwnerReferences = nil
	snapshot.Pods[0].Status.Phase = core.PodRunning
	after := Build(snapshot, report)
	comparison := Compare(before, after)
	change := pickChange(t, comparison.WorkloadChanges, "configuration-references", "deployment-uid")
	if comparison.Complete || change.Classification != NoLongerObservable {
		t.Fatalf("unrelated ready Pod did not prove recovery: %+v", change)
	}
}

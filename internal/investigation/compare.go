package investigation

import (
	"cmp"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

// Compare uses the same observations in both views. The resource view keeps
// object occurrences distinct. The default regression gate uses the workload
// view, whose owner joins require observed matching UIDs.
func Compare(before, after Investigation) Comparison {
	result := Comparison{
		SchemaVersion: ComparisonSchemaVersion, Before: captureRef(before), After: captureRef(after),
		CoverageChanges: coverageChanges(before.Report, after.Report), Limitations: []string{},
	}
	reason := incompatibility(before, after)
	result.Compatible = reason == ""
	result.Complete = result.Compatible && before.Report.Complete && after.Report.Complete && !before.Truncated && !after.Truncated
	if reason != "" {
		result.Limitations = append(result.Limitations, reason)
	}
	if !before.Report.Complete || !after.Report.Complete {
		result.Limitations = append(result.Limitations, "At least one capture has incomplete required collection coverage.")
	}
	if before.Truncated || after.Truncated {
		result.Limitations = append(result.Limitations, "At least one investigation reached an analysis budget. Some associations may be absent.")
	}
	result.History = compareHistory(before.History, after.History)
	if result.History != nil {
		result.Limitations = append(result.Limitations, historyLimitation(*result.History))
		result.Complete = result.Complete && result.History.Complete
	}
	left, right := indexComparison(before), indexComparison(after)
	result.ResourceChanges, _ = compareLevel(left, right, reason, false)
	var incomplete bool
	result.WorkloadChanges, incomplete = compareLevel(left, right, reason, true)
	if incomplete && result.Compatible {
		result.Complete = false
		result.Limitations = append(result.Limitations, "One or more workload changes lack comparable identity or fresh required evidence.")
	}
	return result
}

func captureRef(inv Investigation) CaptureRef {
	return CaptureRef{ClusterID: inv.ClusterID, ClusterProvenance: inv.ClusterProvenance, Scope: inv.Scope, SchemaVersion: inv.SchemaVersion,
		RuleVersion: inv.RuleVersion, CaptureRuleVersion: inv.CaptureRuleVersion, AnalysisMode: inv.AnalysisMode,
		CaptureStartedAt: inv.CaptureStartedAt, CaptureCompletedAt: inv.CaptureCompletedAt, EvaluatedAt: inv.EvaluatedAt}
}

func incompatibility(before, after Investigation) string {
	switch {
	case before.SchemaVersion != SchemaVersion || after.SchemaVersion != SchemaVersion:
		return "An investigation schema version is unsupported. Rebuild both investigations with a compatible executable."
	case before.RuleVersion != model.AnalyzerVersion || after.RuleVersion != model.AnalyzerVersion:
		return "One or more analyzer versions are unsupported for comparison. Use two investigations evaluated under the current rules."
	case !strongProvenance(before) || !strongProvenance(after) || before.ClusterProvenance != after.ClusterProvenance:
		return "Strong cluster provenance is missing from a capture. Context names alone cannot establish cluster identity."
	case before.ClusterID != after.ClusterID:
		return "Capture provenance identifies different clusters. Use two captures from the same cluster."
	case !sameScope(before.Scope, after.Scope):
		return "Capture scopes differ. Compare the same namespace or two explicit cluster-wide captures."
	case !validTimes(before) || !validTimes(after) || !before.EvaluatedAt.Before(after.EvaluatedAt) || before.CaptureCompletedAt.After(after.CaptureStartedAt):
		return "Capture intervals are missing, overlap, or are out of order. Use a completed baseline and a later nonoverlapping observation."
	default:
		return ""
	}
}

func strongProvenance(inv Investigation) bool {
	digest, err := hex.DecodeString(inv.ClusterID)
	if err != nil || len(digest) != 32 {
		return false
	}
	return inv.ClusterProvenance == model.KubeSystemUIDProvenance ||
		(inv.Scope.Kind == model.NamespaceScope && inv.ClusterProvenance == model.NamespaceUIDProvenance)
}

func sameScope(a, b model.Scope) bool {
	return a.Kind == b.Kind && a.Namespace == b.Namespace &&
		(a.Kind == model.NamespaceScope && a.Namespace != "" || a.Kind == model.ClusterScope && a.Namespace == "")
}

func validTimes(inv Investigation) bool {
	return !inv.CaptureStartedAt.IsZero() && !inv.CaptureCompletedAt.IsZero() && !inv.EvaluatedAt.IsZero() &&
		!inv.EvaluatedAt.Before(inv.CaptureStartedAt) && !inv.EvaluatedAt.After(inv.CaptureCompletedAt)
}

type compareSnapshot struct {
	inv       Investigation
	inventory map[model.Resource]ResourceObservation
	byName    map[resourceName]model.Resource
	checks    map[string]model.CheckStatus
	coverage  map[model.Source]model.Coverage
	timing    map[model.Source]bool
	podHealth map[model.Resource]podHealth
}

type podHealth struct{ active, ready int32 }

func indexComparison(inv Investigation) compareSnapshot {
	index := compareSnapshot{inv: inv, inventory: map[model.Resource]ResourceObservation{}, byName: map[resourceName]model.Resource{},
		checks: reportChecks(inv.Report), coverage: reportCoverage(inv.Report), timing: map[model.Source]bool{}, podHealth: map[model.Resource]podHealth{}}
	for _, item := range inv.Resources {
		index.inventory[item.Resource] = item
		index.byName[nameOf(item.Resource)] = item.Resource
		if item.Workload != nil && item.Resource.Kind == "Pod" && item.Active {
			health := index.podHealth[*item.Workload]
			health.active++
			if item.StatusFresh && item.Ready {
				health.ready++
			}
			index.podHealth[*item.Workload] = health
		}
	}
	for _, timing := range inv.SourceTimings {
		valid := !timing.StartedAt.IsZero() && !timing.FinishedAt.IsZero() && !timing.StartedAt.After(timing.FinishedAt) &&
			!timing.StartedAt.Before(inv.CaptureStartedAt) && !timing.FinishedAt.After(inv.CaptureCompletedAt)
		if existing, seen := index.timing[timing.Source]; !seen || existing {
			index.timing[timing.Source] = valid
		}
	}
	return index
}

func (snapshot compareSnapshot) observed(source model.Source) bool {
	return source != "" && snapshot.coverage[source] == model.Complete && snapshot.timing[source]
}

type findingKey struct {
	rule, condition string
	subject         model.Resource
}

func (snapshot compareSnapshot) occurrences(workload bool) map[findingKey][]FindingOccurrence {
	findings := map[findingKey][]FindingOccurrence{}
	for i, finding := range snapshot.inv.Report.Findings {
		subject := finding.Resource
		if workload {
			if anchor := snapshot.inventory[finding.Resource].Workload; anchor != nil {
				subject = *anchor
			}
		}
		key := findingKey{rule: finding.RuleID, subject: subject, condition: conditionKey(finding)}
		findings[key] = append(findings[key], FindingOccurrence{Index: i, Finding: finding})
	}
	return findings
}

func compareLevel(left, right compareSnapshot, incompatibility string, workload bool) ([]Change, bool) {
	before, after := left.occurrences(workload), right.occurrences(workload)
	keys := make([]findingKey, 0, len(before)+len(after))
	for key := range before {
		keys = append(keys, key)
	}
	for key := range after {
		if _, exists := before[key]; !exists {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b findingKey) int {
		return cmp.Or(compareResource(a.subject, b.subject), cmp.Compare(a.rule, b.rule), cmp.Compare(a.condition, b.condition))
	})
	changes, incomplete := make([]Change, 0, len(keys)), false
	for _, key := range keys {
		change := Change{RuleID: key.rule, Subject: key.subject, Condition: key.condition,
			Before: append([]FindingOccurrence{}, before[key]...), After: append([]FindingOccurrence{}, after[key]...), Differences: []string{}}
		uncertain := classify(&change, left, right, incompatibility)
		incomplete = incomplete || uncertain
		changes = append(changes, change)
	}
	return changes, incomplete
}

func classify(change *Change, left, right compareSnapshot, incompatibility string) bool {
	subject := change.Subject
	if incompatibility != "" {
		change.Classification, change.Reason = NotComparable, incompatibility
		return true
	}
	if subject.UID == "" {
		change.Classification, change.Reason = NotComparable, "A subject UID was unavailable. Name alone cannot establish object continuity."
		return true
	}
	if len(change.Before) != 0 && len(change.After) != 0 {
		change.Differences = findingDifferences(change.Before, change.After)
		if slices.ContainsFunc(change.Differences, func(field string) bool { return field != "object_occurrences" }) {
			change.Classification, change.Reason = Changed, "The comparable finding is present in both captures and its observations differ."
		} else {
			change.Classification, change.Reason = Persistent, "The same finding condition is observed in both captures."
		}
		if _, known := left.inventory[subject]; !known {
			change.Classification, change.Reason = NotComparable, "The baseline does not establish the subject's observed UID."
			return true
		}
		if _, known := right.inventory[subject]; !known {
			change.Classification, change.Reason = NotComparable, "The subsequent capture does not establish the subject's observed UID."
			return true
		}
		return false
	}
	if len(change.Before) == 0 {
		finding := change.After[0].Finding
		if limitation := comparisonVisibility(change, finding, left, right); limitation != "" {
			change.Classification, change.Reason = NotComparable, limitation
			return true
		}
		if after, known := right.inventory[subject]; !known || !after.StatusFresh {
			change.Classification, change.Reason = NotComparable, "The positive finding is retained, but subsequent subject status is missing or stale."
			return true
		}
		if before, known := left.inventory[subject]; known && !before.StatusFresh {
			change.Classification, change.Reason = NotComparable, "The baseline subject was present with stale or insufficient status."
			return true
		}
		change.Classification, change.Reason = New, "Observed in the subsequent capture. Comparable baseline coverage did not detect this condition."
		return false
	}

	finding := change.Before[0].Finding
	if limitation := comparisonVisibility(change, finding, left, right); limitation != "" {
		change.Classification, change.Reason = NoLongerObservable, limitation
		return true
	}
	if _, present := right.inventory[subject]; !present {
		change.Classification = NoLongerObservable
		if replacement, exists := right.byName[nameOf(subject)]; exists && replacement.UID != subject.UID {
			change.Reason = "The same name has a different UID. The previous object occurrence cannot be assessed."
		} else {
			change.Reason = "The previous object UID was absent from the subsequent capture. Disappearance does not demonstrate recovery."
		}
		return true
	}
	if historicalOnly(finding) {
		change.Classification, change.Reason = NoLongerObservable, "The earlier finding records a historical occurrence or sample. Recovery remains unverified."
		return false
	}
	if reason := recoveryLimitation(left, right, subject, finding); reason != "" {
		change.Classification, change.Reason = NoLongerObservable, reason
		return true
	}
	change.Classification, change.Reason = Resolved, "Compatible observations cover the same UID and show current subject evidence without the previously detected condition."
	return false
}

func comparisonVisibility(change *Change, finding model.Finding, left, right compareSnapshot) string {
	if !left.inv.Report.Complete || !right.inv.Report.Complete {
		return "Required collection is incomplete in one or both captures. Omission does not establish absence."
	}
	if left.inv.Truncated || right.inv.Truncated {
		return "Investigation budgets limited associations or evidence in one or both captures."
	}
	if left.checks[change.RuleID] != model.Checked || right.checks[change.RuleID] != model.Checked {
		return "The applicable check did not report inspected coverage in both captures."
	}
	dependencies := findingSources(finding)
	required := append(append([]model.Source{}, dependencies...), left.inventory[change.Subject].Source, right.inventory[change.Subject].Source)
	if finding.Resource.Kind == "Pod" && change.Subject != finding.Resource {
		required = append(required, model.SourcePods)
		if change.Subject.Kind == "Deployment" || change.Subject.Kind == "ReplicaSet" {
			required = append(required, model.SourceReplicaSets)
		}
	}
	for _, source := range required {
		if source != "" && (!left.observed(source) || !right.observed(source)) {
			return "Complete, timed observations are unavailable on both sides for " + string(source) + "."
		}
	}
	if finding.RuleID == "pvc-binding" && evidenceValue(finding, "storage_class") != "" {
		evidenceSide := left
		if len(change.Before) == 0 {
			evidenceSide = right
		}
		if !evidenceSide.observed(model.SourceStorageClasses) {
			return "Timed StorageClass evidence is unavailable for the capture reporting the binding problem."
		}
	}
	if len(dependencies) == 0 {
		return "The rule has no registered evidence requirements for absence comparisons."
	}
	return ""
}

func recoveryLimitation(left, right compareSnapshot, subject model.Resource, finding model.Finding) string {
	if rolloutRule(finding.RuleID) {
		return rolloutRecoveryLimit(left.inventory[subject], right.inventory[subject], right, finding)
	}
	if !positiveContinuity(left, right, subject, finding) {
		return "Source coverage alone is insufficient. Fresh status and, for Pod symptoms, comparable active Ready members are needed."
	}
	return podContainerRecoveryLimitation(left, right, subject, finding)
}

func positiveContinuity(left, right compareSnapshot, subject model.Resource, finding model.Finding) bool {
	before, known := left.inventory[subject]
	after := right.inventory[subject]
	if !known || !before.StatusFresh || !after.StatusFresh {
		return false
	}
	if finding.Resource.Kind != "Pod" {
		if supportedWorkload(subject.Kind) && subject != finding.Resource {
			return false // Separate executions under a controller do not repair one another.
		}
		switch finding.RuleID {
		case "service.backends", "service.ports", "service-endpoint-identity":
			return comparableServiceBackend(before, after, finding)
		case "pdb.disruptions":
			return after.Active && after.Ready
		case "job-execution", "pvc-binding":
			return after.Ready
		}
		if handled, current := relevantNonRolloutEvidence(left, right, finding); handled {
			return current
		}
		return true
	}
	if subject.Kind == "Pod" {
		return after.Active && after.Ready
	}
	health := right.podHealth[subject]
	return after.Active && after.ExpectedPods != nil && *after.ExpectedPods > 0 && health.active >= *after.ExpectedPods && health.ready == health.active
}

func historicalOnly(finding model.Finding) bool {
	event := evidenceValue(finding, "event_reason") != ""
	switch finding.RuleID {
	case "recent-warning-events", "current-pod-usage":
		return true
	case "namespace-admission", "rollout-probes":
		return event
	case "cronjob-scheduling":
		return evidenceValue(finding, "suspended") != "true" && (event || evidenceValue(finding, "failed_owned_job") != "")
	case "configuration-references":
		return event && evidenceValue(finding, "reference_kind") == "Secret"
	case "pod-scheduling":
		return event && evidenceValue(finding, "pod_scheduled") == ""
	case "pod-containers":
		return evidenceValue(finding, "last_termination_reason") != "" && evidenceValue(finding, "waiting_reason") == "" &&
			evidenceValue(finding, "termination_reason") == "" && evidenceValue(finding, "exit_code") == "" ||
			strings.Contains(finding.Symptom, "has restarted repeatedly")
	default:
		return false
	}
}

func conditionKey(finding model.Finding) string {
	parts := []string{}
	add := func(fields ...string) {
		for _, field := range fields {
			parts = append(parts, evidenceValue(finding, field))
		}
	}
	switch finding.RuleID {
	case "pod-containers":
		add("container_role", "container")
		switch {
		case evidenceValue(finding, "waiting_reason") != "":
			// Similar image pull waiting codes share the same analyzer symptom.
			parts = append(parts, finding.Symptom)
		case evidenceValue(finding, "termination_reason") != "", evidenceValue(finding, "exit_code") != "":
			parts = append(parts, "current termination")
		case evidenceValue(finding, "last_termination_reason") != "":
			parts = append(parts, "historical termination")
		default:
			parts = append(parts, finding.Symptom)
		}
	case "configuration-references":
		add("reference_kind", "reference_name", "reference_key")
	case "container-resources", "current-pod-usage":
		add("container_role", "container", "probe_kind")
	case "rollout-probes":
		add("container_role", "container", "probe")
	case "recent-warning-events":
		add("event_reason")
	case "service.ports":
		add("service_port", "protocol", "port_name")
	case "ingress.backends":
		add("backend_service", "backend_port")
	case "networkpolicy.isolation":
		add("direction")
	case "networkpolicy-named-ports":
		add("named_port", "protocol")
	case "service.backends":
		switch {
		case evidenceValue(finding, "usable_backend_entries") == "0":
			parts = append(parts, "no usable backends")
		case evidenceValue(finding, "mismatched_pod_uids") != "":
			parts = append(parts, "selector mismatch")
		case evidenceValue(finding, "draining_entries") != "":
			parts = append(parts, "only terminating backends")
		default:
			parts = append(parts, finding.Symptom)
		}
	case "dns.infrastructure", "pvc-binding":
		parts = append(parts, finding.Symptom)
		add("service_port", "protocol")
	}
	return strings.Join(parts, " / ")
}

func findingDifferences(before, after []FindingOccurrence) []string {
	diff := []string{}
	for _, field := range []struct {
		name  string
		value func(model.Finding) any
	}{
		{"severity", func(f model.Finding) any { return f.Severity }},
		{"confidence", func(f model.Finding) any { return f.Confidence }},
		{"symptom", func(f model.Finding) any { return f.Symptom }},
		{"evidence", func(f model.Finding) any {
			items := append([]model.Evidence{}, f.Evidence...)
			slices.SortFunc(items, func(a, b model.Evidence) int {
				return cmp.Or(cmp.Compare(a.Field, b.Field), cmp.Compare(a.Value, b.Value))
			})
			return items
		}},
		{"object_occurrences", func(f model.Finding) any { return f.Resource }},
	} {
		if !slices.Equal(valuesOf(before, field.value), valuesOf(after, field.value)) {
			diff = append(diff, field.name)
		}
	}
	return diff
}

func valuesOf(occurrences []FindingOccurrence, value func(model.Finding) any) []string {
	items := make([]string, 0, len(occurrences))
	for _, occurrence := range occurrences {
		encoded, _ := json.Marshal(value(occurrence.Finding)) // These values are fixed scalar and evidence types.
		items = append(items, string(encoded))
	}
	slices.Sort(items)
	return items
}

// ExitCode implements the workload-level regression gate. An empty threshold
// disables finding failures while preserving comparison errors.
func ExitCode(comparison Comparison, minimum model.Severity) int {
	if !comparison.Compatible || !comparison.Complete {
		return 2
	}
	if minimum != "" {
		for _, change := range comparison.WorkloadChanges {
			before, after := maximumSeverity(change.Before), maximumSeverity(change.After)
			if after >= minimum.Rank() && (change.Classification == New || change.Classification == Changed && after > before) {
				return 1
			}
		}
	}
	return 0
}

func maximumSeverity(occurrences []FindingOccurrence) int {
	maximum := 0
	for _, finding := range occurrences {
		maximum = max(maximum, finding.Finding.Severity.Rank())
	}
	return maximum
}

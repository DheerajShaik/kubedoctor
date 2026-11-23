package analyze

import (
	"math/big"
	"regexp"
	"strconv"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
)

type hpaTarget struct {
	object model.Resource
	pods   []*core.Pod
}

func scalableTargets(s model.Snapshot, owners map[model.Resource][]*core.Pod) map[workloadLocator]hpaTarget {
	targets := map[workloadLocator]hpaTarget{}
	for i := range s.Deployments {
		resource := model.Identity("Deployment", &s.Deployments[i])
		if s.Deployments[i].DeletionTimestamp == nil {
			targets[locateWorkload(resource)] = hpaTarget{object: resource}
		}
	}
	for i := range s.ReplicaSets {
		rs := &s.ReplicaSets[i]
		resource := model.Identity("ReplicaSet", rs)
		if rs.DeletionTimestamp != nil {
			continue
		}
		pods := owners[resource]
		targets[locateWorkload(resource)] = hpaTarget{object: resource, pods: pods}
		parent := controllerIdentity(rs)
		if parent.Kind == "Deployment" && parent.UID != "" {
			key := locateWorkload(parent)
			if target, exists := targets[key]; exists && target.object.UID == parent.UID {
				target.pods = append(target.pods, pods...)
				targets[key] = target
			}
		}
	}
	for _, set := range s.Expansion.StatefulSets {
		if !set.Deleting {
			targets[locateWorkload(set.Resource)] = hpaTarget{object: set.Resource, pods: owners[set.Resource]}
		}
	}
	return targets
}

func hpaTargetSource(hpa model.HPAEvidence) model.Source {
	if hpa.TargetAPIVersion != "apps/v1" || hpa.Target.Namespace != hpa.Resource.Namespace || hpa.Target.Name == "" {
		return ""
	}
	switch hpa.Target.Kind {
	case "Deployment":
		return model.SourceDeployments
	case "ReplicaSet":
		return model.SourceReplicaSets
	case "StatefulSet":
		return model.SourceStatefulSets
	default:
		return ""
	}
}

func analyzeHPAs(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, hpaRule, model.SourceHPAs)
	targets := scalableTargets(s, activeOwnedPods(s.Pods))
	var findings []model.Finding
	for _, hpa := range s.Expansion.HPAs {
		if hpa.Deleting {
			continue
		}
		evidence := []model.Evidence{countEvidence("current_replicas", int64(hpa.CurrentReplicas)), countEvidence("desired_replicas", int64(hpa.DesiredReplicas)),
			countEvidence("min_replicas", int64(hpa.MinReplicas)), countEvidence("max_replicas", int64(hpa.MaxReplicas))}
		source := hpaTargetSource(hpa)
		target := targets[locateWorkload(hpa.Target)]
		if source == "" || s.Coverage(source) != model.Complete {
			workloadUncertain(&check, "At least one HPA scale-target kind or API is outside complete observed coverage")
		}
		if source != "" {
			evidence = append(evidence, workloadEvidence("scale_target_kind", hpa.Target.Kind), workloadEvidence("scale_target", hpa.Target.Name))
		}
		severity, symptom := model.Info, ""
		if source != "" && s.Coverage(source) == model.Complete && target.object.Name == "" {
			severity, symptom = model.Warning, "The HPA's declared scale target is absent from the observed namespace inventory"
		}
		// The upstream HPA controller may omit observedGeneration. A condition
		// on generation one has no earlier spec revision to be stale against.
		fresh := hpa.Generation > 0 && (hpa.ObservedGeneration == hpa.Generation || hpa.Generation == 1 && hpa.ObservedGeneration == 0)
		if !fresh || hpa.TargetsTruncated {
			workloadUncertain(&check, "An HPA has stale status or incomplete utilization configuration")
		}
		if fresh {
			conditionSeverity, conditionSymptom, conditions := hpaConditions(hpa, &check)
			evidence = append(evidence, conditions...)
			if symptom == "" && conditionSymptom != "" {
				severity, symptom = conditionSeverity, conditionSymptom
			}
		}
		requestSourcesVisible := target.object.UID != "" && s.Coverage(model.SourcePods) == model.Complete &&
			(hpa.Target.Kind != "Deployment" || s.Coverage(model.SourceReplicaSets) == model.Complete)
		if len(hpa.UtilizationTargets) > 0 && !requestSourcesVisible && target.object.Name != "" {
			workloadUncertain(&check, "An HPA's owned Pod requests cannot be verified with the available inventory")
		}
		if requestSourcesVisible && source != "" {
			gaps := hpaRequestGaps(target.pods, hpa.UtilizationTargets)
			if len(gaps) > 0 {
				evidence = append(evidence, gaps...)
				if symptom == "" {
					symptom = "An HPA utilization target has owner-linked Pods missing relevant resource requests"
				}
			}
		}
		if symptom == "" {
			continue
		}
		finding := workloadFinding(hpaRule, hpa.Resource, severity, symptom,
			"Check the HPA scale target and metric source. Utilization targets divide usage by relevant requests, so changing requests can change scaling behavior.", evidence...)
		if target.object.UID != "" && source != "" {
			finding.Incident = &target.object
		}
		findings = append(findings, finding)
	}
	return findings, check
}

func hpaConditions(hpa model.HPAEvidence, check *model.CheckResult) (model.Severity, string, []model.Evidence) {
	active := briefCondition(hpa.Conditions, "ScalingActive")
	able := briefCondition(hpa.Conditions, "AbleToScale")
	limited := briefCondition(hpa.Conditions, "ScalingLimited")
	if active == nil || able == nil || active.Status == "Unknown" || able.Status == "Unknown" || limited != nil && limited.Status == "Unknown" {
		workloadUncertain(check, "An HPA does not have conclusive scaling conditions")
	}
	severity, symptom := model.Info, ""
	var evidence []model.Evidence
	if able != nil && able.Status == "False" {
		evidence = appendBriefCondition(evidence, able)
		severity, symptom = model.Warning, "The HPA controller reports that it cannot scale the target"
	}
	if active != nil && active.Status == "False" && active.Reason != "ScalingDisabled" {
		evidence = appendBriefCondition(evidence, active)
		if symptom == "" {
			severity, symptom = model.Warning, "The HPA controller reports that metric-driven scaling is inactive"
		}
	}
	if limited != nil && limited.Status == "True" {
		evidence = appendBriefCondition(evidence, limited)
		if symptom == "" {
			symptom = "The HPA reports that its desired scaling is constrained by policy or replica bounds"
		}
	}
	return severity, symptom, evidence
}

func hpaRequestGaps(pods []*core.Pod, utilization []model.HPAUtilizationTarget) []model.Evidence {
	var evidence []model.Evidence
	for _, metric := range utilization {
		if metric.Resource != "cpu" && metric.Resource != "memory" {
			continue
		}
		var missing int64
		for _, pod := range pods {
			if ready := podReadiness(pod); ready == nil || ready.Status != core.ConditionTrue || pod.Status.Phase != core.PodRunning {
				continue
			}
			if lacksHPARequest(pod, metric) {
				missing++
			}
		}
		if missing > 0 {
			evidence = append(evidence, workloadEvidence("utilization_resource", metric.Resource), countEvidence("pods_with_missing_request", missing))
			if metric.Container != "" {
				evidence = append(evidence, workloadEvidence("metric_container", metric.Container))
			}
		}
	}
	return evidence
}

func lacksHPARequest(pod *core.Pod, metric model.HPAUtilizationTarget) bool {
	name := core.ResourceName(metric.Resource)
	if metric.Container == "" && pod.Spec.Resources != nil {
		if value := pod.Spec.Resources.Requests[name]; value.Sign() > 0 {
			return false
		}
	}
	for _, group := range []struct {
		containers []core.Container
		init       bool
	}{{pod.Spec.Containers, false}, {pod.Spec.InitContainers, true}} {
		for _, container := range group.containers {
			if group.init && (container.RestartPolicy == nil || *container.RestartPolicy != core.ContainerRestartPolicyAlways) ||
				metric.Container != "" && metric.Container != container.Name {
				continue
			}
			if value := container.Resources.Requests[name]; value.Sign() <= 0 {
				return true
			}
		}
	}
	return false
}

var boundedDecimal = regexp.MustCompile(`^[0-9]{1,47}(?:\.[0-9]{1,9})?$`)

func observedQuotaAmounts(value model.QuotaCapacity) (hard, used *big.Rat, known bool) {
	if !value.Current || len(value.Hard) > 48 || len(value.Used) > 48 || !boundedDecimal.MatchString(value.Hard) || !boundedDecimal.MatchString(value.Used) {
		return nil, nil, false
	}
	hard, _ = new(big.Rat).SetString(value.Hard)
	used, _ = new(big.Rat).SetString(value.Used)
	return hard, used, true
}

func analyzeAdmission(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, admissionRule, model.SourceQuotas)
	if s.Coverage(model.SourceLimitRanges) != model.Complete || s.Coverage(model.SourceEvents) != model.Complete {
		workloadUncertain(&check, "LimitRange or admission-event visibility is incomplete")
	}
	quotas := make(map[configLocator]model.QuotaEvidence, len(s.Expansion.Quotas))
	var findings []model.Finding
	for _, quota := range s.Expansion.Quotas {
		quotas[configLocator{quota.Resource.Namespace, quota.Resource.Name}] = quota
		var evidence []model.Evidence
		for _, capacity := range quota.Capacities {
			hard, used, known := observedQuotaAmounts(capacity)
			if !known {
				workloadUncertain(&check, "Quota usage is absent or its hard values do not agree with the current specification")
				continue
			}
			if used.Cmp(hard) >= 0 {
				evidence = append(evidence, workloadEvidence("quota_resource", capacity.Resource), workloadEvidence("quota_hard", capacity.Hard), workloadEvidence("quota_used", capacity.Used))
			}
		}
		if len(evidence) > 0 {
			findings = append(findings, workloadFinding(admissionRule, quota.Resource, model.Info, "At least one observed namespace quota is at or above its hard value",
				"Inspect admission events for an actual rejection. Quotas apply to admission and can intentionally constrain additional objects or resources.", evidence...))
		}
	}
	controllers := admissionControllers(s)
	type admissionKey struct {
		resource                                    model.Resource
		constraint, quota, resourceName, limitScope string
	}
	latest := make(map[admissionKey]model.AdmissionEventEvidence)
	for _, observed := range s.Expansion.AdmissionEvents {
		key := admissionKey{observed.Event.Resource, observed.Constraint, observed.Quota, observed.ResourceName, observed.LimitScope}
		target, known := controllers[locateWorkload(key.resource)]
		if !known || !recentTimedReference(s, observed.Event, target.id, target.created) {
			continue
		}
		previous, ok := latest[key]
		if !ok || observed.Event.Time.After(previous.Event.Time) || observed.Event.Time.Equal(previous.Event.Time) && observed.Event.Count > previous.Event.Count {
			latest[key] = observed
		}
	}
	for _, observed := range latest {
		target := controllers[locateWorkload(observed.Event.Resource)]
		evidence := []model.Evidence{workloadEvidence("admission_constraint", observed.Constraint)}
		evidence = addEventEvidence(evidence, model.Event{Resource: target.id, Reason: "FailedCreate", Time: observed.Event.Time, Count: observed.Event.Count})
		severity := model.Info
		switch observed.Constraint {
		case "quota_exceeded", "quota_required_resources":
			evidence = append(evidence, workloadEvidence("quota", observed.Quota))
			if quota, ok := quotas[configLocator{target.id.Namespace, observed.Quota}]; ok && !quota.Created.After(observed.Event.Time) {
				severity = model.Warning
				evidence = append(evidence, workloadEvidence("quota_corroboration", "named ResourceQuota observed"))
			} else {
				workloadUncertain(&check, "A recent quota rejection cannot be matched to the current policy inventory")
			}
		case "minimum", "maximum", "maximum_ratio":
			evidence = append(evidence, workloadEvidence("resource", observed.ResourceName), workloadEvidence("limit_scope", observed.LimitScope))
			candidates := matchingLimitRanges(s, observed, target.id.Namespace)
			if candidates > 0 {
				severity = model.Warning
				evidence = append(evidence, workloadEvidence("matching_limit_ranges", strconv.Itoa(candidates)))
			} else {
				workloadUncertain(&check, "A recent limit rejection cannot be matched to the current policy inventory")
			}
		default:
			continue
		}
		finding := workloadFinding(admissionRule, target.id, severity, "The controller recently reported an API admission rejection for a resource constraint",
			"Review the rejected object's requests, limits, and quota accounting. The admission rejection occurred before scheduling and does not explain unrelated scheduling failures.", evidence...)
		finding.Incident = target.incident
		findings = append(findings, finding)
	}
	return findings, check
}

func matchingLimitRanges(s model.Snapshot, observed model.AdmissionEventEvidence, namespace string) int {
	var matches int
	for _, limits := range s.Expansion.LimitRanges {
		if limits.Resource.Namespace != namespace || limits.Created.After(observed.Event.Time) {
			continue
		}
		for _, constraint := range limits.Constraints {
			if constraint.Scope == observed.LimitScope && constraint.Resource == observed.ResourceName && constraint.Type == observed.Constraint {
				matches++
				break
			}
		}
	}
	return matches
}

func recentTimedReference(s model.Snapshot, event model.TimedReference, target model.Resource, created time.Time) bool {
	return locateWorkload(event.Resource) == locateWorkload(target) && observedWithin(s.CollectedAt, event.Time, warningHorizon) &&
		eventMatches(model.Event{Resource: event.Resource, Time: event.Time}, target, created)
}

func admissionControllers(s model.Snapshot) map[workloadLocator]warningTarget {
	out := map[workloadLocator]warningTarget{}
	for i := range s.ReplicaSets {
		rs := &s.ReplicaSets[i]
		id := model.Identity("ReplicaSet", rs)
		owner := controllerIdentity(rs)
		value := warningTarget{id: id, created: rs.CreationTimestamp.Time}
		if owner.Kind == "Deployment" {
			value.incident = &owner
		}
		out[locateWorkload(id)] = value
	}
	for i := range s.Deployments {
		d := &s.Deployments[i]
		id := model.Identity("Deployment", d)
		out[locateWorkload(id)] = warningTarget{id: id, created: d.CreationTimestamp.Time}
	}
	for i := range s.DaemonSets {
		d := &s.DaemonSets[i]
		id := model.Identity("DaemonSet", d)
		out[locateWorkload(id)] = warningTarget{id: id, created: d.CreationTimestamp.Time}
	}
	for _, set := range s.Expansion.StatefulSets {
		out[locateWorkload(set.Resource)] = warningTarget{id: set.Resource, created: set.Created}
	}
	for _, job := range s.Expansion.Jobs {
		out[locateWorkload(job.Resource)] = warningTarget{id: job.Resource, created: job.Created, incident: job.Owner}
	}
	return out
}

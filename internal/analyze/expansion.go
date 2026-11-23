package analyze

import (
	"strconv"
	"strings"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	statefulSetRule = "statefulset-rollout"
	jobRule         = "job-execution"
	cronJobRule     = "cronjob-scheduling"
	hpaRule         = "hpa-scaling"
	admissionRule   = "namespace-admission"
	probesRule      = "rollout-probes"
	serviceUIDRule  = "service-endpoint-identity"
	nodeLocalRule   = "node-local-dns"
	policyPortRule  = "networkpolicy-named-ports"
)

var expansionDefinitions = []model.CheckDefinition{
	{ID: statefulSetRule, Description: "Evaluate StatefulSet availability, ordinal progress, and referenced persistent claims"},
	{ID: jobRule, Description: "Observe Job failure, retries, deadlines, and successful or suspended execution"},
	{ID: cronJobRule, Description: "Observe CronJob schedule constraints and owned Job outcomes"},
	{ID: hpaRule, Description: "Evaluate HPA conditions, target references, utilization requests, and scaling bounds"},
	{ID: admissionRule, Description: "Correlate quota and LimitRange admission evidence without inferring scheduling causes"},
	{ID: probesRule, Description: "Link recent classified probe failures to current container status and a known rollout"},
	{ID: serviceUIDRule, Description: "Check controller-managed EndpointSlice Pod identities for stale replacement references"},
	{ID: nodeLocalRule, Description: "Examine conventional NodeLocal DNSCache readiness and observed hosting nodes"},
	{ID: policyPortRule, Description: "Identify unresolved named ingress ports among visibly selected Pods"},
}

func analyzeExpansion(s model.Snapshot, index workloadIndex) ([]model.Finding, []model.CheckResult) {
	var findings []model.Finding
	var checks []model.CheckResult
	for _, result := range []workloadAnalysis{
		workloadResult(analyzeStatefulSets(s)),
		workloadResult(analyzeJobs(s)),
		workloadResult(analyzeCronJobs(s, index)),
		workloadResult(analyzeHPAs(s, index)),
		workloadResult(analyzeAdmission(s)),
		workloadResult(analyzeRolloutProbes(s, index)),
		workloadResult(analyzeServiceIdentities(s)),
		workloadResult(analyzeNodeLocalDNS(s)),
		workloadResult(analyzePolicyPorts(s)),
	} {
		findings = append(findings, result.findings...)
		checks = append(checks, result.status)
	}
	return findings, checks
}

func controllerIdentity(pod metav1.Object) model.Resource {
	ref := metav1.GetControllerOfNoCopy(pod)
	if ref == nil || ref.UID == "" {
		return model.Resource{}
	}
	return model.Resource{Kind: ref.Kind, Namespace: pod.GetNamespace(), Name: ref.Name, UID: string(ref.UID)}
}

func activeOwnedPods(pods []core.Pod) map[model.Resource][]*core.Pod {
	owners := map[model.Resource][]*core.Pod{}
	for i := range pods {
		pod := &pods[i]
		if !activePod(pod) || pod.Status.Phase == core.PodFailed {
			continue
		}
		if owner := controllerIdentity(pod); owner.UID != "" {
			owners[owner] = append(owners[owner], pod)
		}
	}
	return owners
}

func analyzeStatefulSets(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, statefulSetRule, model.SourceStatefulSets)
	owners := activeOwnedPods(s.Pods)
	claims := make(map[configLocator]*core.PersistentVolumeClaim, len(s.PVCs))
	for i := range s.PVCs {
		claim := &s.PVCs[i]
		claims[configLocator{claim.Namespace, claim.Name}] = claim
	}
	modes := make(map[string]storage.VolumeBindingMode, len(s.StorageClasses))
	for _, class := range s.StorageClasses {
		if class.VolumeBindingMode == nil {
			modes[class.Name] = storage.VolumeBindingImmediate
		} else {
			modes[class.Name] = *class.VolumeBindingMode
		}
	}
	var findings []model.Finding
	for _, set := range s.Expansion.StatefulSets {
		if set.Deleting || set.Desired == 0 {
			continue
		}
		if set.ObservedGeneration < set.Generation || set.Generation <= 0 || set.Desired < 0 {
			workloadUncertain(&check, "A StatefulSet does not have usable status for its current generation")
			continue
		}
		owned := owners[set.Resource]
		if s.Coverage(model.SourcePods) != model.Complete || set.TemplatesTruncated || (len(set.VolumeClaimTemplates) > 0 && s.Coverage(model.SourcePVCs) != model.Complete) {
			workloadUncertain(&check, "StatefulSet ordinal or persistent claim evidence is incomplete")
		}
		availabilityGap := set.Available < set.Desired || set.Ready < set.Desired
		// In the supported controller versions the rolling loop indexes replicas
		// relative to the configured start ordinal and stops at the partition.
		updateTarget := max(int32(0), set.Desired-max(set.Partition, 0))
		updateGap := set.Strategy != "OnDelete" && set.RevisionState == "different" && set.Updated < updateTarget
		if !availabilityGap && !updateGap {
			continue
		}
		evidence := rolloutCounts(set.Desired, set.Available, set.Updated)
		evidence = append(evidence, countEvidence("ready", int64(set.Ready)), countEvidence("start_ordinal", int64(set.StartOrdinal)), countEvidence("partition", int64(set.Partition)))
		if set.Management == "OrderedReady" || set.Management == "Parallel" {
			evidence = append(evidence, workloadEvidence("pod_management", set.Management))
		}
		blocked, delayed, latest := statefulProgress(set, owned, claims, modes, s, &check)
		severity := model.Warning
		symptom := "The StatefulSet has a replica shortfall beyond its initial startup grace"
		if len(blocked) > 0 {
			evidence = append(evidence, blocked...)
			symptom = "The StatefulSet has a replica shortfall and an owned ordinal has an observed failure"
		} else {
			grace := max(startupGrace, time.Duration(set.MinReadySeconds)*time.Second)
			if delayed || !latest.IsZero() && !agedEnough(s.CollectedAt, latest, grace) {
				continue
			}
			if set.Generation > 1 || set.Created.IsZero() {
				workloadUncertain(&check, "A StatefulSet has a current replica deficit without a measured rollout interval")
				severity, symptom = model.Info, "The StatefulSet currently reports a replica shortfall and its duration is unknown"
			} else if !agedEnough(s.CollectedAt, set.Created, grace) {
				continue
			} else {
				evidence = appendObservedTime(evidence, "statefulset_created", set.Created)
			}
		}
		next := "Inspect the affected ordinal's Pod and referenced claims. OrderedReady can gate later ordinals until earlier Pods become Ready."
		if set.Management == "Parallel" {
			next = "Inspect the affected Pods, their rollout revisions, and referenced claims. Parallel starts can converge at different times."
		}
		findings = append(findings, workloadFinding(statefulSetRule, set.Resource, severity, symptom, next, evidence...))
	}
	return findings, check
}

func statefulProgress(set model.StatefulSetEvidence, pods []*core.Pod, claims map[configLocator]*core.PersistentVolumeClaim, modes map[string]storage.VolumeBindingMode, s model.Snapshot, check *model.CheckResult) (evidence []model.Evidence, delayed bool, newest time.Time) {
	var lowest int64 = 1 << 62
	for _, pod := range pods {
		ordinalText, valid := strings.CutPrefix(pod.Name, set.Resource.Name+"-")
		ordinal, err := strconv.ParseInt(ordinalText, 10, 32)
		if !valid || err != nil || ordinal < int64(set.StartOrdinal) || ordinal >= int64(set.StartOrdinal)+int64(set.Desired) {
			continue
		}
		if pod.CreationTimestamp.After(newest) {
			newest = pod.CreationTimestamp.Time
		}
		if ready := podReadiness(pod); ready != nil && ready.LastTransitionTime.After(newest) {
			newest = ready.LastTransitionTime.Time
		}
		var block []model.Evidence
		if reason := podStartupFailure(pod); reason != "" {
			block = append(block, workloadEvidence("pod_state", reason))
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim == nil {
				continue
			}
			referenced := volume.PersistentVolumeClaim.ClaimName
			if !matchesStatefulClaim(set.VolumeClaimTemplates, pod.Name, referenced) {
				continue
			}
			claim := claims[configLocator{pod.Namespace, referenced}]
			if claim == nil {
				if s.Coverage(model.SourcePVCs) == model.Complete {
					block = append(block, workloadEvidence("persistentvolumeclaim", referenced), workloadEvidence("claim_phase", "absent"))
				}
				continue
			}
			if claim.Status.Phase == core.ClaimLost {
				block = append(block, workloadEvidence("persistentvolumeclaim", claim.Name), workloadEvidence("claim_phase", "Lost"))
				continue
			}
			if claim.Status.Phase == core.ClaimPending {
				delayed = delayed || pod.Spec.NodeName == "" && claim.Spec.StorageClassName != nil && modes[*claim.Spec.StorageClassName] == storage.VolumeBindingWaitForFirstConsumer
				if claim.Spec.StorageClassName != nil && *claim.Spec.StorageClassName != "" && modes[*claim.Spec.StorageClassName] == "" {
					workloadUncertain(check, "StatefulSet claim binding mode could not be confirmed")
				}
			}
		}
		if len(block) == 0 || ordinal >= lowest {
			continue
		}
		lowest = ordinal
		evidence = append([]model.Evidence{workloadEvidence("owned_pod", pod.Name), countEvidence("ordinal", ordinal)}, block...)
	}
	return evidence, delayed, newest
}

func matchesStatefulClaim(templates []string, pod, claim string) bool {
	for _, template := range templates {
		if claim == template+"-"+pod {
			return true
		}
	}
	return false
}

func podStartupFailure(pod *core.Pod) string {
	for _, partition := range [][]core.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for _, container := range partition {
			if wait := container.State.Waiting; wait != nil {
				if symptom, _, _ := waitingDiagnostic(wait.Reason); symptom != "" {
					return wait.Reason
				}
			}
		}
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == core.PodScheduled && condition.Status == core.ConditionFalse && condition.Reason == core.PodReasonUnschedulable && pod.Spec.NodeName == "" {
			return "Unschedulable"
		}
	}
	return ""
}

func podReadiness(pod *core.Pod) *core.PodCondition {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == core.PodReady {
			return &condition
		}
	}
	return nil
}

func briefCondition(conditions []model.BriefCondition, kind string) *model.BriefCondition {
	var latest *model.BriefCondition
	for _, condition := range conditions {
		if condition.Type == kind && (latest == nil || condition.Transition.After(latest.Transition)) {
			latest = &condition
		}
	}
	return latest
}

func trueCondition(conditions []model.BriefCondition, kinds ...string) *model.BriefCondition {
	for _, kind := range kinds {
		if condition := briefCondition(conditions, kind); condition != nil && condition.Status == "True" {
			return condition
		}
	}
	return nil
}

func appendBriefCondition(evidence []model.Evidence, c *model.BriefCondition) []model.Evidence {
	evidence = append(evidence, workloadEvidence("condition", c.Type), workloadEvidence("condition_status", c.Status))
	if c.Reason != "" {
		evidence = append(evidence, workloadEvidence("condition_reason", c.Reason))
	}
	return appendObservedTime(evidence, "condition_transition", c.Transition)
}

func analyzeJobs(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, jobRule, model.SourceJobs)
	var findings []model.Finding
	for _, job := range s.Expansion.Jobs {
		if job.Deleting || trueCondition(job.Conditions, "Complete", "SuccessCriteriaMet") != nil {
			continue
		}
		evidence := []model.Evidence{countEvidence("active_pods", int64(job.Active)), countEvidence("failed_pods", int64(job.Failed)), countEvidence("succeeded_pods", int64(job.Succeeded))}
		severity, symptom := model.Info, ""
		if failure := trueCondition(job.Conditions, "Failed", "FailureTarget"); failure != nil {
			symptom = "The Job controller reports an unsuccessful execution"
			evidence = appendBriefCondition(evidence, failure)
			if observedWithin(s.CollectedAt, failure.Transition, warningHorizon) {
				severity = model.Warning
			}
		} else if job.Suspended || trueCondition(job.Conditions, "Suspended") != nil || job.Parallelism != nil && *job.Parallelism == 0 {
			continue
		} else if job.DeadlineSeconds != nil && *job.DeadlineSeconds > 0 && !job.Started.IsZero() && job.Active > 0 &&
			int64(s.CollectedAt.Sub(job.Started)/time.Second) >= *job.DeadlineSeconds {
			symptom = "The observed active Job has reached its configured execution deadline while awaiting controller status"
			severity = model.Warning
		} else if job.Failed > 0 {
			symptom = "The Job reports failed attempts without a terminal outcome"
		} else if job.Started.IsZero() && agedEnough(s.CollectedAt, job.Created, startupGrace) {
			workloadUncertain(&check, "At least one unsuspended Job has no observed start or terminal status")
		}
		if symptom == "" {
			continue
		}
		evidence = appendObservedTime(evidence, "job_started", job.Started)
		if job.DeadlineSeconds != nil {
			evidence = append(evidence, countEvidence("active_deadline_seconds", *job.DeadlineSeconds))
		}
		if job.BackoffLimit != nil {
			evidence = append(evidence, countEvidence("backoff_limit", int64(*job.BackoffLimit)))
		}
		finding := workloadFinding(jobRule, job.Resource, severity, symptom,
			"Inspect this execution's controller conditions and owned Pods. The failed Pod count can differ from the controller's retry accounting.", evidence...)
		finding.Incident = job.Owner
		findings = append(findings, finding)
	}
	return findings, check
}

func analyzeCronJobs(s model.Snapshot, index workloadIndex) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, cronJobRule, model.SourceCronJobs)
	children := map[model.Resource][]model.JobEvidence{}
	for _, job := range s.Expansion.Jobs {
		if job.Owner != nil && job.Owner.UID != "" {
			children[*job.Owner] = append(children[*job.Owner], job)
		}
	}
	var findings []model.Finding
	for _, cron := range s.Expansion.CronJobs {
		if cron.Deleting {
			continue
		}
		if s.Coverage(model.SourceJobs) != model.Complete || cron.ActiveTruncated || s.Coverage(model.SourceEvents) != model.Complete {
			workloadUncertain(&check, "CronJob child outcomes or scheduling events are incompletely visible")
		}
		evidence := []model.Evidence{countEvidence("reported_active_jobs", int64(len(cron.ActiveJobs)))}
		if cron.Concurrency != "" {
			evidence = append(evidence, workloadEvidence("concurrency_policy", cron.Concurrency))
		}
		evidence = appendObservedTime(evidence, "last_scheduled", cron.LastScheduled)
		evidence = appendObservedTime(evidence, "last_successful", cron.LastSuccessful)
		if cron.DeadlineSeconds != nil {
			evidence = append(evidence, countEvidence("starting_deadline_seconds", *cron.DeadlineSeconds))
		}
		symptom := ""
		severity := model.Info
		if cron.Suspended {
			symptom = "The CronJob is suspended for future schedules"
			evidence = append(evidence, workloadEvidence("suspended", "true"))
		} else if event, ok := index.recentEvent(cron.Resource, cron.Created, "MissSchedule", "TooManyMissedTimes", "UnknownTimeZone", "FailedCreate"); ok && !event.Time.Before(cron.LastScheduled) {
			symptom, severity = "The CronJob controller recently reported a scheduling or creation problem", model.Warning
			evidence = addEventEvidence(evidence, event)
		} else if event, ok := index.recentEvent(cron.Resource, cron.Created, "JobAlreadyActive"); ok && cron.Concurrency == "Forbid" {
			symptom = "The CronJob skipped an overlapping occurrence under Forbid concurrency"
			evidence = addEventEvidence(evidence, event)
		}
		var newest *model.BriefCondition
		var recentChild model.JobEvidence
		lastSuccess := cron.LastSuccessful
		for _, child := range children[cron.Resource] {
			if completion := trueCondition(child.Conditions, "Complete"); completion != nil {
				for _, timestamp := range []time.Time{child.Completed, completion.Transition} {
					if timestamp.After(lastSuccess) && !timestamp.After(s.CollectedAt) {
						lastSuccess = timestamp
					}
				}
			}
		}
		for _, child := range children[cron.Resource] {
			if child.Deleting || trueCondition(child.Conditions, "Complete", "SuccessCriteriaMet") != nil {
				continue
			}
			failure := trueCondition(child.Conditions, "Failed", "FailureTarget")
			if failure == nil || !observedWithin(s.CollectedAt, failure.Transition, warningHorizon) || !failure.Transition.After(lastSuccess) {
				continue
			}
			if newest == nil || failure.Transition.After(newest.Transition) || failure.Transition.Equal(newest.Transition) && child.Resource.Name < recentChild.Resource.Name {
				newest, recentChild = failure, child
			}
		}
		if newest != nil {
			evidence = append(evidence, workloadEvidence("failed_owned_job", recentChild.Resource.Name))
			evidence = appendBriefCondition(evidence, newest)
			if symptom == "" || severity == model.Info && !cron.Suspended {
				symptom = "The CronJob has a recent failed owned execution after its last recorded success"
			}
			severity = model.Warning
		}
		if symptom != "" {
			findings = append(findings, workloadFinding(cronJobRule, cron.Resource, severity, symptom,
				"Review the schedule policy, timestamps, and owned Jobs. An observed skipped occurrence can follow the configured concurrency policy.", evidence...))
		}
	}
	return findings, check
}

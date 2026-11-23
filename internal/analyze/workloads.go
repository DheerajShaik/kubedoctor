package analyze

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	nodeReadinessRule = "node-readiness"
	nodePressureRule  = "node-pressure"
	podContainersRule = "pod-containers"
	podSchedulingRule = "pod-scheduling"
	deploymentRule    = "deployment-rollout"
	daemonSetRule     = "daemonset-rollout"
	configurationRule = "configuration-references"
	pvcBindingRule    = "pvc-binding"
	resourcesRule     = "container-resources"
	metricsRule       = "current-pod-usage"
	warningEventsRule = "recent-warning-events"

	startupGrace   = 5 * time.Minute
	warningHorizon = time.Hour
	oomHorizon     = 30 * time.Minute
)

var workloadDefinitions = []model.CheckDefinition{
	{ID: nodeReadinessRule, Description: "Observe Node Ready status and transition timing"},
	{ID: nodePressureRule, Description: "Observe active Node disk, memory, and PID pressure"},
	{ID: podContainersRule, Description: "Inspect Pod and init container waiting, termination, restart, and OOM evidence"},
	{ID: podSchedulingRule, Description: "Correlate pending Pods with scheduler conditions and recent events"},
	{ID: deploymentRule, Description: "Evaluate Deployment generation, availability, and rollout progress"},
	{ID: daemonSetRule, Description: "Evaluate DaemonSet generation, availability, and rollout progress"},
	{ID: configurationRule, Description: "Check required ConfigMap and Secret references using available evidence"},
	{ID: pvcBindingRule, Description: "Inspect PVC binding and distinguish expected delayed binding"},
	{ID: resourcesRule, Description: "Advise on absent CPU and memory requests and limits"},
	{ID: metricsRule, Description: "Report recent current CPU and memory samples with their measurement windows"},
	{ID: warningEventsRule, Description: "Correlate recent classified warnings with observed resources"},
}

func analyzeWorkloads(s model.Snapshot) ([]model.Finding, []model.CheckResult) {
	index := newWorkloadIndex(s)
	findings, checks := analyzeNodes(s)
	for _, check := range []workloadAnalysis{
		workloadResult(analyzeContainers(s, index)),
		workloadResult(analyzeScheduling(s, index)),
		workloadResult(analyzeDeployments(s)),
		workloadResult(analyzeDaemonSets(s)),
		workloadResult(analyzeConfiguration(s, index)),
		workloadResult(analyzePVCs(s, index)),
		workloadResult(analyzeResourceSettings(s, index)),
		workloadResult(analyzeMetrics(s, index)),
	} {
		findings = append(findings, check.findings...)
		checks = append(checks, check.status)
	}
	expanded, expandedChecks := analyzeExpansion(s, index)
	findings = append(findings, expanded...)
	checks = append(checks, expandedChecks...)
	eventFindings, eventCheck := analyzeWarningEvents(s, index, findings)
	return append(findings, eventFindings...), append(checks, eventCheck)
}

type workloadAnalysis struct {
	findings []model.Finding
	status   model.CheckResult
}

func workloadResult(findings []model.Finding, status model.CheckResult) workloadAnalysis {
	return workloadAnalysis{findings: findings, status: status}
}

func workloadCoverage(s model.Snapshot, id string, source model.Source) model.CheckResult {
	result := model.CheckResult{ID: id, Status: model.Checked}
	switch s.Coverage(source) {
	case model.Complete:
	case model.NotApplicable:
		result.Status = model.Skipped
		result.Detail = fmt.Sprintf("%s is outside the applicable scope", source)
	default:
		result.Status = model.Inconclusive
		result.Detail = fmt.Sprintf("%s visibility is %s", source, s.Coverage(source))
	}
	return result
}

func workloadUncertain(check *model.CheckResult, detail string) {
	if check.Status == model.Checked {
		check.Status = model.Inconclusive
		check.Detail = detail
	}
}

func workloadEvidence(field, value string) model.Evidence {
	return model.Evidence{Field: field, Value: value}
}

func countEvidence(field string, count int64) model.Evidence {
	return workloadEvidence(field, strconv.FormatInt(count, 10))
}

func workloadFinding(rule string, id model.Resource, severity model.Severity, symptom, next string, evidence ...model.Evidence) model.Finding {
	return model.Finding{
		RuleID: rule, Resource: id, Severity: severity, Symptom: symptom,
		Confidence: model.Observed, NextSteps: []string{next}, Evidence: evidence,
	}
}

func appendObservedTime(evidence []model.Evidence, field string, observed time.Time) []model.Evidence {
	if observed.IsZero() {
		return evidence
	}
	return append(evidence, workloadEvidence(field, observed.UTC().Format(time.RFC3339)))
}

func observedWithin(now, observed time.Time, window time.Duration) bool {
	return !now.IsZero() && !observed.IsZero() && !observed.After(now) && now.Sub(observed) <= window
}

func agedEnough(now, observed time.Time, threshold time.Duration) bool {
	return !now.IsZero() && !observed.IsZero() && now.Sub(observed) >= threshold
}

func analyzeNodes(s model.Snapshot) ([]model.Finding, []model.CheckResult) {
	readiness := workloadCoverage(s, nodeReadinessRule, model.SourceNodes)
	pressure := workloadCoverage(s, nodePressureRule, model.SourceNodes)
	var findings []model.Finding
	for _, n := range s.Nodes {
		id := model.Identity("Node", &n)
		var ready *core.NodeCondition
		var observedPressure, activePressure int
		var conditions []model.Evidence
		for _, c := range n.Status.Conditions {
			switch c.Type {
			case core.NodeReady:
				ready = &c
			case core.NodeMemoryPressure, core.NodeDiskPressure, core.NodePIDPressure:
				if c.Status == core.ConditionTrue || c.Status == core.ConditionFalse {
					observedPressure++
				}
				if c.Status == core.ConditionTrue {
					activePressure++
					conditions = append(conditions, workloadEvidence(string(c.Type), "True"))
					conditions = appendObservedTime(conditions, string(c.Type)+"_since", c.LastTransitionTime.Time)
				}
			}
		}
		if observedPressure < 3 {
			workloadUncertain(&pressure, "One or more standard Node pressure conditions are missing or unknown")
		}
		if activePressure > 0 {
			findings = append(findings, workloadFinding(nodePressureRule, id, model.Warning,
				"The node reports active resource pressure",
				"Inspect the reported resource at the node. Sustained pressure can trigger eviction and affect scheduling.", conditions...))
		}
		if ready == nil || (ready.Status != core.ConditionTrue && ready.Status != core.ConditionFalse && ready.Status != core.ConditionUnknown) {
			workloadUncertain(&readiness, "One or more Nodes have no usable Ready condition")
			continue
		}
		if ready.Status == core.ConditionTrue {
			continue
		}
		evidence := []model.Evidence{workloadEvidence("Ready", string(ready.Status))}
		evidence = appendObservedTime(evidence, "last_transition", ready.LastTransitionTime.Time)
		switch ready.Reason {
		case "KubeletNotReady", "NodeStatusUnknown", "NodeStatusNeverUpdated":
			evidence = append(evidence, workloadEvidence("condition_reason", ready.Reason))
		}
		severity, symptom := model.Critical, "The node reports Ready=False"
		if ready.Status == core.ConditionUnknown {
			severity, symptom = model.Warning, "The node controller cannot confirm node readiness"
			workloadUncertain(&readiness, "One or more Nodes report Ready=Unknown")
		}
		findings = append(findings, workloadFinding(nodeReadinessRule, id, severity, symptom,
			"Inspect the node condition and kubelet or control-plane reachability. Workloads on this node may be affected.", evidence...))
	}
	return findings, []model.CheckResult{readiness, pressure}
}

type workloadLocator struct{ kind, namespace, name string }

func locateWorkload(id model.Resource) workloadLocator {
	return workloadLocator{id.Kind, id.Namespace, id.Name}
}

type workloadIndex struct {
	events      map[workloadLocator][]model.Event
	replicaSets map[string]*apps.ReplicaSet
}

func newWorkloadIndex(s model.Snapshot) workloadIndex {
	index := workloadIndex{
		events:      make(map[workloadLocator][]model.Event),
		replicaSets: make(map[string]*apps.ReplicaSet, len(s.ReplicaSets)),
	}
	for i := range s.ReplicaSets {
		rs := &s.ReplicaSets[i]
		if rs.UID != "" {
			index.replicaSets[string(rs.UID)] = rs
		}
	}
	for _, event := range s.Events {
		if observedWithin(s.CollectedAt, event.Time, warningHorizon) {
			key := locateWorkload(event.Resource)
			index.events[key] = append(index.events[key], event)
		}
	}
	for _, events := range index.events {
		slices.SortFunc(events, func(a, b model.Event) int {
			if order := cmp.Or(b.Time.Compare(a.Time), cmp.Compare(a.Reason, b.Reason),
				cmp.Compare(b.Count, a.Count), cmp.Compare(b.Resource.UID, a.Resource.UID)); order != 0 {
				return order
			}
			if a.Reference == nil || b.Reference == nil {
				switch {
				case a.Reference != nil:
					return -1
				case b.Reference != nil:
					return 1
				default:
					return 0
				}
			}
			return cmp.Or(cmp.Compare(a.Reference.Kind, b.Reference.Kind), cmp.Compare(a.Reference.Name, b.Reference.Name),
				cmp.Compare(a.Reference.Missing, b.Reference.Missing), cmp.Compare(a.Reference.Key, b.Reference.Key))
		})
	}
	return index
}

func eventMatches(event model.Event, resource model.Resource, created time.Time) bool {
	if !created.IsZero() && event.Time.Before(created) {
		return false
	}
	if event.Resource.UID != "" && resource.UID != "" {
		return event.Resource.UID == resource.UID
	}
	return !created.IsZero()
}

func (index workloadIndex) recentEvent(id model.Resource, created time.Time, reasons ...string) (model.Event, bool) {
	for _, event := range index.events[locateWorkload(id)] {
		if eventMatches(event, id, created) {
			for _, reason := range reasons {
				if event.Reason == reason {
					return event, true
				}
			}
		}
	}
	return model.Event{}, false
}

func addEventEvidence(evidence []model.Evidence, event model.Event) []model.Evidence {
	evidence = append(evidence, workloadEvidence("event_reason", event.Reason))
	evidence = appendObservedTime(evidence, "event_last_seen", event.Time)
	if event.Count > 0 {
		evidence = append(evidence, countEvidence("event_count", int64(event.Count)))
	}
	return evidence
}

func (index workloadIndex) podIncident(pod *core.Pod) *model.Resource {
	owner := metav1.GetControllerOfNoCopy(pod)
	if owner == nil || owner.UID == "" {
		return nil
	}
	if owner.Kind == "ReplicaSet" {
		if replicaSet, ok := index.replicaSets[string(owner.UID)]; ok && replicaSet.Namespace == pod.Namespace && replicaSet.Name == owner.Name {
			if parent := metav1.GetControllerOfNoCopy(replicaSet); parent != nil && parent.Kind == "Deployment" && parent.UID != "" {
				owner = parent
			}
		}
	}
	switch owner.Kind {
	case "ReplicaSet", "Deployment", "DaemonSet", "StatefulSet", "Job", "ReplicationController":
		return &model.Resource{Kind: owner.Kind, Namespace: pod.Namespace, Name: owner.Name, UID: string(owner.UID)}
	default:
		return nil
	}
}

func activePod(pod *core.Pod) bool {
	return pod.DeletionTimestamp == nil && pod.Status.Phase != core.PodSucceeded
}

func analyzeContainers(s model.Snapshot, index workloadIndex) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, podContainersRule, model.SourcePods)
	var findings []model.Finding
	for i := range s.Pods {
		pod := &s.Pods[i]
		if !activePod(pod) {
			continue
		}
		if pod.Spec.NodeName != "" || pod.Status.Phase == core.PodRunning {
			if len(pod.Status.ContainerStatuses) < len(pod.Spec.Containers) || len(pod.Status.InitContainerStatuses) < len(pod.Spec.InitContainers) {
				workloadUncertain(&check, "One or more scheduled Pods have incomplete container status")
			}
		}
		id := model.Identity("Pod", pod)
		incident := index.podIncident(pod)
		for _, group := range []struct {
			role     string
			statuses []core.ContainerStatus
		}{{"init", pod.Status.InitContainerStatuses}, {"application", pod.Status.ContainerStatuses}} {
			for _, status := range group.statuses {
				finding, unknown := evaluateContainer(s.CollectedAt, pod, status, group.role, id, index)
				if unknown {
					workloadUncertain(&check, "A container is waiting or has an unclassified state with limited evidence")
				}
				if finding != nil {
					finding.Incident = incident
					findings = append(findings, *finding)
				}
			}
		}
	}
	return findings, check
}

func evaluateContainer(now time.Time, pod *core.Pod, status core.ContainerStatus, role string, id model.Resource, index workloadIndex) (*model.Finding, bool) {
	evidence := []model.Evidence{workloadEvidence("container", status.Name), workloadEvidence("container_role", role)}
	if status.RestartCount > 0 {
		evidence = append(evidence, countEvidence("restart_count", int64(status.RestartCount)))
	}
	last := status.LastTerminationState.Terminated
	recentOOM := last != nil && last.Reason == "OOMKilled" && observedWithin(now, last.FinishedAt.Time, oomHorizon)
	if recentOOM {
		evidence = append(evidence, workloadEvidence("last_termination_reason", "OOMKilled"))
		evidence = appendObservedTime(evidence, "last_termination_finished", last.FinishedAt.Time)
	}
	if w := status.State.Waiting; w != nil {
		symptom, next, corroborating := waitingDiagnostic(w.Reason)
		if symptom != "" {
			evidence = append(evidence, workloadEvidence("waiting_reason", w.Reason))
			if corroborating != "" {
				if event, ok := index.recentEvent(id, pod.CreationTimestamp.Time, corroborating); ok {
					evidence = addEventEvidence(evidence, event)
				}
			}
			finding := workloadFinding(podContainersRule, id, model.Critical, symptom, next, evidence...)
			return &finding, false
		}
		if (w.Reason == "ContainerCreating" || w.Reason == "PodInitializing") && agedEnough(now, pod.CreationTimestamp.Time, startupGrace) {
			evidence = append(evidence, workloadEvidence("waiting_reason", w.Reason))
			evidence = appendObservedTime(evidence, "pod_created", pod.CreationTimestamp.Time)
			finding := workloadFinding(podContainersRule, id, model.Info, "The container is still waiting for startup",
				"Inspect Pod conditions and recent events for mount, sandbox, or initialization evidence.", evidence...)
			return &finding, true
		}
	}
	if t := status.State.Terminated; t != nil && (t.ExitCode != 0 || t.Reason == "OOMKilled") {
		evidence = append(evidence, countEvidence("exit_code", int64(t.ExitCode)))
		if t.Signal > 0 {
			evidence = append(evidence, countEvidence("signal", int64(t.Signal)))
		}
		evidence = appendObservedTime(evidence, "termination_finished", t.FinishedAt.Time)
		severity := model.Warning
		symptom := "The container's current state is an unsuccessful termination"
		next := "Review the exit code, workload lifecycle, and available application diagnostics. An exit code alone does not identify the cause."
		switch t.Reason {
		case "Error", "ContainerCannotRun", "DeadlineExceeded", "ContainerStatusUnknown", "StartError", "OOMKilled":
			evidence = append(evidence, workloadEvidence("termination_reason", t.Reason))
		}
		if t.Reason == "OOMKilled" {
			symptom = "The container's current state is a recorded OOM kill"
			next = "Review the container's memory settings and the node's memory pressure around the termination."
		}
		if !t.FinishedAt.IsZero() && !observedWithin(now, t.FinishedAt.Time, oomHorizon) {
			severity = model.Info
		}
		finding := workloadFinding(podContainersRule, id, severity, symptom, next, evidence...)
		return &finding, false
	}
	if recentOOM {
		symptom := "The container previously recorded an OOM kill"
		if status.State.Running != nil {
			symptom = "The container is running after a recent recorded OOM kill"
		}
		finding := workloadFinding(podContainersRule, id, model.Warning, symptom,
			"Correlate the past termination with container limits and memory observations before changing resource settings.", evidence...)
		return &finding, false
	}
	if status.RestartCount >= 3 {
		finding := workloadFinding(podContainersRule, id, model.Info,
			"The container has restarted repeatedly since this Pod was observed by the kubelet",
			"Check the restart timing and reason. This cumulative counter cannot establish an ongoing restart loop.", evidence...)
		return &finding, false
	}
	if status.State.Waiting != nil {
		return nil, status.State.Waiting.Reason != "ContainerCreating" && status.State.Waiting.Reason != "PodInitializing"
	}
	return nil, status.State.Running == nil && status.State.Terminated == nil
}

func waitingDiagnostic(reason string) (symptom, next, event string) {
	switch reason {
	case "ErrImagePull", "ImagePullBackOff", "ErrImageNeverPull", "InvalidImageName", "RegistryUnavailable":
		return "The container cannot start because image acquisition is failing",
			"Check the image reference, registry reachability, and image pull authentication from the node.", ""
	case "ImageInspectError":
		return "The kubelet reports an image inspection failure",
			"Check the configured image and inspect the hosting node's container runtime diagnostics.", ""
	case "CrashLoopBackOff":
		return "The container is waiting in CrashLoopBackOff",
			"Inspect the previous termination evidence and permitted workload diagnostics to identify the crash cause.", "BackOff"
	case "CreateContainerConfigError":
		return "The container is waiting because its configuration could not be applied",
			"Check the required configuration references and recent Pod events for the specific startup failure.", ""
	case "CreateContainerError", "RunContainerError", "StartError", "ContainerCannotRun":
		return "The container runtime reports a startup failure",
			"Inspect Pod events and runtime diagnostics for this startup reason.", ""
	default:
		return "", "", ""
	}
}

func analyzeScheduling(s model.Snapshot, index workloadIndex) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, podSchedulingRule, model.SourcePods)
	var findings []model.Finding
	for i := range s.Pods {
		pod := &s.Pods[i]
		if !activePod(pod) || pod.Status.Phase != core.PodPending || pod.Spec.NodeName != "" || len(pod.Spec.SchedulingGates) > 0 {
			continue
		}
		var unschedulable, scheduledOrGated bool
		var schedulingTransition time.Time
		for _, condition := range pod.Status.Conditions {
			if condition.Type == core.PodScheduled {
				unschedulable = condition.Status == core.ConditionFalse && condition.Reason == core.PodReasonUnschedulable
				scheduledOrGated = condition.Status == core.ConditionTrue || condition.Reason == core.PodReasonSchedulingGated
				schedulingTransition = condition.LastTransitionTime.Time
			}
		}
		if scheduledOrGated {
			continue
		}
		id := model.Identity("Pod", pod)
		event, corroborated := index.recentEvent(id, pod.CreationTimestamp.Time, "FailedScheduling")
		if s.Coverage(model.SourceEvents) != model.Complete {
			workloadUncertain(&check, "Scheduler event visibility is limited for pending Pods")
		}
		if !unschedulable && !corroborated {
			if agedEnough(s.CollectedAt, pod.CreationTimestamp.Time, startupGrace) {
				workloadUncertain(&check, "A Pod remains unassigned without a conclusive scheduler condition or recent event")
			}
			continue
		}
		evidence := []model.Evidence{workloadEvidence("phase", "Pending")}
		if unschedulable {
			evidence = append(evidence, workloadEvidence("pod_scheduled", "False"), workloadEvidence("condition_reason", "Unschedulable"))
			evidence = appendObservedTime(evidence, "scheduling_last_transition", schedulingTransition)
		}
		if corroborated {
			evidence = addEventEvidence(evidence, event)
		}
		finding := workloadFinding(podSchedulingRule, id, model.Warning, "The pending Pod has documented scheduler difficulty placing it",
			"Inspect the Pod's scheduler condition and correlated event in Kubernetes to identify which constraint prevented placement.", evidence...)
		finding.Incident = index.podIncident(pod)
		findings = append(findings, finding)
	}
	return findings, check
}

func analyzeDeployments(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, deploymentRule, model.SourceDeployments)
	var findings []model.Finding
	for i := range s.Deployments {
		d := &s.Deployments[i]
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		if desired == 0 || d.Spec.Paused || d.DeletionTimestamp != nil {
			continue
		}
		if d.Status.ObservedGeneration < d.Generation {
			workloadUncertain(&check, "A Deployment has not reported status for its current generation")
			continue
		}
		if d.Status.AvailableReplicas >= desired && d.Status.UpdatedReplicas >= desired {
			continue
		}
		evidence := rolloutCounts(desired, d.Status.AvailableReplicas, d.Status.UpdatedReplicas)
		deadline := 10 * time.Minute
		if d.Spec.ProgressDeadlineSeconds != nil && *d.Spec.ProgressDeadlineSeconds > 0 {
			deadline = time.Duration(*d.Spec.ProgressDeadlineSeconds) * time.Second
		}
		var progress *apps.DeploymentCondition
		for _, condition := range d.Status.Conditions {
			if condition.Type == apps.DeploymentProgressing {
				progress = &condition
			}
		}
		deadlineExceeded := progress != nil && progress.Status == core.ConditionFalse && progress.Reason == "ProgressDeadlineExceeded"
		knownAge := d.Generation <= 1 && agedEnough(s.CollectedAt, d.CreationTimestamp.Time, deadline)
		if progress != nil && !progress.LastUpdateTime.IsZero() {
			knownAge = agedEnough(s.CollectedAt, progress.LastUpdateTime.Time, deadline)
			evidence = appendObservedTime(evidence, "progress_last_updated", progress.LastUpdateTime.Time)
		} else if knownAge {
			evidence = appendObservedTime(evidence, "deployment_created", d.CreationTimestamp.Time)
		}
		if !deadlineExceeded && !knownAge {
			if (progress == nil || progress.LastUpdateTime.IsZero()) && (d.Generation > 1 || d.CreationTimestamp.IsZero()) {
				workloadUncertain(&check, "A Deployment has a replica deficit without usable rollout timing")
			}
			continue
		}
		severity := model.Warning
		if d.Status.AvailableReplicas == 0 && deadlineExceeded {
			severity = model.Critical
		}
		symptom := "The Deployment remains below its desired available or updated replicas"
		if deadlineExceeded {
			symptom = "The Deployment controller reports ProgressDeadlineExceeded"
			evidence = append(evidence, workloadEvidence("condition_reason", "ProgressDeadlineExceeded"))
		}
		evidence = append(evidence, countEvidence("progress_deadline_seconds", int64(deadline/time.Second)))
		id := model.Identity("Deployment", d)
		finding := workloadFinding(deploymentRule, id, severity, symptom,
			"Inspect the rollout status and Pod or ReplicaSet findings to identify which replicas are unavailable.", evidence...)
		findings = append(findings, finding)
	}
	return findings, check
}

func analyzeDaemonSets(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, daemonSetRule, model.SourceDaemonSets)
	var findings []model.Finding
	for i := range s.DaemonSets {
		d := &s.DaemonSets[i]
		if d.DeletionTimestamp != nil {
			continue
		}
		if d.Status.ObservedGeneration < d.Generation {
			workloadUncertain(&check, "A DaemonSet has not reported status for its current generation")
			continue
		}
		desired := d.Status.DesiredNumberScheduled
		needsUpdates := d.Spec.UpdateStrategy.Type != apps.OnDeleteDaemonSetStrategyType && d.Status.UpdatedNumberScheduled < desired
		if desired == 0 || (d.Status.NumberAvailable >= desired && !needsUpdates) {
			continue
		}
		grace := max(startupGrace, time.Duration(d.Spec.MinReadySeconds)*time.Second)
		ageEvidence := d.CreationTimestamp.Time
		if d.Generation > 1 {
			ageEvidence = time.Time{}
		}
		if !agedEnough(s.CollectedAt, ageEvidence, grace) {
			if ageEvidence.IsZero() {
				workloadUncertain(&check, "A DaemonSet has a replica deficit without usable rollout timing")
			}
			// The current deficit is reportable without asserting stalled progress.
			if d.Status.NumberAvailable >= desired || !ageEvidence.IsZero() {
				continue
			}
		}
		evidence := rolloutCounts(desired, d.Status.NumberAvailable, d.Status.UpdatedNumberScheduled)
		evidence = append(evidence, countEvidence("ready", int64(d.Status.NumberReady)))
		symptom, severity := "The DaemonSet reports fewer available or updated Pods than desired after its initial startup grace", model.Warning
		if ageEvidence.IsZero() {
			symptom, severity = "The DaemonSet currently has fewer available Pods than desired and rollout timing is unavailable", model.Info
		} else {
			evidence = appendObservedTime(evidence, "daemonset_created", ageEvidence)
		}
		findings = append(findings, workloadFinding(daemonSetRule, model.Identity("DaemonSet", d), severity, symptom,
			"Inspect DaemonSet rollout status and the readiness of its Pods on the targeted nodes.", evidence...))
	}
	return findings, check
}

func rolloutCounts(desired, available, updated int32) []model.Evidence {
	return []model.Evidence{
		countEvidence("desired", int64(desired)),
		countEvidence("available", int64(available)),
		countEvidence("updated", int64(updated)),
	}
}

func warningReason(reason string) bool {
	switch reason {
	case "FailedScheduling", "FailedMount", "FailedAttachVolume", "FailedBinding", "ProvisioningFailed",
		"FailedCreate", "FailedDelete", "Failed", "BackOff", "Unhealthy", "Evicted", "NodeNotReady",
		"FailedCreatePodSandBox", "FailedToRetrieveImagePullSecret", "FailedMapVolume":
		return true
	default:
		return false
	}
}

type warningTarget struct {
	id       model.Resource
	created  time.Time
	incident *model.Resource
}

func analyzeWarningEvents(s model.Snapshot, index workloadIndex, handled []model.Finding) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, warningEventsRule, model.SourceEvents)
	used := make(map[model.Resource]map[string]bool)
	for _, finding := range handled {
		for _, evidence := range finding.Evidence {
			if evidence.Field == "event_reason" {
				if used[finding.Resource] == nil {
					used[finding.Resource] = make(map[string]bool)
				}
				used[finding.Resource][evidence.Value] = true
			}
		}
	}
	var targets []warningTarget
	add := func(kind string, object metav1.Object, incident *model.Resource) {
		targets = append(targets, warningTarget{model.Identity(kind, object), object.GetCreationTimestamp().Time, incident})
	}
	for i := range s.Pods {
		add("Pod", &s.Pods[i], index.podIncident(&s.Pods[i]))
	}
	for i := range s.Nodes {
		add("Node", &s.Nodes[i], nil)
	}
	for i := range s.Deployments {
		add("Deployment", &s.Deployments[i], nil)
	}
	for i := range s.ReplicaSets {
		add("ReplicaSet", &s.ReplicaSets[i], nil)
	}
	for i := range s.DaemonSets {
		add("DaemonSet", &s.DaemonSets[i], nil)
	}
	for i := range s.PVCs {
		add("PersistentVolumeClaim", &s.PVCs[i], nil)
	}
	for i := range s.Services {
		add("Service", &s.Services[i], nil)
	}
	for i := range s.Ingresses {
		add("Ingress", &s.Ingresses[i], nil)
	}
	for i := range s.PDBs {
		add("PodDisruptionBudget", &s.PDBs[i], nil)
	}
	var findings []model.Finding
	for _, target := range targets {
		seen := make(map[string]bool)
		var evidence []model.Evidence
		for _, event := range index.events[locateWorkload(target.id)] {
			if seen[event.Reason] || used[target.id][event.Reason] || !warningReason(event.Reason) || !eventMatches(event, target.id, target.created) {
				continue
			}
			seen[event.Reason] = true
			evidence = addEventEvidence(evidence, event)
			if len(seen) == 8 {
				break
			}
		}
		if len(evidence) == 0 {
			continue
		}
		finding := workloadFinding(warningEventsRule, target.id, model.Info, "Kubernetes recorded recent warnings for this resource",
			"Use the classified reason and last observed time to inspect the warning in context. Its continued impact may differ from the event.", evidence...)
		finding.Incident = target.incident
		findings = append(findings, finding)
	}
	return findings, check
}

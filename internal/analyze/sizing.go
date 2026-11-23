package analyze

import (
	"math"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func analyzeResourceSettings(s model.Snapshot, index workloadIndex) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, resourcesRule, model.SourcePods)
	var findings []model.Finding
	for i := range s.Pods {
		pod := &s.Pods[i]
		if !activePod(pod) || pod.Status.Phase == core.PodFailed {
			continue
		}
		id := model.Identity("Pod", pod)
		for _, group := range []struct {
			role       string
			containers []core.Container
		}{{"init", pod.Spec.InitContainers}, {"application", pod.Spec.Containers}} {
			for _, container := range group.containers {
				evidence := []model.Evidence{workloadEvidence("container", container.Name), workloadEvidence("container_role", group.role)}
				if lacksResource(container.Resources, pod.Spec.Resources, core.ResourceCPU, true) {
					evidence = append(evidence, workloadEvidence("cpu_request", "unset or zero"))
				}
				if lacksResource(container.Resources, pod.Spec.Resources, core.ResourceMemory, true) {
					evidence = append(evidence, workloadEvidence("memory_request", "unset or zero"))
				}
				if lacksResource(container.Resources, pod.Spec.Resources, core.ResourceCPU, false) {
					evidence = append(evidence, workloadEvidence("cpu_limit", "unset or zero"))
				}
				if lacksResource(container.Resources, pod.Spec.Resources, core.ResourceMemory, false) {
					evidence = append(evidence, workloadEvidence("memory_limit", "unset or zero"))
				}
				if len(evidence) == 2 {
					continue
				}
				finding := workloadFinding(resourcesRule, id, model.Info, "The container has unspecified CPU or memory requests or limits",
					"Review these settings against workload needs and namespace policy. Requests affect scheduling and limits constrain resource use.", evidence...)
				finding.Incident = index.podIncident(pod)
				findings = append(findings, finding)
			}
		}
	}
	return findings, check
}

func lacksResource(container core.ResourceRequirements, pod *core.ResourceRequirements, name core.ResourceName, request bool) bool {
	resources := container.Limits
	if request {
		resources = container.Requests
	}
	if value := resources[name]; value.Sign() > 0 {
		return false
	}
	if pod == nil {
		return true
	}
	shared := pod.Limits
	if request {
		shared = pod.Requests
	}
	value := shared[name]
	return value.Sign() <= 0
}

func analyzeMetrics(s model.Snapshot, index workloadIndex) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, metricsRule, model.SourceMetrics)
	if s.Coverage(model.SourcePods) != model.Complete {
		workloadUncertain(&check, "Pod inventory is incomplete for associating metrics")
	}
	byPod := make(map[configLocator]model.PodUsage, len(s.Metrics))
	for _, sample := range s.Metrics {
		key := configLocator{sample.Namespace, sample.Name}
		if previous, exists := byPod[key]; !exists || sample.Timestamp.After(previous.Timestamp) {
			byPod[key] = sample
		}
	}
	var findings []model.Finding
	for i := range s.Pods {
		pod := &s.Pods[i]
		if !activePod(pod) || pod.Status.Phase != core.PodRunning {
			continue
		}
		id := model.Identity("Pod", pod)
		sample, ok := byPod[configLocator{pod.Namespace, pod.Name}]
		if !ok || sample.Window <= 0 || !observedWithin(s.CollectedAt, sample.Timestamp, 5*time.Minute) || pod.CreationTimestamp.IsZero() || sample.Timestamp.Add(-sample.Window).Before(pod.CreationTimestamp.Time) {
			workloadUncertain(&check, "At least one running Pod lacks a fresh Metrics API sample attributable to its lifetime")
			continue
		}
		matches := make(map[string]bool)
		for _, group := range [][]core.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
			for _, status := range group {
				if status.State.Running != nil && (status.State.Running.StartedAt.IsZero() || !sample.Timestamp.Add(-sample.Window).Before(status.State.Running.StartedAt.Time)) {
					matches[status.Name] = false
				}
			}
		}
		for _, usage := range sample.Containers {
			seen, matchesContainer := matches[usage.Name]
			if !matchesContainer || seen {
				continue
			}
			if !boundedUsage(&usage.CPU, 1_000_000) || !boundedUsage(&usage.Memory, math.MaxInt64) {
				continue
			}
			matches[usage.Name] = true
			evidence := []model.Evidence{
				workloadEvidence("container", usage.Name),
				workloadEvidence("cpu_cores", usage.CPU.AsDec().String()),
				countEvidence("memory_bytes", usage.Memory.Value()),
				workloadEvidence("measurement_window", sample.Window.String()),
			}
			evidence = appendObservedTime(evidence, "sample_timestamp", sample.Timestamp)
			finding := workloadFinding(metricsRule, id, model.Info, "The Metrics API reports a recent CPU and memory sample",
				"Use the timestamp and measurement window to place the observation in context. For sizing, collect usage history over a representative interval.", evidence...)
			finding.Incident = index.podIncident(pod)
			findings = append(findings, finding)
		}
		if len(matches) == 0 {
			workloadUncertain(&check, "A Metrics API sample has no container status that covers its measurement window")
		}
		for _, used := range matches {
			if !used {
				workloadUncertain(&check, "At least one running container has no usable Metrics API sample")
				break
			}
		}
	}
	return findings, check
}

func boundedUsage(quantity *resource.Quantity, maxValue int64) bool {
	value := quantity.AsDec()
	// Screen the representation before comparing, formatting or scaling it.
	// A signed 64-bit integer with nine fractional digits fits in 93 bits.
	return value.Sign() >= 0 && value.Scale() >= -18 && value.Scale() <= 9 &&
		value.UnscaledBig().BitLen() <= 93 && quantity.CmpInt64(maxValue) <= 0
}

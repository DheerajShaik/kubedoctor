package history

import (
	"math"
	"slices"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
)

const (
	coverageThreshold = 0.90
	minimumScrapes    = 25
	mebibyte          = 1 << 20
)

// Analyze reproduces history statistics and advice from normalized evidence.
// A nil result means no history provider was configured for this capture.
func Analyze(s model.Snapshot) *model.HistoryReport {
	h := s.History
	if h == nil {
		return nil
	}
	report := &model.HistoryReport{
		SchemaVersion: model.HistorySchemaVersion, Status: h.Status, Reason: h.Reason,
		StartedAt: h.StartedAt, FinishedAt: h.FinishedAt,
		WindowStart: h.WindowStart, WindowEnd: h.WindowEnd,
		StepSeconds: h.StepSeconds, ScrapeSeconds: h.ScrapeSeconds, Targets: []model.HistoryTargetReport{},
	}
	if ValidateCapture(h, s.Scope) != nil || ValidateTargetOwners(s) != nil {
		return &model.HistoryReport{
			SchemaVersion: model.HistorySchemaVersion, Status: model.HistoryFailed,
			Reason: model.HistoryBadResponse, Targets: []model.HistoryTargetReport{},
		}
	}
	index := indexOwners(s)
	complete := 0
	reason := model.HistoryReason("")
	for _, target := range h.Targets {
		item := model.HistoryTargetReport{
			Target: target.Target,
			CPU:    assessMetric(h, target.CPUCounter, true),
			Memory: assessMetric(h, target.Memory, false),
		}
		pod := observedPod(s, target.Target.Pod)
		item.CPU.Advice = advise(s, h, index, pod, target.Target, item.CPU, core.ResourceCPU)
		item.Memory.Advice = advise(s, h, index, pod, target.Target, item.Memory, core.ResourceMemory)
		if h.Grafana != nil {
			item.GrafanaURL = grafanaLink(*h.Grafana, target.Target, h.WindowStart, h.WindowEnd)
		}
		for _, metric := range []model.HistoryMetricReport{item.CPU, item.Memory} {
			if metric.Status == model.HistoryComplete {
				complete++
			} else if reason == "" {
				reason = metric.Reason
			}
		}
		report.Targets = append(report.Targets, item)
	}
	slices.SortFunc(report.Targets, func(a, b model.HistoryTargetReport) int {
		return compareTarget(a.Target, b.Target)
	})
	if report.Status == model.HistoryComplete && complete < len(h.Targets)*2 {
		report.Status, report.Reason = model.HistoryPartial, reason
		if complete == 0 {
			report.Status = model.HistoryInsufficient
		}
	}
	return report
}

func assessMetric(h *model.HistoryCapture, series model.HistorySeries, cpu bool) model.HistoryMetricReport {
	interval := time.Duration(h.ScrapeSeconds) * time.Second
	result := model.HistoryMetricReport{
		Status: series.Status, Reason: series.Reason, Unit: "bytes",
		DistinctScrapes: len(series.Points), ExpectedScrapes: int(h.WindowEnd.Sub(h.WindowStart) / interval),
		Samples: append([]model.HistoryPoint{}, series.Points...),
	}
	if result.Status == model.HistoryComplete && !adequate(series.Points, h.WindowStart, h.WindowEnd, interval) {
		result.Status, result.Reason = model.HistoryInsufficient, model.HistorySparseSamples
	}
	if cpu {
		result.Unit = "millicores"
		var reset bool
		result.Samples, reset = cpuRates(series.Points, interval)
		if reset && series.Status == model.HistoryComplete {
			result.Status, result.Reason = model.HistoryInsufficient, model.HistoryCounterReset
		}
	}
	if len(result.Samples) > 0 {
		values := make([]float64, len(result.Samples))
		for i, point := range result.Samples {
			values[i] = point.Value
		}
		slices.Sort(values)
		result.P50 = optionalNumber(percentile(values, 0.50))
		result.P95 = optionalNumber(percentile(values, 0.95))
		result.Peak = optionalNumber(values[len(values)-1])
	}
	return result
}

func adequate(points []model.HistoryPoint, start, end time.Time, interval time.Duration) bool {
	expected := float64(end.Sub(start)) / float64(interval)
	if len(points) < minimumScrapes || float64(len(points)) < math.Ceil(coverageThreshold*expected) {
		return false
	}
	first, last := points[0].At, points[len(points)-1].At
	if last.Sub(first) < time.Duration(coverageThreshold*float64(end.Sub(start))) ||
		first.Sub(start) > 2*interval || end.Sub(last) > 2*interval {
		return false
	}
	for i := 1; i < len(points); i++ {
		gap := points[i].At.Sub(points[i-1].At)
		if gap > 2*interval || gap < interval/2 {
			return false
		}
	}
	return true
}

func cpuRates(points []model.HistoryPoint, interval time.Duration) ([]model.HistoryPoint, bool) {
	rates := make([]model.HistoryPoint, 0, len(points))
	reset := false
	for i := 1; i < len(points); i++ {
		previous, current := points[i-1], points[i]
		delta := current.Value - previous.Value
		if delta < 0 {
			reset = true
			continue
		}
		elapsed := current.At.Sub(previous.At)
		if elapsed <= 0 || elapsed > 2*interval {
			continue
		}
		rates = append(rates, model.HistoryPoint{At: current.At, Value: 1_000 * delta / elapsed.Seconds()})
	}
	return rates, reset
}

func percentile(sorted []float64, quantile float64) float64 {
	return sorted[int(math.Ceil(quantile*float64(len(sorted))))-1]
}

func observedPod(s model.Snapshot, identity model.Resource) *core.Pod {
	for i := range s.Pods {
		pod := &s.Pods[i]
		if model.Identity("Pod", pod) == identity {
			return pod
		}
	}
	return nil
}

func advise(s model.Snapshot, h *model.HistoryCapture, index ownerIndex, pod *core.Pod,
	target model.HistoryTarget, metric model.HistoryMetricReport, resource core.ResourceName,
) model.HistoryAdvice {
	result := model.HistoryAdvice{}
	if pod != nil {
		for _, container := range pod.Spec.Containers {
			if container.Name == target.Container {
				var validRequest, validLimit bool
				result.CurrentRequest, validRequest = resourceAmount(container.Resources.Requests, resource)
				result.CurrentLimit, validLimit = resourceAmount(container.Resources.Limits, resource)
				if !validRequest || !validLimit {
					result.Reason = "A captured resource quantity is outside the sizing policy's numeric bounds."
					return result
				}
				break
			}
		}
	}
	result.Reason = sizingUncertainty(s, h, index, pod, target, metric, resource)
	if result.Reason != "" {
		return result
	}
	var candidate float64
	if resource == core.ResourceCPU {
		candidate = math.Max(10, math.Ceil(*metric.P95*1.25/10)*10)
		result.Reason = "CPU request candidate equals 125% of observed P95, rounded up to 10 millicores."
	} else {
		candidate = math.Max(mebibyte, math.Ceil(math.Max(*metric.P95*1.30, *metric.Peak*1.10)/mebibyte)*mebibyte)
		result.Reason = "Memory request candidate is the greater of 130% of observed P95 and 110% of observed peak, rounded up to a MiB."
	}
	if candidate > maxRawValue {
		result.Reason = "The calculated request is outside the sizing policy's numeric bounds."
		return result
	}
	if result.CurrentLimit != nil && *result.CurrentLimit > 0 {
		limit := *result.CurrentLimit
		if candidate > limit {
			result.Reason = "The calculated request exceeds the configured limit. Inspect resource pressure before changing either setting."
			return result
		}
		if resource == core.ResourceCPU && (result.CurrentRequest == nil || candidate < *result.CurrentRequest) {
			result.Reason = "An active CPU limit can hide demand through throttling. CPU downsize advice requires additional evidence."
			return result
		}
		if resource == core.ResourceMemory && *metric.Peak >= 0.8*limit {
			result.Reason = "The observed working set approached its memory limit. Censored demand may require additional evidence."
			return result
		}
	}
	result.Request = optionalNumber(candidate)
	return result
}

func sizingUncertainty(s model.Snapshot, h *model.HistoryCapture, index ownerIndex, pod *core.Pod,
	target model.HistoryTarget, metric model.HistoryMetricReport, resource core.ResourceName,
) string {
	switch {
	case h.Status != model.HistoryComplete:
		return "The Prometheus capture has incomplete provider or target coverage."
	case metric.Status != model.HistoryComplete || metric.P95 == nil:
		return "The metric lacks continuous independent scrapes across the requested window."
	case pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase != core.PodRunning:
		return "The captured Pod identity has no current running observation."
	case !target.VPADisabled:
		return "The startup policy does not confirm that this workload is outside VPA management."
	case s.Coverage(model.SourceHPAs) != model.Complete:
		return "HPA inventory is incomplete. Changing a request can change utilization-based scaling."
	}
	if resources := pod.Spec.Resources; resources != nil {
		_, request := resources.Requests[resource]
		_, limit := resources.Limits[resource]
		if request || limit {
			return "Pod-level resources are configured for this resource. Per-container sizing does not model that shared budget."
		}
	}
	at := containerStatus(pod, target.Container)
	if at < 0 {
		return "The captured container has no lifecycle observation."
	}
	status := pod.Status.ContainerStatuses[at]
	if status.State.Running == nil || status.State.Running.StartedAt.IsZero() ||
		status.State.Running.StartedAt.Time.After(h.WindowStart) {
		return "The current container lifetime does not cover the full history window."
	}
	last := status.LastTerminationState.Terminated
	if last != nil && (last.FinishedAt.IsZero() || !last.FinishedAt.Time.Before(h.WindowStart)) {
		return "Recent or unplaced termination evidence limits sizing confidence. Inspect restarts and OOM history."
	}
	if last == nil && status.RestartCount > 0 {
		return "The container has restarts without a captured last termination time."
	}
	ancestors := index.ancestors(pod)
	if len(ancestors) == 2 && ancestors[1].Kind == "ReplicaSet" && s.Coverage(model.SourceReplicaSets) != model.Complete {
		return "Deployment ownership is incomplete. Utilization-based HPA impact cannot be assessed."
	}
	for _, hpa := range s.Expansion.HPAs {
		if !slices.ContainsFunc(ancestors, func(a model.Resource) bool {
			return a.Kind == hpa.Target.Kind && a.Namespace == hpa.Target.Namespace && a.Name == hpa.Target.Name
		}) {
			continue
		}
		if hpa.TargetsTruncated {
			return "A matching HPA has truncated metric observations. Its utilization targets may include this request."
		}
		for _, item := range hpa.UtilizationTargets {
			if item.Resource == string(resource) && (item.Container == "" || item.Container == target.Container) {
				return "A matching HPA uses utilization relative to this request. Review its scaling target before changing the request."
			}
		}
	}
	return ""
}

func resourceAmount(list core.ResourceList, resource core.ResourceName) (*float64, bool) {
	value, found := list[resource]
	if !found {
		return nil, true
	}
	amount := value.AsApproximateFloat64()
	if resource == core.ResourceCPU {
		amount *= 1_000
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount > maxRawValue {
		return nil, false
	}
	return optionalNumber(amount), true
}

func optionalNumber(value float64) *float64 { return &value }

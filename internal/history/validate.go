package history

import (
	"errors"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
)

// ValidateCapture applies the provider's evidence contract to imported data.
func ValidateCapture(h *model.HistoryCapture, scope model.Scope) error {
	if h == nil {
		return nil
	}
	if h.SchemaVersion != model.HistorySchemaVersion || !validOutcome(h.Status, h.Reason) ||
		!h.WindowStart.Before(h.WindowEnd) || h.WindowStart.Unix() < 0 ||
		h.StartedAt.IsZero() || h.FinishedAt.Before(h.StartedAt) ||
		h.StepSeconds < 5 || h.StepSeconds > 900 || h.ScrapeSeconds < 5 || h.ScrapeSeconds > 900 ||
		!validWindow(h.WindowEnd.Sub(h.WindowStart), time.Duration(h.StepSeconds)*time.Second, time.Duration(h.ScrapeSeconds)*time.Second) ||
		len(h.Targets) > maxTargets || h.Status == model.HistoryComplete && len(h.Targets) == 0 ||
		h.Grafana != nil && !validDashboard(*h.Grafana) {
		return errors.New("invalid historical evidence metadata or bounds")
	}
	seen := make(map[model.HistoryTarget]bool, len(h.Targets))
	for _, item := range h.Targets {
		target := item.Target
		key := model.HistoryTarget{Pod: target.Pod, Container: target.Container}
		if target.Pod.Kind != "Pod" || !validResource(target.Pod) || !validResource(target.Workload) ||
			target.Pod.Namespace != target.Workload.Namespace || !dnsLabel.MatchString(target.Container) ||
			(scope.Kind != model.ClusterScope && (scope.Kind != model.NamespaceScope || target.Pod.Namespace != scope.Namespace)) ||
			seen[key] || !validSeries(item.CPUCounter, h) || !validSeries(item.Memory, h) {
			return errors.New("invalid, duplicate, or out-of-scope historical series")
		}
		if h.Status == model.HistoryComplete && (item.CPUCounter.Status != model.HistoryComplete || item.Memory.Status != model.HistoryComplete) {
			return errors.New("historical series contradict complete provider coverage")
		}
		seen[key] = true
	}
	return nil
}

// ValidateTargetOwners checks historical identity links against the captured
// Kubernetes evidence. Runtime IDs are deliberately excluded from this join.
func ValidateTargetOwners(s model.Snapshot) error {
	if s.History == nil {
		return nil
	}
	if !s.CollectedAt.Truncate(time.Second).Equal(s.History.WindowEnd) {
		return errors.New("historical window must end at the captured evaluation time")
	}
	owners := indexOwners(s)
	for _, item := range s.History.Targets {
		target := item.Target
		pod := observedPod(s, target.Pod)
		if pod == nil || !slices.ContainsFunc(pod.Spec.Containers, func(c core.Container) bool {
			return c.Name == target.Container
		}) || !slices.Contains(owners.ancestors(pod), target.Workload) {
			return errors.New("historical target does not match captured Pod, container, and workload evidence")
		}
	}
	return nil
}

func validResource(resource model.Resource) bool {
	return supportedKind(resource.Kind) && dnsLabel.MatchString(resource.Namespace) &&
		dnsName(resource.Name) && simpleID.MatchString(resource.UID)
}

func validOutcome(status model.HistoryStatus, reason model.HistoryReason) bool {
	switch status {
	case model.HistoryComplete:
		return reason == ""
	case model.HistoryPartial, model.HistoryUnavailable, model.HistoryPermission, model.HistoryFailed, model.HistoryTruncated, model.HistoryInsufficient:
		switch reason {
		case model.HistoryNoTargets, model.HistoryTargetsLimited, model.HistoryRequestFailed,
			model.HistoryRequestDenied, model.HistoryRequestTimeout, model.HistoryBadResponse,
			model.HistoryResponseLimited, model.HistoryResponseWarnings, model.HistoryMetricAbsent,
			model.HistoryAmbiguousSeries, model.HistoryBadSamples, model.HistorySparseSamples, model.HistoryCounterReset:
			return true
		}
	}
	return false
}

func validSeries(series model.HistorySeries, h *model.HistoryCapture) bool {
	if !validOutcome(series.Status, series.Reason) || len(series.Points) > maxSteps {
		return false
	}
	var previous time.Time
	for _, point := range series.Points {
		if !point.At.After(previous) || point.At.Before(h.WindowStart) || point.At.After(h.WindowEnd) ||
			math.IsNaN(point.Value) || math.IsInf(point.Value, 0) || point.Value < 0 || point.Value > maxRawValue {
			return false
		}
		previous = point.At
	}
	return true
}

func validDashboard(d model.GrafanaDashboard) bool {
	_, err := validatedURL(d.Origin, false, true)
	return err == nil && simpleID.MatchString(d.UID) && dnsLabel.MatchString(d.Slug)
}

func grafanaLink(d model.GrafanaDashboard, target model.HistoryTarget, start, end time.Time) string {
	u, _ := url.Parse(d.Origin)
	u.Path = "/d/" + d.UID + "/" + d.Slug
	u.RawQuery = url.Values{
		"from":          {strconv.FormatInt(start.UnixMilli(), 10)},
		"to":            {strconv.FormatInt(end.UnixMilli(), 10)},
		"var-container": {target.Container},
		"var-namespace": {target.Pod.Namespace},
		"var-pod":       {target.Pod.Name},
		"var-workload":  {target.Workload.Name},
	}.Encode()
	return u.String()
}

// ValidGrafanaLink accepts the exact HTTPS navigation shape emitted above.
func ValidGrafanaLink(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u == nil || len(raw) > 2_048 || u.User != nil || u.RawPath != "" || strings.Contains(raw, "#") {
		return false
	}
	if _, err := validatedURL((&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), false, true); err != nil {
		return false
	}
	path := strings.Split(u.Path, "/")
	if len(path) != 4 || path[0] != "" || path[1] != "d" || !simpleID.MatchString(path[2]) || !dnsLabel.MatchString(path[3]) {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 6 {
		return false
	}
	for _, key := range []string{"from", "to", "var-container", "var-namespace", "var-pod", "var-workload"} {
		if len(query[key]) != 1 {
			return false
		}
	}
	from, startErr := strconv.ParseInt(query.Get("from"), 10, 64)
	to, endErr := strconv.ParseInt(query.Get("to"), 10, 64)
	return startErr == nil && endErr == nil && from >= 0 && to > from && to-from <= (7*24*time.Hour).Milliseconds() &&
		dnsLabel.MatchString(query.Get("var-container")) && dnsLabel.MatchString(query.Get("var-namespace")) &&
		dnsName(query.Get("var-pod")) && dnsName(query.Get("var-workload"))
}

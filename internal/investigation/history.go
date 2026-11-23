package investigation

import (
	"fmt"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

type historyTargetIdentity struct {
	pod, workload model.Resource
	container     string
}

func compareHistory(before, after *model.HistoryReport) *HistoryComparison {
	if before == nil && after == nil {
		return nil
	}
	summary := &HistoryComparison{BeforeStatus: "not_configured", AfterStatus: "not_configured"}
	left, right := historyTargets(before), historyTargets(after)
	if before != nil {
		summary.BeforeStatus, summary.BeforeTargetCount = string(before.Status), len(left)
	}
	if after != nil {
		summary.AfterStatus, summary.AfterTargetCount = string(after.Status), len(right)
	}
	for target, usable := range left {
		laterUsable, observed := right[target]
		if usable && laterUsable {
			summary.ComparableTargets++
		} else if !observed || usable && !laterUsable {
			summary.LostTargets++
		}
	}
	for target := range right {
		if _, found := left[target]; !found {
			summary.NewTargets++
		}
	}
	summary.Complete = historyComplete(before) && historyComplete(after) && summary.ComparableTargets > 0 &&
		summary.ComparableTargets == len(left) && summary.ComparableTargets == len(right)
	return summary
}

func historyTargets(history *model.HistoryReport) map[historyTargetIdentity]bool {
	targets := make(map[historyTargetIdentity]bool)
	if history != nil {
		for _, item := range history.Targets {
			identity := historyTargetIdentity{pod: item.Target.Pod, workload: item.Target.Workload, container: item.Target.Container}
			targets[identity] = identity.pod.UID != "" && identity.workload.UID != "" && identity.container != "" &&
				item.CPU.Status == model.HistoryComplete && item.Memory.Status == model.HistoryComplete
		}
	}
	return targets
}

func historyComplete(history *model.HistoryReport) bool {
	return history != nil && history.SchemaVersion == model.HistorySchemaVersion && history.Status == model.HistoryComplete && history.WindowStart.Before(history.WindowEnd)
}

func historyLimitation(history HistoryComparison) string {
	return fmt.Sprintf("Historical coverage was %s before and %s after, with %d comparable targets, %d lost, and %d new. Historical windows inform sizing. The severity gate uses Kubernetes findings.",
		history.BeforeStatus, history.AfterStatus, history.ComparableTargets, history.LostTargets, history.NewTargets)
}

package investigation

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
)

// These facts are derived from the same retained evidence used by the rules.
// They support recovery decisions without changing the investigation wire schema.
type rolloutObservation struct {
	ready, available, updated   int32
	updateTarget                *int32
	paused, deadlineExceeded    bool
	revisionsDiffer             bool
	startOrdinal, readyOrdinals int32
}

func deploymentRollout(item *apps.Deployment, desired int32) rolloutObservation {
	return rolloutObservation{
		ready: item.Status.ReadyReplicas, available: item.Status.AvailableReplicas, updated: item.Status.UpdatedReplicas,
		updateTarget: &desired, paused: item.Spec.Paused,
		deadlineExceeded: slices.ContainsFunc(item.Status.Conditions, func(condition apps.DeploymentCondition) bool {
			return condition.Type == apps.DeploymentProgressing && condition.Status == core.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded"
		}),
	}
}

func daemonSetRollout(item *apps.DaemonSet, desired int32) rolloutObservation {
	return rolloutObservation{
		ready: item.Status.NumberReady, available: item.Status.NumberAvailable, updated: item.Status.UpdatedNumberScheduled,
		updateTarget: updateTarget(string(item.Spec.UpdateStrategy.Type), desired, 0),
	}
}

func statefulSetRollout(item model.StatefulSetEvidence) rolloutObservation {
	return rolloutObservation{
		ready: item.Ready, available: item.Available, updated: item.Updated, startOrdinal: item.StartOrdinal,
		updateTarget: updateTarget(item.Strategy, item.Desired, item.Partition), revisionsDiffer: item.RevisionState == "different",
	}
}

func updateTarget(strategy string, desired, partition int32) *int32 {
	switch strategy {
	case "", "RollingUpdate":
		// A StatefulSet's partition indexes its replica range, relative to its
		// starting ordinal. DaemonSet rolling updates use a zero partition.
		target := max(0, max(0, desired)-max(0, partition))
		return &target
	case "OnDelete":
		return new(int32(0))
	}
	return nil
}

func (index *inventory) recordRollout(resource model.Resource, desired int32, observation rolloutObservation) {
	key := nameOf(resource)
	if item, found := index.byName[key]; found && item.Resource == resource {
		item.ExpectedPods = &desired
		item.rollout = &observation
		index.byName[key] = item
	}
}

func (index *inventory) recordReadyStatefulOrdinals() {
	for _, pod := range index.byName {
		if pod.Resource.Kind != "Pod" || !pod.Active || !pod.StatusFresh || !pod.Ready {
			continue
		}
		owner := index.owners[pod.Resource]
		if owner.Kind != "StatefulSet" {
			continue
		}
		set := index.byName[nameOf(owner)]
		if set.Resource != owner || set.rollout == nil || set.ExpectedPods == nil {
			continue
		}
		ordinalText, matches := strings.CutPrefix(pod.Resource.Name, owner.Name+"-")
		ordinal, err := strconv.ParseInt(ordinalText, 10, 32)
		start := int64(set.rollout.startOrdinal)
		if matches && err == nil && strconv.FormatInt(ordinal, 10) == ordinalText && ordinal >= start && ordinal < start+int64(*set.ExpectedPods) {
			set.rollout.readyOrdinals++
		}
	}
}

func rolloutRule(rule string) bool {
	switch rule {
	case "deployment-rollout", "daemonset-rollout", "statefulset-rollout":
		return true
	}
	return false
}

func rolloutRecoveryLimit(before, after ResourceObservation, current compareSnapshot, finding model.Finding) string {
	previous, observed := before.rollout, after.rollout
	if previous == nil || observed == nil || !before.StatusFresh || !after.StatusFresh || after.ExpectedPods == nil {
		return "A current-generation rollout observation was unavailable for this controller."
	}
	desired := *after.ExpectedPods
	if desired <= 0 {
		return "The later capture has zero desired replicas. The prior rollout can no longer be assessed."
	}
	if observed.paused {
		return "The Deployment is paused in the later capture. Rollout completion remains unverified."
	}
	if !after.Active {
		return "The controller is being deleted. Rollout completion remains unverified."
	}
	if observed.available < desired || observed.ready < desired {
		return fmt.Sprintf("The controller reports %d Ready and %d available replicas out of %d desired.", observed.ready, observed.available, desired)
	}
	if observed.updateTarget == nil {
		return "The later rollout update strategy is outside observed coverage."
	}
	if observed.updated < *observed.updateTarget {
		return fmt.Sprintf("The controller reports %d updated replicas against a rollout target of %d.", observed.updated, *observed.updateTarget)
	}
	// A policy change alone does not demonstrate that an outstanding update
	// finished. An availability-only deficit can still recover under OnDelete.
	previousUpdateRelevant := finding.RuleID != "statefulset-rollout" || previous.revisionsDiffer
	if previousUpdateRelevant && previous.updateTarget != nil && previous.updated < *previous.updateTarget && observed.updated < *previous.updateTarget {
		return fmt.Sprintf("The earlier rollout required %d updated replicas. The later policy requires %d and the controller reports %d updated.",
			*previous.updateTarget, *observed.updateTarget, observed.updated)
	}
	if finding.RuleID == "deployment-rollout" && evidenceValue(finding, "condition_reason") == "ProgressDeadlineExceeded" && observed.deadlineExceeded {
		return "The Deployment still reports ProgressDeadlineExceeded."
	}
	if finding.RuleID != "statefulset-rollout" {
		return ""
	}
	if observed.readyOrdinals < desired {
		return fmt.Sprintf("Only %d of %d expected StatefulSet ordinals were Ready with a verified owner UID.", observed.readyOrdinals, desired)
	}
	for _, item := range finding.Evidence {
		if item.Field == "persistentvolumeclaim" {
			claim := current.byName[resourceName{"PersistentVolumeClaim", after.Resource.Namespace, item.Value}]
			status := current.inventory[claim]
			if !status.Active || !status.StatusFresh || !status.Ready {
				return "The previously implicated claim " + item.Value + " lacks current Bound evidence."
			}
		}
	}
	return ""
}

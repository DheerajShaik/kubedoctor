package investigation

import (
	"slices"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
)

// These facts are rebuilt from normalized bundle evidence on each replay.
type comparisonApplicability struct {
	network   networkContinuity
	nodes     map[model.Resource]nodeConditions
	pods      podContainerContinuity
	nodeLocal *nodeLocalContinuity
	hpas      map[model.Resource]hpaContinuity
	cronJobs  map[model.Resource]bool
}

type nodeConditions struct {
	ready, memory, disk, pid core.ConditionStatus
}

type nodeLocalContinuity struct {
	provider  model.NodeLocalDNSEvidence
	hosts     map[string]bool
	unhealthy map[string]bool
	ready     map[string]bool
}

type hpaContinuity struct {
	active, canScale, metricsActive, notLimited bool
	target                                      model.Resource
	targetObserved                              bool
	api                                         string
	utilization                                 []model.HPAUtilizationTarget
}

func applicableEvidence(s model.Snapshot, index *inventory) comparisonApplicability {
	facts := comparisonApplicability{
		network:  networkComparisonEvidence(s),
		nodes:    make(map[model.Resource]nodeConditions, len(s.Nodes)),
		pods:     podContainerEvidence(s, index),
		hpas:     make(map[model.Resource]hpaContinuity, len(s.Expansion.HPAs)),
		cronJobs: make(map[model.Resource]bool, len(s.Expansion.CronJobs)),
	}
	for i := range s.Nodes {
		node := &s.Nodes[i]
		if node.DeletionTimestamp != nil {
			continue
		}
		facts.nodes[model.Identity("Node", node)] = nodeConditions{
			ready: nodeCondition(node, core.NodeReady), memory: nodeCondition(node, core.NodeMemoryPressure),
			disk: nodeCondition(node, core.NodeDiskPressure), pid: nodeCondition(node, core.NodePIDPressure),
		}
	}
	for _, hpa := range s.Expansion.HPAs {
		target, observed := index.resolve(hpa.Target)
		facts.hpas[hpa.Resource] = hpaContinuity{
			active:   hpa.Generation > 0 && hpa.Generation == hpa.ObservedGeneration && !hpa.Deleting && !hpa.TargetsTruncated,
			canScale: hpaCondition(hpa, "AbleToScale") == "True", metricsActive: hpaCondition(hpa, "ScalingActive") == "True",
			notLimited: hpaCondition(hpa, "ScalingLimited") == "False",
			target:     target, targetObserved: observed && target.UID != "", api: hpa.TargetAPIVersion,
			utilization: slices.Clone(hpa.UtilizationTargets),
		}
	}
	for _, cron := range s.Expansion.CronJobs {
		facts.cronJobs[cron.Resource] = !cron.Deleting && !cron.Suspended
	}
	facts.nodeLocal = nodeLocalEvidence(s)
	return facts
}

func nodeCondition(node *core.Node, kind core.NodeConditionType) core.ConditionStatus {
	var state core.ConditionStatus
	seen := false
	for _, condition := range node.Status.Conditions {
		if condition.Type != kind {
			continue
		}
		if seen {
			return ""
		}
		state, seen = condition.Status, true
	}
	return state
}

func hpaCondition(hpa model.HPAEvidence, kind string) string {
	var state string
	seen := false
	for _, condition := range hpa.Conditions {
		if condition.Type != kind {
			continue
		}
		if seen {
			return ""
		}
		state, seen = condition.Status, true
	}
	return state
}

func nodeLocalEvidence(s model.Snapshot) *nodeLocalContinuity {
	provider := s.Expansion.NodeLocalDNS
	if provider == nil {
		return nil
	}
	facts := &nodeLocalContinuity{
		provider: *provider, hosts: map[string]bool{}, unhealthy: map[string]bool{}, ready: map[string]bool{},
	}
	if s.Scope.Kind != model.NamespaceScope {
		return facts
	}
	for i := range s.Pods {
		pod := &s.Pods[i]
		if pod.Namespace == s.Scope.Namespace && pod.Spec.NodeName != "" && pod.DeletionTimestamp == nil &&
			pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
			facts.hosts[pod.Spec.NodeName] = true
		}
	}
	for _, cache := range s.Expansion.NodeLocalPods {
		if cache.Owner == nil || *cache.Owner != provider.Resource || cache.Deleting || !facts.hosts[cache.Node] {
			continue
		}
		facts.ready[cache.Node] = facts.ready[cache.Node] || cache.Ready == "True"
		facts.unhealthy[cache.Node] = facts.unhealthy[cache.Node] || cache.Ready == "False"
	}
	return facts
}

func relevantNonRolloutEvidence(left, right compareSnapshot, finding model.Finding) (handled, current bool) {
	facts := right.inv.applicability
	id := finding.Resource
	switch finding.RuleID {
	case "node-readiness":
		return true, facts.nodes[id].ready == core.ConditionTrue
	case "node-pressure":
		node := facts.nodes[id]
		return true, node.memory == core.ConditionFalse && node.disk == core.ConditionFalse && node.pid == core.ConditionFalse
	case "node-local-dns":
		return true, comparableNodeLocal(left.inv.applicability.nodeLocal, facts.nodeLocal, id, right.inv.Scope.Kind)
	case "cronjob-scheduling":
		// A recorded child failure can age out independently of resumption.
		return true, evidenceValue(finding, "suspended") == "true" &&
			evidenceValue(finding, "failed_owned_job") == "" && facts.cronJobs[id]
	case "hpa-scaling":
		return true, comparableHPA(left.inv.applicability.hpas[id], facts.hpas[id], right, finding)
	default:
		return networkRecoveryEvidence(left, right, finding)
	}
}

func comparableNodeLocal(before, after *nodeLocalContinuity, resource model.Resource, scope model.ScopeKind) bool {
	if before == nil || after == nil || before.provider.Resource != resource || after.provider.Resource != resource ||
		after.provider.Deleting || after.provider.Desired <= 0 ||
		after.provider.Generation <= 0 || after.provider.Generation != after.provider.ObservedGeneration {
		return false
	}
	if scope == model.ClusterScope {
		return after.provider.Desired >= before.provider.Desired && after.provider.Ready >= after.provider.Desired
	}
	proven := false
	for host, unhealthy := range before.unhealthy {
		if !unhealthy || before.ready[host] {
			continue
		}
		if !after.hosts[host] || !after.ready[host] {
			return false
		}
		proven = true
	}
	return proven
}

func comparableHPA(before, after hpaContinuity, right compareSnapshot, finding model.Finding) bool {
	if !after.active || !after.targetObserved || !after.canScale || !after.metricsActive ||
		before.api != after.api || nameOf(before.target) != nameOf(after.target) ||
		before.targetObserved && before.target.UID != after.target.UID {
		return false
	}
	for _, item := range finding.Evidence {
		if item.Field == "condition" && item.Value == "ScalingLimited" && !after.notLimited {
			return false
		}
	}
	if evidenceValue(finding, "utilization_resource") == "" {
		return true
	}
	if !slices.Equal(before.utilization, after.utilization) {
		return false
	}
	health := right.podHealth[after.target]
	status := right.inventory[after.target]
	return status.Active && status.StatusFresh && status.ExpectedPods != nil && *status.ExpectedPods > 0 &&
		health.active >= *status.ExpectedPods && health.ready == health.active
}

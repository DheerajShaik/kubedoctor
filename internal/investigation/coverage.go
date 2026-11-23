package investigation

import (
	"cmp"
	"slices"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

func reportChecks(report model.Report) map[string]model.CheckStatus {
	checks := make(map[string]model.CheckStatus, len(report.Checks))
	for _, check := range report.Checks {
		if seen, ok := checks[check.ID]; ok && seen != check.Status {
			checks[check.ID] = model.Inconclusive
		} else {
			checks[check.ID] = check.Status
		}
	}
	return checks
}

func reportCoverage(report model.Report) map[model.Source]model.Coverage {
	states := make(map[model.Source]model.Coverage, len(report.Sources))
	for _, source := range report.Sources {
		states[source.Source] = mostLimited(states[source.Source], source.Status)
	}
	return states
}

func mostLimited(left, right model.Coverage) model.Coverage {
	if coverageRank(right) > coverageRank(left) {
		return right
	}
	return left
}

func coverageRank(state model.Coverage) int {
	switch state {
	case model.NotApplicable:
		return 1
	case model.Complete:
		return 2
	case model.NotInspected:
		return 3
	case model.Unavailable:
		return 4
	case model.Coverage("stale"):
		return 5
	case model.PermissionDenied:
		return 6
	case model.Truncated:
		return 7
	case model.Failed:
		return 8
	case "":
		return 0
	default:
		return 9
	}
}

type coverageKey struct {
	source   model.Source
	resource string
}

func coverageChanges(before, after model.Report) []CoverageChange {
	statesOf := func(report model.Report) map[coverageKey]model.Coverage {
		states := make(map[coverageKey]model.Coverage, len(report.Sources))
		for _, source := range report.Sources {
			key := coverageKey{source.Source, source.Resource}
			states[key] = mostLimited(states[key], source.Status)
		}
		return states
	}
	left, right := statesOf(before), statesOf(after)
	changes := []CoverageChange{}
	for key, previous := range left {
		current := right[key]
		if current == "" {
			current = model.NotInspected
		}
		if current != previous {
			changes = append(changes, CoverageChange{Source: key.source, Resource: key.resource, Before: previous, After: current})
		}
	}
	for key, current := range right {
		if _, observed := left[key]; !observed && current != model.NotInspected {
			changes = append(changes, CoverageChange{Source: key.source, Resource: key.resource, Before: model.NotInspected, After: current})
		}
	}
	slices.SortFunc(changes, func(a, b CoverageChange) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Resource, b.Resource))
	})
	return changes
}

func findingSources(finding model.Finding) []model.Source {
	switch finding.RuleID {
	case "node-readiness", "node-pressure":
		return []model.Source{model.SourceNodes}
	case "pod-containers", "container-resources":
		return []model.Source{model.SourcePods}
	case "pod-scheduling":
		return []model.Source{model.SourcePods, model.SourceEvents}
	case "deployment-rollout":
		return []model.Source{model.SourceDeployments}
	case "daemonset-rollout":
		return []model.Source{model.SourceDaemonSets}
	case "configuration-references":
		if evidenceValue(finding, "reference_kind") == "ConfigMap" {
			return []model.Source{model.SourcePods, model.SourceConfigMaps}
		}
		return []model.Source{model.SourcePods, model.SourceEvents}
	case "pvc-binding":
		return []model.Source{model.SourcePVCs}
	case "service.backends", "service.ports", "service-endpoint-identity":
		return []model.Source{model.SourceServices, model.SourceEndpointSlices, model.SourcePods}
	case "ingress.backends":
		return []model.Source{model.SourceIngresses, model.SourceServices}
	case "dns.infrastructure":
		return []model.Source{model.SourceDNSServices, model.SourceDNSEndpointSlices, model.SourceDNSPods, model.SourceDNSConfig}
	case "networkpolicy.isolation", "networkpolicy-named-ports":
		return []model.Source{model.SourceNetworkPolicies, model.SourcePods}
	case "pdb.disruptions":
		return []model.Source{model.SourcePDBs}
	case "current-pod-usage":
		return []model.Source{model.SourcePods, model.SourceMetrics}
	case "recent-warning-events":
		return []model.Source{model.SourceEvents}
	case "statefulset-rollout":
		return []model.Source{model.SourceStatefulSets, model.SourcePods, model.SourcePVCs}
	case "job-execution":
		return []model.Source{model.SourceJobs}
	case "cronjob-scheduling":
		return []model.Source{model.SourceCronJobs, model.SourceJobs, model.SourceEvents}
	case "hpa-scaling":
		return []model.Source{model.SourceHPAs, model.SourcePods, model.SourceDeployments, model.SourceStatefulSets, model.SourceReplicaSets}
	case "namespace-admission":
		return []model.Source{model.SourceQuotas, model.SourceLimitRanges, model.SourceEvents}
	case "rollout-probes":
		return []model.Source{model.SourcePods, model.SourceEvents}
	case "node-local-dns":
		return []model.Source{model.SourceNodeLocalDNS, model.SourceNodeLocalPods, model.SourcePods}
	}
	return nil
}

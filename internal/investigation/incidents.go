package investigation

import (
	"cmp"
	"slices"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

func groupIncidents(inv Investigation) []Incident {
	catalog := map[model.Resource]ResourceObservation{}
	for _, item := range inv.Resources {
		catalog[item.Resource] = item
	}
	byAnchor := map[model.Resource]*Incident{}
	memberSets := map[model.Resource]map[model.Resource]struct{}{}
	for findingIndex, finding := range inv.Report.Findings {
		anchor := finding.Resource
		if target := catalog[finding.Resource].Workload; target != nil {
			anchor = *target
		}
		incident := byAnchor[anchor]
		if incident == nil {
			incident = &Incident{
				ID: incidentID(anchor), Anchor: anchor, Severity: finding.Severity,
				Members: []model.Resource{}, FindingIndexes: []int{}, RelationshipIndexes: []int{}, TimelineIndexes: []int{},
				Observations: []Explanation{}, Corroboration: []Explanation{}, Candidates: []Explanation{}, Alternatives: []Explanation{},
				MissingEvidence: []string{}, NextSteps: []string{}, RecoveryChecks: []Explanation{},
			}
			byAnchor[anchor] = incident
			memberSets[anchor] = map[model.Resource]struct{}{anchor: {}}
		}
		memberSets[anchor][finding.Resource] = struct{}{}
		if finding.Severity.Rank() > incident.Severity.Rank() {
			incident.Severity = finding.Severity
		}
		incident.FindingIndexes = append(incident.FindingIndexes, findingIndex)
		explanation := Explanation{Summary: finding.Symptom, Confidence: finding.Confidence, FindingIndexes: []int{findingIndex}}
		if finding.Confidence == model.Observed {
			incident.Observations = append(incident.Observations, explanation)
		} else {
			incident.Candidates = append(incident.Candidates, explanation)
		}
		if finding.ProbableCause != "" {
			incident.Candidates = append(incident.Candidates, Explanation{Summary: finding.ProbableCause, Confidence: model.Hypothesis, FindingIndexes: []int{findingIndex}})
		}
		if evidenceValue(finding, "event_reason") != "" && finding.RuleID != "recent-warning-events" {
			incident.Corroboration = append(incident.Corroboration, Explanation{
				Summary: "A correlated, classified Kubernetes Event corroborates the finding.", Confidence: model.Observed, FindingIndexes: []int{findingIndex},
			})
		}
		incident.NextSteps = append(incident.NextSteps, finding.NextSteps...)
	}

	groups := make([]Incident, 0, len(byAnchor))
	checks := reportChecks(inv.Report)
	coverage := reportCoverage(inv.Report)
	for anchor, incident := range byAnchor {
		for member := range memberSets[anchor] {
			incident.Members = append(incident.Members, member)
		}
		slices.SortFunc(incident.Members, compareResource)
		addAnalysisNarrative(inv.Report, checks, coverage, incident)
		groups = append(groups, *incident)
	}
	slices.SortFunc(groups, func(a, b Incident) int {
		return cmp.Or(cmp.Compare(b.Severity.Rank(), a.Severity.Rank()), compareResource(a.Anchor, b.Anchor))
	})
	return groups
}

func addAnalysisNarrative(report model.Report, checks map[string]model.CheckStatus, coverage map[model.Source]model.Coverage, incident *Incident) {
	configs, startups := map[model.Resource]int{}, map[model.Resource]int{}
	linkedIndex, rolloutIndex := -1, -1
	var recovery []int
	for _, index := range incident.FindingIndexes {
		finding := report.Findings[index]
		if status := checks[finding.RuleID]; status != model.Checked {
			incident.MissingEvidence = append(incident.MissingEvidence, "Check coverage is limited for "+finding.RuleID+".")
		}
		for _, source := range findingSources(finding) {
			if coverage[source] != model.Complete {
				incident.MissingEvidence = append(incident.MissingEvidence, "Complete observations were unavailable for "+string(source)+".")
			}
		}
		switch finding.RuleID {
		case "configuration-references":
			configs[finding.Resource] = index
			linkedIndex = index
		case "pod-containers":
			if evidenceValue(finding, "waiting_reason") == "CreateContainerConfigError" {
				startups[finding.Resource] = index
				linkedIndex = index
			}
		case "deployment-rollout", "daemonset-rollout", "statefulset-rollout":
			rolloutIndex = index
		}
		if len(recovery) < 16 {
			recovery = append(recovery, index)
		}
	}
	correlations := 0
	for _, resource := range incident.Members {
		configIndex, hasConfig := configs[resource]
		startupIndex, hasStartup := startups[resource]
		if resource.UID == "" || !hasConfig || !hasStartup || correlations == 16 {
			continue
		}
		correlations++
		cited := []int{configIndex, startupIndex}
		incident.Corroboration = append(incident.Corroboration, Explanation{
			Summary:    "The required reference finding and the observed configuration-related waiting state refer to the same Pod UID.",
			Confidence: model.Observed, FindingIndexes: cited,
		})
		incident.Candidates = append(incident.Candidates, Explanation{
			Summary:    "The observed missing reference may contribute to the configuration-related startup failure in this Pod.",
			Confidence: model.Hypothesis, FindingIndexes: cited,
		})
		incident.Alternatives = append(incident.Alternatives, Explanation{
			Summary:    "Inspect other required references and check which container is waiting. This Pod can have multiple independent configuration failures.",
			Confidence: model.Hypothesis, FindingIndexes: cited,
		})
	}
	if rolloutIndex >= 0 && linkedIndex >= 0 && supportedWorkload(incident.Anchor.Kind) {
		cited := []int{rolloutIndex, linkedIndex}
		incident.Candidates = append(incident.Candidates, Explanation{
			Summary: "The linked Pod startup issue may contribute to the workload availability or rollout symptom.", Confidence: model.Hypothesis, FindingIndexes: cited,
		})
		incident.Alternatives = append(incident.Alternatives, Explanation{
			Summary: "Other replicas, scheduling constraints, or rollout strategy may independently affect availability.", Confidence: model.Hypothesis, FindingIndexes: cited,
		})
	}
	incident.RecoveryChecks = append(incident.RecoveryChecks, Explanation{
		Summary:    "After a change, capture the same cluster and scope with comparable coverage. Confirm readiness or the relevant current condition and compare the resulting evidence.",
		Confidence: model.Observed, FindingIndexes: recovery,
	})
	slices.Sort(incident.NextSteps)
	incident.NextSteps = slices.Compact(incident.NextSteps)
	slices.Sort(incident.MissingEvidence)
	incident.MissingEvidence = slices.Compact(incident.MissingEvidence)
}

func linkIncidentEvidence(inv *Investigation) {
	groupsByMember := map[model.Resource][]int{}
	for i, incident := range inv.Incidents {
		for _, member := range incident.Members {
			groupsByMember[member] = append(groupsByMember[member], i)
		}
	}
	count := 0
	add := func(entryIndex int, members []model.Resource, isRelationship bool) {
		for _, member := range members {
			for _, group := range groupsByMember[member] {
				target := &inv.Incidents[group].TimelineIndexes
				if isRelationship {
					target = &inv.Incidents[group].RelationshipIndexes
				}
				if n := len(*target); n != 0 && (*target)[n-1] == entryIndex {
					continue
				}
				if count >= maxIncidentReferences {
					return
				}
				*target = append(*target, entryIndex)
				count++
			}
		}
	}
	for i, edge := range inv.Relationships {
		add(i, []model.Resource{edge.From, edge.To}, true)
	}
	for i, event := range inv.Timeline {
		if event.Resource != nil {
			add(i, []model.Resource{*event.Resource}, false)
		}
	}
	if count == maxIncidentReferences {
		inv.Truncated = true
		inv.Limitations = append(inv.Limitations, "Incident evidence navigation reached its budget. The underlying findings remain available.")
	}
}

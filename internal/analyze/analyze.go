// Package analyze evaluates a normalized snapshot without I/O.
package analyze

import (
	"cmp"
	"slices"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

func Checks() []model.CheckDefinition {
	definitions := make([]model.CheckDefinition, 0, len(workloadDefinitions)+len(networkDefinitions)+len(expansionDefinitions))
	definitions = append(definitions, workloadDefinitions...)
	definitions = append(definitions, networkDefinitions...)
	definitions = append(definitions, expansionDefinitions...)
	slices.SortFunc(definitions, func(a, b model.CheckDefinition) int {
		return cmp.Compare(a.ID, b.ID)
	})
	return definitions
}

func Run(snapshot model.Snapshot) ([]model.Finding, []model.CheckResult) {
	workloadFindings, workloadResults := analyzeWorkloads(snapshot)
	networkFindings, networkResults := analyzeNetwork(snapshot)
	return append(workloadFindings, networkFindings...), append(workloadResults, networkResults...)
}

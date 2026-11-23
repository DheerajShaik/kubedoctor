package investigation

import (
	"errors"

	"github.com/DheerajShaik/kubedoctor/internal/bundle"
)

// FromBundle explicitly analyzes retained evidence. Stored reports remain
// viewable through bundle.Stored without applying the current investigation rules.
func FromBundle(input *bundle.Bundle, mode bundle.Mode) (Investigation, error) {
	if mode == bundle.Stored {
		return Investigation{}, errors.New("stored mode returns the captured diagnostic report; choose replay or reevaluate to build an investigation")
	}
	result, err := input.Analyze(mode)
	if err != nil {
		return Investigation{}, err
	}
	snapshot, err := input.Snapshot()
	if err != nil {
		return Investigation{}, err
	}
	output := Build(snapshot, result.Report)
	output.AnalysisMode = string(result.Mode)
	output.CaptureRuleVersion = result.BundleAnalyzerVersion
	if result.InterpretationChanged {
		output.Limitations = append(output.Limitations, "This investigation reevaluated evidence with a different analyzer version than the bundle's stored result.")
	}
	return output, nil
}

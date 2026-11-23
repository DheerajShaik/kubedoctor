package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

func renderReport(out io.Writer, report model.Report, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	var buf bytes.Buffer
	scope := string(report.Scope.Kind)
	if report.Scope.Namespace != "" {
		scope += " " + report.Scope.Namespace
	}
	fmt.Fprintf(&buf, "KubeDoctor · %s · context %s · %s\n",
		terminalText(scope), terminalText(report.Scope.Context), report.CollectedAt.Format(time.RFC3339))
	if !report.Complete {
		buf.WriteString("Required collection incomplete. Available evidence follows.\n")
	}
	if len(report.Findings) == 0 {
		buf.WriteString("\nNo findings at the displayed severity.\n")
	}
	for _, group := range groupFindings(report.Findings) {
		fmt.Fprintf(&buf, "\n%s\n", resourceLabel(group.resource))
		for _, finding := range group.findings {
			fmt.Fprintf(&buf, "  %s [%s, %s] %s\n", terminalText(string(finding.Severity)),
				terminalText(finding.RuleID), terminalText(string(finding.Confidence)), terminalText(finding.Symptom))
			if finding.Resource != group.resource {
				fmt.Fprintf(&buf, "    Resource %s\n", resourceLabel(finding.Resource))
			}
			for _, item := range finding.Evidence {
				fmt.Fprintf(&buf, "    %s = %s\n", terminalText(item.Field), terminalText(item.Value))
			}
			if finding.ProbableCause != "" {
				fmt.Fprintf(&buf, "    Probable cause %s\n", terminalText(finding.ProbableCause))
			}
			for _, step := range finding.NextSteps {
				fmt.Fprintf(&buf, "    Next step %s\n", terminalText(step))
			}
		}
	}
	if len(report.CollectionErrors) > 0 {
		buf.WriteString("\nCollection coverage\n")
		for _, item := range report.CollectionErrors {
			scope := string(item.Source)
			if item.Resource != "" {
				scope += " " + item.Resource
			}
			if item.Required {
				scope += " (required)"
			}
			fmt.Fprintf(&buf, "  %s · %s · %s\n", terminalText(scope), terminalText(string(item.Status)), terminalText(item.Detail))
		}
	}
	for _, check := range report.Checks {
		if check.Status != model.Checked {
			fmt.Fprintf(&buf, "Check %s · %s · %s\n", terminalText(check.ID), terminalText(string(check.Status)), terminalText(check.Detail))
		}
	}
	_, err := buf.WriteTo(out)
	return err
}

type findingGroup struct {
	resource model.Resource
	findings []model.Finding
}

func groupFindings(findings []model.Finding) []findingGroup {
	var groups []findingGroup
	positions := make(map[model.Resource]int)
	for _, finding := range findings {
		resource := finding.Resource
		if finding.Incident != nil {
			resource = *finding.Incident
		}
		index, exists := positions[resource]
		if !exists {
			index = len(groups)
			positions[resource] = index
			groups = append(groups, findingGroup{resource: resource})
		}
		groups[index].findings = append(groups[index].findings, finding)
	}
	return groups
}

func resourceLabel(resource model.Resource) string {
	name := resource.Name
	if resource.Namespace != "" {
		name = resource.Namespace + "/" + name
	}
	return terminalText(resource.Kind + " " + name)
}

// Prevent control characters in untrusted identifiers from altering the
// terminal, including escape sequences and bidirectional format controls.
func terminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return '�'
		}
		return r
	}, value)
}

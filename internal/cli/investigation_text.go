package cli

import (
	"bufio"
	"fmt"
	"io"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/artifact"
	"github.com/DheerajShaik/kubedoctor/internal/htmlreport"
	"github.com/DheerajShaik/kubedoctor/internal/investigation"
	"github.com/DheerajShaik/kubedoctor/internal/model"
)

func writeInvestigation(w io.Writer, v investigation.Investigation, format string) error {
	switch format {
	case "json":
		return writeJSON(w, v)
	case "html":
		return htmlreport.Write(w, v)
	}
	buf := bufio.NewWriter(artifact.LimitOutput(w, maxInvestigationOutput))
	fmt.Fprintf(buf, "Investigation · %s · rules %s · capture rules %s\n", terminalText(v.AnalysisMode), terminalText(v.RuleVersion), terminalText(v.CaptureRuleVersion))
	if err := renderReport(buf, v.Report, "text"); err != nil {
		return err
	}
	for _, group := range v.Incidents {
		fmt.Fprintf(buf, "\nInvestigation %s · %s\n", terminalText(group.ID), resourceLabel(group.Anchor))
		for _, e := range group.Candidates {
			fmt.Fprintf(buf, "  Candidate (%s) · %s\n", terminalText(string(e.Confidence)), terminalText(e.Summary))
		}
		for _, e := range group.Alternatives {
			fmt.Fprintf(buf, "  Alternative · %s\n", terminalText(e.Summary))
		}
		for _, e := range group.MissingEvidence {
			fmt.Fprintf(buf, "  Missing evidence · %s\n", terminalText(e))
		}
		for _, e := range group.RecoveryChecks {
			fmt.Fprintf(buf, "  Verify recovery · %s\n", terminalText(e.Summary))
		}
	}
	for _, e := range v.Relationships {
		fmt.Fprintf(buf, "  %s → %s · %s (%s, %s)\n", resourceLabel(e.From), resourceLabel(e.To), terminalText(string(e.Kind)), terminalText(string(e.Basis)), terminalText(string(e.Source)))
	}
	if len(v.Timeline) > 0 {
		buf.WriteString("\nTimeline\n")
	}
	for _, e := range v.Timeline {
		fmt.Fprintf(buf, "  %s · %s · %s\n", e.Time.UTC().Format(time.RFC3339), terminalText(string(e.TimeKind)), terminalText(e.Summary))
	}
	for _, l := range v.Limitations {
		fmt.Fprintf(buf, "  Limitation · %s\n", terminalText(l))
	}
	if v.History != nil {
		buf.WriteString("\nResource history\n")
		fmt.Fprintf(buf, "%s · %s to %s · step %ds, expected scrape %ds\n", terminalText(string(v.History.Status)), stampTime(v.History.WindowStart), stampTime(v.History.WindowEnd), v.History.StepSeconds, v.History.ScrapeSeconds)
		for _, target := range v.History.Targets {
			fmt.Fprintf(buf, "  %s · %s · %s\n", resourceLabel(target.Target.Workload), resourceLabel(target.Target.Pod), terminalText(target.Target.Container))
			for _, metric := range []struct {
				name   string
				report model.HistoryMetricReport
			}{{"CPU", target.CPU}, {"Memory", target.Memory}} {
				r := metric.report
				fmt.Fprintf(buf, "    %s · %s %s · %d/%d distinct scrapes · %s\n", metric.name, terminalText(string(r.Status)), terminalText(string(r.Reason)), r.DistinctScrapes, r.ExpectedScrapes, terminalText(r.Unit))
				fmt.Fprintf(buf, "      median %s · p95 %s · peak %s · request candidate %s · %s\n", historyNumber(r.P50), historyNumber(r.P95), historyNumber(r.Peak), historyNumber(r.Advice.Request), terminalText(r.Advice.Reason))
			}
		}
	}
	return buf.Flush()
}

func writeComparisonText(w io.Writer, result investigation.Comparison) error {
	buf := bufio.NewWriter(artifact.LimitOutput(w, maxInvestigationOutput))
	fmt.Fprintf(buf, "Comparison · %s to %s · compatible %t · complete %t\n", result.Before.EvaluatedAt.UTC().Format(time.RFC3339), result.After.EvaluatedAt.UTC().Format(time.RFC3339), result.Compatible, result.Complete)
	for _, l := range result.Limitations {
		fmt.Fprintf(buf, "  Limitation · %s\n", terminalText(l))
	}
	for _, c := range result.CoverageChanges {
		fmt.Fprintf(buf, "  Coverage · %s %s · %s → %s\n", terminalText(string(c.Source)), terminalText(c.Resource), terminalText(string(c.Before)), terminalText(string(c.After)))
	}
	writeOccurrence := func(when string, f model.Finding) {
		fmt.Fprintf(buf, "    %s %s · %s · %s · %s\n", when, resourceLabel(f.Resource), terminalText(string(f.Severity)), terminalText(string(f.Confidence)), terminalText(f.Symptom))
		for _, e := range f.Evidence {
			fmt.Fprintf(buf, "      %s = %s\n", terminalText(e.Field), terminalText(e.Value))
		}
	}
	for _, section := range []struct {
		label   string
		changes []investigation.Change
	}{{"Resource changes", result.ResourceChanges}, {"Workload changes", result.WorkloadChanges}} {
		fmt.Fprintf(buf, "\n%s (%d)\n", section.label, len(section.changes))
		for _, c := range section.changes {
			fmt.Fprintf(buf, "  %s · %s · %s · %s\n    %s\n", terminalText(string(c.Classification)), resourceLabel(c.Subject), terminalText(c.RuleID), terminalText(c.Condition), terminalText(c.Reason))
			for _, diff := range c.Differences {
				fmt.Fprintf(buf, "    Change · %s\n", terminalText(diff))
			}
			for _, e := range c.Before {
				writeOccurrence("Before", e.Finding)
			}
			for _, e := range c.After {
				writeOccurrence("After", e.Finding)
			}
		}
	}
	return buf.Flush()
}

func stampTime(at time.Time) string { return at.UTC().Format(time.RFC3339) }
func historyNumber(n *float64) string {
	if n == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.5g", *n)
}

// Package engine coordinates Kubernetes collection and deterministic diagnosis.
package engine

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/analyze"
	"github.com/DheerajShaik/kubedoctor/internal/model"
)

type Collector interface {
	Collect(context.Context, model.ScanRequest) model.Snapshot
}

type Scanner interface {
	Scan(context.Context, model.ScanRequest) model.Report
}

type Engine struct {
	collector Collector
}

func New(collector Collector) *Engine {
	return &Engine{collector: collector}
}

func (e *Engine) Scan(ctx context.Context, request model.ScanRequest) model.Report {
	return Analyze(e.capture(ctx, request))
}

// Capture retains evidence for offline analysis and requests optional
// provenance unavailable to legacy scans.
func (e *Engine) Capture(ctx context.Context, request model.ScanRequest) model.Snapshot {
	request.IncludeProvenance = true
	return e.capture(ctx, request)
}

func (e *Engine) capture(ctx context.Context, request model.ScanRequest) model.Snapshot {
	snapshot := e.collector.Collect(ctx, request)
	snapshot.Scope = request.Scope
	if snapshot.CollectedAt.IsZero() {
		snapshot.CollectedAt = time.Now().UTC()
	}
	if snapshot.StartedAt.IsZero() {
		snapshot.StartedAt = snapshot.CollectedAt
	}
	if snapshot.FinishedAt.IsZero() {
		snapshot.FinishedAt = snapshot.CollectedAt
	}
	if ctx.Err() != nil {
		snapshot.Sources = append(slices.Clone(snapshot.Sources), model.SourceResult{
			Source:   "scan",
			Status:   model.Failed,
			Required: true,
			Detail:   "Scan timeout or cancellation interrupted collection.",
		})
	}
	return snapshot
}

// Analyze deterministically evaluates a captured snapshot without any I/O.
func Analyze(snapshot model.Snapshot) model.Report {
	findings, checks := analyze.Run(snapshot)
	report := newReport(snapshot.Scope, snapshot.CollectedAt)
	report.Findings = append(report.Findings, findings...)
	report.Checks = append(report.Checks, checks...)
	for _, observation := range snapshot.Sources {
		addSource(&report, observation)
	}
	sortReport(&report)
	return report
}

// FailureReport represents a scan that could not start. Detail must contain a
// caller-authored explanation and must not contain a raw authentication error.
func FailureReport(scope model.Scope, detail string) model.Report {
	report := newReport(scope, time.Now())
	addSource(&report, model.SourceResult{
		Source:   "configuration",
		Status:   model.Failed,
		Required: true,
		Detail:   detail,
	})
	for _, check := range analyze.Checks() {
		report.Checks = append(report.Checks, model.CheckResult{
			ID:     check.ID,
			Status: model.Skipped,
			Detail: "Kubernetes client could not be configured.",
		})
	}
	return report
}

func newReport(scope model.Scope, at time.Time) model.Report {
	return model.Report{
		SchemaVersion:    model.SchemaVersion,
		Scope:            scope,
		CollectedAt:      at.UTC(),
		Complete:         true,
		Findings:         []model.Finding{},
		CollectionErrors: []model.SourceResult{},
		Sources:          []model.SourceResult{},
		Checks:           []model.CheckResult{},
	}
}

func addSource(report *model.Report, observation model.SourceResult) {
	report.Sources = append(report.Sources, observation)
	if observation.Status == model.Complete || observation.Status == model.NotApplicable {
		return
	}
	report.CollectionErrors = append(report.CollectionErrors, observation)
	if observation.Required {
		report.Complete = false
	}
}

func sortReport(report *model.Report) {
	slices.SortFunc(report.Findings, func(a, b model.Finding) int {
		return cmp.Or(
			cmp.Compare(b.Severity.Rank(), a.Severity.Rank()),
			cmp.Compare(a.RuleID, b.RuleID),
			compareResource(a.Resource, b.Resource),
			cmp.Compare(a.Symptom, b.Symptom),
			slices.CompareFunc(a.Evidence, b.Evidence, func(a, b model.Evidence) int {
				return cmp.Or(cmp.Compare(a.Field, b.Field), cmp.Compare(a.Value, b.Value))
			}),
			cmp.Compare(a.Confidence, b.Confidence),
			cmp.Compare(a.ProbableCause, b.ProbableCause),
			slices.Compare(a.NextSteps, b.NextSteps),
			compareIncident(a.Incident, b.Incident),
		)
	})
	slices.SortFunc(report.Checks, func(a, b model.CheckResult) int {
		return cmp.Compare(a.ID, b.ID)
	})
	slices.SortFunc(report.Sources, compareSource)
	slices.SortFunc(report.CollectionErrors, compareSource)
}

func compareIncident(a, b *model.Resource) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	default:
		return compareResource(*a, *b)
	}
}

func compareResource(a, b model.Resource) int {
	return cmp.Or(
		cmp.Compare(a.Kind, b.Kind),
		cmp.Compare(a.Namespace, b.Namespace),
		cmp.Compare(a.Name, b.Name),
		cmp.Compare(a.UID, b.UID),
	)
}

func compareSource(a, b model.SourceResult) int {
	return cmp.Or(
		cmp.Compare(a.Source, b.Source),
		cmp.Compare(a.Resource, b.Resource),
		cmp.Compare(a.Status, b.Status),
		cmp.Compare(a.Detail, b.Detail),
	)
}

// ExitCode evaluates the full report. An empty threshold disables the
// findings threshold and leaves the required-coverage policy in force.
func ExitCode(report model.Report, threshold model.Severity) int {
	if !report.Complete {
		return 2
	}
	if threshold != "" {
		for _, finding := range report.Findings {
			if finding.Severity.Rank() >= threshold.Rank() {
				return 1
			}
		}
	}
	return 0
}

func Filter(report model.Report, minimum model.Severity) model.Report {
	findings := make([]model.Finding, 0, len(report.Findings))
	for _, finding := range report.Findings {
		if finding.Severity.Rank() >= minimum.Rank() {
			findings = append(findings, finding)
		}
	}
	report.Findings = findings
	return report
}

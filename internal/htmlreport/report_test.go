package htmlreport

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/investigation"
	"github.com/DheerajShaik/kubedoctor/internal/model"
)

// The browser fixture includes text significant to HTML parsers. It contains
// no executable instructions, external resource, or real cluster evidence.
func browserView() investigation.Investigation {
	at := time.Date(2026, 1, 12, 16, 0, 0, 0, time.UTC)
	res := model.Resource{Kind: "Pod", Namespace: "demo", Name: "shop-a", UID: "example"}
	wl := model.Resource{Kind: "Deployment", Namespace: "demo", Name: "shop", UID: "example-owner"}
	f := model.Finding{RuleID: "fixture.wait", Resource: res, Severity: model.Warning, Symptom: "Image pull is waiting", Confidence: model.Observed, Evidence: []model.Evidence{{Field: "inert_marker", Value: "<x-observation>quoted & observed</x-observation>"}}, NextSteps: []string{"Inspect the image reference."}}
	second := model.Finding{RuleID: "fixture.requests", Resource: wl, Severity: model.Info, Symptom: "CPU request unspecified", Confidence: model.Observed}
	v := investigation.Investigation{SchemaVersion: "1", RuleVersion: "1", CaptureRuleVersion: "1", AnalysisMode: "replay", CaptureStartedAt: at.Add(-time.Second), CaptureCompletedAt: at, EvaluatedAt: at, Scope: model.Scope{Kind: model.NamespaceScope, Namespace: "demo", Context: "browser-fixture"}}
	v.Report = model.Report{Complete: false, Findings: []model.Finding{f, second}, Sources: []model.SourceResult{{Source: model.SourcePods, Status: model.Complete, Required: true}, {Source: model.SourceDeployments, Status: model.PermissionDenied, Required: true}}}
	v.Resources = []investigation.ResourceObservation{{Resource: res, Workload: &wl}, {Resource: wl, Workload: &wl}}
	v.Relationships = []investigation.Relationship{{Kind: investigation.Owns, From: res, To: wl, Source: model.SourcePods, Basis: investigation.MatchUID}}
	v.Incidents = []investigation.Incident{{Anchor: wl, Severity: model.Warning, FindingIndexes: []int{0}, RelationshipIndexes: []int{0}}}
	v.Timeline = []investigation.TimelineEntry{{Time: at, TimeKind: investigation.ObservationTime, Source: model.SourcePods, Summary: "Pod waiting observed"}}
	v.Limitations = []string{"Deployment inventory was denied."}
	v.History = &model.HistoryReport{Status: model.HistoryPartial, WindowStart: at.Add(-time.Hour), WindowEnd: at, Targets: []model.HistoryTargetReport{{Target: model.HistoryTarget{Pod: res, Workload: wl, Container: "shop"}, CPU: model.HistoryMetricReport{Unit: "mCPU", Status: model.HistoryInsufficient, Samples: []model.HistoryPoint{{At: at.Add(-time.Hour), Value: 50}, {At: at, Value: 12}}}}}}
	return v
}

func TestRenderedReportAndBrowserFixture(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, browserView()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "<x-observation>") || !strings.Contains(b.String(), "&lt;x-observation&gt;") || !strings.Contains(b.String(), "permission_denied") {
		t.Fatal("inert text escaping or coverage failed")
	}
	if dir := os.Getenv("KUBEDOCTOR_HTML_TEST_OUTPUT"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "browser.html"), b.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestChartPreservesPeaksAndMissingTimes(t *testing.T) {
	at := time.Unix(1000, 0)
	points := []model.HistoryPoint{{At: at, Value: 0}, {At: at.Add(time.Millisecond), Value: 1000}, {At: at.Add(time.Hour), Value: 5}}
	bins, maximum := chartEnvelope(points, at, at.Add(time.Hour))
	if len(bins) != 2 || maximum != "1000" || bins[0].YLow <= bins[0].YHigh {
		t.Fatalf("%v %s", bins, maximum)
	}
}
func BenchmarkRenderInvestigation(b *testing.B) {
	v := browserView()
	b.ReportAllocs()
	for b.Loop() {
		if err := Write(io.Discard, v); err != nil {
			b.Fatal(err)
		}
	}
}

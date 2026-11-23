// Package htmlreport renders standalone, offline investigation and comparison documents.
package htmlreport

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html/template"
	"io"
	"strings"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/artifact"
	"github.com/DheerajShaik/kubedoctor/internal/investigation"
	"github.com/DheerajShaik/kubedoctor/internal/model"
)

//go:embed report.tmpl
var markup string

//go:embed report.css
var styles string

//go:embed filters.js
var filters string

var document = template.Must(template.New("page").Funcs(template.FuncMap{
	"label": label, "stamp": stamp,
	"fixedCSS": func() template.CSS { return template.CSS(styles) },
	"fixedJS":  func() template.JS { return template.JS(filters) },
	"value":    formatValue,
	"number":   func(i int) int { return i + 1 },
}).Parse(strings.Replace(markup, "SCRIPT_DIGEST", scriptDigest(filters), 1)))

func scriptDigest(script string) string {
	digest := sha256.Sum256([]byte(script))
	return base64.StdEncoding.EncodeToString(digest[:])
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "Unrecorded"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func label(r model.Resource) string {
	if r.Namespace != "" {
		return r.Kind + " " + r.Namespace + "/" + r.Name
	}
	return r.Kind + " " + r.Name
}

type page struct {
	Title    string
	View     *investigation.Investigation
	Diff     *investigation.Comparison
	Findings []findingRow
	Counts   counts
	History  []historyChart
}

type findingRow struct {
	model.Finding
	Workload string
	Index    int
}

type counts struct{ Critical, Warning, Info int }

func Write(w io.Writer, v investigation.Investigation) error {
	p := page{Title: "Incident investigation", View: &v}
	owners := make(map[model.Resource]string)
	for _, r := range v.Resources {
		if r.Workload != nil {
			owners[r.Resource] = label(*r.Workload)
		}
	}
	for i, f := range v.Report.Findings {
		wl := owners[f.Resource]
		if wl == "" && f.Incident != nil {
			wl = label(*f.Incident)
		}
		p.Findings = append(p.Findings, findingRow{Finding: f, Index: i, Workload: wl})
		switch f.Severity {
		case model.Critical:
			p.Counts.Critical++
		case model.Warning:
			p.Counts.Warning++
		case model.Info:
			p.Counts.Info++
		}
	}
	p.History = historyCharts(v.History)
	return execute(w, p)
}

func WriteComparison(w io.Writer, v investigation.Comparison) error {
	return execute(w, page{Title: "Capture comparison", Diff: &v})
}

const maxHTMLBytes = 32 << 20

func execute(w io.Writer, p page) error {
	return document.Execute(artifact.LimitOutput(w, maxHTMLBytes), p)
}

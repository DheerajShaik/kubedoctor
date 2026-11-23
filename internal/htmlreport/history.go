package htmlreport

import (
	"math"
	"strconv"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/history"
	"github.com/DheerajShaik/kubedoctor/internal/model"
)

type historyChart struct {
	Name     string
	Identity string
	Metric   model.HistoryMetricReport
	Maximum  string
	Bins     []chartBin
	Link     string
}

type chartBin struct{ X, YLow, YHigh int }

func formatValue(n *float64) string {
	if n == nil {
		return "unavailable"
	}
	return strconv.FormatFloat(*n, 'g', 5, 64)
}

func historyCharts(h *model.HistoryReport) []historyChart {
	if h == nil {
		return nil
	}
	var result []historyChart
	for _, t := range h.Targets {
		for _, series := range []struct {
			name string
			m    model.HistoryMetricReport
		}{{"CPU", t.CPU}, {"Memory", t.Memory}} {
			chart := historyChart{Name: series.name, Identity: label(t.Target.Workload) + " · " + label(t.Target.Pod) + " · " + t.Target.Container, Metric: series.m}
			if history.ValidGrafanaLink(t.GrafanaURL) {
				chart.Link = t.GrafanaURL
			}
			chart.Bins, chart.Maximum = chartEnvelope(series.m.Samples, h.WindowStart, h.WindowEnd)
			result = append(result, chart)
		}
	}
	return result
}

// Bucket extrema preserve spikes and leave missing time buckets blank. The
// table reports statistics computed by the history analyzer on all samples.
func chartEnvelope(points []model.HistoryPoint, start, end time.Time) ([]chartBin, string) {
	const columns = 96
	if !end.After(start) {
		return nil, ""
	}
	type span struct {
		low, high float64
		seen      bool
	}
	var buckets [columns]span
	var peak float64
	for _, p := range points {
		if !p.At.Before(start) && !p.At.After(end) && p.Value >= 0 && !math.IsNaN(p.Value) && !math.IsInf(p.Value, 0) {
			i := min(columns-1, int(p.At.Sub(start).Seconds()/end.Sub(start).Seconds()*columns))
			b := &buckets[i]
			if !b.seen || p.Value < b.low {
				b.low = p.Value
			}
			b.high = max(b.high, p.Value)
			b.seen = true
			peak = max(peak, p.Value)
		}
	}
	scale := peak
	if scale == 0 {
		scale = 1
	}
	var out []chartBin
	for i, b := range buckets {
		if b.seen {
			out = append(out, chartBin{X: 2 + i*396/(columns-1), YLow: 78 - int(b.low/scale*76), YHigh: 78 - int(b.high/scale*76)})
		}
	}
	return out, formatValue(&peak)
}

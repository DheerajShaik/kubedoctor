package history

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

const (
	maxResponseBytes = 2 << 20
	maxSeries        = 2*maxTargets + 1
	maxRawValue      = 1e18
)

type matrixSeries struct {
	Labels     map[string]string   `json:"metric"`
	Values     [][]json.RawMessage `json:"values"`
	Histograms []json.RawMessage   `json:"histograms"`
}

type rangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string         `json:"resultType"`
		Result     []matrixSeries `json:"result"`
	} `json:"data"`
	Warnings []json.RawMessage `json:"warnings"`
	Infos    []json.RawMessage `json:"infos"`
}

type queryResult struct {
	series map[seriesKey]matrixSeries
	status model.HistoryStatus
	reason model.HistoryReason
}

type seriesKey struct {
	target    targetKey
	component string
}

func (p *Provider) readMetric(ctx context.Context, metric string, targets []sourceTarget, start, end time.Time) map[targetKey]model.HistorySeries {
	results := make(map[targetKey]model.HistorySeries, len(targets))
	known := make(map[targetKey]bool, len(targets))
	queries := make([]string, 0, len(targets)*2)
	for _, target := range targets {
		key := target.key()
		if key.runtime == "" {
			results[key] = unavailable(model.HistoryUnavailable, model.HistoryRuntimeUnknown)
			continue
		}
		known[key] = true
		selector := p.selector(metric, key)
		// Both parts are evaluated in the same Prometheus request. This
		// avoids pairing a value with a scrape that arrived between requests.
		queries = append(queries, labelComponent(selector, "value"), labelComponent("timestamp("+selector+")", "source_time"))
	}
	if len(queries) == 0 {
		return results
	}
	response := p.rangeQuery(ctx, strings.Join(queries, " or "), known, start, end)
	if response.series == nil {
		for key := range known {
			results[key] = unavailable(response.status, response.reason)
		}
		return results
	}
	for key := range known {
		value, haveValue := response.series[seriesKey{key, "value"}]
		stamp, haveStamp := response.series[seriesKey{key, "source_time"}]
		result := unavailable(model.HistoryUnavailable, model.HistoryMetricAbsent)
		if haveValue && haveStamp {
			result = joinScrapes(value, stamp, start, end, p.scrape)
		}
		if response.status != model.HistoryComplete {
			result.Status, result.Reason = response.status, response.reason
		}
		results[key] = result
	}
	return results
}

func labelComponent(expr, component string) string {
	return "label_replace(" + expr + `,"kubedoctor_component","` + component + `","cluster",".+")`
}

func unavailable(status model.HistoryStatus, reason model.HistoryReason) model.HistorySeries {
	return model.HistorySeries{Status: status, Reason: reason, Points: []model.HistoryPoint{}}
}

func (p *Provider) selector(metric string, key targetKey) string {
	// Every label value is quoted before it enters a fixed equality selector.
	// Metric identifiers have already been syntax-checked in New.
	quote := strconv.Quote
	return metric + "{cluster=" + quote(p.cluster) +
		",namespace=" + quote(key.namespace) + ",pod=" + quote(key.pod) +
		",pod_uid=" + quote(key.uid) + ",container=" + quote(key.container) +
		",container_id=" + quote(key.runtime) + "}"
}

func (p *Provider) rangeQuery(ctx context.Context, query string, known map[targetKey]bool, start, end time.Time) queryResult {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	form := url.Values{
		"query":   {query},
		"start":   {start.Format(time.RFC3339)},
		"end":     {end.Format(time.RFC3339)},
		"step":    {strconv.FormatInt(int64(p.step/time.Second), 10)},
		"timeout": {"5s"},
		"limit":   {strconv.Itoa(maxSeries)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return queryFault(model.HistoryFailed, model.HistoryRequestFailed)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	response, err := p.client.Do(req)
	if err != nil {
		reason := model.HistoryRequestFailed
		if ctx.Err() != nil {
			reason = model.HistoryRequestTimeout
		}
		return queryFault(model.HistoryFailed, reason)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return queryFault(model.HistoryPermission, model.HistoryRequestDenied)
	default:
		return queryFault(model.HistoryFailed, model.HistoryRequestFailed)
	}
	if response.ContentLength > maxResponseBytes {
		return queryFault(model.HistoryTruncated, model.HistoryResponseLimited)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return queryFault(model.HistoryFailed, model.HistoryRequestTimeout)
		}
		return queryFault(model.HistoryFailed, model.HistoryRequestFailed)
	}
	if len(body) > maxResponseBytes {
		return queryFault(model.HistoryTruncated, model.HistoryResponseLimited)
	}
	var envelope rangeResponse
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Status != "success" || envelope.Data.ResultType != "matrix" {
		return queryFault(model.HistoryFailed, model.HistoryBadResponse)
	}
	if len(envelope.Data.Result) >= maxSeries {
		return queryFault(model.HistoryTruncated, model.HistoryResponseLimited)
	}
	result := queryResult{series: make(map[seriesKey]matrixSeries), status: model.HistoryComplete}
	if len(envelope.Warnings)+len(envelope.Infos) > 0 {
		result.status, result.reason = model.HistoryPartial, model.HistoryResponseWarnings
	}
	duplicate := make(map[seriesKey]bool)
	for _, row := range envelope.Data.Result {
		key := seriesKey{
			target:    targetKey{row.Labels["namespace"], row.Labels["pod"], row.Labels["pod_uid"], row.Labels["container"], row.Labels["container_id"]},
			component: row.Labels["kubedoctor_component"],
		}
		if row.Labels["cluster"] != p.cluster || !known[key.target] || key.component != "value" && key.component != "source_time" {
			result.status, result.reason = model.HistoryPartial, model.HistoryBadResponse
			continue
		}
		if _, exists := result.series[key]; exists || duplicate[key] {
			result.status, result.reason = model.HistoryPartial, model.HistoryAmbiguousSeries
			delete(result.series, key)
			duplicate[key] = true
			continue
		}
		if len(row.Values) > maxSteps {
			return queryFault(model.HistoryTruncated, model.HistoryResponseLimited)
		}
		result.series[key] = row
	}
	return result
}

func queryFault(status model.HistoryStatus, reason model.HistoryReason) queryResult {
	return queryResult{status: status, reason: reason}
}

func joinScrapes(values, stamps matrixSeries, start, end time.Time, scrape time.Duration) model.HistorySeries {
	origin := make(map[int64]int64, len(stamps.Values))
	points := make(map[int64]float64, len(values.Values))
	startMS, endMS := start.UnixMilli(), end.UnixMilli()
	maxAge := (2 * scrape).Milliseconds()
	invalid := len(values.Histograms)+len(stamps.Histograms) > 0
	for _, pair := range stamps.Values {
		evaluated, source, ok := pairValues(pair)
		if !ok || evaluated < startMS || evaluated > endMS || source > float64(evaluated)/1e3 {
			invalid = true
			continue
		}
		sourceMS, ok := milliseconds(source)
		if !ok {
			invalid = true
			continue
		}
		if previous, exists := origin[evaluated]; exists && previous != sourceMS {
			invalid = true
			continue
		}
		origin[evaluated] = sourceMS
	}
	seenEvaluations := make(map[int64]bool, len(values.Values))
	for _, pair := range values.Values {
		evaluated, value, valid := pairValues(pair)
		source, hasOrigin := origin[evaluated]
		if !valid || !hasOrigin || value < 0 || value > maxRawValue || seenEvaluations[evaluated] {
			invalid = true
			continue
		}
		seenEvaluations[evaluated] = true
		if source < startMS || evaluated-source > maxAge {
			continue
		}
		if previous, exists := points[source]; exists && previous != value {
			invalid = true
			continue
		}
		points[source] = value
	}
	invalid = invalid || len(seenEvaluations) != len(origin)
	times := make([]int64, 0, len(points))
	for when := range points {
		times = append(times, when)
	}
	slices.Sort(times)
	result := model.HistorySeries{Status: model.HistoryComplete, Points: make([]model.HistoryPoint, 0, len(times))}
	for _, when := range times {
		result.Points = append(result.Points, model.HistoryPoint{At: time.UnixMilli(when).UTC(), Value: points[when]})
	}
	if invalid {
		result.Status, result.Reason = model.HistoryInsufficient, model.HistoryBadSamples
	}
	return result
}

func pairValues(pair []json.RawMessage) (int64, float64, bool) {
	if len(pair) != 2 {
		return 0, 0, false
	}
	var timestamp float64
	var value string
	if json.Unmarshal(pair[0], &timestamp) != nil || json.Unmarshal(pair[1], &value) != nil {
		return 0, 0, false
	}
	at, ok := milliseconds(timestamp)
	number, err := strconv.ParseFloat(value, 64)
	return at, number, ok && err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func milliseconds(seconds float64) (int64, bool) {
	// Permit RFC3339 years used for capture times through Go's JSON encoder.
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds >= 253402300800 {
		return 0, false
	}
	return int64(math.Round(seconds * 1e3)), true
}

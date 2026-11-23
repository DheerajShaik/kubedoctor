// Package history collects optional Prometheus evidence and evaluates it offline.
package history

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

const (
	maxConfigBytes = 16 << 10
	maxCABytes     = 128 << 10
	maxTargets     = 8
	maxSteps       = 2_048
)

var (
	metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]{0,127}$`)
	envName    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,127}$`)
	simpleID   = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	clusterID  = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)
	runtimeID  = regexp.MustCompile(`^[a-z0-9_-]+://[a-f0-9]{64}$`)
	dnsLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	pathPart   = regexp.MustCompile(`^[a-zA-Z0-9_-][a-zA-Z0-9_.-]{0,63}$`)
)

type TargetPolicy struct {
	Namespace   string `json:"namespace"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Container   string `json:"container"`
	VPADisabled bool   `json:"vpa_disabled"`
}

type Config struct {
	URL            string                  `json:"url"`
	BearerTokenEnv string                  `json:"bearer_token_env"`
	CAFile         string                  `json:"ca_file"`
	Cluster        string                  `json:"cluster"`
	CPUCounter     string                  `json:"cpu_counter_metric"`
	MemoryWorking  string                  `json:"memory_working_set_metric"`
	Window         string                  `json:"window"`
	Step           string                  `json:"step"`
	ScrapeInterval string                  `json:"scrape_interval"`
	Timeout        string                  `json:"timeout"`
	Targets        []TargetPolicy          `json:"targets"`
	Grafana        *model.GrafanaDashboard `json:"grafana"`
}

type Provider struct {
	endpoint *url.URL
	client   *http.Client
	token    string
	cluster  string
	cpu      string
	memory   string
	window   time.Duration
	step     time.Duration
	scrape   time.Duration
	timeout  time.Duration
	targets  []TargetPolicy
	grafana  *model.GrafanaDashboard
}

// Load reads one bounded startup configuration. It does not contact a provider.
func Load(path string) (*Provider, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("could not open history configuration")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return nil, errors.New("could not read history configuration within its byte budget")
	}
	var cfg Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("history configuration must be one supported JSON object")
	}
	if cfg.CAFile != "" && !filepath.IsAbs(cfg.CAFile) {
		cfg.CAFile = filepath.Join(filepath.Dir(path), cfg.CAFile)
	}
	return New(cfg)
}

// New takes trusted process startup or operator configuration.
func New(cfg Config) (*Provider, error) {
	endpoint, err := validatedURL(cfg.URL, cfg.BearerTokenEnv == "" && cfg.CAFile == "", false)
	if err != nil {
		return nil, fmt.Errorf("invalid Prometheus URL: %w", err)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/query_range"
	if !clusterID.MatchString(cfg.Cluster) {
		return nil, errors.New("history requires an explicit cluster label value")
	}
	if cfg.CPUCounter == "" {
		cfg.CPUCounter = "container_cpu_usage_seconds_total"
	}
	if cfg.MemoryWorking == "" {
		cfg.MemoryWorking = "container_memory_working_set_bytes"
	}
	if cfg.CPUCounter == cfg.MemoryWorking || !metricName.MatchString(cfg.CPUCounter) || !metricName.MatchString(cfg.MemoryWorking) {
		return nil, errors.New("history metric names must be distinct Prometheus identifiers")
	}
	window, windowErr := duration(cfg.Window, "6h")
	step, stepErr := duration(cfg.Step, "1m")
	scrape, scrapeErr := duration(cfg.ScrapeInterval, "1m")
	timeout, timeoutErr := duration(cfg.Timeout, "20s")
	if windowErr != nil || stepErr != nil || scrapeErr != nil || timeoutErr != nil ||
		!validWindow(window, step, scrape) || timeout < time.Second || timeout > 30*time.Second {
		return nil, errors.New("invalid history window, step, scrape interval, or timeout")
	}
	if err := validateTargetPolicies(cfg.Targets); err != nil {
		return nil, err
	}
	var dashboard *model.GrafanaDashboard
	if cfg.Grafana != nil {
		if !validDashboard(*cfg.Grafana) {
			return nil, errors.New("Grafana requires an HTTPS origin and dashboard UID and slug")
		}
		value := *cfg.Grafana
		dashboard = &value
	}
	token, err := credential(cfg.BearerTokenEnv)
	if err != nil {
		return nil, err
	}
	transport, err := newTransport(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	return &Provider{
		endpoint: endpoint, token: token, cluster: cfg.Cluster, cpu: cfg.CPUCounter, memory: cfg.MemoryWorking,
		window: window, step: step, scrape: scrape, timeout: timeout,
		targets: append([]TargetPolicy(nil), cfg.Targets...), grafana: dashboard,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func duration(value, fallback string) (time.Duration, error) {
	if value == "" {
		value = fallback
	}
	return time.ParseDuration(value)
}

func validWindow(window, step, scrape time.Duration) bool {
	return window >= 15*time.Minute && window <= 7*24*time.Hour &&
		step >= 5*time.Second && step <= scrape && scrape <= 15*time.Minute &&
		window%time.Second == 0 && step%time.Second == 0 && scrape%time.Second == 0 &&
		window%step == 0 && window/step+1 <= maxSteps && window/scrape >= 25
}

func validateTargetPolicies(targets []TargetPolicy) error {
	if len(targets) == 0 || len(targets) > maxTargets {
		return errors.New("history requires one to eight exact target policies")
	}
	seen := make(map[TargetPolicy]bool, len(targets))
	for _, policy := range targets {
		key := policy
		key.VPADisabled = false
		if !dnsLabel.MatchString(policy.Namespace) || !dnsName(policy.Name) || !dnsLabel.MatchString(policy.Container) ||
			!supportedKind(policy.Kind) || seen[key] {
			return errors.New("invalid or repeated history target policy")
		}
		seen[key] = true
	}
	return nil
}

func supportedKind(kind string) bool {
	switch kind {
	case "Pod", "Deployment", "ReplicaSet", "DaemonSet", "StatefulSet", "Job":
		return true
	default:
		return false
	}
}

func dnsName(value string) bool {
	if len(value) > 253 {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if !dnsLabel.MatchString(part) {
			return false
		}
	}
	return true
}

func validatedURL(raw string, allowLoopback, originOnly bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 1_024 || u == nil || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.Opaque != "" || u.RawPath != "" {
		return nil, errors.New("expected an absolute URL without credentials, query, fragment, or escaped path")
	}
	addr, addrErr := netip.ParseAddr(u.Hostname())
	if addrErr != nil && !dnsName(strings.ToLower(u.Hostname())) {
		return nil, errors.New("invalid host")
	}
	if u.Scheme != "https" && !(allowLoopback && u.Scheme == "http" && addrErr == nil && addr.IsLoopback()) {
		return nil, errors.New("HTTPS is required except for credential-free literal loopback")
	}
	if port := u.Port(); port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return nil, errors.New("invalid port")
		}
	}
	if originOnly && u.Path != "" && u.Path != "/" {
		return nil, errors.New("expected a bare origin")
	}
	for _, component := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if component != "" && !pathPart.MatchString(component) {
			return nil, errors.New("invalid path component")
		}
	}
	return u, nil
}

func credential(key string) (string, error) {
	if key == "" {
		return "", nil
	}
	if !envName.MatchString(key) {
		return "", errors.New("invalid bearer token environment variable name")
	}
	value := os.Getenv(key)
	if value == "" || len(value) > 8_192 || strings.ContainsFunc(value, func(r rune) bool { return r <= 32 || r >= 127 }) {
		return "", errors.New("bearer token environment value is missing or malformed")
	}
	return value, nil
}

func newTransport(caFile string) (*http.Transport, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		file, err := os.Open(caFile)
		if err != nil {
			return nil, errors.New("could not open Prometheus CA file")
		}
		defer file.Close()
		pem, err := io.ReadAll(io.LimitReader(file, maxCABytes+1))
		if err != nil || len(pem) > maxCABytes {
			return nil, errors.New("could not read Prometheus CA file within its byte budget")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("could not load system trust roots")
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("Prometheus CA file did not contain a certificate")
		}
		config.RootCAs = roots
	}
	return &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSClientConfig:        config,
		TLSHandshakeTimeout:    5 * time.Second,
		IdleConnTimeout:        30 * time.Second,
		MaxConnsPerHost:        2,
		MaxIdleConnsPerHost:    2,
		MaxResponseHeaderBytes: 32 << 10,
		ForceAttemptHTTP2:      true,
	}, nil
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	"k8s.io/client-go/rest"
)

type scanFunc func(context.Context, model.ScanRequest) model.Report

func (f scanFunc) Scan(ctx context.Context, request model.ScanRequest) model.Report {
	return f(ctx, request)
}

const fixtureConfig = `apiVersion: v1
kind: Config
clusters:
- name: one
  cluster: {server: "https://api.one.invalid"}
- name: two
  cluster: {server: "https://api.two.invalid"}
contexts:
- name: one
  context: {cluster: one, namespace: current-namespace}
- name: two
  context: {cluster: two, namespace: alternate-namespace}
current-context: one
`

func TestScopeAndContextSelection(t *testing.T) {
	kubeconfig := configFile(t, fixtureConfig)
	for _, tc := range []struct {
		name, host string
		args       []string
		scope      model.Scope
	}{
		{"current kubeconfig namespace", "https://api.one.invalid", []string{"namespace"}, model.Scope{
			Kind: model.NamespaceScope, Namespace: "current-namespace", Context: "one",
		}},
		{"selected context and explicit namespace", "https://api.two.invalid", []string{"namespace", "payments", "--context", "two"}, model.Scope{
			Kind: model.NamespaceScope, Namespace: "payments", Context: "two",
		}},
		{"namespace flag", "https://api.one.invalid", []string{"namespace", "-n", "payments"}, model.Scope{
			Kind: model.NamespaceScope, Namespace: "payments", Context: "one",
		}},
		{"cluster skips namespace default", "https://api.two.invalid", []string{"--context", "two", "cluster"}, model.Scope{
			Kind: model.ClusterScope, Context: "two",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "--kubeconfig", kubeconfig, "-o", "json", "--timeout", "2s")
			factory := func(config *rest.Config) (engine.Scanner, error) {
				if config.Host != tc.host {
					t.Fatalf("host = %q, want %q", config.Host, tc.host)
				}
				return scanFunc(func(ctx context.Context, request model.ScanRequest) model.Report {
					if request.Scope != tc.scope {
						t.Fatalf("scope = %+v, want %+v", request.Scope, tc.scope)
					}
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > 2*time.Second {
						t.Fatalf("wrong deadline %v", deadline)
					}
					return engine.New(emptyCollector{}).Scan(ctx, request)
				}), nil
			}
			code, stdout, stderr := run(t, args, factory)
			if code != 0 || stderr != "" {
				t.Fatalf("exit = %d, stderr = %q", code, stderr)
			}
			var report model.Report
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatal(err)
			}
			if report.Scope != tc.scope || report.SchemaVersion != model.SchemaVersion || report.Findings == nil ||
				report.Checks == nil || report.CollectionErrors == nil {
				t.Fatalf("invalid report %+v", report)
			}
		})
	}
	after, err := os.ReadFile(kubeconfig)
	if err != nil || string(after) != fixtureConfig {
		t.Fatalf("kubeconfig changed: %v", err)
	}
}

func TestInputsRejectedBeforeKubeconfigOrNetwork(t *testing.T) {
	for _, args := range [][]string{
		{"namespace", "payments", "--namespace", "other"},
		{"namespace", "bad/name"},
		{"namespace", ""},
		{"namespace", "--namespace="},
		{"cluster", "payments"},
		{"cluster", "--namespace", "payments"},
		{"namespace", "payments", "-o", "yaml"},
		{"namespace", "payments", "--fail-on", "error"},
		{"namespace", "payments", "--min-severity", "none"},
		{"namespace", "payments", "--timeout", "0s"},
		{"namespace", "payments", "--timeout", "6m"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			exit, stdout, stderr := run(t, args, func(*rest.Config) (engine.Scanner, error) {
				t.Fatal("scanner factory called for invalid arguments")
				return nil, nil
			})
			if exit != 2 || stdout != "" || stderr == "" {
				t.Fatalf("exit = %d, stdout = %q, stderr = %q", exit, stdout, stderr)
			}
		})
	}
}

func TestIncompleteAndFilterExitPrecedence(t *testing.T) {
	kubeconfig := configFile(t, fixtureConfig)
	for _, tc := range []struct {
		name     string
		complete bool
		flags    []string
		exit     int
		visible  int
	}{
		{"threshold hit", true, nil, 1, 1},
		{"filter cannot hide exit", true, []string{"--min-severity", "critical"}, 1, 0},
		{"high threshold", true, []string{"--fail-on", "critical"}, 0, 1},
		{"threshold disabled", true, []string{"--fail-on", "none"}, 0, 1},
		{"incomplete has precedence", false, nil, 2, 1},
		{"threshold disabled while incomplete", false, []string{"--fail-on", "none"}, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"namespace", "payments", "--kubeconfig", kubeconfig, "-o", "json"}, tc.flags...)
			exit, stdout, stderr := run(t, args, func(*rest.Config) (engine.Scanner, error) {
				return scanFunc(func(_ context.Context, request model.ScanRequest) model.Report {
					return model.Report{
						Scope: request.Scope, SchemaVersion: model.SchemaVersion, Complete: tc.complete,
						Findings: []model.Finding{{Severity: model.Warning}},
					}
				}), nil
			})
			if exit != tc.exit || stderr != "" {
				t.Fatalf("exit = %d, stderr = %q, want exit %d", exit, stderr, tc.exit)
			}
			var report model.Report
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Findings) != tc.visible {
				t.Fatalf("visible findings = %d, want %d", len(report.Findings), tc.visible)
			}
		})
	}
}

func TestConfigurationFailureIsRedacted(t *testing.T) {
	const sentinel = "SENSITIVE_SENTINEL_AUTH"
	kubeconfig := configFile(t, "bad:\n  % "+sentinel)
	args := []string{"namespace", "payments", "--kubeconfig", kubeconfig, "-o", "json"}
	check := func(exit int, stdout, stderr string) {
		t.Helper()
		if exit != 2 || strings.Contains(stdout+stderr, sentinel) || stderr != "" {
			t.Fatalf("unexpected result %d, %q, %q", exit, stdout, stderr)
		}
		var report model.Report
		if err := json.Unmarshal([]byte(stdout), &report); err != nil || report.Complete || len(report.CollectionErrors) != 1 {
			t.Fatalf("invalid failure report %+v: %v", report, err)
		}
	}
	exit, stdout, stderr := run(t, args, func(*rest.Config) (engine.Scanner, error) {
		t.Fatal("invalid kubeconfig reached the scanner")
		return nil, nil
	})
	check(exit, stdout, stderr)
	args[3] = configFile(t, fixtureConfig)
	exit, stdout, stderr = run(t, args, func(*rest.Config) (engine.Scanner, error) {
		return nil, errors.New(sentinel)
	})
	check(exit, stdout, stderr)
}

func TestTextGroupingAndControls(t *testing.T) {
	owner := model.Resource{Kind: "Deployment", Namespace: "payments", Name: "checkout"}
	pod := model.Resource{Kind: "Pod", Namespace: "payments", Name: "checkout-1"}
	report := model.Report{
		Complete: true,
		Checks:   []model.CheckResult{{ID: "dns", Status: model.Inconclusive}},
		Findings: []model.Finding{
			{Resource: pod, Incident: &owner, Symptom: "waiting\x1b[31m"},
			{Resource: owner, Incident: &owner, Symptom: "rollout stalled"},
		},
	}
	var out bytes.Buffer
	if err := renderReport(&out, report, "text"); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Count(text, "Deployment payments/checkout") != 1 || strings.ContainsRune(text, '\x1b') ||
		!strings.Contains(text, "Pod payments/checkout-1") || !strings.Contains(text, "inconclusive") {
		t.Fatalf("unexpected text report %q", text)
	}
}

func configFile(t *testing.T, data string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func run(t *testing.T, args []string, factory scannerFactory) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := execute(t.Context(), args, &stdout, &stderr, Build{Version: "test", Commit: "test"}, factory)
	return exit, stdout.String(), stderr.String()
}

type emptyCollector struct{}

func (emptyCollector) Collect(_ context.Context, request model.ScanRequest) model.Snapshot {
	return model.Snapshot{Scope: request.Scope, CollectedAt: time.Now()}
}

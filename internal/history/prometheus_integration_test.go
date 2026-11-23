package history

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

func TestPrometheusIntegration(t *testing.T) {
	prometheus := os.Getenv("KUBEDOCTOR_PROMETHEUS_BIN")
	promtool := os.Getenv("KUBEDOCTOR_PROMTOOL_BIN")
	if prometheus == "" && promtool == "" {
		t.Skip("run scripts/history-integration.sh for the pinned live Prometheus fixture")
	}
	if !filepath.IsAbs(prometheus) || !filepath.IsAbs(promtool) {
		t.Fatal("live fixture requires absolute Prometheus/promtool paths")
	}
	for _, binary := range []string{prometheus, promtool} {
		output, err := exec.CommandContext(t.Context(), binary, "--version").CombinedOutput()
		if err != nil || !strings.Contains(string(output), "version 3.16.0") {
			t.Fatalf("fixture requires upstream Prometheus and promtool v3.16.0: %s %v", output, err)
		}
	}
	end := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	endpoint := startFixturePrometheus(t, prometheus, promtool, end)
	cfg := exampleConfig(endpoint)
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := exampleSnapshot(end)
	p.Enrich(t.Context(), &snapshot)
	assertAdequate(t, Analyze(snapshot))
	data, err := json.Marshal(snapshot.History)
	if err != nil || strings.Contains(string(data), "SYNTHETIC_PROVIDER_LABEL_SENTINEL") || strings.Contains(string(data), fixtureRuntime) {
		t.Fatal("normalized real Prometheus history retained unrelated labels or runtime identifiers")
	}

	const sparseRuntime = "containerd://fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	cfg.Targets = []TargetPolicy{{Namespace: "payments", Kind: "Pod", Name: "checkout-sparse", Container: "app", VPADisabled: true}}
	p, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Pods[0].Name, snapshot.Pods[0].UID = "checkout-sparse", "sparse-pod-uid"
	snapshot.Pods[0].Status.ContainerStatuses[0].ContainerID = sparseRuntime
	p.Enrich(t.Context(), &snapshot)
	sparse := Analyze(snapshot)
	if sparse.Status != model.HistoryInsufficient || len(sparse.Targets) != 1 ||
		sparse.Targets[0].CPU.DistinctScrapes != 3 || sparse.Targets[0].Memory.DistinctScrapes != 3 ||
		sparse.Targets[0].CPU.Advice.Request != nil || sparse.Targets[0].Memory.Advice.Request != nil {
		t.Fatalf("sparse real Prometheus data produced unsupported advice: %+v", sparse)
	}

	cfg.Targets[0].Name = "checkout-ambiguous"
	p, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Pods[0].Name, snapshot.Pods[0].UID = "checkout-ambiguous", "duplicate-pod-uid"
	p.Enrich(t.Context(), &snapshot)
	ambiguous := Analyze(snapshot)
	if ambiguous.Status != model.HistoryPartial || len(ambiguous.Targets) != 1 || ambiguous.Reason != model.HistoryAmbiguousSeries ||
		len(ambiguous.Targets[0].CPU.Samples) != 0 || len(ambiguous.Targets[0].Memory.Samples) != 0 {
		t.Fatalf("duplicated exporters were not marked ambiguous: %+v", ambiguous)
	}
}

func startFixturePrometheus(t *testing.T, prometheus, promtool string, end time.Time) string {
	t.Helper()
	dir := t.TempDir()
	config, metrics, database := filepath.Join(dir, "prometheus.yml"), filepath.Join(dir, "fixture.openmetrics"), filepath.Join(dir, "tsdb")
	if err := os.WriteFile(config, []byte("global:\n  scrape_interval: 30s\nscrape_configs: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metrics, []byte(fixtureMetrics(end)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	seed, err := exec.CommandContext(ctx, promtool, "tsdb", "create-blocks-from", "openmetrics", metrics, database).CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("could not seed isolated synthetic history: %v\n%s", err, seed)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "prometheus.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	process := exec.CommandContext(t.Context(), prometheus, "--config.file="+config, "--storage.tsdb.path="+database,
		"--web.listen-address="+address, "--query.timeout=5s")
	process.Stderr, process.Stdout = logFile, logFile
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	base := "http://" + address
	client := &http.Client{Timeout: 200 * time.Millisecond, Transport: &http.Transport{}}
	wait, stop := context.WithTimeout(t.Context(), 12*time.Second)
	defer stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(wait, http.MethodGet, base+"/-/ready", nil)
		if err != nil {
			t.Fatal(err)
		}
		if response, err := client.Do(req); err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return base
			}
		}
		select {
		case <-wait.Done():
			output, _ := os.ReadFile(logPath)
			t.Fatalf("disposable Prometheus did not start: %v\n%s", wait.Err(), output)
		case <-ticker.C:
		}
	}
}

func fixtureMetrics(end time.Time) string {
	const sparseRuntime = "containerd://fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	var out strings.Builder
	start := end.Add(-15 * time.Minute)
	for _, metric := range []struct {
		name, typeInfo string
		value          func(int) int
	}{
		{"container_cpu_usage_seconds_total", "# TYPE container_cpu_usage_seconds counter", func(i int) int { return 100 + i*9 }},
		{"container_memory_working_set_bytes", "# TYPE container_memory_working_set_bytes gauge", func(i int) int { return (64 + i) * mebibyte }},
	} {
		fmt.Fprintln(&out, metric.typeInfo)
		for _, target := range []struct {
			name, uid, runtime, instance string
			first                        int
		}{
			{"checkout-demo", "pod-uid", fixtureRuntime, "first", 0},
			{"checkout-sparse", "sparse-pod-uid", sparseRuntime, "first", 28},
			{"checkout-ambiguous", "duplicate-pod-uid", sparseRuntime, "first", 0},
			{"checkout-ambiguous", "duplicate-pod-uid", sparseRuntime, "second", 0},
		} {
			for i := target.first; i <= 30; i++ {
				at := start.Add(time.Duration(i) * 30 * time.Second)
				fmt.Fprintf(&out, "%s{cluster=\"lab\",container=\"app\",container_id=%q,instance=%q,namespace=\"payments\",pod=%q,pod_uid=%q,unrelated=\"SYNTHETIC_PROVIDER_LABEL_SENTINEL\"} %d %d\n",
					metric.name, target.runtime, target.instance, target.name, target.uid, metric.value(i), at.Unix())
			}
		}
	}
	fmt.Fprintln(&out, "# EOF")
	return out.String()
}

package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/collect"
	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	"k8s.io/client-go/rest"
)

func TestCaptureReplaysUnversionedHPAEvidence(t *testing.T) {
	const privateMessage = "PRIVATE_HPA_ERROR_SENTINEL"
	var requests atomic.Int32
	at := time.Now().UTC()
	namespace := "payments"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		const root = "/api/v1/namespaces/payments"
		timestamp := at.Add(-30 * time.Second).Format(time.RFC3339)
		switch req.URL.Path {
		case root:
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"payments","uid":"disposable-hpa-namespace"}}`)
		case "/apis/discovery.k8s.io/v1":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"discovery.k8s.io/v1","resources":[{"name":"endpointslices","namespaced":true,"kind":"EndpointSlice"}]}`)
		case root + "/events":
			fmt.Fprintf(w, `{"apiVersion":"v1","kind":"EventList","items":[{"metadata":{"namespace":"payments","name":"hpa-event"},"type":"Warning","source":{"component":"horizontal-pod-autoscaler"},"involvedObject":{"kind":"HorizontalPodAutoscaler","namespace":"payments","name":"fixture-scaler","uid":"hpa-uid"},"reason":"FailedGetResourceMetric","lastTimestamp":%q,"count":2,"message":%q}]}`, timestamp, privateMessage)
		case "/apis/autoscaling/v2/namespaces/payments/horizontalpodautoscalers":
			fmt.Fprintf(w, `{"apiVersion":"autoscaling/v2","kind":"HorizontalPodAutoscalerList","items":[{"metadata":{"namespace":"payments","name":"fixture-scaler","uid":"hpa-uid","creationTimestamp":%q},"spec":{"scaleTargetRef":{"apiVersion":"apps/v1","kind":"Deployment","name":"fixture-target"},"minReplicas":1,"maxReplicas":2},"status":{"currentReplicas":1,"desiredReplicas":1,"conditions":[{"type":"AbleToScale","status":"True","reason":"SucceededGetScale","lastTransitionTime":%q},{"type":"ScalingActive","status":"False","reason":"FailedGetResourceMetric","lastTransitionTime":%q,"message":%q}]}}]}`, at.Add(-time.Minute).Format(time.RFC3339), timestamp, timestamp, privateMessage)
		case "/apis/apps/v1/namespaces/payments/deployments":
			fmt.Fprint(w, `{"apiVersion":"apps/v1","kind":"DeploymentList","items":[{"metadata":{"namespace":"payments","name":"fixture-target","uid":"deployment-uid","generation":1},"spec":{"replicas":1},"status":{"observedGeneration":1,"availableReplicas":1,"updatedReplicas":1}}]}`)
		default:
			if writeEmptyHPACaptureList(req.URL.Path, w) {
				return
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"NotFound","code":404}`)
		}
	}))
	defer server.Close()
	capturePath := filepath.Join(t.TempDir(), "hpa-bundle.json")
	code, output, stderr := run(t, []string{"capture", "namespace", namespace, "--bundle", capturePath, "--kubeconfig", configFile(t, fixtureConfig), "-o", "json"}, func(*rest.Config) (engine.Scanner, error) {
		collector, err := collect.New(&rest.Config{Host: server.URL})
		if err != nil {
			return nil, err
		}
		return engine.New(collector), nil
	})
	var document struct {
		Report model.Report `json:"report"`
	}
	if code != 0 || json.Unmarshal([]byte(output), &document) != nil || !document.Report.Complete || !slices.ContainsFunc(document.Report.Findings, func(f model.Finding) bool {
		return f.RuleID == "hpa-scaling" && f.Severity == model.Warning && slices.Contains(f.Evidence, model.Evidence{Field: "event_reason", Value: "FailedGetResourceMetric"})
	}) {
		t.Fatalf("HPA capture exit %d, evidence %+v, stderr %s", code, document.Report.Findings, stderr)
	}
	raw, err := os.ReadFile(capturePath)
	if err != nil || strings.Contains(string(raw)+output+stderr, privateMessage) || !strings.Contains(string(raw), "FailedGetResourceMetric") {
		t.Fatalf("HPA evidence or privacy changed, error %v", err)
	}
	before := requests.Load()
	code, replay, stderr := run(t, []string{"replay", capturePath, "--kubeconfig", filepath.Join(t.TempDir(), "absent"), "-o", "json"}, func(*rest.Config) (engine.Scanner, error) {
		t.Fatal("offline replay configured live collection")
		return nil, nil
	})
	if code != 0 || replay != output || requests.Load() != before {
		t.Fatalf("HPA offline replay diverged, code %d and stderr %s", code, stderr)
	}
}

func writeEmptyHPACaptureList(path string, w http.ResponseWriter) bool {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if (len(segments) != 5 && len(segments) != 6) || segments[len(segments)-2] != "payments" {
		return false
	}
	kind := map[string]string{"pods": "Pod", "configmaps": "ConfigMap", "persistentvolumeclaims": "PersistentVolumeClaim",
		"deployments": "Deployment", "replicasets": "ReplicaSet", "daemonsets": "DaemonSet", "services": "Service", "endpointslices": "EndpointSlice",
		"ingresses": "Ingress", "networkpolicies": "NetworkPolicy", "poddisruptionbudgets": "PodDisruptionBudget", "statefulsets": "StatefulSet",
		"jobs": "Job", "cronjobs": "CronJob", "resourcequotas": "ResourceQuota", "limitranges": "LimitRange"}[segments[len(segments)-1]]
	if kind == "" {
		return false
	}
	apiVersion := "v1"
	if segments[0] == "apis" {
		apiVersion = segments[1] + "/" + segments[2]
	}
	fmt.Fprintf(w, `{"apiVersion":%q,"kind":%q,"items":[]}`, apiVersion, kind+"List")
	return true
}

package analyze

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var workloadTestTime = time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC)

func workloadFixture() model.Snapshot {
	s := model.Snapshot{Scope: model.Scope{Kind: model.NamespaceScope, Namespace: "payments"}, CollectedAt: workloadTestTime}
	for _, source := range []model.Source{
		model.SourceNodes, model.SourcePods, model.SourceEvents, model.SourceConfigMaps, model.SourceDeployments,
		model.SourceDaemonSets, model.SourcePVCs, model.SourceStorageClasses, model.SourceMetrics,
	} {
		s.Sources = append(s.Sources, model.SourceResult{Source: source, Status: model.Complete})
	}
	return s
}

func setWorkloadCoverage(s *model.Snapshot, source model.Source, coverage model.Coverage) {
	for i := range s.Sources {
		if s.Sources[i].Source == source {
			s.Sources[i].Status = coverage
			return
		}
	}
	s.Sources = append(s.Sources, model.SourceResult{Source: source, Status: coverage})
}

func workloadPod() core.Pod {
	return core.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "payments", Name: "processor", UID: "pod-1", CreationTimestamp: metav1.NewTime(workloadTestTime.Add(-15 * time.Minute))},
		Spec:       core.PodSpec{NodeName: "node-1", Containers: []core.Container{{Name: "app", Resources: workloadTestResources()}}},
		Status: core.PodStatus{Phase: core.PodRunning, ContainerStatuses: []core.ContainerStatus{{
			Name: "app", Ready: true, State: core.ContainerState{Running: &core.ContainerStateRunning{StartedAt: metav1.NewTime(workloadTestTime.Add(-10 * time.Minute))}},
		}}},
	}
}

func workloadTestResources() core.ResourceRequirements {
	return core.ResourceRequirements{
		Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("25m"), core.ResourceMemory: resource.MustParse("64Mi")},
		Limits:   core.ResourceList{core.ResourceCPU: resource.MustParse("200m"), core.ResourceMemory: resource.MustParse("128Mi")},
	}
}

func workloadExpect(t *testing.T, s model.Snapshot, rule string, status model.CheckStatus, count int) []model.Finding {
	t.Helper()
	findings, checks := analyzeWorkloads(s)
	matching := make([]model.Finding, 0, count)
	for _, finding := range findings {
		if finding.RuleID == rule {
			matching = append(matching, finding)
		}
	}
	if len(matching) != count {
		t.Fatalf("%s findings = %#v, want %d", rule, matching, count)
	}
	for _, check := range checks {
		if check.ID == rule {
			if check.Status != status {
				t.Fatalf("%s status = %+v, want %s", rule, check, status)
			}
			return matching
		}
	}
	t.Fatalf("rule %s is absent from check outcomes", rule)
	return nil
}

func workloadEvidenceValue(finding model.Finding, field string) string {
	for _, evidence := range finding.Evidence {
		if evidence.Field == field {
			return evidence.Value
		}
	}
	return ""
}

func TestNodeReadinessAndPressure(t *testing.T) {
	s := workloadFixture()
	transition := metav1.NewTime(workloadTestTime.Add(-time.Minute))
	s.Nodes = []core.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: "node-1"}, Status: core.NodeStatus{Conditions: []core.NodeCondition{
		{Type: core.NodeReady, Status: core.ConditionFalse, LastTransitionTime: transition},
		{Type: core.NodeMemoryPressure, Status: core.ConditionTrue, LastTransitionTime: transition},
		{Type: core.NodePIDPressure, Status: core.ConditionFalse},
		{Type: core.NodeDiskPressure, Status: core.ConditionFalse},
	}}}}
	readiness := workloadExpect(t, s, nodeReadinessRule, model.Checked, 1)[0]
	pressure := workloadExpect(t, s, nodePressureRule, model.Checked, 1)[0]
	if readiness.Severity != model.Critical || workloadEvidenceValue(readiness, "last_transition") == "" || workloadEvidenceValue(pressure, "MemoryPressure_since") == "" {
		t.Fatalf("missing Node transition evidence: readiness=%+v pressure=%+v", readiness, pressure)
	}
	s.Nodes[0].Status.Conditions[0].Status = core.ConditionUnknown
	unknown := workloadExpect(t, s, nodeReadinessRule, model.Inconclusive, 1)[0]
	if unknown.Severity != model.Warning || workloadEvidenceValue(unknown, "Ready") != "Unknown" {
		t.Fatalf("Ready=Unknown was not distinguished: %+v", unknown)
	}
	s.Nodes[0].Status.Conditions[0].Status = core.ConditionTrue
	s.Nodes[0].Status.Conditions[1].Status = core.ConditionFalse
	workloadExpect(t, s, nodeReadinessRule, model.Checked, 0)
	workloadExpect(t, s, nodePressureRule, model.Checked, 0)
	s.Nodes = nil
	setWorkloadCoverage(&s, model.SourceNodes, model.PermissionDenied)
	workloadExpect(t, s, nodeReadinessRule, model.Inconclusive, 0)
	workloadExpect(t, s, nodePressureRule, model.Inconclusive, 0)
}

func TestContainerObservations(t *testing.T) {
	const sentinel = "do-not-emit-credential-72b746"
	for _, tc := range []struct {
		name, contains, evidence, value string
		phase                           core.PodPhase
		apply                           func(*core.Pod)
		status                          model.CheckStatus
		count                           int
		severity                        model.Severity
	}{
		{"running", "", "", "", core.PodRunning, func(*core.Pod) {}, model.Checked, 0, ""},
		{"image pull", "image acquisition", "waiting_reason", "ImagePullBackOff", core.PodPending, setWaiting("ImagePullBackOff", sentinel), model.Checked, 1, model.Critical},
		{"missing config is startup", "configuration", "waiting_reason", "CreateContainerConfigError", core.PodPending, setWaiting("CreateContainerConfigError", sentinel), model.Checked, 1, model.Critical},
		{"crash loop", "CrashLoopBackOff", "waiting_reason", "CrashLoopBackOff", core.PodRunning, setWaiting("CrashLoopBackOff", sentinel), model.Checked, 1, model.Critical},
		{"init image pull", "image acquisition", "container_role", "init", core.PodPending, func(p *core.Pod) {
			p.Spec.InitContainers = []core.Container{{Name: "setup"}}
			p.Status.InitContainerStatuses = []core.ContainerStatus{{Name: "setup", State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "ErrImagePull", Message: sentinel}}}}
		}, model.Checked, 1, model.Critical},
		{"restart history", "restarted repeatedly", "restart_count", "3", core.PodRunning, func(p *core.Pod) { p.Status.ContainerStatuses[0].RestartCount = 3 }, model.Checked, 1, model.Info},
		{"exit 137", "unsuccessful termination", "exit_code", "137", core.PodFailed, setTerminated("Error", sentinel, 137), model.Checked, 1, model.Warning},
		{"actual OOM termination", "recorded OOM kill", "termination_reason", "OOMKilled", core.PodFailed, setTerminated("OOMKilled", sentinel, 137), model.Checked, 1, model.Warning},
		{"recent recovered OOM", "running after", "last_termination_reason", "OOMKilled", core.PodRunning, setLastTermination("OOMKilled", -time.Minute), model.Checked, 1, model.Warning},
		{"old recovered OOM", "", "", "", core.PodRunning, setLastTermination("OOMKilled", -time.Hour), model.Checked, 0, ""},
		{"completed Job", "", "", "", core.PodSucceeded, setWaiting("CrashLoopBackOff", sentinel), model.Checked, 0, ""},
		{"missing status", "", "", "", core.PodRunning, func(p *core.Pod) { p.Status.ContainerStatuses = nil }, model.Inconclusive, 0, ""},
		{"unrecognized wait", "", "", "", core.PodPending, setWaiting(sentinel, sentinel), model.Inconclusive, 0, ""},
		{"prolonged startup", "still waiting", "waiting_reason", "ContainerCreating", core.PodPending, setWaiting("ContainerCreating", sentinel), model.Inconclusive, 1, model.Info},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := workloadFixture()
			p := workloadPod()
			p.Status.Phase = tc.phase
			tc.apply(&p)
			s.Pods = []core.Pod{p}
			findings := workloadExpect(t, s, podContainersRule, tc.status, tc.count)
			if len(findings) == 0 {
				return
			}
			finding := findings[0]
			if finding.Severity != tc.severity || !strings.Contains(finding.Symptom, tc.contains) || workloadEvidenceValue(finding, tc.evidence) != tc.value {
				t.Fatalf("unexpected container finding: %+v", finding)
			}
			encoded, err := json.Marshal(finding)
			if err != nil || strings.Contains(string(encoded), sentinel) || (tc.name == "exit 137" && strings.Contains(string(encoded), "OOM")) || (tc.name == "missing config is startup" && strings.Contains(string(encoded), "CrashLoopBackOff")) {
				t.Fatalf("unsafe or inaccurate evidence: %s, error=%v", encoded, err)
			}
		})
	}
}

func setWaiting(reason, message string) func(*core.Pod) {
	return func(p *core.Pod) {
		p.Status.ContainerStatuses[0].State = core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: reason, Message: message}}
	}
}

func setTerminated(reason, message string, exitCode int32) func(*core.Pod) {
	return func(p *core.Pod) {
		p.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{
			Reason: reason, Message: message, ExitCode: exitCode, FinishedAt: metav1.NewTime(workloadTestTime.Add(-time.Minute)),
		}}
	}
}

func setLastTermination(reason string, relative time.Duration) func(*core.Pod) {
	return func(p *core.Pod) {
		p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &core.ContainerStateTerminated{Reason: reason, FinishedAt: metav1.NewTime(workloadTestTime.Add(relative))}
	}
}

func TestSchedulingEvidenceAndOwnership(t *testing.T) {
	s := workloadFixture()
	pod := workloadPod()
	controller := true
	pod.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "processor-rs", UID: "rs-1", Controller: &controller}}
	s.ReplicaSets = []apps.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{
		Namespace: pod.Namespace, Name: "processor-rs", UID: "rs-1",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "processor", UID: "deployment-1", Controller: &controller}},
	}}}
	pod.Spec.NodeName = ""
	pod.Status = core.PodStatus{Phase: core.PodPending}
	s.Pods = []core.Pod{pod}
	id := model.Identity("Pod", &pod)
	s.Events = []model.Event{
		{Resource: id, Reason: "FailedScheduling", Time: workloadTestTime.Add(-2 * time.Hour)},
		{Resource: model.Resource{Kind: id.Kind, Namespace: id.Namespace, Name: id.Name, UID: "old-pod"}, Reason: "FailedScheduling", Time: workloadTestTime.Add(-time.Minute)},
	}
	workloadExpect(t, s, podSchedulingRule, model.Inconclusive, 0)
	workloadExpect(t, s, warningEventsRule, model.Checked, 0)
	s.Pods[0].Status.Conditions = []core.PodCondition{{Type: core.PodScheduled, Status: core.ConditionFalse, Reason: core.PodReasonUnschedulable}}
	s.Events = append(s.Events, model.Event{Resource: id, Reason: "FailedScheduling", Time: workloadTestTime.Add(-time.Minute), Count: 4})
	finding := workloadExpect(t, s, podSchedulingRule, model.Checked, 1)[0]
	if workloadEvidenceValue(finding, "condition_reason") != "Unschedulable" || workloadEvidenceValue(finding, "event_count") != "4" || finding.Incident == nil || finding.Incident.UID != "deployment-1" || finding.ProbableCause != "" {
		t.Fatalf("scheduler evidence or UID-linked owner missing: %+v", finding)
	}
	workloadExpect(t, s, warningEventsRule, model.Checked, 0)
	setWorkloadCoverage(&s, model.SourceEvents, model.PermissionDenied)
	s.Events = nil
	workloadExpect(t, s, podSchedulingRule, model.Inconclusive, 1)
	s.Pods[0].Spec.NodeName = "node-1"
	workloadExpect(t, s, podSchedulingRule, model.Checked, 0)
}

func TestRolloutTiming(t *testing.T) {
	s := workloadFixture()
	meta := metav1.ObjectMeta{Name: "processor", Namespace: "payments", Generation: 2, CreationTimestamp: metav1.NewTime(workloadTestTime.Add(-time.Hour))}
	s.Deployments = []apps.Deployment{{ObjectMeta: meta, Status: apps.DeploymentStatus{ObservedGeneration: 2,
		Conditions: []apps.DeploymentCondition{{Type: apps.DeploymentProgressing, Status: core.ConditionFalse, Reason: "ProgressDeadlineExceeded"}},
	}}}
	s.DaemonSets = []apps.DaemonSet{{ObjectMeta: meta, Status: apps.DaemonSetStatus{ObservedGeneration: 2, DesiredNumberScheduled: 2}}}
	failed := workloadExpect(t, s, deploymentRule, model.Checked, 1)[0]
	if failed.Severity != model.Critical || workloadEvidenceValue(failed, "condition_reason") != "ProgressDeadlineExceeded" {
		t.Fatalf("rollout deadline evidence missing: %+v", failed)
	}
	daemon := workloadExpect(t, s, daemonSetRule, model.Inconclusive, 1)[0]
	if daemon.Severity != model.Info || workloadEvidenceValue(daemon, "desired") != "2" {
		t.Fatalf("untimed DaemonSet observation was overstated: %+v", daemon)
	}
	s.DaemonSets[0].Generation, s.DaemonSets[0].Status.ObservedGeneration = 1, 1
	workloadExpect(t, s, daemonSetRule, model.Checked, 1)
	s.DaemonSets[0].CreationTimestamp = metav1.NewTime(workloadTestTime.Add(-time.Minute))
	workloadExpect(t, s, daemonSetRule, model.Checked, 0)
	s.DaemonSets[0].Spec.UpdateStrategy.Type = apps.OnDeleteDaemonSetStrategyType
	s.DaemonSets[0].Status.NumberAvailable = 2
	workloadExpect(t, s, daemonSetRule, model.Checked, 0)
	s.DaemonSets[0].Status.ObservedGeneration = 0
	workloadExpect(t, s, daemonSetRule, model.Inconclusive, 0)
	s.Deployments[0].Status.ObservedGeneration = 1
	workloadExpect(t, s, deploymentRule, model.Inconclusive, 0)
	s.Deployments[0].Spec.Paused = true
	workloadExpect(t, s, deploymentRule, model.Checked, 0)
	s.Deployments[0].Spec.Paused = false
	zero := int32(0)
	s.Deployments[0].Spec.Replicas = &zero
	workloadExpect(t, s, deploymentRule, model.Checked, 0)
	s.Deployments[0].Spec.Replicas = nil
	s.Deployments[0].Status.ObservedGeneration, s.Deployments[0].Status.AvailableReplicas, s.Deployments[0].Status.UpdatedReplicas = 2, 1, 1
	workloadExpect(t, s, deploymentRule, model.Checked, 0)
}

func TestConfigurationReferences(t *testing.T) {
	s := workloadFixture()
	pod := workloadPod()
	optional := true
	pod.Spec.Containers[0].Env = []core.EnvVar{
		{Name: "SECRET_TOKEN", ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: "required-secret"}, Key: "token"}}},
		{Name: "OPTIONAL_TOKEN", ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: "optional-secret"}, Optional: &optional}}},
		{Name: "CONFIG", ValueFrom: &core.EnvVarSource{ConfigMapKeyRef: &core.ConfigMapKeySelector{LocalObjectReference: core.LocalObjectReference{Name: "settings"}, Key: "blob"}}},
		{Name: "LITERAL", Value: "redact-credential-5b2136f3"},
	}
	pod.Spec.Volumes = []core.Volume{{Name: "config", VolumeSource: core.VolumeSource{ConfigMap: &core.ConfigMapVolumeSource{
		LocalObjectReference: core.LocalObjectReference{Name: "settings"}, Items: []core.KeyToPath{{Key: "blob", Path: "blob"}},
	}}}}
	pod.Status.Phase = core.PodPending
	setWaiting("CreateContainerConfigError", "do-not-emit-raw-status")(&pod)
	s.Pods = []core.Pod{pod}
	s.ConfigMaps = []model.ConfigMap{{Namespace: "payments", Name: "settings", BinaryKeys: []string{"blob"}}}
	missing := workloadExpect(t, s, configurationRule, model.Inconclusive, 1)[0]
	if workloadEvidenceValue(missing, "reference_kind") != "ConfigMap" || workloadEvidenceValue(missing, "reference_key") != "blob" {
		t.Fatalf("ConfigMap binary key was accepted in env: %+v", missing)
	}
	id := model.Identity("Pod", &pod)
	s.Events = []model.Event{{Resource: id, Reason: "Failed", Time: workloadTestTime.Add(-time.Minute), Reference: &model.ConfigIssue{Kind: "Secret", Name: "required-secret", Key: "token", Missing: "key"}}}
	findings := workloadExpect(t, s, configurationRule, model.Checked, 2)
	if findings[0].Severity != model.Warning || workloadEvidenceValue(findings[0], "reference_key") != "token" || workloadEvidenceValue(findings[0], "event_reason") != "Failed" {
		t.Fatalf("required Secret evidence missing: %+v", findings)
	}
	workloadExpect(t, s, warningEventsRule, model.Checked, 0)
	all, _ := analyzeWorkloads(s)
	data, err := json.Marshal(all)
	if err != nil || strings.Contains(string(data), "DATABASE_URL") || strings.Contains(string(data), "optional-secret") || strings.Contains(string(data), "redact-credential") || strings.Contains(string(data), "do-not-emit-raw-status") || strings.Contains(string(data), "CrashLoopBackOff") {
		t.Fatalf("configuration diagnostic included unrelated or sensitive evidence: %s, error=%v", data, err)
	}
	s.Pods[0].Spec.Containers[0].Env = s.Pods[0].Spec.Containers[0].Env[1:]
	s.Pods[0].Status = workloadPod().Status
	s.ConfigMaps[0].Keys = []string{"blob"}
	s.Events = nil
	workloadExpect(t, s, configurationRule, model.Checked, 0)
	s.ConfigMaps = nil
	setWorkloadCoverage(&s, model.SourceConfigMaps, model.PermissionDenied)
	workloadExpect(t, s, configurationRule, model.Inconclusive, 0)
	setWorkloadCoverage(&s, model.SourceConfigMaps, model.Complete)
	missing = workloadExpect(t, s, configurationRule, model.Checked, 1)[0]
	if workloadEvidenceValue(missing, "missing") != "object" {
		t.Fatalf("missing ConfigMap was not reported: %+v", missing)
	}
}

func TestClaimBindingAndWaitForFirstConsumer(t *testing.T) {
	s := workloadFixture()
	className := "disks"
	mode := storage.VolumeBindingWaitForFirstConsumer
	s.StorageClasses = []storage.StorageClass{{ObjectMeta: metav1.ObjectMeta{Name: className}, VolumeBindingMode: &mode}}
	s.PVCs = []core.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "payments", UID: "claim-1", CreationTimestamp: metav1.NewTime(workloadTestTime.Add(-time.Hour))},
		Spec:       core.PersistentVolumeClaimSpec{StorageClassName: &className}, Status: core.PersistentVolumeClaimStatus{Phase: core.ClaimPending},
	}}
	workloadExpect(t, s, pvcBindingRule, model.Checked, 0)
	setWorkloadCoverage(&s, model.SourcePods, model.PermissionDenied)
	workloadExpect(t, s, pvcBindingRule, model.Inconclusive, 0)
	setWorkloadCoverage(&s, model.SourcePods, model.Complete)
	s.PVCs[0].Annotations = map[string]string{"volume.kubernetes.io/selected-node": "node-1"}
	binding := workloadExpect(t, s, pvcBindingRule, model.Checked, 1)[0]
	if workloadEvidenceValue(binding, "binding_mode") != "WaitForFirstConsumer" || binding.Severity != model.Warning {
		t.Fatalf("delayed binding evidence missing: %+v", binding)
	}
	s.StorageClasses = nil
	setWorkloadCoverage(&s, model.SourceStorageClasses, model.PermissionDenied)
	workloadExpect(t, s, pvcBindingRule, model.Inconclusive, 0)
	id := model.Identity("PersistentVolumeClaim", &s.PVCs[0])
	s.Events = []model.Event{{Resource: id, Reason: "ProvisioningFailed", Time: workloadTestTime.Add(-time.Minute), Count: 3}}
	binding = workloadExpect(t, s, pvcBindingRule, model.Inconclusive, 1)[0]
	if workloadEvidenceValue(binding, "event_reason") != "ProvisioningFailed" {
		t.Fatalf("pending claim event evidence missing: %+v", binding)
	}
	s.PVCs[0].Status.Phase = core.ClaimLost
	workloadExpect(t, s, pvcBindingRule, model.Checked, 1)
	s.PVCs[0].Status.Phase = core.ClaimBound
	workloadExpect(t, s, pvcBindingRule, model.Checked, 0)
}

func TestResourcesAndCurrentMetrics(t *testing.T) {
	s := workloadFixture()
	s.Pods = []core.Pod{workloadPod()}
	workloadExpect(t, s, resourcesRule, model.Checked, 0)
	s.Pods[0].Spec.Containers[0].Resources = core.ResourceRequirements{}
	advice := workloadExpect(t, s, resourcesRule, model.Checked, 1)[0]
	if advice.Severity != model.Info || workloadEvidenceValue(advice, "memory_limit") == "" {
		t.Fatalf("resource advice missing: %+v", advice)
	}
	podBudget := workloadTestResources()
	s.Pods[0].Spec.Resources = &podBudget
	workloadExpect(t, s, resourcesRule, model.Checked, 0)
	setWorkloadCoverage(&s, model.SourceMetrics, model.Unavailable)
	workloadExpect(t, s, metricsRule, model.Inconclusive, 0)
	setWorkloadCoverage(&s, model.SourceMetrics, model.Complete)
	workloadExpect(t, s, metricsRule, model.Inconclusive, 0)
	s.Metrics = []model.PodUsage{{Namespace: "payments", Name: "processor", Timestamp: workloadTestTime.Add(-time.Minute), Window: 25 * time.Second,
		Containers: []model.ContainerUsage{{Name: "app", CPU: resource.MustParse("6500u"), Memory: resource.MustParse("80Mi")}},
	}}
	usage := workloadExpect(t, s, metricsRule, model.Checked, 1)[0]
	cpu := resource.MustParse(workloadEvidenceValue(usage, "cpu_cores"))
	if cpu.Cmp(resource.MustParse("6500u")) != 0 || workloadEvidenceValue(usage, "memory_bytes") != "83886080" || workloadEvidenceValue(usage, "measurement_window") != "25s" || workloadEvidenceValue(usage, "sample_timestamp") == "" {
		t.Fatalf("metric units or sample window missing: %+v", usage)
	}
	data, err := json.Marshal(usage)
	if err != nil || strings.Contains(string(data), "P95") || strings.Contains(string(data), "recommended_request") {
		t.Fatalf("metric led to unsupported historical sizing: %s, error=%v", data, err)
	}
	s.Pods[0].CreationTimestamp = metav1.NewTime(workloadTestTime.Add(-time.Second))
	workloadExpect(t, s, metricsRule, model.Inconclusive, 0)
	setWorkloadCoverage(&s, model.SourcePods, model.Truncated)
	workloadExpect(t, s, resourcesRule, model.Inconclusive, 0)
}

func TestMetricsQuantityLimits(t *testing.T) {
	for _, tc := range []struct {
		name, cpu, memory string
	}{
		{"large CPU exponent", "1e100000000", "80Mi"},
		{"large memory exponent", "6500u", "1e100000000"},
		{"CPU above bound", "1000000.000000001", "80Mi"},
		{"memory just above int64", "6500u", "9223372036854775807.000000001"},
		{"negative memory", "6500u", "-1"},
		{"negative CPU", "-1n", "80Mi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := workloadFixture()
			s.Pods = []core.Pod{workloadPod()}
			s.Metrics = []model.PodUsage{{Namespace: "payments", Name: "processor", Timestamp: workloadTestTime, Window: time.Minute,
				Containers: []model.ContainerUsage{{Name: "app", CPU: resource.MustParse(tc.cpu), Memory: resource.MustParse(tc.memory)}},
			}}
			workloadExpect(t, s, metricsRule, model.Inconclusive, 0)
		})
	}

	s := workloadFixture()
	s.Pods = []core.Pod{workloadPod()}
	s.Metrics = []model.PodUsage{{Namespace: "payments", Name: "processor", Timestamp: workloadTestTime, Window: time.Minute,
		Containers: []model.ContainerUsage{{Name: "app", CPU: resource.MustParse("1n"), Memory: resource.MustParse("9223372036854775807")}},
	}}
	finding := workloadExpect(t, s, metricsRule, model.Checked, 1)[0]
	if workloadEvidenceValue(finding, "cpu_cores") != "0.000000001" || workloadEvidenceValue(finding, "memory_bytes") != "9223372036854775807" {
		t.Fatalf("valid quantity boundaries were changed: %+v", finding)
	}
}

func TestRecentWarningsBoundedAndCorrelated(t *testing.T) {
	s := workloadFixture()
	p := workloadPod()
	s.Pods = []core.Pod{p}
	id := model.Identity("Pod", &p)
	oldResource := id
	oldResource.UID = "old-uid"
	missingUID := id
	missingUID.UID = ""
	otherNamespace := id
	otherNamespace.Namespace = "outside"
	s.Events = []model.Event{
		{Resource: id, Reason: "BackOff", Time: workloadTestTime.Add(-time.Minute), Count: 2},
		{Resource: id, Reason: "BackOff", Time: workloadTestTime.Add(-time.Minute), Count: 5},
		{Resource: oldResource, Reason: "FailedScheduling", Time: workloadTestTime.Add(-time.Minute)},
		{Resource: id, Reason: "Evicted", Time: workloadTestTime.Add(-2 * time.Hour)},
		{Resource: missingUID, Reason: "Unhealthy", Time: workloadTestTime.Add(-2 * time.Minute)},
		{Resource: missingUID, Reason: "FailedMount", Time: workloadTestTime.Add(-20 * time.Minute)},
		{Resource: otherNamespace, Reason: "FailedMount", Time: workloadTestTime.Add(-time.Minute)},
		{Resource: id, Reason: "redact-hostile-event-7c89e3", Time: workloadTestTime.Add(-time.Minute)},
	}
	findings := workloadExpect(t, s, warningEventsRule, model.Checked, 1)
	data, err := json.Marshal(findings)
	if err != nil || strings.Count(string(data), "BackOff") != 1 || !strings.Contains(string(data), "Unhealthy") || workloadEvidenceValue(findings[0], "event_count") != "5" {
		t.Fatalf("warning deduplication or fallback identity failed: %s, error=%v", data, err)
	}
	for _, unwanted := range []string{"FailedScheduling", "Evicted", "FailedMount", "redact-hostile-event"} {
		if strings.Contains(string(data), unwanted) {
			t.Fatalf("irrelevant warning reached findings: %s", data)
		}
	}
	s.Events = nil
	workloadExpect(t, s, warningEventsRule, model.Checked, 0)
	setWorkloadCoverage(&s, model.SourceEvents, model.PermissionDenied)
	workloadExpect(t, s, warningEventsRule, model.Inconclusive, 0)
}

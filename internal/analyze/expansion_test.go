package analyze

import (
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	storage "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func expansionFixture() model.Snapshot {
	s := workloadFixture()
	for _, source := range []model.Source{
		model.SourceStatefulSets, model.SourceJobs, model.SourceCronJobs, model.SourceHPAs, model.SourceQuotas, model.SourceLimitRanges, model.SourceNodeLocalDNS,
		model.SourceNodeLocalPods, model.SourceReplicaSets, model.SourceNetworkPolicies, model.SourceServices, model.SourceEndpointSlices,
	} {
		setWorkloadCoverage(&s, source, model.Complete)
	}
	return s
}

func expansionID(kind, ns, name string) model.Resource {
	return model.Resource{Kind: kind, Namespace: ns, Name: name, UID: "uid-" + name}
}

func expansionMeta(id model.Resource) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: id.Name, Namespace: id.Namespace, UID: types.UID(id.UID), Generation: 1,
		CreationTimestamp: metav1.NewTime(workloadTestTime.Add(-20 * time.Minute))}
}

func expansionOwner(parent model.Resource) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: parent.Kind, Name: parent.Name, UID: types.UID(parent.UID), Controller: new(true)}}
}

func TestStatefulSetOrdinalStorageAndRollout(t *testing.T) {
	s := expansionFixture()
	id := expansionID("StatefulSet", "payments", "queue")
	set := model.StatefulSetEvidence{Resource: id, Generation: 1, ObservedGeneration: 1, Desired: 2, Ready: 1, Available: 1,
		Management: "OrderedReady", Strategy: "RollingUpdate", StartOrdinal: 3, Created: workloadTestTime.Add(-20 * time.Minute), VolumeClaimTemplates: []string{"state"}}
	s.Expansion.StatefulSets = []model.StatefulSetEvidence{set}
	pod := workloadPod()
	pod.Name, pod.Spec.NodeName = "queue-3", ""
	pod.OwnerReferences = expansionOwner(id)
	pod.Status.Phase = core.PodPending
	pod.Spec.Volumes = []core.Volume{{Name: "state", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "state-queue-3"}}}}
	class, delayed := "local", storage.VolumeBindingWaitForFirstConsumer
	s.StorageClasses = []storage.StorageClass{{ObjectMeta: metav1.ObjectMeta{Name: class}, VolumeBindingMode: &delayed}}
	claim := core.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "state-queue-3", Namespace: "payments"},
		Spec: core.PersistentVolumeClaimSpec{StorageClassName: &class}, Status: core.PersistentVolumeClaimStatus{Phase: core.ClaimPending}}
	s.Pods, s.PVCs = []core.Pod{pod}, []core.PersistentVolumeClaim{claim}
	workloadExpect(t, s, statefulSetRule, model.Checked, 0)
	s.PVCs[0].Status.Phase = core.ClaimLost
	finding := workloadExpect(t, s, statefulSetRule, model.Checked, 1)[0]
	if workloadEvidenceValue(finding, "claim_phase") != "Lost" || workloadEvidenceValue(finding, "ordinal") != "3" {
		t.Fatalf("owned ordinal and PVC were not corroborated: %+v", finding)
	}
	s.PVCs[0].Status.Phase = core.ClaimBound
	setWaiting("ImagePullBackOff", "sensitive-excluded")(&s.Pods[0])
	finding = workloadExpect(t, s, statefulSetRule, model.Checked, 1)[0]
	if workloadEvidenceValue(finding, "pod_state") != "ImagePullBackOff" || workloadEvidenceValue(finding, "pod_management") != "OrderedReady" {
		t.Fatalf("owned Pod failure was not linked: %+v", finding)
	}
	s.Expansion.StatefulSets[0].ObservedGeneration = 0
	workloadExpect(t, s, statefulSetRule, model.Inconclusive, 0)
	s.Expansion.StatefulSets[0].Desired = 0
	workloadExpect(t, s, statefulSetRule, model.Checked, 0)
	s.Expansion.StatefulSets[0] = set
	s.Expansion.StatefulSets[0].Ready, s.Expansion.StatefulSets[0].Available = 2, 2
	s.Expansion.StatefulSets[0].Updated, s.Expansion.StatefulSets[0].Partition = 1, 1
	s.Expansion.StatefulSets[0].RevisionState = "different"
	s.Pods = nil
	workloadExpect(t, s, statefulSetRule, model.Checked, 0)
	setWorkloadCoverage(&s, model.SourceStatefulSets, model.PermissionDenied)
	s.Expansion.StatefulSets = nil
	workloadExpect(t, s, statefulSetRule, model.Inconclusive, 0)
}

func TestBatchTerminalRetryAndScheduleEvidence(t *testing.T) {
	s := expansionFixture()
	cron := model.CronJobEvidence{Resource: expansionID("CronJob", "payments", "nightly"), Concurrency: "Forbid",
		Created: workloadTestTime.Add(-time.Hour), LastScheduled: workloadTestTime.Add(-10 * time.Minute)}
	job := model.JobEvidence{Resource: expansionID("Job", "payments", "nightly-run"), Owner: &cron.Resource, Created: workloadTestTime.Add(-10 * time.Minute),
		Started: workloadTestTime.Add(-9 * time.Minute), Failed: 2, BackoffLimit: new(int32(2))}
	s.Expansion.Jobs, s.Expansion.CronJobs = []model.JobEvidence{job}, []model.CronJobEvidence{cron}
	failure := model.BriefCondition{Type: "Failed", Status: "True", Reason: "BackoffLimitExceeded", Transition: workloadTestTime.Add(-time.Minute)}
	s.Expansion.Jobs[0].Conditions = []model.BriefCondition{failure}
	finding := workloadExpect(t, s, jobRule, model.Checked, 1)[0]
	if finding.Severity != model.Warning || workloadEvidenceValue(finding, "condition_reason") != "BackoffLimitExceeded" || finding.Incident == nil || finding.Incident.UID != cron.Resource.UID {
		t.Fatalf("failed Job evidence or owner missing: %+v", finding)
	}
	if finding := workloadExpect(t, s, cronJobRule, model.Checked, 1)[0]; workloadEvidenceValue(finding, "failed_owned_job") != job.Resource.Name {
		t.Fatalf("failed CronJob execution missing: %+v", finding)
	}
	s.Expansion.CronJobs[0].LastSuccessful = workloadTestTime.Add(-30 * time.Second)
	workloadExpect(t, s, cronJobRule, model.Checked, 0)
	s.Expansion.Jobs[0].Conditions = []model.BriefCondition{{Type: "Complete", Status: "True"}}
	workloadExpect(t, s, jobRule, model.Checked, 0)
	s.Expansion.Jobs[0].Conditions, s.Expansion.Jobs[0].Active = nil, 1
	s.Expansion.Jobs[0].DeadlineSeconds = new(int64(60))
	if finding := workloadExpect(t, s, jobRule, model.Checked, 1)[0]; finding.Severity != model.Warning || workloadEvidenceValue(finding, "active_deadline_seconds") != "60" {
		t.Fatalf("deadline evidence missing: %+v", finding)
	}
	s.Expansion.Jobs[0].DeadlineSeconds = nil
	if finding := workloadExpect(t, s, jobRule, model.Checked, 1)[0]; finding.Severity != model.Info {
		t.Fatalf("retry count was overstated: %+v", finding)
	}
	s.Expansion.Jobs[0].Suspended = true
	workloadExpect(t, s, jobRule, model.Checked, 0)
	s.Events = []model.Event{{Resource: cron.Resource, Reason: "JobAlreadyActive", Time: workloadTestTime.Add(-time.Minute), Count: 2}}
	if finding := workloadExpect(t, s, cronJobRule, model.Checked, 1)[0]; finding.Severity != model.Info || workloadEvidenceValue(finding, "event_reason") != "JobAlreadyActive" {
		t.Fatalf("expected concurrency was overstated: %+v", finding)
	}
	s.Expansion.CronJobs[0].Suspended = true
	workloadExpect(t, s, cronJobRule, model.Checked, 1)
	setWorkloadCoverage(&s, model.SourceJobs, model.Unavailable)
	s.Expansion.Jobs = nil
	workloadExpect(t, s, jobRule, model.Inconclusive, 0)
	workloadExpect(t, s, cronJobRule, model.Inconclusive, 1)
}

func TestHPAConditionsTargetAndUtilization(t *testing.T) {
	s := expansionFixture()
	set := model.StatefulSetEvidence{Resource: expansionID("StatefulSet", "payments", "queue"), Generation: 1, ObservedGeneration: 1, Ready: 1, Available: 1, Desired: 1}
	s.Expansion.StatefulSets = []model.StatefulSetEvidence{set}
	hpa := model.HPAEvidence{Resource: expansionID("HorizontalPodAutoscaler", "payments", "worker-scaler"), Target: set.Resource, TargetAPIVersion: "apps/v1",
		Generation: 1, ObservedGeneration: 1, CurrentReplicas: 1, DesiredReplicas: 1, MinReplicas: 1, MaxReplicas: 2,
		UtilizationTargets: []model.HPAUtilizationTarget{{Resource: "cpu", Percent: 70}},
		Conditions:         []model.BriefCondition{{Type: "AbleToScale", Status: "True"}, {Type: "ScalingActive", Status: "False", Reason: "FailedGetResourceMetric"}}}
	hpa.Target.UID = ""
	pod := workloadPod()
	pod.OwnerReferences = expansionOwner(set.Resource)
	pod.Status.Conditions = []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}
	pod.Spec.Containers[0].Resources.Requests = nil
	s.Pods, s.Expansion.HPAs = []core.Pod{pod}, []model.HPAEvidence{hpa}
	finding := workloadExpect(t, s, hpaRule, model.Checked, 1)[0]
	if finding.Severity != model.Warning || workloadEvidenceValue(finding, "condition_reason") != "FailedGetResourceMetric" || workloadEvidenceValue(finding, "pods_with_missing_request") != "1" || finding.Incident == nil || finding.Incident.UID != set.Resource.UID {
		t.Fatalf("HPA metric and target evidence missing: %+v", finding)
	}
	s.Expansion.HPAs[0].Conditions[1].Status = "True"
	advice := workloadExpect(t, s, hpaRule, model.Checked, 1)[0]
	if advice.Severity != model.Info {
		t.Fatalf("static request gap overstated: %+v", advice)
	}
	s.Pods[0].Spec.Containers[0].Resources.Requests = workloadTestResources().Requests
	workloadExpect(t, s, hpaRule, model.Checked, 0)
	s.Expansion.HPAs[0].Conditions = append(s.Expansion.HPAs[0].Conditions, model.BriefCondition{Type: "ScalingLimited", Status: "True", Reason: "TooManyReplicas"})
	if bound := workloadExpect(t, s, hpaRule, model.Checked, 1)[0]; bound.Severity != model.Info || workloadEvidenceValue(bound, "max_replicas") != "2" {
		t.Fatalf("scale limit evidence missing: %+v", bound)
	}
	s.Expansion.HPAs[0].Generation = 2
	workloadExpect(t, s, hpaRule, model.Inconclusive, 0)
	s.Expansion.StatefulSets = nil
	setWorkloadCoverage(&s, model.SourceStatefulSets, model.PermissionDenied)
	workloadExpect(t, s, hpaRule, model.Inconclusive, 0)
}

func TestUpstreamHPAWithUnversionedStatus(t *testing.T) {
	s := expansionFixture()
	set := model.StatefulSetEvidence{Resource: expansionID("StatefulSet", "payments", "queue"), Desired: 1}
	s.Expansion.StatefulSets = []model.StatefulSetEvidence{set}
	id := expansionID("HorizontalPodAutoscaler", "payments", "queue-scaler")
	transition := workloadTestTime.Add(-time.Minute)
	s.Expansion.HPAs = []model.HPAEvidence{{Resource: id, Target: set.Resource, TargetAPIVersion: "apps/v1",
		Created: workloadTestTime.Add(-2 * time.Hour), CurrentReplicas: 1, MinReplicas: 1, MaxReplicas: 2,
		Conditions: []model.BriefCondition{{Type: "AbleToScale", Status: "True", Transition: transition},
			{Type: "ScalingActive", Status: "False", Reason: "FailedGetResourceMetric", Transition: transition}},
	}}
	initial := workloadExpect(t, s, hpaRule, model.Inconclusive, 1)[0]
	if initial.Severity != model.Info || workloadEvidenceValue(initial, "status_generation") != "unreported" {
		t.Fatalf("unversioned condition was overstated or discarded: %+v", initial)
	}
	s.Events = []model.Event{{Resource: id, Reason: "FailedGetResourceMetric", Time: transition.Add(-time.Second), Count: 2}}
	corroborated := workloadExpect(t, s, hpaRule, model.Inconclusive, 1)[0]
	if corroborated.Severity != model.Warning || workloadEvidenceValue(corroborated, "event_reason") != "FailedGetResourceMetric" ||
		workloadEvidenceValue(corroborated, "status_generation") != "unreported" {
		t.Fatalf("fresh same-UID metric error was not corroborated: %+v", corroborated)
	}
	s.Events[0].Resource.UID = "different-hpa-uid"
	if other := workloadExpect(t, s, hpaRule, model.Inconclusive, 1)[0]; other.Severity != model.Info || workloadEvidenceValue(other, "event_reason") != "" {
		t.Fatalf("old UID corroborated a replacement HPA: %+v", other)
	}
	s.Events[0].Resource.UID = id.UID
	s.Expansion.HPAs[0].Conditions[1] = model.BriefCondition{Type: "ScalingActive", Status: "True", Transition: workloadTestTime.Add(-30 * time.Second)}
	workloadExpect(t, s, hpaRule, model.Inconclusive, 0)
	s.Expansion.HPAs[0].Conditions[1] = model.BriefCondition{Type: "ScalingActive", Status: "False", Reason: "FailedGetResourceMetric", Transition: transition}
	s.Expansion.HPAs[0].Generation, s.Expansion.HPAs[0].ObservedGeneration = 2, 1
	workloadExpect(t, s, hpaRule, model.Inconclusive, 0)
}

func TestQuotaAndLimitAdmissionCorroboration(t *testing.T) {
	s := expansionFixture()
	id := expansionID("ReplicaSet", "payments", "api-rs")
	quota := model.QuotaEvidence{Resource: expansionID("ResourceQuota", "payments", "pod-cap"), Created: workloadTestTime.Add(-20 * time.Minute),
		Capacities: []model.QuotaCapacity{{Resource: "pods", Hard: "2", Used: "2", Current: true}}}
	s.Expansion.Quotas = []model.QuotaEvidence{quota}
	s.ReplicaSets = []apps.ReplicaSet{{ObjectMeta: expansionMeta(id)}}
	if current := workloadExpect(t, s, admissionRule, model.Checked, 1)[0]; current.Severity != model.Info {
		t.Fatalf("quota capacity was treated as proven failure: %+v", current)
	}
	s.Expansion.LimitRanges = []model.LimitRangeEvidence{{Resource: expansionID("LimitRange", "payments", "sizes"), Created: quota.Created,
		Constraints: []model.LimitConstraint{{Scope: "Container", Resource: "cpu", Type: "minimum"}}}}
	when := workloadTestTime.Add(-time.Minute)
	s.Expansion.AdmissionEvents = []model.AdmissionEventEvidence{
		{Event: model.TimedReference{Resource: id, Time: when}, Constraint: "quota_exceeded", Quota: quota.Resource.Name},
		{Event: model.TimedReference{Resource: id, Time: when}, Constraint: "minimum", ResourceName: "cpu", LimitScope: "Container"},
	}
	findings := workloadExpect(t, s, admissionRule, model.Checked, 3)
	warn := 0
	for _, finding := range findings {
		if finding.Severity == model.Warning && workloadEvidenceValue(finding, "event_reason") == "FailedCreate" && finding.Resource == id {
			warn++
		}
	}
	if warn != 2 {
		t.Fatalf("two admission constraints were not corroborated: %+v", findings)
	}
	s.Expansion.AdmissionEvents[0].Event.Resource.UID = "old-replicaset"
	s.Expansion.Quotas[0].Capacities[0].Used = "1"
	workloadExpect(t, s, admissionRule, model.Checked, 1)
	s.Expansion.LimitRanges = nil
	setWorkloadCoverage(&s, model.SourceLimitRanges, model.PermissionDenied)
	if partial := workloadExpect(t, s, admissionRule, model.Inconclusive, 1)[0]; partial.Severity != model.Info {
		t.Fatalf("unconfirmed LimitRange rejection was overstated: %+v", partial)
	}
}

func TestProbeDuringRolloutAndRecovery(t *testing.T) {
	s := expansionFixture()
	d := expansionID("Deployment", "payments", "processor")
	rs := expansionID("ReplicaSet", "payments", "processor-rs")
	s.Deployments = []apps.Deployment{{ObjectMeta: expansionMeta(d), Status: apps.DeploymentStatus{ObservedGeneration: 1}}}
	s.ReplicaSets = []apps.ReplicaSet{{ObjectMeta: expansionMeta(rs)}}
	s.ReplicaSets[0].OwnerReferences = expansionOwner(d)
	pod := workloadPod()
	pod.OwnerReferences = expansionOwner(rs)
	pod.Status.Conditions = []core.PodCondition{{Type: core.PodReady, Status: core.ConditionFalse}}
	pod.Status.ContainerStatuses[0].Ready = false
	s.Pods = []core.Pod{pod}
	id := model.Identity("Pod", &pod)
	s.Expansion.Probes = []model.PodProbeEvidence{{Pod: id, Containers: []model.ContainerProbe{{Name: "app", Role: "application", Kinds: []string{"readiness"}}}}}
	s.Expansion.ProbeEvents = []model.ProbeEventEvidence{{Event: model.TimedReference{Resource: id, Time: workloadTestTime.Add(-time.Minute), Count: 2}, Kind: "readiness", Container: "app", Role: "application"}}
	finding := workloadExpect(t, s, probesRule, model.Checked, 1)[0]
	if finding.Severity != model.Warning || workloadEvidenceValue(finding, "probe") != "readiness" || finding.Incident == nil || finding.Incident.UID != d.UID {
		t.Fatalf("probe episode not linked to rollout: %+v", finding)
	}
	s.Pods[0].Status.Conditions[0].Status = core.ConditionTrue
	workloadExpect(t, s, probesRule, model.Checked, 0)
	s.Pods[0].Status.Conditions = nil
	workloadExpect(t, s, probesRule, model.Inconclusive, 0)
	s.Expansion.ProbeEvents[0].Event.Resource.UID = "old-pod"
	workloadExpect(t, s, probesRule, model.Checked, 0)
	setWorkloadCoverage(&s, model.SourceEvents, model.PermissionDenied)
	workloadExpect(t, s, probesRule, model.Inconclusive, 0)
}

func TestEndpointReplacementAndNamedIngressPort(t *testing.T) {
	s := expansionFixture()
	p := workloadPod()
	p.Labels = map[string]string{"app": "api"}
	s.Pods = []core.Pod{p}
	svc := core.Service{ObjectMeta: expansionMeta(expansionID("Service", "payments", "api")), Spec: core.ServiceSpec{Selector: p.Labels}}
	s.Services = []core.Service{svc}
	slice := discovery.EndpointSlice{ObjectMeta: expansionMeta(expansionID("EndpointSlice", "payments", "api-one")), AddressType: discovery.AddressTypeIPv4,
		Endpoints: []discovery.Endpoint{{Addresses: []string{""}, TargetRef: &core.ObjectReference{Kind: "Pod", Name: p.Name, Namespace: p.Namespace, UID: "previous-pod"}}}}
	slice.Labels = map[string]string{discovery.LabelServiceName: svc.Name, discovery.LabelManagedBy: endpointController}
	slice.OwnerReferences = expansionOwner(model.Identity("Service", &svc))
	s.EndpointSlices = []discovery.EndpointSlice{slice}
	workloadExpect(t, s, serviceUIDRule, model.Checked, 1)
	s.EndpointSlices[0].Endpoints[0].Conditions.Terminating = new(true)
	workloadExpect(t, s, serviceUIDRule, model.Checked, 0)
	s.EndpointSlices[0].Endpoints[0].Conditions.Terminating = nil
	s.EndpointSlices[0].Endpoints[0].TargetRef.UID = p.UID
	workloadExpect(t, s, serviceUIDRule, model.Checked, 0)
	setWorkloadCoverage(&s, model.SourceEndpointSlices, model.Unavailable)
	s.EndpointSlices = nil
	workloadExpect(t, s, serviceUIDRule, model.Inconclusive, 0)
	policy := networking.NetworkPolicy{ObjectMeta: expansionMeta(expansionID("NetworkPolicy", "payments", "allow-api")), Spec: networking.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: p.Labels}, Ingress: []networking.NetworkPolicyIngressRule{{Ports: []networking.NetworkPolicyPort{{Port: new(intstr.FromString("http"))}}}},
	}}
	s.NetworkPolicies = []networking.NetworkPolicy{policy}
	finding := workloadExpect(t, s, policyPortRule, model.Checked, 1)[0]
	if finding.Severity != model.Info || workloadEvidenceValue(finding, "protocol") != "TCP" {
		t.Fatalf("static named-port evidence mislabeled: %+v", finding)
	}
	s.Pods[0].Spec.Containers[0].Ports = []core.ContainerPort{{Name: "http", ContainerPort: 8080}}
	workloadExpect(t, s, policyPortRule, model.Checked, 0)
	setWorkloadCoverage(&s, model.SourcePods, model.Truncated)
	workloadExpect(t, s, policyPortRule, model.Inconclusive, 0)
}

func TestNodeLocalProviderVisibilityAndReadiness(t *testing.T) {
	s := expansionFixture()
	s.Pods = []core.Pod{workloadPod()}
	nodeLocal := model.NodeLocalDNSEvidence{Resource: expansionID("DaemonSet", "kube-system", "node-local-dns"), Created: workloadTestTime.Add(-time.Hour),
		Generation: 1, ObservedGeneration: 1, Desired: 1}
	s.Expansion.NodeLocalDNS = &nodeLocal
	s.Expansion.NodeLocalPods = []model.NodeLocalPodEvidence{{Pod: expansionID("Pod", "kube-system", "node-local-one"), Owner: &nodeLocal.Resource, Node: "node-1", Ready: "False"}}
	finding := workloadExpect(t, s, nodeLocalRule, model.Checked, 1)[0]
	if finding.Severity != model.Warning || workloadEvidenceValue(finding, "hosting_nodes_with_unready_cache") != "1" {
		t.Fatalf("NodeLocal failed readiness missing: %+v", finding)
	}
	s.Expansion.NodeLocalPods[0].Ready = "True"
	workloadExpect(t, s, nodeLocalRule, model.Checked, 0)
	s.Expansion.NodeLocalPods[0].Node = "another-node"
	workloadExpect(t, s, nodeLocalRule, model.Inconclusive, 0)
	s.Expansion.NodeLocalDNS, s.Expansion.NodeLocalPods = nil, nil
	setWorkloadCoverage(&s, model.SourceNodeLocalDNS, model.NotApplicable)
	workloadExpect(t, s, nodeLocalRule, model.Skipped, 0)
	setWorkloadCoverage(&s, model.SourceNodeLocalDNS, model.PermissionDenied)
	workloadExpect(t, s, nodeLocalRule, model.Inconclusive, 0)
}

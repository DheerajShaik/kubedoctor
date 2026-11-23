package collect

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	autoscaling "k8s.io/api/autoscaling/v2"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const maxExpansionMembers = 64

func projectStatefulSet(value apps.StatefulSet) model.StatefulSetEvidence {
	out := model.StatefulSetEvidence{
		Resource: projectedIdentity("StatefulSet", &value), Created: value.CreationTimestamp.Time,
		Deleting: value.DeletionTimestamp != nil, Generation: value.Generation,
		ObservedGeneration: value.Status.ObservedGeneration, Desired: 1, Ready: value.Status.ReadyReplicas,
		Available: value.Status.AvailableReplicas, Updated: value.Status.UpdatedReplicas,
		MinReadySeconds: value.Spec.MinReadySeconds,
		Management:      allowed(string(value.Spec.PodManagementPolicy), "OrderedReady", "Parallel"),
		Strategy:        allowed(string(value.Spec.UpdateStrategy.Type), "RollingUpdate", "OnDelete"),
	}
	if value.Spec.Replicas != nil {
		out.Desired = *value.Spec.Replicas
	}
	if value.Spec.Ordinals != nil {
		out.StartOrdinal = value.Spec.Ordinals.Start
	}
	if rolling := value.Spec.UpdateStrategy.RollingUpdate; rolling != nil && rolling.Partition != nil {
		out.Partition = *rolling.Partition
	}
	if value.Status.CurrentRevision != "" && value.Status.UpdateRevision != "" {
		out.RevisionState = "same"
		if value.Status.CurrentRevision != value.Status.UpdateRevision {
			out.RevisionState = "different"
		}
	}
	for _, claim := range value.Spec.VolumeClaimTemplates {
		if len(out.VolumeClaimTemplates) == maxExpansionMembers {
			out.TemplatesTruncated = true
			break
		}
		if claimName := name(claim.Name); claimName != "" {
			out.VolumeClaimTemplates = append(out.VolumeClaimTemplates, claimName)
		}
	}
	slices.Sort(out.VolumeClaimTemplates)
	return out
}

func knownController(kind string, object metav1.Object) *model.Resource {
	owner := metav1.GetControllerOfNoCopy(object)
	if owner == nil || owner.Kind != kind || name(owner.Name) == "" || token(string(owner.UID)) == "" {
		return nil
	}
	return &model.Resource{Kind: kind, Namespace: name(object.GetNamespace()), Name: owner.Name, UID: string(owner.UID)}
}

func projectJob(value batch.Job) model.JobEvidence {
	out := model.JobEvidence{
		Resource: projectedIdentity("Job", &value), Owner: knownController("CronJob", &value), Created: value.CreationTimestamp.Time,
		Deleting: value.DeletionTimestamp != nil, Suspended: value.Spec.Suspend != nil && *value.Spec.Suspend,
		DeadlineSeconds: value.Spec.ActiveDeadlineSeconds, BackoffLimit: value.Spec.BackoffLimit, Parallelism: value.Spec.Parallelism,
		Started: optionalTime(value.Status.StartTime), Completed: optionalTime(value.Status.CompletionTime),
		Active: value.Status.Active, Failed: value.Status.Failed, Succeeded: value.Status.Succeeded,
	}
	for _, c := range value.Status.Conditions {
		typ := allowed(string(c.Type), "Complete", "Failed", "FailureTarget", "SuccessCriteriaMet", "Suspended")
		if typ != "" && len(out.Conditions) < maxExpansionMembers {
			out.Conditions = append(out.Conditions, model.BriefCondition{
				Type: typ, Status: string(projectStatus(c.Status)), Transition: c.LastTransitionTime.Time,
				Reason: allowed(c.Reason, "BackoffLimitExceeded", "DeadlineExceeded", "PodFailurePolicy", "FailedIndexes", "MaxFailedIndexesExceeded", "SuccessPolicy", "CompletionsReached", "JobSuspended", "JobResumed"),
			})
		}
	}
	return out
}

func projectCronJob(value batch.CronJob) model.CronJobEvidence {
	out := model.CronJobEvidence{
		Resource: projectedIdentity("CronJob", &value), Created: value.CreationTimestamp.Time,
		Deleting: value.DeletionTimestamp != nil, Suspended: value.Spec.Suspend != nil && *value.Spec.Suspend,
		Concurrency: allowed(string(value.Spec.ConcurrencyPolicy), "Allow", "Forbid", "Replace"), DeadlineSeconds: value.Spec.StartingDeadlineSeconds,
		LastScheduled: optionalTime(value.Status.LastScheduleTime), LastSuccessful: optionalTime(value.Status.LastSuccessfulTime),
	}
	for _, active := range value.Status.Active {
		if len(out.ActiveJobs) == maxExpansionMembers {
			out.ActiveTruncated = true
			break
		}
		if active.Kind != "Job" || active.Namespace != value.Namespace || name(active.Name) == "" {
			continue
		}
		out.ActiveJobs = append(out.ActiveJobs, model.Resource{Kind: "Job", Namespace: value.Namespace, Name: active.Name, UID: token(string(active.UID))})
	}
	return out
}

func projectHPA(value autoscaling.HorizontalPodAutoscaler) model.HPAEvidence {
	out := model.HPAEvidence{
		Resource: projectedIdentity("HorizontalPodAutoscaler", &value), Created: value.CreationTimestamp.Time,
		Deleting: value.DeletionTimestamp != nil, Generation: value.Generation,
		CurrentReplicas: value.Status.CurrentReplicas, DesiredReplicas: value.Status.DesiredReplicas,
		MinReplicas: 1, MaxReplicas: value.Spec.MaxReplicas,
		Target: model.Resource{
			Kind:      allowed(value.Spec.ScaleTargetRef.Kind, "Deployment", "StatefulSet", "ReplicaSet"),
			Namespace: value.Namespace, Name: name(value.Spec.ScaleTargetRef.Name),
		},
		TargetAPIVersion: allowed(value.Spec.ScaleTargetRef.APIVersion, "apps/v1"),
	}
	if observed := value.Status.ObservedGeneration; observed != nil {
		out.ObservedGeneration = *observed
	}
	if minReplicas := value.Spec.MinReplicas; minReplicas != nil {
		out.MinReplicas = *minReplicas
	}
	for _, c := range value.Status.Conditions {
		typ := allowed(string(c.Type), "AbleToScale", "ScalingActive", "ScalingLimited")
		if typ == "" || len(out.Conditions) == maxExpansionMembers {
			continue
		}
		out.Conditions = append(out.Conditions, model.BriefCondition{
			Type: typ, Status: string(projectStatus(c.Status)), Transition: c.LastTransitionTime.Time,
			Reason: allowed(c.Reason, "FailedGetScale", "FailedUpdateScale", "FailedGetResourceMetric", "FailedGetContainerResourceMetric",
				"FailedGetPodsMetric", "FailedGetObjectMetric", "FailedGetExternalMetric", "FailedComputeMetricsReplicas", "InvalidMetricSourceType",
				"ScalingDisabled", "ReadyForNewScale", "SucceededGetScale", "ValidMetricFound", "DesiredWithinRange", "TooManyReplicas",
				"TooFewReplicas", "ScaleUpLimit", "ScaleDownLimit", "ScaleDownStabilized", "ScaleUpStabilized"),
		})
	}
	for _, metric := range value.Spec.Metrics {
		if len(out.UtilizationTargets) == maxExpansionMembers {
			out.TargetsTruncated = true
			break
		}
		var target autoscaling.MetricTarget
		var resourceName, container string
		switch metric.Type {
		case autoscaling.ResourceMetricSourceType:
			if metric.Resource != nil {
				resourceName, target = string(metric.Resource.Name), metric.Resource.Target
			}
		case autoscaling.ContainerResourceMetricSourceType:
			if metric.ContainerResource != nil && name(metric.ContainerResource.Container) != "" {
				resourceName, target = string(metric.ContainerResource.Name), metric.ContainerResource.Target
				container = metric.ContainerResource.Container
			}
		}
		if target.Type == autoscaling.UtilizationMetricType && target.AverageUtilization != nil &&
			(resourceName == "cpu" || resourceName == "memory") {
			out.UtilizationTargets = append(out.UtilizationTargets, model.HPAUtilizationTarget{
				Resource: resourceName, Container: container, Percent: *target.AverageUtilization,
			})
		}
	}
	return out
}

var quotaResourceNames = []core.ResourceName{
	"pods", "cpu", "memory", "requests.cpu", "requests.memory", "limits.cpu", "limits.memory", "requests.storage", "persistentvolumeclaims",
	"requests.ephemeral-storage", "limits.ephemeral-storage", "count/jobs.batch", "count/cronjobs.batch",
}

func projectQuota(value core.ResourceQuota) model.QuotaEvidence {
	out := model.QuotaEvidence{Resource: projectedIdentity("ResourceQuota", &value), Created: value.CreationTimestamp.Time}
	for _, key := range quotaResourceNames {
		specified, present := value.Spec.Hard[key]
		if !present {
			continue
		}
		status, hasStatus := value.Status.Hard[key]
		used, hasUse := value.Status.Used[key]
		capacity := model.QuotaCapacity{Resource: string(key), Hard: quotaDecimal(specified)}
		normalizedStatus := quotaDecimal(status)
		if hasStatus && hasUse && capacity.Hard != "" && normalizedStatus != "" {
			capacity.Used = quotaDecimal(used)
			capacity.Current = capacity.Used != "" && specified.Cmp(status) == 0
		}
		out.Capacities = append(out.Capacities, capacity)
	}
	return out
}

func quotaDecimal(quantity resource.Quantity) string {
	decimal := quantity.AsDec()
	if decimal.Sign() < 0 || decimal.Scale() < -18 || decimal.Scale() > 9 || decimal.UnscaledBig().BitLen() > 93 {
		return ""
	}
	return decimal.String()
}

func projectLimitRange(value core.LimitRange) model.LimitRangeEvidence {
	out := model.LimitRangeEvidence{Resource: projectedIdentity("LimitRange", &value), Created: value.CreationTimestamp.Time}
	for _, item := range value.Spec.Limits {
		kind := allowed(string(item.Type), "Container", "Pod", "PersistentVolumeClaim")
		if kind == "" {
			continue
		}
		for _, constraint := range []struct {
			typ    string
			values core.ResourceList
		}{
			{"maximum", item.Max}, {"minimum", item.Min}, {"default_limit", item.Default},
			{"default_request", item.DefaultRequest}, {"maximum_ratio", item.MaxLimitRequestRatio},
		} {
			for _, key := range []core.ResourceName{"cpu", "memory", "storage", "ephemeral-storage"} {
				if _, exists := constraint.values[key]; exists && len(out.Constraints) < maxExpansionMembers {
					out.Constraints = append(out.Constraints, model.LimitConstraint{Scope: kind, Resource: string(key), Type: constraint.typ})
				}
			}
		}
	}
	return out
}

func optionalTime(value *metav1.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.Time
}

func projectPodProbes(pod core.Pod) model.PodProbeEvidence {
	out := model.PodProbeEvidence{Pod: projectedIdentity("Pod", &pod)}
	for _, group := range []struct {
		role       string
		containers []core.Container
	}{{"application", pod.Spec.Containers}, {"init", pod.Spec.InitContainers}} {
		for _, container := range group.containers {
			entry := model.ContainerProbe{Name: name(container.Name), Role: group.role}
			if container.StartupProbe != nil {
				entry.Kinds = append(entry.Kinds, "startup")
			}
			if container.ReadinessProbe != nil {
				entry.Kinds = append(entry.Kinds, "readiness")
			}
			if container.LivenessProbe != nil {
				entry.Kinds = append(entry.Kinds, "liveness")
			}
			if len(entry.Kinds) == 0 || entry.Name == "" {
				continue
			}
			if len(out.Containers) == maxExpansionMembers {
				out.Truncated = true
				return out
			}
			out.Containers = append(out.Containers, entry)
		}
	}
	return out
}

var containerIDPattern = regexp.MustCompile(`^[a-z0-9_-]+://[a-f0-9]{64}$`)

func projectContainerID(input string) string {
	if len(input) <= 96 && containerIDPattern.MatchString(input) {
		return input
	}
	return ""
}

func projectNodeLocalPod(pod core.Pod) model.NodeLocalPodEvidence {
	out := model.NodeLocalPodEvidence{Pod: projectedIdentity("Pod", &pod), Owner: knownController("DaemonSet", &pod), Node: name(pod.Spec.NodeName), Deleting: pod.DeletionTimestamp != nil}
	for _, c := range pod.Status.Conditions {
		if c.Type == core.PodReady {
			out.Ready = string(projectStatus(c.Status))
		}
	}
	return out
}

// An admission failure is kept only as a structured, recognized Kubernetes
// rejection. The remainder of the message can contain arbitrary API text.
var admissionPrefix = regexp.MustCompile(`^(?:Error creating: )?(?:pods|persistentvolumeclaims) "[a-z0-9.-]{1,253}" is forbidden: (.+)$`)
var rangeRejection = regexp.MustCompile(`^(maximum|minimum|max limit to request ratio) (cpu|memory|ephemeral-storage|storage) usage per (Container|Pod|PersistentVolumeClaim) is `)
var ratioRejection = regexp.MustCompile(`^(cpu|memory|ephemeral-storage|storage) max limit to request ratio per (Container|Pod|PersistentVolumeClaim) is `)

func projectAdmissionEvent(observed model.Event, raw core.Event) *model.AdmissionEventEvidence {
	if observed.Reason != "FailedCreate" || raw.Type != core.EventTypeWarning || len(raw.Message) > 4096 ||
		allowed(observed.Resource.Kind, "ReplicaSet", "Deployment", "StatefulSet", "DaemonSet", "Job") == "" {
		return nil
	}
	rejection := admissionPrefix.FindStringSubmatch(raw.Message)
	if len(rejection) == 0 {
		return nil
	}
	out := &model.AdmissionEventEvidence{Event: model.TimedReference{Resource: observed.Resource, Time: observed.Time, Count: observed.Count}}
	for _, prefix := range []struct {
		message, constraint string
	}{{"exceeded quota: ", "quota_exceeded"}, {"failed quota: ", "quota_required_resources"}} {
		if rest, ok := strings.CutPrefix(rejection[1], prefix.message); ok {
			quota, _, hasSuffix := strings.Cut(rest, ",")
			if !hasSuffix {
				quota, _, hasSuffix = strings.Cut(rest, ":")
			}
			if !hasSuffix || !validResourceName(quota) {
				return nil
			}
			out.Constraint, out.Quota = prefix.constraint, quota
			return out
		}
	}
	if match := rangeRejection.FindStringSubmatch(rejection[1]); len(match) != 0 {
		out.Constraint, out.ResourceName, out.LimitScope = allowed(match[1], "minimum", "maximum"), match[2], match[3]
		if out.Constraint != "" {
			return out
		}
	}
	if match := ratioRejection.FindStringSubmatch(rejection[1]); len(match) != 0 {
		out.Constraint, out.ResourceName, out.LimitScope = "maximum_ratio", match[1], match[2]
		return out
	}
	return nil
}

func projectProbeEvent(observed model.Event, raw core.Event) *model.ProbeEventEvidence {
	if observed.Reason != "Unhealthy" || observed.Resource.Kind != "Pod" || raw.Type != core.EventTypeWarning || len(raw.Message) > 4096 ||
		(raw.Source.Component != "kubelet" && raw.ReportingController != "kubelet" && raw.ReportingController != "kubernetes.io/kubelet") {
		return nil
	}
	var contained, role string
	for _, candidate := range []struct{ prefix, role string }{{"spec.containers{", "application"}, {"spec.initContainers{", "init"}} {
		if value, ok := strings.CutPrefix(raw.InvolvedObject.FieldPath, candidate.prefix); ok {
			if value, ok = strings.CutSuffix(value, "}"); ok && name(value) != "" {
				contained, role = value, candidate.role
				break
			}
		}
	}
	if contained == "" {
		return nil
	}
	for _, category := range []struct{ prefix, kind string }{
		{"Startup probe failed: ", "startup"}, {"Readiness probe failed: ", "readiness"}, {"Liveness probe failed: ", "liveness"},
	} {
		if strings.HasPrefix(raw.Message, category.prefix) {
			return &model.ProbeEventEvidence{
				Event:     model.TimedReference{Resource: observed.Resource, Time: observed.Time, Count: observed.Count},
				Container: contained, Role: role, Kind: category.kind,
			}
		}
	}
	return nil
}

func projectedIdentity(kind string, object metav1.Object) model.Resource {
	return model.Resource{Kind: kind, Namespace: name(object.GetNamespace()), Name: name(object.GetName()), UID: token(string(object.GetUID()))}
}

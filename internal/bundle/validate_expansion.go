package bundle

import (
	"strconv"
	"strings"

	"github.com/DheerajShaik/kubedoctor/internal/history"
	"github.com/DheerajShaik/kubedoctor/internal/model"
)

func validExpansion(scope model.Scope, e model.ExpansionEvidence, hosts map[string]bool) bool {
	resource := func(r model.Resource, kind string) bool {
		return r.Kind == kind && validResource(r) && namespaceInScope(scope, r.Namespace)
	}
	related := func(r *model.Resource, namespace, kind string) bool {
		return r == nil || (r.Kind == kind && validResource(*r) && r.Namespace == namespace)
	}
	event := func(r model.TimedReference) bool {
		return validResource(r.Resource) && namespaceInScope(scope, r.Resource.Namespace) && r.Count >= 0
	}
	if !all(e.StatefulSets, func(s model.StatefulSetEvidence) bool {
		return resource(s.Resource, "StatefulSet") && oneOf(s.Management, "", "OrderedReady", "Parallel") &&
			oneOf(s.Strategy, "", "RollingUpdate", "OnDelete") && oneOf(s.RevisionState, "", "same", "different") &&
			len(s.VolumeClaimTemplates) <= 64 && all(s.VolumeClaimTemplates, validName)
	}) || !all(e.Jobs, func(s model.JobEvidence) bool {
		return resource(s.Resource, "Job") && related(s.Owner, s.Resource.Namespace, "CronJob") &&
			validBriefConditions(s.Conditions) && boundedSeconds(s.DeadlineSeconds)
	}) || !all(e.CronJobs, func(s model.CronJobEvidence) bool {
		return resource(s.Resource, "CronJob") && oneOf(s.Concurrency, "", "Allow", "Forbid", "Replace") &&
			boundedSeconds(s.DeadlineSeconds) && len(s.ActiveJobs) <= 64 &&
			all(s.ActiveJobs, func(j model.Resource) bool { return related(&j, s.Resource.Namespace, "Job") })
	}) || !all(e.HPAs, func(s model.HPAEvidence) bool {
		target := s.Target
		return resource(s.Resource, "HorizontalPodAutoscaler") && target.Namespace == s.Resource.Namespace &&
			(target.Name == "" || validName(target.Name)) && simpleToken(target.UID, 253) &&
			oneOf(target.Kind, "", "Deployment", "StatefulSet", "ReplicaSet") && oneOf(s.TargetAPIVersion, "", "apps/v1") &&
			validBriefConditions(s.Conditions) && len(s.UtilizationTargets) <= 64 &&
			all(s.UtilizationTargets, func(t model.HPAUtilizationTarget) bool {
				return oneOf(t.Resource, "cpu", "memory") && (t.Container == "" || validName(t.Container)) &&
					t.Percent >= 0
			})
	}) || !all(e.Quotas, func(s model.QuotaEvidence) bool {
		return resource(s.Resource, "ResourceQuota") && len(s.Capacities) <= 64 &&
			all(s.Capacities, func(c model.QuotaCapacity) bool {
				return oneOf(c.Resource, "pods", "cpu", "memory", "requests.cpu", "requests.memory", "limits.cpu",
					"limits.memory", "requests.storage", "persistentvolumeclaims", "requests.ephemeral-storage",
					"limits.ephemeral-storage", "count/jobs.batch", "count/cronjobs.batch") &&
					quotaQuantity(c.Hard) && quotaQuantity(c.Used) && (!c.Current || c.Hard != "" && c.Used != "")
			})
	}) || !all(e.LimitRanges, func(s model.LimitRangeEvidence) bool {
		return resource(s.Resource, "LimitRange") && len(s.Constraints) <= 64 &&
			all(s.Constraints, func(c model.LimitConstraint) bool {
				return oneOf(c.Scope, "Container", "Pod", "PersistentVolumeClaim") &&
					oneOf(c.Resource, "cpu", "memory", "storage", "ephemeral-storage") &&
					oneOf(c.Type, "maximum", "minimum", "default_limit", "default_request", "maximum_ratio")
			})
	}) || !all(e.Probes, func(s model.PodProbeEvidence) bool {
		return resource(s.Pod, "Pod") && len(s.Containers) <= 64 &&
			all(s.Containers, func(c model.ContainerProbe) bool {
				return validName(c.Name) && oneOf(c.Role, "application", "init") && len(c.Kinds) > 0 && len(c.Kinds) <= 3 &&
					all(c.Kinds, probeKind)
			})
	}) || !all(e.AdmissionEvents, func(s model.AdmissionEventEvidence) bool {
		if !event(s.Event) || !oneOf(s.Event.Resource.Kind, "ReplicaSet", "Deployment", "StatefulSet", "DaemonSet", "Job") {
			return false
		}
		switch s.Constraint {
		case "quota_exceeded", "quota_required_resources":
			return validName(s.Quota) && s.ResourceName == "" && s.LimitScope == ""
		case "minimum", "maximum", "maximum_ratio":
			return s.Quota == "" && oneOf(s.ResourceName, "cpu", "memory", "ephemeral-storage", "storage") &&
				oneOf(s.LimitScope, "Container", "Pod", "PersistentVolumeClaim")
		}
		return false
	}) || !all(e.ProbeEvents, func(s model.ProbeEventEvidence) bool {
		return event(s.Event) && s.Event.Resource.Kind == "Pod" &&
			validName(s.Container) && oneOf(s.Role, "application", "init") && probeKind(s.Kind)
	}) {
		return false
	}

	dns := e.NodeLocalDNS
	if dns == nil {
		return len(e.NodeLocalPods) == 0
	}
	if !validResource(dns.Resource) || dns.Resource.Kind != "DaemonSet" ||
		dns.Resource.Namespace != "kube-system" || dns.Resource.Name != "node-local-dns" {
		return false
	}
	return all(e.NodeLocalPods, func(p model.NodeLocalPodEvidence) bool {
		return validResource(p.Pod) && p.Pod.Kind == "Pod" && p.Pod.Namespace == "kube-system" &&
			related(p.Owner, "kube-system", "DaemonSet") &&
			(p.Owner == nil || p.Owner.Name == "node-local-dns") &&
			(p.Node == "" || validName(p.Node)) && (!isNamespaceScope(scope) || hosts[p.Node]) &&
			oneOf(p.Ready, "", "True", "False", "Unknown")
	})
}

func boundedSeconds(seconds *int64) bool {
	// Duration arithmetic must stay within Go's underlying int64 nanoseconds.
	return seconds == nil || (*seconds >= 0 && *seconds <= 9_223_372_036)
}

func validBriefConditions(conditions []model.BriefCondition) bool {
	return len(conditions) <= 64 && all(conditions, func(c model.BriefCondition) bool {
		return c.Type != "" && simpleToken(c.Type, 64) && simpleToken(c.Reason, 128) &&
			oneOf(c.Status, "", "True", "False", "Unknown")
	})
}

func quotaQuantity(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > 49 || strings.Count(raw, ".") > 1 || strings.Trim(raw, "0123456789.") != "" {
		return false
	}
	_, err := strconv.ParseFloat(raw, 64)
	return err == nil
}

func probeKind(kind string) bool { return oneOf(kind, "startup", "readiness", "liveness") }

func validHistory(p *payload) bool {
	h := p.Evidence.History
	if h == nil {
		return true
	}
	if history.ValidateCapture(h, p.Scope) != nil || h.StartedAt.Before(p.StartedAt) || h.FinishedAt.After(p.FinishedAt) ||
		h.WindowEnd.After(p.EvaluatedAt) {
		return false
	}
	pods := make(map[model.Resource]pod, len(p.Evidence.Pods))
	for _, pod := range p.Evidence.Pods {
		id := pod.Identity
		pods[model.Resource{Kind: "Pod", Namespace: id.Namespace, Name: id.Name, UID: id.UID}] = pod
	}
	return all(h.Targets, func(target model.HistoryTargetCapture) bool {
		pod, found := pods[target.Target.Pod]
		return found && anyContainerNamed(pod.Containers, target.Target.Container)
	})
}

func anyContainerNamed(containers []container, name string) bool {
	for _, container := range containers {
		if container.Name == name {
			return true
		}
	}
	return false
}

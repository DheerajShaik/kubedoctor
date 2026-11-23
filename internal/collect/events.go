package collect

import (
	"fmt"
	"strings"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
)

type resourceKey struct {
	namespace string
	name      string
}

func eventProjector(pods []core.Pod) func(core.Event) (model.Event, bool) {
	byName := make(map[resourceKey]core.Pod, len(pods))
	for _, pod := range pods {
		byName[resourceKey{pod.Namespace, pod.Name}] = pod
	}
	return func(event core.Event) (model.Event, bool) {
		reason := eventReason(event)
		if reason == "" {
			return model.Event{}, false
		}
		ref := event.InvolvedObject
		kind := allowed(ref.Kind, "Pod", "Node", "Deployment", "ReplicaSet", "DaemonSet",
			"PersistentVolumeClaim", "Service", "Ingress", "PodDisruptionBudget", "NetworkPolicy", "StatefulSet", "Job", "CronJob", "HorizontalPodAutoscaler")
		object := name(ref.Name)
		if kind == "" || object == "" || (ref.UID != "" && token(string(ref.UID)) == "") {
			return model.Event{}, false
		}
		namespace := name(ref.Namespace)
		if namespace == "" && kind != "Node" {
			namespace = name(event.Namespace)
		}
		id := model.Resource{Kind: kind, Name: object, Namespace: namespace, UID: token(string(ref.UID))}
		when, count, timeBasis := eventTimestamp(event)
		out := model.Event{Resource: id, Reason: reason, Time: when, Count: count, TimeBasis: timeBasis}
		if kind != "Pod" || (reason != "Failed" && reason != "FailedMount") {
			return out, true
		}
		pod, found := byName[resourceKey{id.Namespace, id.Name}]
		if found && matchesEventPod(id, when, pod) && len(event.Message) <= 4096 {
			out.Reference = classifyConfigFailure(event.Message, pod)
		}
		return out, true
	}
}

func eventReason(event core.Event) string {
	switch event.Type {
	case core.EventTypeWarning:
		if event.InvolvedObject.Kind == "HorizontalPodAutoscaler" &&
			(event.Source.Component == "horizontal-pod-autoscaler" || event.ReportingController == "horizontal-pod-autoscaler") && model.HPAWarningReason(event.Reason) {
			return event.Reason
		}
		return allowed(event.Reason, "FailedScheduling", "FailedMount", "FailedAttachVolume", "FailedBinding", "ProvisioningFailed",
			"FailedCreate", "FailedDelete", "Failed", "BackOff", "Unhealthy", "Evicted", "NodeNotReady",
			"FailedCreatePodSandBox", "FailedToRetrieveImagePullSecret", "FailedMapVolume", "MissSchedule", "TooManyMissedTimes", "UnknownTimeZone")
	case core.EventTypeNormal:
		return allowed(event.Reason, "WaitForFirstConsumer", "WaitForPodScheduled", "JobAlreadyActive")
	default:
		return ""
	}
}

func eventTimestamp(event core.Event) (time.Time, int32, string) {
	if event.Series != nil && !event.Series.LastObservedTime.IsZero() {
		return event.Series.LastObservedTime.Time, event.Series.Count, "series_last_observed"
	}
	for _, timestamp := range []struct {
		time  time.Time
		basis string
	}{
		{event.LastTimestamp.Time, "last_reported"}, {event.EventTime.Time, "event_time"},
		{event.FirstTimestamp.Time, "first_reported"}, {event.CreationTimestamp.Time, "event_object_created"},
	} {
		if !timestamp.time.IsZero() {
			return timestamp.time, event.Count, timestamp.basis
		}
	}
	return time.Time{}, event.Count, ""
}

func matchesEventPod(id model.Resource, when time.Time, pod core.Pod) bool {
	if id.UID != "" && pod.UID != "" {
		return id.UID == string(pod.UID)
	}
	return !when.IsZero() && !pod.CreationTimestamp.IsZero() &&
		!when.Before(pod.CreationTimestamp.Time)
}

type reference struct {
	kind   string
	name   string
	key    string
	volume string
}

func requiredReferences(pod core.Pod) []reference {
	var refs []reference
	add := func(kind, object, key, volume string, optional *bool) {
		if object != "" && (optional == nil || !*optional) {
			refs = append(refs, reference{kind: kind, name: object, key: key, volume: volume})
		}
	}
	for _, containers := range [][]core.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, container := range containers {
			for _, variable := range container.Env {
				source := variable.ValueFrom
				if source == nil {
					continue
				}
				if ref := source.ConfigMapKeyRef; ref != nil {
					add("ConfigMap", ref.Name, ref.Key, "", ref.Optional)
				}
				if ref := source.SecretKeyRef; ref != nil {
					add("Secret", ref.Name, ref.Key, "", ref.Optional)
				}
			}
			for _, source := range container.EnvFrom {
				if ref := source.ConfigMapRef; ref != nil {
					add("ConfigMap", ref.Name, "", "", ref.Optional)
				}
				if ref := source.SecretRef; ref != nil {
					add("Secret", ref.Name, "", "", ref.Optional)
				}
			}
		}
	}
	addVolume := func(kind, object, volume string, keys []core.KeyToPath, optional *bool) {
		add(kind, object, "", volume, optional)
		for _, key := range keys {
			add(kind, object, key.Key, volume, optional)
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if ref := volume.ConfigMap; ref != nil {
			addVolume("ConfigMap", ref.Name, volume.Name, ref.Items, ref.Optional)
		}
		if ref := volume.Secret; ref != nil {
			addVolume("Secret", ref.SecretName, volume.Name, ref.Items, ref.Optional)
		}
		if volume.Projected == nil {
			continue
		}
		for _, source := range volume.Projected.Sources {
			if ref := source.ConfigMap; ref != nil {
				addVolume("ConfigMap", ref.Name, volume.Name, ref.Items, ref.Optional)
			}
			if ref := source.Secret; ref != nil {
				addVolume("Secret", ref.Name, volume.Name, ref.Items, ref.Optional)
			}
		}
	}
	return refs
}

func classifyConfigFailure(raw string, pod core.Pod) *model.ConfigIssue {
	message := strings.TrimPrefix(raw, "Error: ")
	var volume string
	if tail, mounted := strings.CutPrefix(message, "MountVolume.SetUp failed for volume \""); mounted {
		volumeName, failure, ok := strings.Cut(tail, "\" : ")
		if !ok || !validResourceName(volumeName) {
			return nil
		}
		volume, message = volumeName, failure
	}

	var match *model.ConfigIssue
	for _, ref := range requiredReferences(pod) {
		if ref.volume != volume {
			continue
		}
		kind := strings.ToLower(ref.kind)
		candidate := model.ConfigIssue{Kind: ref.kind, Name: ref.name}
		switch {
		case message == fmt.Sprintf("%s %q not found", kind, ref.name):
			candidate.Missing = "object"
		case ref.key != "" && volume == "" &&
			message == fmt.Sprintf("couldn't find key %s in %s %s/%s", ref.key, ref.kind, pod.Namespace, ref.name):
			candidate.Key, candidate.Missing = ref.key, "key"
		case ref.key != "" && volume != "" && kind == "secret" &&
			message == "references non-existent secret key: "+ref.key:
			candidate.Key, candidate.Missing = ref.key, "key"
		case ref.key != "" && volume != "" && kind == "configmap" &&
			message == "configmap references non-existent config key: "+ref.key:
			candidate.Key, candidate.Missing = ref.key, "key"
		default:
			continue
		}
		if match != nil && *match != candidate {
			return nil
		}
		match = &candidate
	}
	return match
}

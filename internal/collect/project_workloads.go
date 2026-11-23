package collect

import (
	"slices"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

func asIdentity(meta metav1.ObjectMeta) metav1.ObjectMeta {
	out := metav1.ObjectMeta{
		Name: name(meta.Name), Namespace: name(meta.Namespace), UID: types.UID(token(string(meta.UID))),
		Generation: meta.Generation, CreationTimestamp: meta.CreationTimestamp,
		DeletionTimestamp: meta.DeletionTimestamp,
	}
	for _, owner := range meta.OwnerReferences {
		out.OwnerReferences = append(out.OwnerReferences, metav1.OwnerReference{
			Kind: token(owner.Kind), Name: name(owner.Name), UID: types.UID(token(string(owner.UID))),
			APIVersion: allowed(owner.APIVersion, "v1", "apps/v1", "batch/v1", "autoscaling/v2"),
			Controller: owner.Controller,
		})
	}
	return out
}

func name(value string) string {
	if !validResourceName(value) {
		return ""
	}
	return value
}

// Resource API identifiers occasionally use case or punctuation outside DNS
// names. Retain a bounded token; free-form text and control bytes are dropped.
func token(value string) string {
	if len(value) > 253 {
		return ""
	}
	for _, c := range value {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return ""
		}
	}
	return value
}

func allowed(value string, options ...string) string {
	if slices.Contains(options, value) {
		return value
	}
	return ""
}

func labels(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for key, value := range input {
		if len(validation.IsQualifiedName(key)) == 0 && len(validation.IsValidLabelValue(value)) == 0 {
			out[key] = value
		}
	}
	return out
}

func projectSelector(input *metav1.LabelSelector) *metav1.LabelSelector {
	if input == nil {
		return nil
	}
	// Preserve invalid selectors as unknown without retaining the original text.
	if _, err := metav1.LabelSelectorAsSelector(input); err != nil {
		return &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "invalid", Operator: "Invalid"},
		}}
	}
	return input.DeepCopy()
}

func projectNode(node core.Node) core.Node {
	out := core.Node{ObjectMeta: asIdentity(node.ObjectMeta)}
	for _, condition := range node.Status.Conditions {
		switch condition.Type {
		case core.NodeReady, core.NodeMemoryPressure, core.NodeDiskPressure, core.NodePIDPressure, core.NodeNetworkUnavailable:
			out.Status.Conditions = append(out.Status.Conditions, core.NodeCondition{
				Type: condition.Type, Status: projectStatus(condition.Status), LastHeartbeatTime: condition.LastHeartbeatTime,
				LastTransitionTime: condition.LastTransitionTime, Reason: allowed(condition.Reason,
					"KubeletReady", "KubeletNotReady", "NodeStatusUnknown", "NodeStatusNeverUpdated",
					"KubeletHasSufficientMemory", "KubeletHasInsufficientMemory",
					"KubeletHasNoDiskPressure", "KubeletHasDiskPressure",
					"KubeletHasSufficientPID", "KubeletHasInsufficientPID", "RouteCreated", "NoRouteCreated"),
			})
		}
	}
	return out
}

func projectPod(pod core.Pod) core.Pod {
	out := core.Pod{ObjectMeta: asIdentity(pod.ObjectMeta)}
	out.Labels = labels(pod.Labels)
	out.Spec.NodeName = name(pod.Spec.NodeName)
	out.Spec.HostNetwork = pod.Spec.HostNetwork
	if len(pod.Spec.SchedulingGates) > 0 {
		out.Spec.SchedulingGates = []core.PodSchedulingGate{{}}
	}
	out.Spec.Containers = projectContainers(pod.Spec.Containers)
	out.Spec.InitContainers = projectContainers(pod.Spec.InitContainers)
	if pod.Spec.Resources != nil {
		out.Spec.Resources = new(projectResources(*pod.Spec.Resources))
	}
	for _, volume := range pod.Spec.Volumes {
		if projected, ok := projectVolume(volume); ok {
			out.Spec.Volumes = append(out.Spec.Volumes, projected)
		}
	}
	switch pod.Status.Phase {
	case core.PodPending, core.PodRunning, core.PodSucceeded, core.PodFailed, core.PodUnknown:
		out.Status.Phase = pod.Status.Phase
	}
	out.Status.Reason = allowed(pod.Status.Reason, "Evicted", "NodeLost", "DeadlineExceeded",
		"Shutdown", "ContainerStatusUnknown", "UnexpectedAdmissionError")
	out.Status.StartTime = pod.Status.StartTime
	for _, condition := range pod.Status.Conditions {
		switch condition.Type {
		case core.PodScheduled, core.PodReady, core.ContainersReady, core.PodInitialized:
			out.Status.Conditions = append(out.Status.Conditions, core.PodCondition{
				Type: condition.Type, Status: projectStatus(condition.Status),
				Reason:             allowed(condition.Reason, core.PodReasonUnschedulable, "SchedulingGated", "ContainersNotReady", "PodCompleted"),
				LastTransitionTime: condition.LastTransitionTime,
			})
		}
	}
	out.Status.ContainerStatuses = projectContainerStatuses(pod.Status.ContainerStatuses)
	out.Status.InitContainerStatuses = projectContainerStatuses(pod.Status.InitContainerStatuses)
	return out
}

func projectStatus(status core.ConditionStatus) core.ConditionStatus {
	switch status {
	case core.ConditionTrue, core.ConditionFalse, core.ConditionUnknown:
		return status
	default:
		return core.ConditionUnknown
	}
}

func projectContainers(containers []core.Container) []core.Container {
	out := make([]core.Container, 0, len(containers))
	for _, container := range containers {
		result := core.Container{
			Name: name(container.Name), Resources: projectResources(container.Resources),
		}
		if container.RestartPolicy != nil && *container.RestartPolicy == core.ContainerRestartPolicyAlways {
			result.RestartPolicy = new(core.ContainerRestartPolicyAlways)
		}
		for _, port := range container.Ports {
			result.Ports = append(result.Ports, core.ContainerPort{
				Name: token(port.Name), ContainerPort: port.ContainerPort, Protocol: projectProtocol(port.Protocol),
			})
		}
		for _, variable := range container.Env {
			if variable.ValueFrom == nil {
				continue
			}
			source := &core.EnvVarSource{}
			if ref := variable.ValueFrom.ConfigMapKeyRef; ref != nil {
				source.ConfigMapKeyRef = &core.ConfigMapKeySelector{
					LocalObjectReference: core.LocalObjectReference{Name: name(ref.Name)},
					Key:                  token(ref.Key), Optional: ref.Optional,
				}
			}
			if ref := variable.ValueFrom.SecretKeyRef; ref != nil {
				source.SecretKeyRef = &core.SecretKeySelector{
					LocalObjectReference: core.LocalObjectReference{Name: name(ref.Name)},
					Key:                  token(ref.Key), Optional: ref.Optional,
				}
			}
			if source.ConfigMapKeyRef != nil || source.SecretKeyRef != nil {
				result.Env = append(result.Env, core.EnvVar{Name: token(variable.Name), ValueFrom: source})
			}
		}
		for _, entry := range container.EnvFrom {
			source := core.EnvFromSource{}
			if ref := entry.ConfigMapRef; ref != nil {
				source.ConfigMapRef = &core.ConfigMapEnvSource{
					LocalObjectReference: core.LocalObjectReference{Name: name(ref.Name)}, Optional: ref.Optional,
				}
			}
			if ref := entry.SecretRef; ref != nil {
				source.SecretRef = &core.SecretEnvSource{
					LocalObjectReference: core.LocalObjectReference{Name: name(ref.Name)}, Optional: ref.Optional,
				}
			}
			if source.ConfigMapRef != nil || source.SecretRef != nil {
				result.EnvFrom = append(result.EnvFrom, source)
			}
		}
		out = append(out, result)
	}
	return out
}

func projectResources(resources core.ResourceRequirements) core.ResourceRequirements {
	return core.ResourceRequirements{
		Requests: computeResources(resources.Requests),
		Limits:   computeResources(resources.Limits),
	}
}

func computeResources(resources core.ResourceList) core.ResourceList {
	out := core.ResourceList{}
	for _, key := range []core.ResourceName{core.ResourceCPU, core.ResourceMemory} {
		if value, ok := resources[key]; ok {
			out[key] = value.DeepCopy()
		}
	}
	return out
}

func projectVolume(volume core.Volume) (core.Volume, bool) {
	if volume.ConfigMap == nil && volume.Secret == nil && volume.Projected == nil && volume.PersistentVolumeClaim == nil {
		return core.Volume{}, false
	}
	out := core.Volume{Name: name(volume.Name)}
	if ref := volume.ConfigMap; ref != nil {
		out.ConfigMap = &core.ConfigMapVolumeSource{
			LocalObjectReference: core.LocalObjectReference{Name: name(ref.Name)},
			Items:                keyItems(ref.Items), Optional: ref.Optional,
		}
	}
	if ref := volume.Secret; ref != nil {
		out.Secret = &core.SecretVolumeSource{SecretName: name(ref.SecretName), Items: keyItems(ref.Items), Optional: ref.Optional}
	}
	if volume.Projected != nil {
		projected := &core.ProjectedVolumeSource{}
		for _, entry := range volume.Projected.Sources {
			source := core.VolumeProjection{}
			if ref := entry.ConfigMap; ref != nil {
				source.ConfigMap = &core.ConfigMapProjection{
					LocalObjectReference: core.LocalObjectReference{Name: name(ref.Name)},
					Items:                keyItems(ref.Items), Optional: ref.Optional,
				}
			}
			if ref := entry.Secret; ref != nil {
				source.Secret = &core.SecretProjection{
					LocalObjectReference: core.LocalObjectReference{Name: name(ref.Name)},
					Items:                keyItems(ref.Items), Optional: ref.Optional,
				}
			}
			if source.ConfigMap != nil || source.Secret != nil {
				projected.Sources = append(projected.Sources, source)
			}
		}
		if len(projected.Sources) > 0 {
			out.Projected = projected
		}
	}
	if volume.PersistentVolumeClaim != nil {
		out.PersistentVolumeClaim = &core.PersistentVolumeClaimVolumeSource{ClaimName: name(volume.PersistentVolumeClaim.ClaimName)}
	}
	return out, true
}

func keyItems(items []core.KeyToPath) []core.KeyToPath {
	out := make([]core.KeyToPath, 0, len(items))
	for _, item := range items {
		out = append(out, core.KeyToPath{Key: token(item.Key)})
	}
	return out
}

func projectContainerStatuses(statuses []core.ContainerStatus) []core.ContainerStatus {
	out := make([]core.ContainerStatus, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, core.ContainerStatus{
			Name: name(status.Name), Ready: status.Ready, RestartCount: status.RestartCount, Started: status.Started,
			State: projectContainerState(status.State), LastTerminationState: projectContainerState(status.LastTerminationState),
			ContainerID: projectContainerID(status.ContainerID),
		})
	}
	return out
}

func projectContainerState(state core.ContainerState) core.ContainerState {
	out := core.ContainerState{}
	if waiting := state.Waiting; waiting != nil {
		out.Waiting = &core.ContainerStateWaiting{Reason: allowed(waiting.Reason,
			"CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "ErrImageNeverPull", "InvalidImageName",
			"RegistryUnavailable", "ImageInspectError", "CreateContainerConfigError",
			"CreateContainerError", "RunContainerError", "StartError", "ContainerCreating", "PodInitializing")}
	}
	if terminated := state.Terminated; terminated != nil {
		out.Terminated = &core.ContainerStateTerminated{
			Reason: allowed(terminated.Reason, "OOMKilled", "Error", "Completed", "ContainerCannotRun",
				"DeadlineExceeded", "ContainerStatusUnknown", "StartError"),
			ExitCode: terminated.ExitCode, Signal: terminated.Signal,
			StartedAt: terminated.StartedAt, FinishedAt: terminated.FinishedAt,
		}
	}
	if state.Running != nil {
		out.Running = &core.ContainerStateRunning{StartedAt: state.Running.StartedAt}
	}
	return out
}

func projectReplicaSet(value apps.ReplicaSet) apps.ReplicaSet {
	out := apps.ReplicaSet{ObjectMeta: asIdentity(value.ObjectMeta)}
	out.Spec.Replicas = value.Spec.Replicas
	out.Spec.Template.Labels = labels(value.Spec.Template.Labels)
	out.Spec.Selector = projectSelector(value.Spec.Selector)
	out.Status.ObservedGeneration = value.Status.ObservedGeneration
	out.Status.ReadyReplicas = value.Status.ReadyReplicas
	return out
}

func projectDeployment(value apps.Deployment) apps.Deployment {
	out := apps.Deployment{ObjectMeta: asIdentity(value.ObjectMeta)}
	out.Spec.Replicas = value.Spec.Replicas
	out.Spec.Selector = projectSelector(value.Spec.Selector)
	out.Spec.Template.Labels = labels(value.Spec.Template.Labels)
	out.Spec.Paused = value.Spec.Paused
	out.Spec.MinReadySeconds = value.Spec.MinReadySeconds
	out.Spec.ProgressDeadlineSeconds = value.Spec.ProgressDeadlineSeconds
	out.Status.ObservedGeneration = value.Status.ObservedGeneration
	out.Status.Replicas = value.Status.Replicas
	out.Status.UpdatedReplicas = value.Status.UpdatedReplicas
	out.Status.ReadyReplicas = value.Status.ReadyReplicas
	out.Status.AvailableReplicas = value.Status.AvailableReplicas
	out.Status.UnavailableReplicas = value.Status.UnavailableReplicas
	for _, condition := range value.Status.Conditions {
		switch condition.Type {
		case apps.DeploymentAvailable, apps.DeploymentProgressing, apps.DeploymentReplicaFailure:
			out.Status.Conditions = append(out.Status.Conditions, apps.DeploymentCondition{
				Type: condition.Type, Status: projectStatus(condition.Status),
				Reason: allowed(condition.Reason, "ProgressDeadlineExceeded", "MinimumReplicasAvailable",
					"MinimumReplicasUnavailable", "NewReplicaSetAvailable", "FoundNewReplicaSet", "NewReplicaSetCreated",
					"ReplicaSetUpdated", "DeploymentPaused", "DeploymentResumed", "FailedCreate", "FailedDelete"),
				LastUpdateTime: condition.LastUpdateTime, LastTransitionTime: condition.LastTransitionTime,
			})
		}
	}
	return out
}

func projectDaemonSet(value apps.DaemonSet) apps.DaemonSet {
	out := apps.DaemonSet{ObjectMeta: asIdentity(value.ObjectMeta)}
	out.Spec.Selector = projectSelector(value.Spec.Selector)
	out.Spec.Template.Labels = labels(value.Spec.Template.Labels)
	out.Spec.MinReadySeconds = value.Spec.MinReadySeconds
	out.Spec.UpdateStrategy.Type = apps.DaemonSetUpdateStrategyType(allowed(string(value.Spec.UpdateStrategy.Type),
		string(apps.RollingUpdateDaemonSetStrategyType), string(apps.OnDeleteDaemonSetStrategyType)))
	out.Status.ObservedGeneration = value.Status.ObservedGeneration
	out.Status.DesiredNumberScheduled = value.Status.DesiredNumberScheduled
	out.Status.CurrentNumberScheduled = value.Status.CurrentNumberScheduled
	out.Status.NumberMisscheduled = value.Status.NumberMisscheduled
	out.Status.UpdatedNumberScheduled = value.Status.UpdatedNumberScheduled
	out.Status.NumberReady = value.Status.NumberReady
	out.Status.NumberAvailable = value.Status.NumberAvailable
	out.Status.NumberUnavailable = value.Status.NumberUnavailable
	return out
}

func projectPVC(value core.PersistentVolumeClaim) core.PersistentVolumeClaim {
	out := core.PersistentVolumeClaim{ObjectMeta: asIdentity(value.ObjectMeta)}
	if selected := name(value.Annotations["volume.kubernetes.io/selected-node"]); selected != "" {
		out.Annotations = map[string]string{"volume.kubernetes.io/selected-node": selected}
	}
	if value.Spec.StorageClassName != nil {
		out.Spec.StorageClassName = new(name(*value.Spec.StorageClassName))
	}
	out.Spec.VolumeName = name(value.Spec.VolumeName)
	switch value.Status.Phase {
	case core.ClaimPending, core.ClaimBound, core.ClaimLost:
		out.Status.Phase = value.Status.Phase
	}
	return out
}

func projectStorageClass(value storage.StorageClass) storage.StorageClass {
	out := storage.StorageClass{ObjectMeta: asIdentity(value.ObjectMeta)}
	for _, key := range []string{"storageclass.kubernetes.io/is-default-class", "storageclass.beta.kubernetes.io/is-default-class"} {
		if value.Annotations[key] == "true" {
			if out.Annotations == nil {
				out.Annotations = make(map[string]string)
			}
			out.Annotations[key] = "true"
		}
	}
	if value.VolumeBindingMode != nil {
		switch *value.VolumeBindingMode {
		case storage.VolumeBindingImmediate, storage.VolumeBindingWaitForFirstConsumer:
			out.VolumeBindingMode = value.VolumeBindingMode
		}
	}
	return out
}

func projectConfigMap(value core.ConfigMap) model.ConfigMap {
	out := model.ConfigMap{UID: token(string(value.UID)), Namespace: name(value.Namespace), Name: name(value.Name)}
	for key := range value.Data {
		if safe := token(key); safe != "" {
			out.Keys = append(out.Keys, safe)
		}
	}
	for key := range value.BinaryData {
		if safe := token(key); safe != "" {
			out.BinaryKeys = append(out.BinaryKeys, safe)
		}
	}
	slices.Sort(out.Keys)
	slices.Sort(out.BinaryKeys)
	return out
}

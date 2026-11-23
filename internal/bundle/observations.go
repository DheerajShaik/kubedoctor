package bundle

import (
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type owner struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid,omitempty"`
	Controller *bool  `json:"controller,omitempty"`
}

type identity struct {
	Name       string     `json:"name"`
	Namespace  string     `json:"namespace,omitempty"`
	UID        string     `json:"uid,omitempty"`
	Generation int64      `json:"generation,omitempty"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	Owners     []owner    `json:"owners,omitempty"`
	Labels     labelSet   `json:"labels,omitempty"`
}

func describe(meta metav1.ObjectMeta, retainLabels bool) identity {
	i := identity{
		Name: meta.Name, Namespace: meta.Namespace, UID: string(meta.UID), Generation: meta.Generation,
		CreatedAt: timestamp(meta.CreationTimestamp.Time),
		Owners: mapItems(meta.OwnerReferences, func(ref metav1.OwnerReference) owner {
			return owner{Kind: ref.Kind, Name: ref.Name, UID: string(ref.UID), Controller: ref.Controller}
		}),
	}
	if meta.DeletionTimestamp != nil {
		i.DeletedAt = timestamp(meta.DeletionTimestamp.Time)
	}
	if retainLabels {
		i.Labels = meta.Labels
	}
	return i
}

func (i identity) metadata() metav1.ObjectMeta {
	m := metav1.ObjectMeta{
		Name: i.Name, Namespace: i.Namespace, UID: types.UID(i.UID), Generation: i.Generation,
		CreationTimestamp: metaTime(i.CreatedAt), Labels: i.Labels,
		OwnerReferences: mapItems(i.Owners, func(ref owner) metav1.OwnerReference {
			return metav1.OwnerReference{Kind: ref.Kind, Name: ref.Name, UID: types.UID(ref.UID), Controller: ref.Controller}
		}),
	}
	if i.DeletedAt != nil {
		m.DeletionTimestamp = new(metaTime(i.DeletedAt))
	}
	return m
}

type expression struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

type selector struct {
	Labels      labelSet     `json:"labels,omitempty"`
	Expressions []expression `json:"expressions,omitempty"`
}

func describeSelector(s *metav1.LabelSelector) *selector {
	if s == nil {
		return nil
	}
	return &selector{Labels: s.MatchLabels, Expressions: mapItems(s.MatchExpressions, func(e metav1.LabelSelectorRequirement) expression {
		return expression{Key: e.Key, Operator: string(e.Operator), Values: e.Values}
	})}
}

func (s *selector) meta() *metav1.LabelSelector {
	if s == nil {
		return nil
	}
	return &metav1.LabelSelector{MatchLabels: s.Labels, MatchExpressions: mapItems(s.Expressions, func(e expression) metav1.LabelSelectorRequirement {
		return metav1.LabelSelectorRequirement{Key: e.Key, Operator: metav1.LabelSelectorOperator(e.Operator), Values: e.Values}
	})}
}

type condition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	TransitionAt       *time.Time `json:"transition_at,omitempty"`
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`
	ObservedGeneration int64      `json:"observed_generation,omitempty"`
}

type node struct {
	Identity   identity    `json:"identity"`
	Conditions []condition `json:"conditions,omitempty"`
}

func describeNode(n core.Node) node {
	return node{Identity: describe(n.ObjectMeta, false), Conditions: mapItems(n.Status.Conditions, func(c core.NodeCondition) condition {
		return condition{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason,
			TransitionAt: timestamp(c.LastTransitionTime.Time), UpdatedAt: timestamp(c.LastHeartbeatTime.Time),
		}
	})}
}

func (n node) observation() core.Node {
	out := core.Node{ObjectMeta: n.Identity.metadata()}
	out.Status.Conditions = mapItems(n.Conditions, func(c condition) core.NodeCondition {
		return core.NodeCondition{
			Type: core.NodeConditionType(c.Type), Status: core.ConditionStatus(c.Status), Reason: c.Reason,
			LastTransitionTime: metaTime(c.TransitionAt), LastHeartbeatTime: metaTime(c.UpdatedAt),
		}
	})
	return out
}

type resourcePair struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type requirements struct {
	Requests resourcePair `json:"requests"`
	Limits   resourcePair `json:"limits"`
}

func describeResources(r core.ResourceRequirements) requirements {
	describe := func(list core.ResourceList) resourcePair {
		var out resourcePair
		if cpu, has := list[core.ResourceCPU]; has {
			out.CPU = cpu.String()
		}
		if memory, has := list[core.ResourceMemory]; has {
			out.Memory = memory.String()
		}
		return out
	}
	return requirements{Requests: describe(r.Requests), Limits: describe(r.Limits)}
}

func (r requirements) observation() core.ResourceRequirements {
	decode := func(pair resourcePair) core.ResourceList {
		out := core.ResourceList{}
		if pair.CPU != "" {
			out[core.ResourceCPU], _ = resource.ParseQuantity(pair.CPU)
		}
		if pair.Memory != "" {
			out[core.ResourceMemory], _ = resource.ParseQuantity(pair.Memory)
		}
		return out
	}
	return core.ResourceRequirements{Requests: decode(r.Requests), Limits: decode(r.Limits)}
}

type port struct {
	Name     string `json:"name,omitempty"`
	Number   int32  `json:"number,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type configReference struct {
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Key      string   `json:"key,omitempty"`
	Keys     []string `json:"keys,omitempty"`
	Optional *bool    `json:"optional,omitempty"`
	Variable string   `json:"variable,omitempty"`
}

type container struct {
	Name        string            `json:"name"`
	Resources   requirements      `json:"resources"`
	Ports       []port            `json:"ports,omitempty"`
	Environment []configReference `json:"environment_refs,omitempty"`
	Imports     []configReference `json:"environment_imports,omitempty"`
	Sidecar     bool              `json:"restartable_init,omitempty"`
}

func configEnv(kind, object, key, variable string, optional *bool) configReference {
	return configReference{Kind: kind, Name: object, Key: key, Variable: variable, Optional: optional}
}

func describeContainer(c core.Container) container {
	out := container{
		Name: c.Name, Resources: describeResources(c.Resources),
		Sidecar: c.RestartPolicy != nil && *c.RestartPolicy == core.ContainerRestartPolicyAlways,
		Ports: mapItems(c.Ports, func(p core.ContainerPort) port {
			return port{Name: p.Name, Number: p.ContainerPort, Protocol: string(p.Protocol)}
		}),
	}
	for _, v := range c.Env {
		if v.ValueFrom == nil {
			continue
		}
		if r := v.ValueFrom.ConfigMapKeyRef; r != nil {
			out.Environment = append(out.Environment, configEnv("ConfigMap", r.Name, r.Key, v.Name, r.Optional))
		}
		if r := v.ValueFrom.SecretKeyRef; r != nil {
			out.Environment = append(out.Environment, configEnv("Secret", r.Name, r.Key, v.Name, r.Optional))
		}
	}
	for _, v := range c.EnvFrom {
		if r := v.ConfigMapRef; r != nil {
			out.Imports = append(out.Imports, configEnv("ConfigMap", r.Name, "", "", r.Optional))
		}
		if r := v.SecretRef; r != nil {
			out.Imports = append(out.Imports, configEnv("Secret", r.Name, "", "", r.Optional))
		}
	}
	return out
}

func (c container) observation() core.Container {
	out := core.Container{
		Name: c.Name, Resources: c.Resources.observation(),
		Ports: mapItems(c.Ports, func(p port) core.ContainerPort {
			return core.ContainerPort{Name: p.Name, ContainerPort: p.Number, Protocol: core.Protocol(p.Protocol)}
		}),
	}
	if c.Sidecar {
		out.RestartPolicy = new(core.ContainerRestartPolicyAlways)
	}
	for _, r := range c.Environment {
		source := &core.EnvVarSource{}
		if r.Kind == "Secret" {
			source.SecretKeyRef = &core.SecretKeySelector{
				LocalObjectReference: core.LocalObjectReference{Name: r.Name}, Key: r.Key, Optional: r.Optional,
			}
		} else {
			source.ConfigMapKeyRef = &core.ConfigMapKeySelector{
				LocalObjectReference: core.LocalObjectReference{Name: r.Name}, Key: r.Key, Optional: r.Optional,
			}
		}
		out.Env = append(out.Env, core.EnvVar{Name: r.Variable, ValueFrom: source})
	}
	for _, r := range c.Imports {
		source := core.EnvFromSource{}
		if r.Kind == "Secret" {
			source.SecretRef = &core.SecretEnvSource{
				LocalObjectReference: core.LocalObjectReference{Name: r.Name}, Optional: r.Optional,
			}
		} else {
			source.ConfigMapRef = &core.ConfigMapEnvSource{
				LocalObjectReference: core.LocalObjectReference{Name: r.Name}, Optional: r.Optional,
			}
		}
		out.EnvFrom = append(out.EnvFrom, source)
	}
	return out
}

type termination struct {
	Reason     string     `json:"reason,omitempty"`
	ExitCode   int32      `json:"exit_code"`
	Signal     int32      `json:"signal,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type containerState struct {
	Waiting      *string      `json:"waiting_reason,omitempty"`
	Running      bool         `json:"running,omitempty"`
	RunningSince *time.Time   `json:"running_since,omitempty"`
	Terminated   *termination `json:"terminated,omitempty"`
}

type containerStatus struct {
	Name         string         `json:"name"`
	Ready        bool           `json:"ready,omitempty"`
	RestartCount int32          `json:"restart_count,omitempty"`
	Started      *bool          `json:"started,omitempty"`
	Current      containerState `json:"current"`
	Previous     containerState `json:"previous"`
}

func describeState(s core.ContainerState) containerState {
	var out containerState
	if s.Waiting != nil {
		out.Waiting = new(s.Waiting.Reason)
	}
	if s.Running != nil {
		out.Running = true
		out.RunningSince = timestamp(s.Running.StartedAt.Time)
	}
	if t := s.Terminated; t != nil {
		out.Terminated = &termination{
			Reason: t.Reason, ExitCode: t.ExitCode, Signal: t.Signal,
			StartedAt: timestamp(t.StartedAt.Time), FinishedAt: timestamp(t.FinishedAt.Time),
		}
	}
	return out
}

func (s containerState) observation() core.ContainerState {
	var out core.ContainerState
	if s.Waiting != nil {
		out.Waiting = &core.ContainerStateWaiting{Reason: *s.Waiting}
	}
	if s.Running {
		out.Running = &core.ContainerStateRunning{StartedAt: metaTime(s.RunningSince)}
	}
	if t := s.Terminated; t != nil {
		out.Terminated = &core.ContainerStateTerminated{
			Reason: t.Reason, ExitCode: t.ExitCode, Signal: t.Signal,
			StartedAt: metaTime(t.StartedAt), FinishedAt: metaTime(t.FinishedAt),
		}
	}
	return out
}

func describeContainerStatus(s core.ContainerStatus) containerStatus {
	return containerStatus{
		Name: s.Name, Ready: s.Ready, RestartCount: s.RestartCount, Started: s.Started,
		Current: describeState(s.State), Previous: describeState(s.LastTerminationState),
	}
}

func (s containerStatus) observation() core.ContainerStatus {
	return core.ContainerStatus{
		Name: s.Name, Ready: s.Ready, RestartCount: s.RestartCount, Started: s.Started,
		State: s.Current.observation(), LastTerminationState: s.Previous.observation(),
	}
}

type volume struct {
	Name       string            `json:"name"`
	Claim      string            `json:"claim,omitempty"`
	Direct     []configReference `json:"direct_refs,omitempty"`
	Projection []configReference `json:"projected_refs,omitempty"`
}

func volumeRef(kind, object string, items []core.KeyToPath, optional *bool) configReference {
	return configReference{Kind: kind, Name: object, Optional: optional, Keys: mapItems(items, func(v core.KeyToPath) string {
		return v.Key
	})}
}

func describeVolume(v core.Volume) volume {
	out := volume{Name: v.Name}
	if v.PersistentVolumeClaim != nil {
		out.Claim = v.PersistentVolumeClaim.ClaimName
	}
	if r := v.ConfigMap; r != nil {
		out.Direct = append(out.Direct, volumeRef("ConfigMap", r.Name, r.Items, r.Optional))
	}
	if r := v.Secret; r != nil {
		out.Direct = append(out.Direct, volumeRef("Secret", r.SecretName, r.Items, r.Optional))
	}
	if v.Projected != nil {
		for _, p := range v.Projected.Sources {
			if r := p.ConfigMap; r != nil {
				out.Projection = append(out.Projection, volumeRef("ConfigMap", r.Name, r.Items, r.Optional))
			}
			if r := p.Secret; r != nil {
				out.Projection = append(out.Projection, volumeRef("Secret", r.Name, r.Items, r.Optional))
			}
		}
	}
	return out
}

func (v volume) observation() core.Volume {
	out := core.Volume{Name: v.Name}
	if v.Claim != "" {
		out.PersistentVolumeClaim = &core.PersistentVolumeClaimVolumeSource{ClaimName: v.Claim}
	}
	items := func(r configReference) []core.KeyToPath {
		return mapItems(r.Keys, func(key string) core.KeyToPath { return core.KeyToPath{Key: key} })
	}
	for _, r := range v.Direct {
		if r.Kind == "Secret" {
			out.Secret = &core.SecretVolumeSource{SecretName: r.Name, Optional: r.Optional, Items: items(r)}
		} else {
			out.ConfigMap = &core.ConfigMapVolumeSource{
				LocalObjectReference: core.LocalObjectReference{Name: r.Name}, Optional: r.Optional, Items: items(r),
			}
		}
	}
	if len(v.Projection) > 0 {
		out.Projected = &core.ProjectedVolumeSource{}
		for _, r := range v.Projection {
			local := core.LocalObjectReference{Name: r.Name}
			entry := core.VolumeProjection{}
			if r.Kind == "Secret" {
				entry.Secret = &core.SecretProjection{LocalObjectReference: local, Optional: r.Optional, Items: items(r)}
			} else {
				entry.ConfigMap = &core.ConfigMapProjection{LocalObjectReference: local, Optional: r.Optional, Items: items(r)}
			}
			out.Projected.Sources = append(out.Projected.Sources, entry)
		}
	}
	return out
}

type pod struct {
	Identity     identity          `json:"identity"`
	HostNode     string            `json:"host_node,omitempty"`
	HostNetwork  bool              `json:"host_network,omitempty"`
	Gated        bool              `json:"scheduling_gated,omitempty"`
	Resources    *requirements     `json:"pod_resources,omitempty"`
	Containers   []container       `json:"containers,omitempty"`
	Init         []container       `json:"init_containers,omitempty"`
	Volumes      []volume          `json:"volumes,omitempty"`
	Phase        string            `json:"phase,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	Conditions   []condition       `json:"conditions,omitempty"`
	Statuses     []containerStatus `json:"container_statuses,omitempty"`
	InitStatuses []containerStatus `json:"init_statuses,omitempty"`
}

func describePod(p core.Pod) pod {
	out := pod{
		Identity: describe(p.ObjectMeta, true), HostNode: p.Spec.NodeName, HostNetwork: p.Spec.HostNetwork,
		Gated:      len(p.Spec.SchedulingGates) > 0,
		Containers: mapItems(p.Spec.Containers, describeContainer), Init: mapItems(p.Spec.InitContainers, describeContainer),
		Volumes: mapItems(p.Spec.Volumes, describeVolume),
		Phase:   string(p.Status.Phase), Reason: p.Status.Reason,
		Conditions: mapItems(p.Status.Conditions, func(c core.PodCondition) condition {
			return condition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, TransitionAt: timestamp(c.LastTransitionTime.Time)}
		}),
		Statuses:     mapItems(p.Status.ContainerStatuses, describeContainerStatus),
		InitStatuses: mapItems(p.Status.InitContainerStatuses, describeContainerStatus),
	}
	if p.Spec.Resources != nil {
		out.Resources = new(describeResources(*p.Spec.Resources))
	}
	if p.Status.StartTime != nil {
		out.StartedAt = timestamp(p.Status.StartTime.Time)
	}
	return out
}

func (p pod) observation() core.Pod {
	out := core.Pod{ObjectMeta: p.Identity.metadata()}
	out.Spec.NodeName, out.Spec.HostNetwork = p.HostNode, p.HostNetwork
	if p.Gated {
		out.Spec.SchedulingGates = []core.PodSchedulingGate{{}}
	}
	if p.Resources != nil {
		out.Spec.Resources = new(p.Resources.observation())
	}
	out.Spec.Containers = mapItems(p.Containers, container.observation)
	out.Spec.InitContainers = mapItems(p.Init, container.observation)
	out.Spec.Volumes = mapItems(p.Volumes, volume.observation)
	out.Status.Phase, out.Status.Reason = core.PodPhase(p.Phase), p.Reason
	if p.StartedAt != nil {
		out.Status.StartTime = new(metaTime(p.StartedAt))
	}
	out.Status.Conditions = mapItems(p.Conditions, func(c condition) core.PodCondition {
		return core.PodCondition{Type: core.PodConditionType(c.Type), Status: core.ConditionStatus(c.Status), Reason: c.Reason, LastTransitionTime: metaTime(c.TransitionAt)}
	})
	out.Status.ContainerStatuses = mapItems(p.Statuses, containerStatus.observation)
	out.Status.InitContainerStatuses = mapItems(p.InitStatuses, containerStatus.observation)
	return out
}

type claim struct {
	Identity     identity `json:"identity"`
	Class        *string  `json:"storage_class,omitempty"`
	Volume       string   `json:"volume,omitempty"`
	SelectedNode string   `json:"selected_node,omitempty"`
	Phase        string   `json:"phase,omitempty"`
}

const selectedNode = "volume.kubernetes.io/selected-node"

func describeClaim(c core.PersistentVolumeClaim) claim {
	return claim{Identity: describe(c.ObjectMeta, false), Class: c.Spec.StorageClassName,
		Volume: c.Spec.VolumeName, SelectedNode: c.Annotations[selectedNode], Phase: string(c.Status.Phase)}
}

func (c claim) observation() core.PersistentVolumeClaim {
	out := core.PersistentVolumeClaim{ObjectMeta: c.Identity.metadata()}
	out.Spec.StorageClassName, out.Spec.VolumeName = c.Class, c.Volume
	out.Status.Phase = core.PersistentVolumeClaimPhase(c.Phase)
	if c.SelectedNode != "" {
		out.Annotations = map[string]string{selectedNode: c.SelectedNode}
	}
	return out
}

type storageClass struct {
	Identity identity `json:"identity"`
	Binding  string   `json:"binding,omitempty"`
	Default  bool     `json:"default,omitempty"`
}

const defaultClass = "storageclass.kubernetes.io/is-default-class"
const betaDefaultClass = "storageclass.beta.kubernetes.io/is-default-class"

func describeClass(c storage.StorageClass) storageClass {
	out := storageClass{Identity: describe(c.ObjectMeta, false), Default: c.Annotations[defaultClass] == "true" || c.Annotations[betaDefaultClass] == "true"}
	if c.VolumeBindingMode != nil {
		out.Binding = string(*c.VolumeBindingMode)
	}
	return out
}

func (c storageClass) observation() storage.StorageClass {
	out := storage.StorageClass{ObjectMeta: c.Identity.metadata()}
	if c.Default {
		out.Annotations = map[string]string{defaultClass: "true"}
	}
	if c.Binding != "" {
		out.VolumeBindingMode = new(storage.VolumeBindingMode(c.Binding))
	}
	return out
}

type replicaSet struct {
	Identity       identity  `json:"identity"`
	Desired        *int32    `json:"desired,omitempty"`
	Selector       *selector `json:"selector,omitempty"`
	TemplateLabels labelSet  `json:"template_labels,omitempty"`
}

func describeReplicaSet(r apps.ReplicaSet) replicaSet {
	return replicaSet{Identity: describe(r.ObjectMeta, false), Desired: r.Spec.Replicas,
		Selector: describeSelector(r.Spec.Selector), TemplateLabels: r.Spec.Template.Labels}
}

func (r replicaSet) observation() apps.ReplicaSet {
	out := apps.ReplicaSet{ObjectMeta: r.Identity.metadata()}
	out.Spec.Replicas, out.Spec.Selector, out.Spec.Template.Labels = r.Desired, r.Selector.meta(), r.TemplateLabels
	return out
}

type deployment struct {
	Identity           identity    `json:"identity"`
	Desired            *int32      `json:"desired,omitempty"`
	Selector           *selector   `json:"selector,omitempty"`
	TemplateLabels     labelSet    `json:"template_labels,omitempty"`
	Paused             bool        `json:"paused,omitempty"`
	MinReadySeconds    int32       `json:"min_ready_seconds,omitempty"`
	DeadlineSeconds    *int32      `json:"progress_deadline_seconds,omitempty"`
	ObservedGeneration int64       `json:"observed_generation,omitempty"`
	Total              int32       `json:"replicas,omitempty"`
	Updated            int32       `json:"updated,omitempty"`
	Ready              int32       `json:"ready,omitempty"`
	Available          int32       `json:"available,omitempty"`
	Unavailable        int32       `json:"unavailable,omitempty"`
	Conditions         []condition `json:"conditions,omitempty"`
}

func describeDeployment(d apps.Deployment) deployment {
	return deployment{
		Identity: describe(d.ObjectMeta, false), Desired: d.Spec.Replicas, Selector: describeSelector(d.Spec.Selector),
		TemplateLabels: d.Spec.Template.Labels, Paused: d.Spec.Paused, MinReadySeconds: d.Spec.MinReadySeconds,
		DeadlineSeconds: d.Spec.ProgressDeadlineSeconds, ObservedGeneration: d.Status.ObservedGeneration,
		Total: d.Status.Replicas, Updated: d.Status.UpdatedReplicas, Ready: d.Status.ReadyReplicas,
		Available: d.Status.AvailableReplicas, Unavailable: d.Status.UnavailableReplicas,
		Conditions: mapItems(d.Status.Conditions, func(c apps.DeploymentCondition) condition {
			return condition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason,
				TransitionAt: timestamp(c.LastTransitionTime.Time), UpdatedAt: timestamp(c.LastUpdateTime.Time)}
		}),
	}
}

func (d deployment) observation() apps.Deployment {
	out := apps.Deployment{ObjectMeta: d.Identity.metadata()}
	out.Spec.Replicas, out.Spec.Selector, out.Spec.Template.Labels = d.Desired, d.Selector.meta(), d.TemplateLabels
	out.Spec.Paused, out.Spec.MinReadySeconds, out.Spec.ProgressDeadlineSeconds = d.Paused, d.MinReadySeconds, d.DeadlineSeconds
	out.Status.ObservedGeneration, out.Status.Replicas, out.Status.UpdatedReplicas = d.ObservedGeneration, d.Total, d.Updated
	out.Status.ReadyReplicas, out.Status.AvailableReplicas, out.Status.UnavailableReplicas = d.Ready, d.Available, d.Unavailable
	out.Status.Conditions = mapItems(d.Conditions, func(c condition) apps.DeploymentCondition {
		return apps.DeploymentCondition{Type: apps.DeploymentConditionType(c.Type), Status: core.ConditionStatus(c.Status),
			Reason: c.Reason, LastTransitionTime: metaTime(c.TransitionAt), LastUpdateTime: metaTime(c.UpdatedAt)}
	})
	return out
}

type daemonSet struct {
	Identity           identity  `json:"identity"`
	Selector           *selector `json:"selector,omitempty"`
	TemplateLabels     labelSet  `json:"template_labels,omitempty"`
	MinReadySeconds    int32     `json:"min_ready_seconds,omitempty"`
	Strategy           string    `json:"strategy,omitempty"`
	ObservedGeneration int64     `json:"observed_generation,omitempty"`
	Desired            int32     `json:"desired,omitempty"`
	Current            int32     `json:"current,omitempty"`
	Misscheduled       int32     `json:"misscheduled,omitempty"`
	Updated            int32     `json:"updated,omitempty"`
	Ready              int32     `json:"ready,omitempty"`
	Available          int32     `json:"available,omitempty"`
	Unavailable        int32     `json:"unavailable,omitempty"`
}

func describeDaemonSet(d apps.DaemonSet) daemonSet {
	return daemonSet{
		Identity: describe(d.ObjectMeta, false), Selector: describeSelector(d.Spec.Selector), TemplateLabels: d.Spec.Template.Labels,
		MinReadySeconds: d.Spec.MinReadySeconds, Strategy: string(d.Spec.UpdateStrategy.Type),
		ObservedGeneration: d.Status.ObservedGeneration, Desired: d.Status.DesiredNumberScheduled,
		Current: d.Status.CurrentNumberScheduled, Misscheduled: d.Status.NumberMisscheduled,
		Updated: d.Status.UpdatedNumberScheduled, Ready: d.Status.NumberReady,
		Available: d.Status.NumberAvailable, Unavailable: d.Status.NumberUnavailable,
	}
}

func (d daemonSet) observation() apps.DaemonSet {
	out := apps.DaemonSet{ObjectMeta: d.Identity.metadata()}
	out.Spec.Selector, out.Spec.Template.Labels = d.Selector.meta(), d.TemplateLabels
	out.Spec.MinReadySeconds, out.Spec.UpdateStrategy.Type = d.MinReadySeconds, apps.DaemonSetUpdateStrategyType(d.Strategy)
	out.Status.ObservedGeneration, out.Status.DesiredNumberScheduled = d.ObservedGeneration, d.Desired
	out.Status.CurrentNumberScheduled, out.Status.NumberMisscheduled = d.Current, d.Misscheduled
	out.Status.UpdatedNumberScheduled, out.Status.NumberReady = d.Updated, d.Ready
	out.Status.NumberAvailable, out.Status.NumberUnavailable = d.Available, d.Unavailable
	return out
}

type classifiedEvent struct {
	Resource  model.Resource `json:"resource"`
	Reason    string         `json:"reason"`
	Time      time.Time      `json:"time"`
	TimeBasis string         `json:"time_basis,omitempty"`
	Count     int32          `json:"count,omitempty"`
	Reference *configIssue   `json:"missing_reference,omitempty"`
}

type configIssue struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Key     string `json:"key,omitempty"`
	Missing string `json:"missing"`
}

func describeEvent(e model.Event) classifiedEvent {
	out := classifiedEvent{Resource: e.Resource, Reason: e.Reason, Time: e.Time, TimeBasis: e.TimeBasis, Count: e.Count}
	if e.Reference != nil {
		out.Reference = new(configIssue(*e.Reference))
	}
	return out
}

func (e classifiedEvent) observation() model.Event {
	out := model.Event{Resource: e.Resource, Reason: e.Reason, Time: e.Time, TimeBasis: e.TimeBasis, Count: e.Count}
	if e.Reference != nil {
		out.Reference = new(model.ConfigIssue(*e.Reference))
	}
	return out
}

type configMap struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Keys      []string `json:"keys,omitempty"`
	Binary    []string `json:"binary_keys,omitempty"`
}

func describeConfigMap(c model.ConfigMap) configMap {
	return configMap{Namespace: c.Namespace, Name: c.Name, Keys: c.Keys, Binary: c.BinaryKeys}
}

func (c configMap) observation() model.ConfigMap {
	return model.ConfigMap{Namespace: c.Namespace, Name: c.Name, Keys: c.Keys, BinaryKeys: c.Binary}
}

type currentUsage struct {
	Namespace  string           `json:"namespace"`
	Name       string           `json:"name"`
	Timestamp  time.Time        `json:"timestamp"`
	Window     time.Duration    `json:"window_nanoseconds"`
	Containers []containerUsage `json:"containers,omitempty"`
}

type containerUsage struct {
	Name  string       `json:"name"`
	Usage resourcePair `json:"usage"`
}

func describeUsage(u model.PodUsage) currentUsage {
	return currentUsage{
		Namespace: u.Namespace, Name: u.Name, Timestamp: u.Timestamp, Window: u.Window,
		Containers: mapItems(u.Containers, func(c model.ContainerUsage) containerUsage {
			return containerUsage{Name: c.Name, Usage: resourcePair{CPU: c.CPU.String(), Memory: c.Memory.String()}}
		}),
	}
}

func (u currentUsage) observation() model.PodUsage {
	return model.PodUsage{
		Namespace: u.Namespace, Name: u.Name, Timestamp: u.Timestamp, Window: u.Window,
		Containers: mapItems(u.Containers, func(c containerUsage) model.ContainerUsage {
			cpu, _ := resource.ParseQuantity(c.Usage.CPU)
			memory, _ := resource.ParseQuantity(c.Usage.Memory)
			return model.ContainerUsage{Name: c.Name, CPU: cpu, Memory: memory}
		}),
	}
}

func timestamp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return new(t.UTC())
}

func metaTime(t *time.Time) metav1.Time {
	if t == nil {
		return metav1.Time{}
	}
	return metav1.NewTime(t.UTC())
}

func mapItems[I, O any](in []I, project func(I) O) []O {
	if len(in) == 0 {
		return nil
	}
	out := make([]O, len(in))
	for i, item := range in {
		out[i] = project(item)
	}
	return out
}

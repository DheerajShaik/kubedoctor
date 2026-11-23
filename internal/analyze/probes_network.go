package analyze

import (
	"slices"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func rolloutDeficits(s model.Snapshot) map[model.Resource]model.Evidence {
	rollouts := map[model.Resource]model.Evidence{}
	for i := range s.Deployments {
		d := &s.Deployments[i]
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		if d.DeletionTimestamp == nil && !d.Spec.Paused && d.Generation > 0 && d.Status.ObservedGeneration == d.Generation && d.Status.AvailableReplicas < desired {
			rollouts[model.Identity("Deployment", d)] = countEvidence("controller_available_replicas", int64(d.Status.AvailableReplicas))
		}
	}
	for i := range s.DaemonSets {
		d := &s.DaemonSets[i]
		if d.DeletionTimestamp == nil && d.Generation > 0 && d.Status.ObservedGeneration == d.Generation && d.Status.NumberAvailable < d.Status.DesiredNumberScheduled {
			rollouts[model.Identity("DaemonSet", d)] = countEvidence("controller_available_replicas", int64(d.Status.NumberAvailable))
		}
	}
	for _, set := range s.Expansion.StatefulSets {
		if !set.Deleting && set.Generation > 0 && set.ObservedGeneration == set.Generation && set.Available < set.Desired {
			rollouts[set.Resource] = countEvidence("controller_available_replicas", int64(set.Available))
		}
	}
	return rollouts
}

func analyzeRolloutProbes(s model.Snapshot, index workloadIndex) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, probesRule, model.SourcePods)
	if len(s.Expansion.Probes) == 0 {
		if check.Status == model.Checked {
			check.Status, check.Detail = model.Skipped, "No captured Pod applications or init containers declared a probe"
		}
		return nil, check
	}
	if s.Coverage(model.SourceEvents) != model.Complete {
		workloadUncertain(&check, "Probe-specific kubelet event visibility is incomplete")
	}
	configured := make(map[model.Resource]model.PodProbeEvidence, len(s.Expansion.Probes))
	for _, declaration := range s.Expansion.Probes {
		configured[declaration.Pod] = declaration
		if declaration.Truncated {
			workloadUncertain(&check, "A Pod's probe declarations were truncated")
		}
	}
	pods := map[workloadLocator]*core.Pod{}
	for i := range s.Pods {
		p := &s.Pods[i]
		if activePod(p) && p.Status.Phase != core.PodFailed {
			pods[locateWorkload(model.Identity("Pod", p))] = p
		}
	}
	rollouts := rolloutDeficits(s)
	type probeKey struct {
		pod                   model.Resource
		container, role, kind string
	}
	latest := map[probeKey]model.ProbeEventEvidence{}
	for _, event := range s.Expansion.ProbeEvents {
		pod := pods[locateWorkload(event.Event.Resource)]
		if pod == nil {
			continue
		}
		id := model.Identity("Pod", pod)
		if !recentTimedReference(s, event.Event, id, pod.CreationTimestamp.Time) || !observedWithin(s.CollectedAt, event.Event.Time, 15*time.Minute) ||
			!declaredProbe(configured[id], event) {
			continue
		}
		key := probeKey{id, event.Container, event.Role, event.Kind}
		previous, ok := latest[key]
		if !ok || event.Event.Time.After(previous.Event.Time) || event.Event.Time.Equal(previous.Event.Time) && event.Event.Count > previous.Event.Count {
			latest[key] = event
		}
	}
	var findings []model.Finding
	for key, event := range latest {
		pod := pods[locateWorkload(key.pod)]
		status := probeContainerStatus(pod, key.container, key.role)
		readiness := podReadiness(pod)
		if status == nil || readiness == nil || readiness.Status == core.ConditionUnknown {
			workloadUncertain(&check, "Probe events lack usable current container or Pod readiness evidence")
			continue
		}
		if readiness.Status == core.ConditionTrue || status.Ready || !currentProbeEffect(status, event) {
			continue
		}
		incident := index.podIncident(pod)
		severity := model.Info
		evidence := []model.Evidence{workloadEvidence("probe", key.kind), workloadEvidence("container", key.container), workloadEvidence("container_role", key.role), workloadEvidence("pod_ready", "False")}
		evidence = addEventEvidence(evidence, model.Event{Resource: key.pod, Reason: "Unhealthy", Time: event.Event.Time, Count: event.Event.Count})
		symptom := "A recent kubelet probe failure is consistent with the currently unready container"
		if incident != nil {
			if rollout, observed := rollouts[*incident]; observed {
				severity, symptom = model.Warning, "A current controller availability shortfall includes a Pod with a corroborated recent probe failure"
				evidence = append(evidence, rollout)
			}
		}
		finding := workloadFinding(probesRule, key.pod, severity, symptom,
			"Review this container's declared probe and its current application startup state. Readiness gates serving and workload availability, while startup and liveness can affect restarts.", evidence...)
		finding.Incident = incident
		findings = append(findings, finding)
	}
	return findings, check
}

func declaredProbe(declaration model.PodProbeEvidence, event model.ProbeEventEvidence) bool {
	for _, container := range declaration.Containers {
		if container.Name == event.Container && container.Role == event.Role && slices.Contains(container.Kinds, event.Kind) {
			return true
		}
	}
	return false
}

func probeContainerStatus(pod *core.Pod, container, role string) *core.ContainerStatus {
	statuses := pod.Status.ContainerStatuses
	if role == "init" {
		statuses = pod.Status.InitContainerStatuses
	}
	for _, status := range statuses {
		if status.Name == container {
			return &status
		}
	}
	return nil
}

func currentProbeEffect(status *core.ContainerStatus, event model.ProbeEventEvidence) bool {
	if event.Kind == "startup" && status.Started != nil && !*status.Started {
		return status.State.Running != nil && !status.State.Running.StartedAt.IsZero() && !event.Event.Time.Before(status.State.Running.StartedAt.Time) ||
			probeRestartEvidence(status, event.Event.Time)
	}
	if event.Kind == "liveness" {
		return probeRestartEvidence(status, event.Event.Time)
	}
	return event.Kind == "readiness" && status.State.Running != nil && !status.State.Running.StartedAt.IsZero() &&
		!event.Event.Time.Before(status.State.Running.StartedAt.Time)
}

func probeRestartEvidence(status *core.ContainerStatus, timestamp time.Time) bool {
	terminated := status.LastTerminationState.Terminated
	if terminated == nil {
		terminated = status.State.Terminated
	}
	return terminated != nil && !terminated.StartedAt.IsZero() && !timestamp.Before(terminated.StartedAt.Time) &&
		observedWithin(terminated.FinishedAt.Time, timestamp, 5*time.Minute)
}

func analyzeServiceIdentities(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, serviceUIDRule, model.SourceEndpointSlices)
	if s.Coverage(model.SourceServices) != model.Complete || s.Coverage(model.SourcePods) != model.Complete {
		workloadUncertain(&check, "Service or current Pod identity inventory is incomplete")
	}
	pods := map[networkKey]*core.Pod{}
	for i := range s.Pods {
		pod := &s.Pods[i]
		if activePod(pod) && pod.Status.Phase != core.PodFailed {
			pods[networkKey{pod.Namespace, pod.Name}] = pod
		}
	}
	services := map[networkKey]*core.Service{}
	for i := range s.Services {
		service := &s.Services[i]
		if service.DeletionTimestamp == nil && service.Spec.Type != core.ServiceTypeExternalName && len(service.Spec.Selector) > 0 {
			services[networkKey{service.Namespace, service.Name}] = service
		}
	}
	mismatches := map[model.Resource]map[string]bool{}
	for _, slice := range s.EndpointSlices {
		if slice.Labels[discovery.LabelManagedBy] != endpointController || (slice.AddressType != discovery.AddressTypeIPv4 && slice.AddressType != discovery.AddressTypeIPv6) {
			continue
		}
		service := services[networkKey{slice.Namespace, slice.Labels[discovery.LabelServiceName]}]
		if service == nil || service.UID == "" || !slices.ContainsFunc(slice.OwnerReferences, func(owner metav1.OwnerReference) bool {
			return owner.UID == service.UID && owner.Kind == "Service" && owner.Name == service.Name
		}) {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			ref := endpoint.TargetRef
			if ref == nil || ref.Kind != "Pod" || ref.Namespace != service.Namespace || ref.UID == "" || len(endpoint.Addresses) == 0 ||
				endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating {
				continue
			}
			pod := pods[networkKey{ref.Namespace, ref.Name}]
			if pod == nil || pod.UID == "" || pod.UID == ref.UID || !labels.SelectorFromSet(service.Spec.Selector).Matches(labels.Set(pod.Labels)) ||
				!agedEnough(s.CollectedAt, pod.CreationTimestamp.Time, serviceReconcileGrace) {
				continue
			}
			key := model.Identity("Service", service)
			if mismatches[key] == nil {
				mismatches[key] = map[string]bool{}
			}
			mismatches[key][ref.Name] = true
		}
	}
	var findings []model.Finding
	for service, names := range mismatches {
		findings = append(findings, workloadFinding(serviceUIDRule, service, model.Info, "A controller-managed EndpointSlice still refers to a replaced Pod identity",
			"Compare the current Service selector, Pod UID, and EndpointSlice target UID. The API objects establish stale identity evidence. Verify request behavior separately.",
			countEvidence("replaced_pod_names", int64(len(names)))))
	}
	return findings, check
}

func analyzeNodeLocalDNS(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, nodeLocalRule, model.SourceNodeLocalDNS)
	provider := s.Expansion.NodeLocalDNS
	if provider == nil {
		if check.Status == model.Skipped {
			check.Detail = "Conventional NodeLocal DNSCache was absent. Other provider layouts may be present."
		}
		return nil, check
	}
	if provider.Generation <= 0 || provider.Generation != provider.ObservedGeneration {
		workloadUncertain(&check, "The NodeLocal DNSCache provider has stale or unobserved DaemonSet status")
		return nil, check
	}
	nodeCache := map[string]struct{ ready, unready bool }{}
	for _, pod := range s.Expansion.NodeLocalPods {
		if pod.Deleting || pod.Owner == nil || *pod.Owner != provider.Resource || pod.Node == "" {
			continue
		}
		current := nodeCache[pod.Node]
		current.ready = current.ready || pod.Ready == "True"
		current.unready = current.unready || pod.Ready == "False"
		nodeCache[pod.Node] = current
	}
	var relevantUnready, unknown int64
	if s.Scope.Kind == model.NamespaceScope && s.Coverage(model.SourceNodeLocalPods) != model.NotApplicable {
		hosts := map[string]bool{}
		for i := range s.Pods {
			pod := &s.Pods[i]
			if activePod(pod) && pod.Status.Phase != core.PodFailed && pod.Spec.NodeName != "" {
				hosts[pod.Spec.NodeName] = true
			}
		}
		for host := range hosts {
			observed := nodeCache[host]
			if observed.ready {
				continue
			}
			if observed.unready && s.Coverage(model.SourceNodeLocalPods) == model.Complete {
				relevantUnready++
			} else {
				unknown++
			}
		}
	}
	if unknown > 0 || s.Coverage(model.SourceNodeLocalPods) != model.Complete && s.Coverage(model.SourceNodeLocalPods) != model.NotApplicable {
		workloadUncertain(&check, "A current Ready NodeLocal DNSCache Pod could not be established on all observed hosting nodes")
	}
	if provider.Desired == 0 || relevantUnready == 0 && (s.Scope.Kind != model.ClusterScope || provider.Ready >= provider.Desired || !agedEnough(s.CollectedAt, provider.Created, startupGrace)) {
		return nil, check
	}
	evidence := []model.Evidence{countEvidence("provider_desired", int64(provider.Desired)), countEvidence("provider_ready", int64(provider.Ready))}
	symptom := "The conventional NodeLocal DNSCache DaemonSet reports fewer Ready Pods than desired"
	if relevantUnready > 0 {
		symptom = "The conventional NodeLocal DNSCache has observed unready cache Pods on workload hosting nodes"
		evidence = append(evidence, countEvidence("hosting_nodes_with_unready_cache", relevantUnready))
	}
	return []model.Finding{workloadFinding(nodeLocalRule, provider.Resource, model.Warning, symptom,
		"Check which DNS settings the affected workloads use and inspect the cache and upstream DNS components. Readiness alone cannot establish name resolution from a Pod.", evidence...)}, check
}

func analyzePolicyPorts(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, policyPortRule, model.SourceNetworkPolicies)
	if s.Coverage(model.SourcePods) != model.Complete {
		workloadUncertain(&check, "Pod inventory must be complete to evaluate absent named port declarations")
		return nil, check
	}
	pods := map[string][]*core.Pod{}
	for i := range s.Pods {
		pod := &s.Pods[i]
		if activePod(pod) && pod.Status.Phase != core.PodFailed {
			pods[pod.Namespace] = append(pods[pod.Namespace], pod)
		}
	}
	var findings []model.Finding
	for i := range s.NetworkPolicies {
		policy := &s.NetworkPolicies[i]
		ingress, _ := policyDirections(policy.Spec)
		if policy.DeletionTimestamp != nil || !ingress {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if err != nil {
			workloadUncertain(&check, "A NetworkPolicy Pod selector could not be evaluated")
			continue
		}
		var selected []*core.Pod
		for _, pod := range pods[policy.Namespace] {
			if selector.Matches(labels.Set(pod.Labels)) {
				selected = append(selected, pod)
			}
		}
		if len(selected) == 0 {
			continue
		}
		type namedPort struct {
			name     string
			protocol core.Protocol
		}
		seen := map[namedPort]bool{}
		for _, rule := range policy.Spec.Ingress {
			for _, port := range rule.Ports {
				if port.Port == nil || port.Port.Type != intstr.String || port.Port.StrVal == "" {
					continue
				}
				protocol := core.ProtocolTCP
				if port.Protocol != nil {
					protocol = *port.Protocol
				}
				key := namedPort{port.Port.StrVal, protocol}
				if seen[key] {
					continue
				}
				seen[key] = true
				if slices.ContainsFunc(selected, func(pod *core.Pod) bool { return hasNamedIngressPort(pod, key.name, key.protocol) }) {
					continue
				}
				finding := workloadFinding(policyPortRule, model.Identity("NetworkPolicy", policy), model.Info,
					"A named ingress port is undeclared on all visibly selected Pods",
					"Compare the ingress rule with the selected Pods' named ports and protocol. Other policies may allow traffic, and enforcement depends on the networking implementation.",
					workloadEvidence("named_port", key.name), workloadEvidence("protocol", string(key.protocol)), countEvidence("selected_pods", int64(len(selected))))
				findings = append(findings, finding)
			}
		}
	}
	return findings, check
}

func hasNamedIngressPort(pod *core.Pod, name string, protocol core.Protocol) bool {
	for _, group := range []struct {
		containers []core.Container
		init       bool
	}{{pod.Spec.Containers, false}, {pod.Spec.InitContainers, true}} {
		for _, container := range group.containers {
			if group.init && (container.RestartPolicy == nil || *container.RestartPolicy != core.ContainerRestartPolicyAlways) {
				continue
			}
			for _, port := range container.Ports {
				candidate := port.Protocol
				if candidate == "" {
					candidate = core.ProtocolTCP
				}
				if port.Name == name && port.ContainerPort > 0 && candidate == protocol {
					return true
				}
			}
		}
	}
	return false
}

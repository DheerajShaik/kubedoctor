package analyze

import (
	"slices"
	"strconv"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	serviceBackendsID = "service.backends"
	servicePortsID    = "service.ports"
	ingressBackendsID = "ingress.backends"
	dnsID             = "dns.infrastructure"
	networkPolicyID   = "networkpolicy.isolation"
	pdbID             = "pdb.disruptions"

	serviceReconcileGrace = time.Minute
	endpointController    = "endpointslice-controller.k8s.io"
)

var networkDefinitions = []model.CheckDefinition{
	{ID: serviceBackendsID, Description: "Service selectors, active Pods, and published backend addresses"},
	{ID: servicePortsID, Description: "Service target ports and published backend port mappings"},
	{ID: ingressBackendsID, Description: "Ingress references to same-namespace Services and their declared ports"},
	{ID: dnsID, Description: "Conventional cluster DNS Service, endpoint readiness, and configuration metadata"},
	{ID: networkPolicyID, Description: "Pods selected by NetworkPolicies with no observed allow rules in a direction"},
	{ID: pdbID, Description: "Current PodDisruptionBudget allowances and voluntary eviction constraints"},
}

func analyzeNetwork(s model.Snapshot) ([]model.Finding, []model.CheckResult) {
	services := make(map[networkKey]*core.Service, len(s.Services))
	pods := make(map[string][]*core.Pod)
	for i := range s.Services {
		service := &s.Services[i]
		services[networkKey{service.Namespace, service.Name}] = service
	}
	for i := range s.Pods {
		pod := &s.Pods[i]
		pods[pod.Namespace] = append(pods[pod.Namespace], pod)
	}

	findings, checks := analyzeServiceBackends(s, pods)
	add := func(ruleFindings []model.Finding, check model.CheckResult) {
		findings = append(findings, ruleFindings...)
		checks = append(checks, check)
	}
	add(analyzeIngressBackends(s, services))
	add(analyzeDNS(s))
	add(analyzeNetworkPolicies(s, pods))
	add(analyzePDBs(s))
	return findings, checks
}

type networkKey struct {
	namespace string
	name      string
}

type networkPort struct {
	name     string
	number   *int32
	protocol core.Protocol
}

type networkBackendGroup struct {
	owner             types.UID
	ports             []networkPort
	controllerTargets []types.UID
	ready             int
	draining          int
	observed          int
	partial           bool
}

type networkBackendInventory struct {
	groups   map[networkKey][]networkBackendGroup
	coverage model.Coverage
	legacy   bool
}

func networkBackendSource(s model.Snapshot, dns bool) networkBackendInventory {
	sliceCoverage, legacyCoverage := s.Coverage(model.SourceEndpointSlices), s.Coverage(model.SourceLegacyEndpoints)
	endpointSlices, legacyEndpoints := s.EndpointSlices, s.LegacyEndpoints
	if dns {
		sliceCoverage, legacyCoverage = s.Coverage(model.SourceDNSEndpointSlices), s.Coverage(model.SourceDNSLegacyEndpoints)
		endpointSlices, legacyEndpoints = s.DNSEndpointSlices, s.DNSLegacyEndpoints
	}
	// A denied or partially collected API never triggers a legacy fallback.
	if sliceCoverage == model.Unavailable && (legacyCoverage == model.Complete || legacyCoverage == model.Truncated) {
		return networkBackendInventory{groups: networkLegacyGroups(legacyEndpoints), coverage: legacyCoverage, legacy: true}
	}
	return networkBackendInventory{groups: networkSliceGroups(endpointSlices), coverage: sliceCoverage}
}

func networkSliceGroups(endpointSlices []discovery.EndpointSlice) map[networkKey][]networkBackendGroup {
	groups := make(map[networkKey][]networkBackendGroup)
	for i := range endpointSlices {
		slice := &endpointSlices[i]
		serviceName := slice.Labels[discovery.LabelServiceName]
		if serviceName == "" {
			continue
		}
		key := networkKey{slice.Namespace, serviceName}
		var group networkBackendGroup
		for _, owner := range slice.OwnerReferences {
			if owner.Kind == "Service" && owner.Name == serviceName {
				group.owner = owner.UID
				break
			}
		}
		if slice.AddressType != "" && slice.AddressType != discovery.AddressTypeIPv4 && slice.AddressType != discovery.AddressTypeIPv6 {
			// Address families consumed by the built-in Service proxy are IPv4 and IPv6.
			group.partial = true
			groups[key] = append(groups[key], group)
			continue
		}
		for _, port := range slice.Ports {
			projected := networkPort{number: port.Port}
			if port.Name != nil {
				projected.name = *port.Name
			}
			if port.Protocol != nil {
				projected.protocol = *port.Protocol
			}
			group.ports = append(group.ports, projected)
		}
		for _, endpoint := range slice.Endpoints {
			if len(endpoint.Addresses) == 0 {
				continue
			}
			group.observed++
			if group.owner != "" && slice.Labels[discovery.LabelManagedBy] == endpointController && endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" && endpoint.TargetRef.UID != "" {
				group.controllerTargets = append(group.controllerTargets, endpoint.TargetRef.UID)
			}
			ready := endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready
			if endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating {
				serving := endpoint.Conditions.Serving == nil || *endpoint.Conditions.Serving
				if serving || ready {
					group.draining++
				}
				continue
			}
			if ready {
				group.ready++
			}
		}
		groups[key] = append(groups[key], group)
	}
	return groups
}

func networkLegacyGroups(endpoints []core.Endpoints) map[networkKey][]networkBackendGroup {
	groups := make(map[networkKey][]networkBackendGroup)
	for _, endpoint := range endpoints {
		key := networkKey{endpoint.Namespace, endpoint.Name}
		if endpoint.Annotations["endpoints.kubernetes.io/over-capacity"] == "truncated" {
			groups[key] = append(groups[key], networkBackendGroup{partial: true})
		}
		for _, subset := range endpoint.Subsets {
			group := networkBackendGroup{ready: len(subset.Addresses), observed: len(subset.Addresses) + len(subset.NotReadyAddresses)}
			for _, port := range subset.Ports {
				number := port.Port
				group.ports = append(group.ports, networkPort{name: port.Name, number: &number, protocol: port.Protocol})
			}
			groups[key] = append(groups[key], group)
		}
	}
	return groups
}

func (inventory networkBackendInventory) forService(service *core.Service) ([]networkBackendGroup, bool) {
	groups := inventory.groups[networkKey{service.Namespace, service.Name}]
	current := make([]networkBackendGroup, 0, len(groups))
	complete := inventory.coverage == model.Complete
	for _, group := range groups {
		if group.owner != "" && service.UID != "" && group.owner != service.UID {
			continue
		}
		complete = complete && !group.partial
		current = append(current, group)
	}
	return current, complete
}

func networkEndpointCounts(groups []networkBackendGroup) (ready, draining, observed int) {
	for _, group := range groups {
		ready += group.ready
		draining += group.draining
		observed += group.observed
	}
	return ready, draining, observed
}

func analyzeServiceBackends(s model.Snapshot, pods map[string][]*core.Pod) ([]model.Finding, []model.CheckResult) {
	inventory := networkBackendSource(s, false)
	podsByUID := make(map[types.UID]*core.Pod, len(s.Pods))
	for i := range s.Pods {
		pod := &s.Pods[i]
		if pod.UID != "" {
			podsByUID[pod.UID] = pod
		}
	}
	dedicatedDNS := networkObservedDNSService(s)
	dnsEndpointsVisible := s.Coverage(model.SourceDNSEndpointSlices) == model.Complete ||
		(s.Coverage(model.SourceDNSEndpointSlices) == model.Unavailable && s.Coverage(model.SourceDNSLegacyEndpoints) == model.Complete)
	backendCheck := model.CheckResult{ID: serviceBackendsID, Status: model.Checked}
	portCheck := model.CheckResult{ID: servicePortsID, Status: model.Checked}
	if s.Coverage(model.SourceServices) != model.Complete {
		backendCheck.Status, portCheck.Status = model.Inconclusive, model.Inconclusive
		backendCheck.Detail, portCheck.Detail = "Service inventory is incomplete.", "Service inventory is incomplete."
	} else if len(s.Services) == 0 {
		backendCheck.Status, portCheck.Status = model.Skipped, model.Skipped
		backendCheck.Detail, portCheck.Detail = "No Services in this scope.", "No Services in this scope."
	}

	var findings []model.Finding
	applicable, portsMapped := false, false
	for i := range s.Services {
		service := &s.Services[i]
		if service.Spec.Type == core.ServiceTypeExternalName {
			continue
		}
		// The DNS rule owns the conventional DNS Service when the dedicated inventory contains it.
		if dedicatedDNS != nil && dnsEndpointsVisible && service.Namespace == dedicatedDNS.Namespace && service.Name == dedicatedDNS.Name && service.UID == dedicatedDNS.UID {
			continue
		}
		applicable = true
		groups, complete := inventory.forService(service)
		ready, draining, observed := networkEndpointCounts(groups)
		if !complete {
			backendCheck.Status = model.Inconclusive
			backendCheck.Detail = "Endpoint inventory is incomplete for one or more Services."
			if len(service.Spec.Ports) > 0 {
				portCheck.Status = model.Inconclusive
				portCheck.Detail = "Endpoint port visibility is incomplete for one or more Services."
			}
		}
		if networkIsFresh(service.CreationTimestamp.Time, s.CollectedAt) {
			backendCheck.Status = model.Inconclusive
			backendCheck.Detail = "One or more Services are within the endpoint reconciliation grace period."
			if len(service.Spec.Ports) > 0 {
				portCheck.Status = model.Inconclusive
				portCheck.Detail = "One or more Services are within the endpoint reconciliation grace period."
			}
			continue
		}
		if ready+draining > 0 && len(service.Spec.Selector) > 0 {
			if mismatched := serviceSelectorMismatches(service, groups, podsByUID); mismatched > 0 {
				findings = append(findings, networkFinding(serviceBackendsID, "Service", service, model.Info, model.Observed,
					"Controller-published backend targets include Pods whose current labels differ from the Service selector.",
					[]model.Evidence{{Field: "mismatched_pod_uids", Value: strconv.Itoa(mismatched)}},
					"Check recent Service and Pod label changes and endpoint reconciliation because the published target identity disagrees with the selector."))
			}
		}

		if ready+draining == 0 {
			if !complete {
				continue
			}
			if len(service.Spec.Selector) == 0 && observed == 0 {
				backendCheck.Status = model.Inconclusive
				backendCheck.Detail = "One or more selectorless Services have no observed published backend addresses."
				if len(service.Spec.Ports) > 0 {
					portCheck.Status = model.Inconclusive
					portCheck.Detail = "One or more Services have no eligible backend port mappings to inspect."
				}
				continue
			}
			if len(service.Spec.Selector) > 0 && s.Coverage(model.SourcePods) != model.Complete {
				backendCheck.Status = model.Inconclusive
				backendCheck.Detail = "Pod inventory is incomplete for selector inspection."
			}
			if finding, ok := serviceMissingBackends(s, service, pods[service.Namespace], observed); ok {
				findings = append(findings, finding)
				if len(service.Spec.Ports) > 0 {
					portCheck.Status = model.Inconclusive
					portCheck.Detail = "One or more Services have no eligible backend port mappings to inspect."
				}
			}
			if s.Coverage(model.SourcePods) == model.Complete && networkCheckNumericTarget(service) {
				findings = append(findings, serviceNamedTargetGaps(service, pods[service.Namespace])...)
			}
			continue
		}

		if ready == 0 && complete {
			findings = append(findings, networkFinding(serviceBackendsID, "Service", service, model.Info, model.Observed,
				"Only terminating backend entries remain eligible by their published conditions.",
				[]model.Evidence{{Field: "draining_entries", Value: strconv.Itoa(draining)}},
				"Check rollout or termination progress because available connections may depend on terminating endpoints."))
		}
		if !complete {
			continue
		}
		for _, port := range service.Spec.Ports {
			portsMapped = true
			switch networkBackendPortCoverage(groups, port, networkCheckNumericTarget(service)) {
			case model.Unavailable:
				findings = append(findings, networkFinding(servicePortsID, "Service", service, model.Warning, model.Observed,
					"Usable backend entries do not publish a matching Service port.", networkServicePortEvidence(port),
					"Compare the Service port name, protocol and targetPort with the endpoint port mapping for its backends."))
			case model.NotInspected:
				portCheck.Status = model.Inconclusive
				portCheck.Detail = "One or more endpoint ports have unspecified values interpreted by their consumer."
			}
		}
	}

	if !applicable && backendCheck.Status == model.Checked {
		backendCheck.Status = model.Skipped
		backendCheck.Detail = "No applicable Services outside the dedicated DNS check."
	}
	if !portsMapped && portCheck.Status == model.Checked {
		portCheck.Status = model.Skipped
		portCheck.Detail = "No eligible Service backend port mappings in this scope."
	}
	if inventory.legacy && backendCheck.Status == model.Checked {
		backendCheck.Detail = "Legacy Endpoints fallback has reduced readiness and termination detail."
	}
	return findings, []model.CheckResult{backendCheck, portCheck}
}

func serviceSelectorMismatches(service *core.Service, groups []networkBackendGroup, podsByUID map[types.UID]*core.Pod) int {
	selector := labels.SelectorFromSet(service.Spec.Selector)
	mismatched := make(map[types.UID]bool)
	for _, group := range groups {
		if group.owner != service.UID {
			continue
		}
		for _, uid := range group.controllerTargets {
			pod := podsByUID[uid]
			if pod != nil && pod.Namespace == service.Namespace && networkActivePod(pod) && !selector.Matches(labels.Set(pod.Labels)) {
				mismatched[uid] = true
			}
		}
	}
	return len(mismatched)
}

func serviceMissingBackends(s model.Snapshot, service *core.Service, pods []*core.Pod, observed int) (model.Finding, bool) {
	active, succeeded := 0, 0
	if len(service.Spec.Selector) > 0 {
		selector := labels.SelectorFromSet(service.Spec.Selector)
		for _, pod := range pods {
			if !selector.Matches(labels.Set(pod.Labels)) {
				continue
			}
			if pod.Status.Phase == core.PodSucceeded {
				succeeded++
			}
			if networkActivePod(pod) {
				active++
			}
		}
		if active == 0 && s.Coverage(model.SourcePods) == model.Complete {
			scaledToZero, workloadExpectsPods := serviceControllerIntent(s, service.Namespace, selector)
			if scaledToZero || (succeeded > 0 && !workloadExpectsPods) {
				return model.Finding{}, false
			}
			severity := model.Info
			symptom := "The selector matches no active Pods and no usable backend entries were observed. The intended backend count is unverified."
			if workloadExpectsPods {
				severity = model.Warning
				symptom = "The selector matches no active Pods and no usable backend entries were observed despite a matching controller expecting Pods."
			}
			return networkFinding(serviceBackendsID, "Service", service, severity, model.Observed, symptom,
				[]model.Evidence{{Field: "matching_active_pods", Value: "0"}, {Field: "usable_backend_entries", Value: "0"}},
				"Compare the Service selector with Pod template labels and confirm that the expected workload has active replicas."), true
		}
	}

	severity := model.Warning
	if len(service.Spec.Selector) > 0 && active == 0 && observed == 0 {
		severity = model.Info
	}
	finding := networkFinding(serviceBackendsID, "Service", service, severity, model.Observed,
		"No ready or draining, serving backend entries were observed.",
		[]model.Evidence{{Field: "published_backend_entries", Value: strconv.Itoa(observed)}, {Field: "usable_backend_entries", Value: "0"}},
		"Inspect backend readiness and the Service endpoint controller or external endpoint manager for this Service.")
	finding.Incident = &finding.Resource
	return finding, true
}

func serviceControllerIntent(s model.Snapshot, namespace string, selector labels.Selector) (scaledToZero bool, workloadExpectsPods bool) {
	matched := false
	complete := s.Coverage(model.SourceDeployments) == model.Complete && s.Coverage(model.SourceDaemonSets) == model.Complete
	for _, deployment := range s.Deployments {
		if deployment.Namespace != namespace || !selector.Matches(labels.Set(deployment.Spec.Template.Labels)) {
			continue
		}
		matched = true
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
			return false, true
		}
	}
	for _, daemonSet := range s.DaemonSets {
		if daemonSet.Namespace != namespace || !selector.Matches(labels.Set(daemonSet.Spec.Template.Labels)) {
			continue
		}
		matched = true
		if daemonSet.Status.ObservedGeneration < daemonSet.Generation {
			complete = false
			continue
		}
		if daemonSet.Status.DesiredNumberScheduled > 0 {
			return false, true
		}
	}
	return matched && complete, false
}

func networkCheckNumericTarget(service *core.Service) bool {
	return service.Spec.ClusterIP != core.ClusterIPNone && len(service.Spec.Selector) > 0
}

func serviceNamedTargetGaps(service *core.Service, pods []*core.Pod) []model.Finding {
	selector := labels.SelectorFromSet(service.Spec.Selector)
	var findings []model.Finding
	for _, port := range service.Spec.Ports {
		if port.TargetPort.Type != intstr.String || port.TargetPort.StrVal == "" {
			continue
		}
		selected := 0
		found := false
		for _, pod := range pods {
			if !networkActivePod(pod) || !selector.Matches(labels.Set(pod.Labels)) {
				continue
			}
			selected++
			if serviceNamedTargetMatches(pod, port) {
				found = true
				break
			}
		}
		if selected == 0 || found {
			continue
		}
		evidence := append(networkServicePortEvidence(port),
			model.Evidence{Field: "named_target_port", Value: port.TargetPort.StrVal},
			model.Evidence{Field: "matching_active_pods", Value: strconv.Itoa(selected)})
		finding := networkFinding(servicePortsID, "Service", service, model.Warning, model.Observed,
			"The named targetPort is absent from all selected active Pod container port definitions.", evidence,
			"Check the targetPort name and protocol against the selected Pods' declared container ports because named Service targets use those declarations.")
		finding.Incident = &finding.Resource
		findings = append(findings, finding)
	}
	return findings
}

func serviceNamedTargetMatches(pod *core.Pod, port core.ServicePort) bool {
	matchesPort := func(container core.Container) bool {
		return slices.ContainsFunc(container.Ports, func(declared core.ContainerPort) bool {
			return declared.Name == port.TargetPort.StrVal && networkProtocol(declared.Protocol) == networkProtocol(port.Protocol)
		})
	}
	return slices.ContainsFunc(pod.Spec.Containers, matchesPort) || slices.ContainsFunc(pod.Spec.InitContainers, func(container core.Container) bool {
		return container.RestartPolicy != nil && *container.RestartPolicy == core.ContainerRestartPolicyAlways && matchesPort(container)
	})
}

func networkBackendPortCoverage(groups []networkBackendGroup, servicePort core.ServicePort, checkNumericTarget bool) model.Coverage {
	possible := false
	target := servicePort.Port
	if servicePort.TargetPort.Type == intstr.Int && servicePort.TargetPort.IntVal > 0 {
		target = servicePort.TargetPort.IntVal
	}
	for _, group := range groups {
		if group.ready+group.draining == 0 {
			continue
		}
		for _, port := range group.ports {
			if port.name != servicePort.Name || networkProtocol(port.protocol) != networkProtocol(servicePort.Protocol) {
				continue
			}
			if port.number == nil {
				possible = true
				continue
			}
			if *port.number > 0 && (!checkNumericTarget || servicePort.TargetPort.Type == intstr.String || *port.number == target) {
				return model.Complete
			}
		}
	}
	if possible {
		return model.NotInspected
	}
	return model.Unavailable
}

func networkServicePortEvidence(port core.ServicePort) []model.Evidence {
	evidence := []model.Evidence{
		{Field: "service_port", Value: strconv.Itoa(int(port.Port))},
		{Field: "protocol", Value: string(networkProtocol(port.Protocol))},
	}
	if port.Name != "" {
		evidence = append(evidence, model.Evidence{Field: "port_name", Value: port.Name})
	}
	if port.TargetPort.Type == intstr.Int && port.TargetPort.IntVal > 0 {
		evidence = append(evidence, model.Evidence{Field: "target_port", Value: strconv.Itoa(int(port.TargetPort.IntVal))})
	}
	return evidence
}

func networkActivePod(pod *core.Pod) bool {
	return pod.DeletionTimestamp == nil && pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed
}

func networkIsFresh(created, collected time.Time) bool {
	return !created.IsZero() && !collected.IsZero() && collected.Sub(created) < serviceReconcileGrace
}

func networkProtocol(protocol core.Protocol) core.Protocol {
	if protocol == "" {
		return core.ProtocolTCP
	}
	return protocol
}

func networkFinding(id, kind string, obj metav1.Object, severity model.Severity, confidence model.Confidence, symptom string, evidence []model.Evidence, nextStep string) model.Finding {
	return model.Finding{
		RuleID:     id,
		Resource:   model.Identity(kind, obj),
		Severity:   severity,
		Symptom:    symptom,
		Evidence:   evidence,
		Confidence: confidence,
		NextSteps:  []string{nextStep},
	}
}

func networkPodReady(pod *core.Pod) bool {
	return networkActivePod(pod) && slices.ContainsFunc(pod.Status.Conditions, func(condition core.PodCondition) bool {
		return condition.Type == core.PodReady && condition.Status == core.ConditionTrue
	})
}

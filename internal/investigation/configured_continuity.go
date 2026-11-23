package investigation

import (
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type configuredContinuity struct {
	ingresses map[model.Resource]map[ingressBackend]ingressObservation
	quotas    map[model.Resource]map[string]bool
	services  publishedContinuity
	dns       publishedContinuity
}

type ingressBackend struct{ service, port string }

type ingressObservation struct {
	service model.Resource
	tcpPort bool
}

type publishedContinuity struct {
	ready    map[model.Resource]bool
	ports    map[model.Resource]map[ServicePortReference]bool
	declared map[model.Resource][]ServicePortReference
}

func configuredEvidence(s model.Snapshot, index *inventory) configuredContinuity {
	facts := configuredContinuity{
		ingresses: observedIngressBackends(s.Ingresses, index),
		quotas:    make(map[model.Resource]map[string]bool, len(s.Expansion.Quotas)),
		services:  observedPublications(s.Services, s.EndpointSlices, s.Pods),
		dns:       observedPublications(s.DNSServices, s.DNSEndpointSlices, s.DNSPods),
	}
	for _, quota := range s.Expansion.Quotas {
		headroom := make(map[string]bool, len(quota.Capacities))
		for _, capacity := range quota.Capacities {
			if _, duplicated := headroom[capacity.Resource]; duplicated {
				headroom[capacity.Resource] = false
				continue
			}
			hard, used := quotaDecimal(capacity.Hard), quotaDecimal(capacity.Used)
			headroom[capacity.Resource] = capacity.Current && hard != nil && used != nil && used.Cmp(hard) < 0
		}
		facts.quotas[quota.Resource] = headroom
	}
	return facts
}

func quotaDecimal(value string) *big.Rat {
	if value == "" || len(value) > 48 || strings.Trim(value, "0123456789.") != "" {
		return nil
	}
	amount, _ := new(big.Rat).SetString(value)
	return amount
}

func observedIngressBackends(ingresses []networking.Ingress, index *inventory) map[model.Resource]map[ingressBackend]ingressObservation {
	out := make(map[model.Resource]map[ingressBackend]ingressObservation, len(ingresses))
	remaining := networkContinuityWorkLimit
	for i := range ingresses {
		ingress := &ingresses[i]
		if ingress.DeletionTimestamp != nil {
			continue
		}
		backends := make(map[ingressBackend]ingressObservation)
		record := func(backend *networking.IngressServiceBackend) {
			if backend == nil || backend.Port.Name == "" && backend.Port.Number <= 0 {
				return
			}
			port := backend.Port.Name
			if port == "" {
				port = strconv.Itoa(int(backend.Port.Number))
			}
			service := index.byName[resourceName{"Service", ingress.Namespace, backend.Name}]
			observation := ingressObservation{service: service.Resource}
			if service.Active && service.Resource.UID != "" && service.Service != nil {
				remaining -= len(service.Service.DeclaredPorts)
				observation.tcpPort = slices.ContainsFunc(service.Service.DeclaredPorts, func(declared ServicePortReference) bool {
					return declared.Number > 0 && declared.Protocol == string(core.ProtocolTCP) &&
						(backend.Port.Name != "" && declared.Name == backend.Port.Name ||
							backend.Port.Name == "" && declared.Number == backend.Port.Number)
				})
			}
			backends[ingressBackend{backend.Name, port}] = observation
		}
		if ingress.Spec.DefaultBackend != nil {
			record(ingress.Spec.DefaultBackend.Service)
		}
		for _, rule := range ingress.Spec.Rules {
			remaining--
			if remaining < 0 {
				return out
			}
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				remaining--
				if remaining < 0 {
					return out
				}
				record(path.Backend.Service)
			}
		}
		if remaining < 0 {
			return out
		}
		out[model.Identity("Ingress", ingress)] = backends
	}
	return out
}

func observedPublications(services []core.Service, endpoints []discovery.EndpointSlice, pods []core.Pod) publishedContinuity {
	out := publishedContinuity{
		ready: map[model.Resource]bool{}, ports: map[model.Resource]map[ServicePortReference]bool{},
		declared: map[model.Resource][]ServicePortReference{},
	}
	byName := make(map[resourceName]*core.Service, len(services))
	remaining := networkContinuityWorkLimit
	for i := range services {
		service := &services[i]
		if service.UID != "" && service.DeletionTimestamp == nil && service.Spec.Type != core.ServiceTypeExternalName {
			resource := model.Identity("Service", service)
			byName[nameOf(resource)] = service
			remaining -= len(service.Spec.Ports)
			if remaining < 0 {
				return out
			}
			for _, port := range service.Spec.Ports {
				out.declared[resource] = append(out.declared[resource], portReference(port))
			}
		}
	}
	byPod := make(map[model.Resource]*core.Pod, len(pods))
	for i := range pods {
		pod := &pods[i]
		byPod[model.Identity("Pod", pod)] = pod
	}
	for i := range endpoints {
		slice := &endpoints[i]
		service := byName[resourceName{"Service", slice.Namespace, slice.Labels[discovery.LabelServiceName]}]
		if service == nil || !currentEndpointSlice(slice, service) {
			continue
		}
		resource := model.Identity("Service", service)
		var ready []discovery.Endpoint
		for _, endpoint := range slice.Endpoints {
			remaining--
			if remaining < 0 {
				return out
			}
			if endpoint.Conditions.Ready != nil && serviceEndpointReady(endpoint, service.Spec.PublishNotReadyAddresses) {
				ready = append(ready, endpoint)
			}
		}
		if len(ready) == 0 {
			continue
		}
		out.ready[resource] = true
		for _, servicePort := range service.Spec.Ports {
			remaining--
			if remaining < 0 {
				return out
			}
			key := portReference(servicePort)
			for _, endpointPort := range slice.Ports {
				remaining--
				if remaining < 0 {
					return out
				}
				if !matchesPublication(endpointPort, key) {
					continue
				}
				if len(service.Spec.Selector) > 0 && service.Spec.ClusterIP != core.ClusterIPNone {
					if servicePort.TargetPort.Type == intstr.String {
						if !namedTargetPublished(service, servicePort, *endpointPort.Port, ready, byPod, &remaining) {
							continue
						}
					} else if *endpointPort.Port != numericTarget(servicePort) {
						continue
					}
				}
				if out.ports[resource] == nil {
					out.ports[resource] = map[ServicePortReference]bool{}
				}
				out.ports[resource][key] = true
			}
		}
	}
	return out
}

func currentEndpointSlice(slice *discovery.EndpointSlice, service *core.Service) bool {
	if slice.DeletionTimestamp != nil || slice.AddressType != discovery.AddressTypeIPv4 && slice.AddressType != discovery.AddressTypeIPv6 {
		return false
	}
	owned, unambiguous := serviceSliceOwner(slice, service)
	return unambiguous && (len(service.Spec.Selector) == 0 || owned)
}

func matchesPublication(endpoint discovery.EndpointPort, key ServicePortReference) bool {
	var name string
	var protocol core.Protocol
	if endpoint.Name != nil {
		name = *endpoint.Name
	}
	if endpoint.Protocol != nil {
		protocol = *endpoint.Protocol
	}
	return endpoint.Port != nil && *endpoint.Port > 0 && name == key.Name && protocolName(protocol) == key.Protocol
}

func protocolName(protocol core.Protocol) string {
	if protocol == "" {
		return string(core.ProtocolTCP)
	}
	return string(protocol)
}

func portReference(port core.ServicePort) ServicePortReference {
	return ServicePortReference{Name: port.Name, Number: port.Port, Protocol: protocolName(port.Protocol)}
}

func numericTarget(port core.ServicePort) int32 {
	if port.TargetPort.IntVal > 0 {
		return port.TargetPort.IntVal
	}
	return port.Port
}

func namedTargetPublished(service *core.Service, port core.ServicePort, number int32, ready []discovery.Endpoint, pods map[model.Resource]*core.Pod, remaining *int) bool {
	target := namedIngressPort{port.TargetPort.StrVal, core.ProtocolTCP}
	if port.Protocol != "" {
		target.protocol = port.Protocol
	}
	for _, endpoint := range ready {
		*remaining -= 1 + len(service.Spec.Selector)
		if *remaining < 0 {
			return false
		}
		ref := endpoint.TargetRef
		if ref == nil || ref.Kind != "Pod" || ref.Namespace != service.Namespace || ref.UID == "" {
			continue
		}
		pod := pods[model.Resource{Kind: ref.Kind, Name: ref.Name, Namespace: ref.Namespace, UID: string(ref.UID)}]
		if pod == nil {
			continue
		}
		*remaining -= len(pod.Status.Conditions)
		if *remaining < 0 || !readyIdentityPod(pod) {
			continue
		}
		selected := true
		for label, value := range service.Spec.Selector {
			if actual, exists := pod.Labels[label]; !exists || actual != value {
				selected = false
				break
			}
		}
		if selected && observedContainerPort(pod, target, number, remaining) {
			return true
		}
	}
	return false
}

func observedContainerPort(pod *core.Pod, key namedIngressPort, number int32, remaining *int) bool {
	for _, group := range []struct {
		containers []core.Container
		init       bool
	}{{pod.Spec.Containers, false}, {pod.Spec.InitContainers, true}} {
		for _, container := range group.containers {
			*remaining -= 1 + len(container.Ports)
			if *remaining < 0 {
				return false
			}
			if group.init && (container.RestartPolicy == nil || *container.RestartPolicy != core.ContainerRestartPolicyAlways) {
				continue
			}
			for _, port := range container.Ports {
				if port.Name == key.name && port.ContainerPort == number && protocolName(port.Protocol) == protocolName(key.protocol) {
					return true
				}
			}
		}
	}
	return false
}

func (current publishedContinuity) portReady(finding model.Finding) bool {
	number, err := strconv.ParseInt(evidenceValue(finding, "service_port"), 10, 32)
	if err != nil {
		return false
	}
	key := ServicePortReference{Name: evidenceValue(finding, "port_name"), Number: int32(number), Protocol: evidenceValue(finding, "protocol")}
	return current.ports[finding.Resource][key]
}

func configuredRecoveryEvidence(left, right compareSnapshot, finding model.Finding) (bool, bool) {
	before, after := left.inv.applicability.configured, right.inv.applicability.configured
	id := finding.Resource
	switch finding.RuleID {
	case "ingress.backends":
		want := ingressBackend{evidenceValue(finding, "backend_service"), evidenceValue(finding, "backend_port")}
		matched := false
		for backend, earlier := range before.ingresses[id] {
			if backend.service != want.service || want.port != "" && backend.port != want.port {
				continue
			}
			current := after.ingresses[id][backend]
			if !current.tcpPort || earlier.service.UID != "" && earlier.service != current.service {
				return true, false
			}
			matched = true
		}
		return true, matched
	case "namespace-admission":
		matched := false
		for _, item := range finding.Evidence {
			if item.Field == "quota_resource" {
				if !after.quotas[id][item.Value] {
					return true, false
				}
				matched = true
			}
		}
		return true, matched
	case "dns.infrastructure":
		return true, currentDNSRecovery(left, right, finding, after.dns)
	}
	return networkRecoveryEvidence(left, right, finding)
}

func currentDNSRecovery(left, right compareSnapshot, finding model.Finding, dns publishedContinuity) bool {
	handled, current := networkRecoveryEvidence(left, right, finding)
	if handled && !current {
		return false
	}
	switch {
	case evidenceValue(finding, "service_port") != "":
		return dns.portReady(finding)
	case evidenceValue(finding, "usable_backend_entries") == "0", evidenceValue(finding, "draining_entries") != "":
		return dns.ready[finding.Resource]
	case evidenceValue(finding, "inspected_ready_dns_pods") == "0":
		return handled && current
	case evidenceValue(finding, "missing_standard_port") != "":
		for _, item := range finding.Evidence {
			if item.Field == "missing_standard_port" && !slices.ContainsFunc(dns.declared[finding.Resource], func(port ServicePortReference) bool {
				return port.Number == 53 && item.Value == "53/"+port.Protocol
			}) {
				return false
			}
		}
		return true
	}
	return false
}

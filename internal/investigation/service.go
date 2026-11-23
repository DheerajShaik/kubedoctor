package investigation

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func (index *inventory) addService(service *core.Service, source model.Source) {
	resource := model.Identity("Service", service)
	index.add(resource, source, service.CreationTimestamp.Time, service.DeletionTimestamp == nil, true, false)
	key := nameOf(resource)
	item, ok := index.byName[key]
	if !ok || item.Resource != resource {
		return
	}
	shape := ServiceObservation{BackendMode: "selector", Headless: service.Spec.ClusterIP == core.ClusterIPNone,
		PublishNotReady: service.Spec.PublishNotReadyAddresses, DeclaredPorts: []ServicePortReference{}}
	if service.Spec.Type == core.ServiceTypeExternalName {
		shape.BackendMode = "external_name"
	} else if len(service.Spec.Selector) == 0 {
		shape.BackendMode = "selectorless"
	}
	for _, port := range service.Spec.Ports {
		protocol := port.Protocol
		if protocol == "" {
			protocol = core.ProtocolTCP
		}
		shape.DeclaredPorts = append(shape.DeclaredPorts, ServicePortReference{Name: port.Name, Number: port.Port, Protocol: string(protocol)})
	}
	item.Service = &shape
	index.byName[key] = item
}

func (index *inventory) markReadyBackends(endpointSlices []discovery.EndpointSlice) {
	for _, slice := range endpointSlices {
		key := resourceName{"Service", slice.Namespace, slice.Labels[discovery.LabelServiceName]}
		service, observed := index.byName[key]
		if !observed || service.Service == nil || service.Ready || service.Service.BackendMode == "external_name" ||
			slice.AddressType != discovery.AddressTypeIPv4 && slice.AddressType != discovery.AddressTypeIPv6 {
			continue
		}
		if slices.ContainsFunc(slice.OwnerReferences, func(ref metav1.OwnerReference) bool {
			return ref.Kind == "Service" && (ref.Name != service.Resource.Name || string(ref.UID) != service.Resource.UID)
		}) {
			continue
		}
		service.Ready = slices.ContainsFunc(slice.Endpoints, func(endpoint discovery.Endpoint) bool {
			return serviceEndpointReady(endpoint, service.Service.PublishNotReady)
		})
		index.byName[key] = service
	}
}

func comparableServiceBackend(before, after ResourceObservation, finding model.Finding) bool {
	previous, current := before.Service, after.Service
	if previous == nil || current == nil || !after.Active || !after.Ready || previous.BackendMode != current.BackendMode ||
		previous.Headless != current.Headless || previous.PublishNotReady != current.PublishNotReady {
		return false
	}
	if finding.RuleID == "service-endpoint-identity" {
		return endpointIdentitiesRecovered(previous.identities, current.identities, finding)
	}
	if finding.RuleID != "service.ports" {
		return true
	}
	number, err := strconv.ParseInt(evidenceValue(finding, "service_port"), 10, 32)
	if err != nil {
		return false
	}
	port := ServicePortReference{Name: evidenceValue(finding, "port_name"), Number: int32(number), Protocol: evidenceValue(finding, "protocol")}
	return slices.Contains(current.DeclaredPorts, port)
}

const (
	builtInServiceEndpointController = "endpointslice-controller.k8s.io"
	endpointIdentityObservationGrace = time.Minute
)

type serviceIdentityContinuity struct {
	selector          map[string]string
	targets           map[servicePodAddressFamily]serviceEndpointIdentity
	selectorEndpoints serviceEndpointSelectorContinuity
}

type servicePodAddressFamily struct {
	name   string
	family discovery.AddressType
}

type serviceEndpointIdentity struct {
	mismatch     bool
	corroborated bool
	conflicting  bool
}

type serviceIdentityInventory struct {
	service  *core.Service
	selector labels.Selector
	evidence *serviceIdentityContinuity
}

// Use only primary Pod and EndpointSlice observations for this rule.
// The DNS supplement is collected separately and can have a different view.
func (index *inventory) recordEndpointIdentities(s model.Snapshot) {
	if len(s.EndpointSlices) == 0 || len(s.Pods) == 0 ||
		s.Coverage(model.SourceServices) != model.Complete || s.Coverage(model.SourcePods) != model.Complete ||
		s.Coverage(model.SourceEndpointSlices) != model.Complete {
		return
	}
	pods := make(map[resourceName]*core.Pod, len(s.Pods))
	for i := range s.Pods {
		pod := &s.Pods[i]
		if pod.UID != "" && pod.DeletionTimestamp == nil &&
			pod.Status.Phase != core.PodFailed && pod.Status.Phase != core.PodSucceeded {
			pods[resourceName{"Pod", pod.Namespace, pod.Name}] = pod
		}
	}
	services := make(map[resourceName]serviceIdentityInventory, len(s.Services))
	for i := range s.Services {
		service := &s.Services[i]
		if service.UID == "" || service.DeletionTimestamp != nil ||
			service.Spec.Type == core.ServiceTypeExternalName || len(service.Spec.Selector) == 0 {
			continue
		}
		services[resourceName{"Service", service.Namespace, service.Name}] = serviceIdentityInventory{
			service: service, selector: labels.SelectorFromSet(service.Spec.Selector),
			evidence: &serviceIdentityContinuity{
				selector: maps.Clone(service.Spec.Selector), targets: make(map[servicePodAddressFamily]serviceEndpointIdentity),
			},
		}
	}

	remaining := networkContinuityWorkLimit
	for i := range s.EndpointSlices {
		slice := &s.EndpointSlices[i]
		if slice.Labels[discovery.LabelManagedBy] != builtInServiceEndpointController ||
			(slice.AddressType != discovery.AddressTypeIPv4 && slice.AddressType != discovery.AddressTypeIPv6) {
			continue
		}
		service, found := services[resourceName{"Service", slice.Namespace, slice.Labels[discovery.LabelServiceName]}]
		if !found {
			continue
		}
		owned, unambiguous := serviceSliceOwner(slice, service.service)
		// A competing target can remain published on another slice even when
		// one updated slice shows the correct UID.
		for _, endpoint := range slice.Endpoints {
			remaining--
			if remaining < 0 {
				return // Later unexamined entries could contradict positives.
			}
			if !relevantSelectorEndpoint(endpoint) {
				continue
			}
			ref := endpoint.TargetRef
			var pod *core.Pod
			if ref != nil && ref.Kind == "Pod" && (ref.Namespace == "" || ref.Namespace == slice.Namespace) {
				pod = pods[resourceName{"Pod", slice.Namespace, ref.Name}]
				if pod != nil {
					remaining -= len(service.evidence.selector) + len(pod.Status.Conditions)
					if remaining < 0 {
						return
					}
				}
			}
			selected := pod != nil && service.selector.Matches(labels.Set(pod.Labels))
			sameUID := pod != nil && ref.UID != "" && ref.UID == pod.UID && ref.Namespace == slice.Namespace
			sameOwnedSlice := owned && unambiguous && slice.DeletionTimestamp == nil
			ready := serviceEndpointReady(endpoint, service.service.Spec.PublishNotReadyAddresses) &&
				(endpoint.Conditions.Serving == nil || *endpoint.Conditions.Serving)
			service.evidence.selectorEndpoints.observe(sameOwnedSlice && sameUID && selected &&
				(pod.Status.Phase == core.PodPending || pod.Status.Phase == core.PodRunning), ready)
			if !serviceEndpointHasAddress(endpoint) || !selected {
				continue
			}

			key := servicePodAddressFamily{pod.Name, slice.AddressType}
			state := service.evidence.targets[key]
			state.mismatch = state.mismatch || (owned && ref.Namespace == slice.Namespace && ref.UID != "" && ref.UID != pod.UID &&
				!pod.CreationTimestamp.IsZero() && s.CollectedAt.Sub(pod.CreationTimestamp.Time) >= endpointIdentityObservationGrace)
			state.conflicting = state.conflicting || !owned || !unambiguous || !sameUID
			state.corroborated = state.corroborated || (sameOwnedSlice && sameUID && ready && readyIdentityPod(pod))
			service.evidence.targets[key] = state
		}
	}
	for key, service := range services {
		item, seen := index.byName[key]
		if seen && item.Resource == model.Identity("Service", service.service) && item.Service != nil {
			item.Service.identities = service.evidence
		}
	}
}

func serviceSliceOwner(slice *discovery.EndpointSlice, service *core.Service) (matches, unambiguous bool) {
	unambiguous = true
	for _, ref := range slice.OwnerReferences {
		if ref.Kind != "Service" {
			continue
		}
		if service.UID != "" && ref.UID == service.UID && ref.Name == service.Name {
			matches = true
		} else {
			unambiguous = false
		}
	}
	return matches, unambiguous
}

func serviceEndpointHasAddress(endpoint discovery.Endpoint) bool {
	return len(endpoint.Addresses) != 0 && (endpoint.Conditions.Terminating == nil || !*endpoint.Conditions.Terminating)
}

func serviceEndpointReady(endpoint discovery.Endpoint, publishNotReady bool) bool {
	return serviceEndpointHasAddress(endpoint) && (endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready) &&
		(!publishNotReady || endpoint.Conditions.Serving != nil && *endpoint.Conditions.Serving)
}

func readyIdentityPod(pod *core.Pod) bool {
	if pod.Status.Phase != core.PodRunning || pod.DeletionTimestamp != nil {
		return false
	}
	count, ready := 0, false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == core.PodReady {
			count++
			ready = condition.Status == core.ConditionTrue
		}
	}
	return count == 1 && ready
}

func endpointIdentitiesRecovered(before, after *serviceIdentityContinuity, finding model.Finding) bool {
	if before == nil || after == nil || !maps.Equal(before.selector, after.selector) {
		return false
	}
	expected, err := strconv.Atoi(evidenceValue(finding, "replaced_pod_names"))
	if err != nil || expected <= 0 {
		return false
	}
	names := make(map[string]bool)
	for key, state := range before.targets {
		if state.mismatch {
			observed := after.targets[key]
			if !observed.corroborated || observed.conflicting {
				return false
			}
			names[key.name] = true
		}
	}
	return len(names) == expected
}

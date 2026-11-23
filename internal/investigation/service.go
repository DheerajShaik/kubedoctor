package investigation

import (
	"slices"
	"strconv"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			return len(endpoint.Addresses) != 0 && (endpoint.Conditions.Terminating == nil || !*endpoint.Conditions.Terminating) &&
				(endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready) &&
				(!service.Service.PublishNotReady || endpoint.Conditions.Serving != nil && *endpoint.Conditions.Serving)
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

package collect

import (
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	policy "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	metrics "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func projectProtocol(protocol core.Protocol) core.Protocol {
	return core.Protocol(allowed(string(protocol), string(core.ProtocolTCP), string(core.ProtocolUDP), string(core.ProtocolSCTP)))
}

func projectService(value core.Service) core.Service {
	out := core.Service{ObjectMeta: asIdentity(value.ObjectMeta)}
	switch value.Spec.Type {
	case core.ServiceTypeClusterIP, core.ServiceTypeNodePort, core.ServiceTypeLoadBalancer, core.ServiceTypeExternalName:
		out.Spec.Type = value.Spec.Type
	}
	if value.Spec.ClusterIP == core.ClusterIPNone {
		out.Spec.ClusterIP = core.ClusterIPNone
	}
	out.Spec.Selector = labels(value.Spec.Selector)
	out.Spec.PublishNotReadyAddresses = value.Spec.PublishNotReadyAddresses
	for _, port := range value.Spec.Ports {
		target := intstr.FromInt32(port.TargetPort.IntVal)
		if port.TargetPort.Type == intstr.String {
			target = intstr.FromString(token(port.TargetPort.StrVal))
		}
		out.Spec.Ports = append(out.Spec.Ports, core.ServicePort{
			Name: token(port.Name), Port: port.Port, Protocol: projectProtocol(port.Protocol), TargetPort: target,
		})
	}
	return out
}

func projectEndpointSlice(value discovery.EndpointSlice) discovery.EndpointSlice {
	out := discovery.EndpointSlice{ObjectMeta: asIdentity(value.ObjectMeta)}
	if serviceName := name(value.Labels[discovery.LabelServiceName]); serviceName != "" {
		out.Labels = map[string]string{discovery.LabelServiceName: serviceName}
		if value.Labels[discovery.LabelManagedBy] == "endpointslice-controller.k8s.io" {
			out.Labels[discovery.LabelManagedBy] = "endpointslice-controller.k8s.io"
		}
	}
	switch value.AddressType {
	case discovery.AddressTypeIPv4, discovery.AddressTypeIPv6, discovery.AddressTypeFQDN:
		out.AddressType = value.AddressType
	}
	for _, entry := range value.Endpoints {
		out.Endpoints = append(out.Endpoints, discovery.Endpoint{
			Addresses: make([]string, len(entry.Addresses)), Conditions: entry.Conditions, TargetRef: projectObjectRef(entry.TargetRef),
		})
	}
	for _, port := range value.Ports {
		projected := discovery.EndpointPort{Port: port.Port}
		if port.Name != nil {
			projected.Name = new(token(*port.Name))
		}
		if port.Protocol != nil {
			projected.Protocol = new(projectProtocol(*port.Protocol))
		}
		out.Ports = append(out.Ports, projected)
	}
	return out
}

func projectObjectRef(ref *core.ObjectReference) *core.ObjectReference {
	if ref == nil {
		return nil
	}
	return &core.ObjectReference{
		Kind: token(ref.Kind), Namespace: name(ref.Namespace), Name: name(ref.Name), UID: types.UID(token(string(ref.UID))),
	}
}

const overCapacity = "endpoints.kubernetes.io/over-capacity"

func projectLegacyEndpoints(value core.Endpoints) core.Endpoints {
	out := core.Endpoints{ObjectMeta: asIdentity(value.ObjectMeta)}
	if value.Annotations[overCapacity] == "truncated" {
		out.Annotations = map[string]string{overCapacity: "truncated"}
	}
	for _, subset := range value.Subsets {
		projected := core.EndpointSubset{}
		for _, address := range subset.Addresses {
			projected.Addresses = append(projected.Addresses, core.EndpointAddress{TargetRef: projectObjectRef(address.TargetRef)})
		}
		for _, address := range subset.NotReadyAddresses {
			projected.NotReadyAddresses = append(projected.NotReadyAddresses, core.EndpointAddress{TargetRef: projectObjectRef(address.TargetRef)})
		}
		for _, port := range subset.Ports {
			projected.Ports = append(projected.Ports, core.EndpointPort{
				Name: token(port.Name), Port: port.Port, Protocol: projectProtocol(port.Protocol),
			})
		}
		out.Subsets = append(out.Subsets, projected)
	}
	return out
}

func projectIngress(value networking.Ingress) networking.Ingress {
	out := networking.Ingress{ObjectMeta: asIdentity(value.ObjectMeta)}
	if value.Spec.DefaultBackend != nil {
		out.Spec.DefaultBackend = new(projectIngressBackend(*value.Spec.DefaultBackend))
	}
	for _, rule := range value.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		http := networking.HTTPIngressRuleValue{}
		for _, entry := range rule.HTTP.Paths {
			http.Paths = append(http.Paths, networking.HTTPIngressPath{Backend: projectIngressBackend(entry.Backend)})
		}
		out.Spec.Rules = append(out.Spec.Rules, networking.IngressRule{
			IngressRuleValue: networking.IngressRuleValue{HTTP: &http},
		})
	}
	return out
}

func projectIngressBackend(value networking.IngressBackend) networking.IngressBackend {
	out := networking.IngressBackend{}
	if value.Service != nil {
		out.Service = &networking.IngressServiceBackend{
			Name: name(value.Service.Name),
			Port: networking.ServiceBackendPort{Name: token(value.Service.Port.Name), Number: value.Service.Port.Number},
		}
	}
	if value.Resource != nil {
		out.Resource = &core.TypedLocalObjectReference{Kind: token(value.Resource.Kind), Name: name(value.Resource.Name)}
	}
	return out
}

func projectNetworkPolicy(value networking.NetworkPolicy) networking.NetworkPolicy {
	out := networking.NetworkPolicy{ObjectMeta: asIdentity(value.ObjectMeta)}
	out.Spec.PodSelector = *projectSelector(&value.Spec.PodSelector)
	for _, mode := range value.Spec.PolicyTypes {
		if mode == networking.PolicyTypeIngress || mode == networking.PolicyTypeEgress {
			out.Spec.PolicyTypes = append(out.Spec.PolicyTypes, mode)
		}
	}
	for _, rule := range value.Spec.Ingress {
		out.Spec.Ingress = append(out.Spec.Ingress, networking.NetworkPolicyIngressRule{
			From:  make([]networking.NetworkPolicyPeer, len(rule.From)),
			Ports: make([]networking.NetworkPolicyPort, len(rule.Ports)),
		})
	}
	for _, rule := range value.Spec.Egress {
		out.Spec.Egress = append(out.Spec.Egress, networking.NetworkPolicyEgressRule{
			To:    make([]networking.NetworkPolicyPeer, len(rule.To)),
			Ports: make([]networking.NetworkPolicyPort, len(rule.Ports)),
		})
	}
	return out
}

func projectPDB(value policy.PodDisruptionBudget) policy.PodDisruptionBudget {
	out := policy.PodDisruptionBudget{ObjectMeta: asIdentity(value.ObjectMeta)}
	out.Spec.Selector = projectSelector(value.Spec.Selector)
	out.Status.ObservedGeneration = value.Status.ObservedGeneration
	out.Status.DisruptionsAllowed = value.Status.DisruptionsAllowed
	out.Status.CurrentHealthy = value.Status.CurrentHealthy
	out.Status.DesiredHealthy = value.Status.DesiredHealthy
	out.Status.ExpectedPods = value.Status.ExpectedPods
	for _, condition := range value.Status.Conditions {
		if condition.Type == policy.DisruptionAllowedCondition {
			out.Status.Conditions = append(out.Status.Conditions, metav1.Condition{
				Type:               condition.Type,
				Status:             metav1.ConditionStatus(projectStatus(core.ConditionStatus(condition.Status))),
				ObservedGeneration: condition.ObservedGeneration,
				Reason:             allowed(condition.Reason, policy.SyncFailedReason, policy.InsufficientPodsReason, policy.SufficientPodsReason),
				LastTransitionTime: condition.LastTransitionTime,
			})
		}
	}
	return out
}

func projectMetrics(value metrics.PodMetrics) model.PodUsage {
	out := model.PodUsage{
		Namespace: name(value.Namespace), Name: name(value.Name),
		Timestamp: value.Timestamp.Time, Window: value.Window.Duration,
	}
	for _, container := range value.Containers {
		cpu, hasCPU := container.Usage[core.ResourceCPU]
		memory, hasMemory := container.Usage[core.ResourceMemory]
		if !hasCPU || !hasMemory {
			continue
		}
		out.Containers = append(out.Containers, model.ContainerUsage{
			Name: name(container.Name),
			CPU:  cpu.DeepCopy(), Memory: memory.DeepCopy(),
		})
	}
	return out
}

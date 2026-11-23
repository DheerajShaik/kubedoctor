package bundle

import (
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	policy "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type servicePort struct {
	Port    port   `json:"port"`
	Target  int32  `json:"target_number,omitempty"`
	Named   string `json:"target_name,omitempty"`
	IsNamed bool   `json:"target_is_named,omitempty"`
}

type service struct {
	Identity        identity      `json:"identity"`
	Type            string        `json:"type,omitempty"`
	Headless        bool          `json:"headless,omitempty"`
	Selector        labelSet      `json:"selector,omitempty"`
	PublishNotReady bool          `json:"publish_not_ready,omitempty"`
	Ports           []servicePort `json:"ports,omitempty"`
}

func describeService(s core.Service) service {
	return service{
		Identity: describe(s.ObjectMeta, false), Type: string(s.Spec.Type), Headless: s.Spec.ClusterIP == core.ClusterIPNone,
		Selector: s.Spec.Selector, PublishNotReady: s.Spec.PublishNotReadyAddresses,
		Ports: mapItems(s.Spec.Ports, func(p core.ServicePort) servicePort {
			out := servicePort{Port: port{Name: p.Name, Number: p.Port, Protocol: string(p.Protocol)}}
			if p.TargetPort.Type == intstr.String {
				out.Named, out.IsNamed = p.TargetPort.StrVal, true
			} else {
				out.Target = p.TargetPort.IntVal
			}
			return out
		}),
	}
}

func (s service) observation() core.Service {
	out := core.Service{ObjectMeta: s.Identity.metadata()}
	out.Spec.Type, out.Spec.Selector, out.Spec.PublishNotReadyAddresses = core.ServiceType(s.Type), s.Selector, s.PublishNotReady
	if s.Headless {
		out.Spec.ClusterIP = core.ClusterIPNone
	}
	out.Spec.Ports = mapItems(s.Ports, func(p servicePort) core.ServicePort {
		target := intstr.FromInt32(p.Target)
		if p.IsNamed {
			target = intstr.FromString(p.Named)
		}
		return core.ServicePort{Name: p.Port.Name, Port: p.Port.Number, Protocol: core.Protocol(p.Port.Protocol), TargetPort: target}
	})
	return out
}

type endpointPort struct {
	Name     *string `json:"name,omitempty"`
	Number   *int32  `json:"number,omitempty"`
	Protocol *string `json:"protocol,omitempty"`
}

type endpoint struct {
	AddressCount int             `json:"address_count"`
	Ready        *bool           `json:"ready,omitempty"`
	Serving      *bool           `json:"serving,omitempty"`
	Terminating  *bool           `json:"terminating,omitempty"`
	Target       *model.Resource `json:"target,omitempty"`
}

type endpointSlice struct {
	Identity    identity       `json:"identity"`
	ServiceName string         `json:"service_name,omitempty"`
	Managed     bool           `json:"built_in_controller,omitempty"`
	AddressType string         `json:"address_type,omitempty"`
	Ports       []endpointPort `json:"ports,omitempty"`
	Endpoints   []endpoint     `json:"endpoints,omitempty"`
}

const builtInEndpointController = "endpointslice-controller.k8s.io"

func describeEndpointSlice(s discovery.EndpointSlice) endpointSlice {
	return endpointSlice{
		Identity: describe(s.ObjectMeta, false), ServiceName: s.Labels[discovery.LabelServiceName],
		Managed: s.Labels[discovery.LabelManagedBy] == builtInEndpointController, AddressType: string(s.AddressType),
		Ports: mapItems(s.Ports, func(p discovery.EndpointPort) endpointPort {
			out := endpointPort{Name: p.Name, Number: p.Port}
			if p.Protocol != nil {
				out.Protocol = new(string(*p.Protocol))
			}
			return out
		}),
		Endpoints: mapItems(s.Endpoints, func(e discovery.Endpoint) endpoint {
			return endpoint{AddressCount: len(e.Addresses), Ready: e.Conditions.Ready, Serving: e.Conditions.Serving,
				Terminating: e.Conditions.Terminating, Target: describeRef(e.TargetRef, s.Namespace)}
		}),
	}
}

func (s endpointSlice) observation() discovery.EndpointSlice {
	out := discovery.EndpointSlice{ObjectMeta: s.Identity.metadata(), AddressType: discovery.AddressType(s.AddressType)}
	if s.ServiceName != "" {
		out.Labels = map[string]string{discovery.LabelServiceName: s.ServiceName}
		if s.Managed {
			out.Labels[discovery.LabelManagedBy] = builtInEndpointController
		}
	}
	out.Ports = mapItems(s.Ports, func(p endpointPort) discovery.EndpointPort {
		out := discovery.EndpointPort{Name: p.Name, Port: p.Number}
		if p.Protocol != nil {
			out.Protocol = new(core.Protocol(*p.Protocol))
		}
		return out
	})
	out.Endpoints = mapItems(s.Endpoints, func(e endpoint) discovery.Endpoint {
		return discovery.Endpoint{
			Addresses: make([]string, observedPresence(e.AddressCount)), TargetRef: materializeRef(e.Target),
			Conditions: discovery.EndpointConditions{Ready: e.Ready, Serving: e.Serving, Terminating: e.Terminating},
		}
	})
	return out
}

func describeRef(ref *core.ObjectReference, namespace string) *model.Resource {
	if ref == nil || ref.Name == "" || (ref.Namespace != "" && ref.Namespace != namespace) {
		return nil
	}
	return &model.Resource{Kind: ref.Kind, Name: ref.Name, Namespace: ref.Namespace, UID: string(ref.UID)}
}

func materializeRef(ref *model.Resource) *core.ObjectReference {
	if ref == nil {
		return nil
	}
	return &core.ObjectReference{Kind: ref.Kind, Name: ref.Name, Namespace: ref.Namespace, UID: types.UID(ref.UID)}
}

type legacySubset struct {
	Ready    []*model.Resource `json:"ready,omitempty"`
	NotReady []*model.Resource `json:"not_ready,omitempty"`
	Ports    []port            `json:"ports,omitempty"`
}

type legacyEndpoints struct {
	Identity  identity       `json:"identity"`
	Truncated bool           `json:"truncated,omitempty"`
	Subsets   []legacySubset `json:"subsets,omitempty"`
}

const endpointsCapacity = "endpoints.kubernetes.io/over-capacity"

func describeLegacy(s core.Endpoints) legacyEndpoints {
	address := func(a core.EndpointAddress) *model.Resource { return describeRef(a.TargetRef, s.Namespace) }
	return legacyEndpoints{
		Identity: describe(s.ObjectMeta, false), Truncated: s.Annotations[endpointsCapacity] == "truncated",
		Subsets: mapItems(s.Subsets, func(sub core.EndpointSubset) legacySubset {
			return legacySubset{
				Ready: mapItems(sub.Addresses, address), NotReady: mapItems(sub.NotReadyAddresses, address),
				Ports: mapItems(sub.Ports, func(p core.EndpointPort) port {
					return port{Name: p.Name, Number: p.Port, Protocol: string(p.Protocol)}
				}),
			}
		}),
	}
}

func (s legacyEndpoints) observation() core.Endpoints {
	out := core.Endpoints{ObjectMeta: s.Identity.metadata()}
	if s.Truncated {
		out.Annotations = map[string]string{endpointsCapacity: "truncated"}
	}
	address := func(ref *model.Resource) core.EndpointAddress {
		return core.EndpointAddress{TargetRef: materializeRef(ref)}
	}
	out.Subsets = mapItems(s.Subsets, func(sub legacySubset) core.EndpointSubset {
		return core.EndpointSubset{
			Addresses: mapItems(sub.Ready, address), NotReadyAddresses: mapItems(sub.NotReady, address),
			Ports: mapItems(sub.Ports, func(p port) core.EndpointPort {
				return core.EndpointPort{Name: p.Name, Port: p.Number, Protocol: core.Protocol(p.Protocol)}
			}),
		}
	})
	return out
}

type ingressBackend struct {
	Service      string `json:"service,omitempty"`
	PortName     string `json:"port_name,omitempty"`
	PortNumber   int32  `json:"port_number,omitempty"`
	ResourceKind string `json:"resource_kind,omitempty"`
	ResourceName string `json:"resource_name,omitempty"`
}

type ingress struct {
	Identity identity           `json:"identity"`
	Default  *ingressBackend    `json:"default,omitempty"`
	Rules    [][]ingressBackend `json:"rules,omitempty"`
}

func describeIngress(s networking.Ingress) ingress {
	out := ingress{Identity: describe(s.ObjectMeta, false)}
	if s.Spec.DefaultBackend != nil {
		out.Default = new(describeIngressBackend(*s.Spec.DefaultBackend))
	}
	for _, rule := range s.Spec.Rules {
		if rule.HTTP != nil {
			out.Rules = append(out.Rules, mapItems(rule.HTTP.Paths, func(p networking.HTTPIngressPath) ingressBackend {
				return describeIngressBackend(p.Backend)
			}))
		}
	}
	return out
}

func describeIngressBackend(b networking.IngressBackend) ingressBackend {
	var out ingressBackend
	if b.Service != nil {
		out.Service, out.PortName, out.PortNumber = b.Service.Name, b.Service.Port.Name, b.Service.Port.Number
	}
	if b.Resource != nil {
		out.ResourceKind, out.ResourceName = b.Resource.Kind, b.Resource.Name
	}
	return out
}

func (i ingress) observation() networking.Ingress {
	out := networking.Ingress{ObjectMeta: i.Identity.metadata()}
	if i.Default != nil {
		out.Spec.DefaultBackend = new(i.Default.observation())
	}
	out.Spec.Rules = mapItems(i.Rules, func(paths []ingressBackend) networking.IngressRule {
		rule := networking.HTTPIngressRuleValue{Paths: mapItems(paths, func(b ingressBackend) networking.HTTPIngressPath {
			return networking.HTTPIngressPath{Backend: b.observation()}
		})}
		return networking.IngressRule{IngressRuleValue: networking.IngressRuleValue{HTTP: &rule}}
	})
	return out
}

func (b ingressBackend) observation() networking.IngressBackend {
	var out networking.IngressBackend
	if b.Service != "" {
		out.Service = &networking.IngressServiceBackend{Name: b.Service,
			Port: networking.ServiceBackendPort{Name: b.PortName, Number: b.PortNumber}}
	}
	if b.ResourceKind != "" || b.ResourceName != "" {
		out.Resource = &core.TypedLocalObjectReference{Kind: b.ResourceKind, Name: b.ResourceName}
	}
	return out
}

type networkRule struct {
	Peers      int    `json:"peers,omitempty"`
	Ports      int    `json:"ports,omitempty"`
	NamedPorts []port `json:"named_ports,omitempty"`
}

type networkPolicy struct {
	Identity identity      `json:"identity"`
	Selector *selector     `json:"selector,omitempty"`
	Types    []string      `json:"types,omitempty"`
	Ingress  []networkRule `json:"ingress,omitempty"`
	Egress   []networkRule `json:"egress,omitempty"`
}

func describeNetworkPolicy(p networking.NetworkPolicy) networkPolicy {
	return networkPolicy{
		Identity: describe(p.ObjectMeta, false), Selector: describeSelector(&p.Spec.PodSelector),
		Types: mapItems(p.Spec.PolicyTypes, func(p networking.PolicyType) string { return string(p) }),
		Ingress: mapItems(p.Spec.Ingress, func(rule networking.NetworkPolicyIngressRule) networkRule {
			return describePolicyRule(len(rule.From), rule.Ports)
		}),
		Egress: mapItems(p.Spec.Egress, func(rule networking.NetworkPolicyEgressRule) networkRule {
			return describePolicyRule(len(rule.To), rule.Ports)
		}),
	}
}

func describePolicyRule(peers int, ports []networking.NetworkPolicyPort) networkRule {
	out := networkRule{Peers: peers, Ports: len(ports)}
	for _, p := range ports {
		if p.Port == nil || p.Port.Type != intstr.String || p.Port.StrVal == "" {
			continue
		}
		named := port{Name: p.Port.StrVal}
		if p.Protocol != nil {
			named.Protocol = string(*p.Protocol)
		}
		out.NamedPorts = append(out.NamedPorts, named)
	}
	return out
}

func (r networkRule) observationPorts() []networking.NetworkPolicyPort {
	out := mapItems(r.NamedPorts, func(p port) networking.NetworkPolicyPort {
		port := networking.NetworkPolicyPort{Port: new(intstr.FromString(p.Name))}
		if p.Protocol != "" {
			port.Protocol = new(core.Protocol(p.Protocol))
		}
		return port
	})
	if len(out) < r.Ports {
		out = append(out, networking.NetworkPolicyPort{})
	}
	return out
}

func (p networkPolicy) observation() networking.NetworkPolicy {
	out := networking.NetworkPolicy{ObjectMeta: p.Identity.metadata()}
	if s := p.Selector.meta(); s != nil {
		out.Spec.PodSelector = *s
	}
	out.Spec.PolicyTypes = mapItems(p.Types, func(t string) networking.PolicyType { return networking.PolicyType(t) })
	out.Spec.Ingress = mapItems(p.Ingress, func(rule networkRule) networking.NetworkPolicyIngressRule {
		return networking.NetworkPolicyIngressRule{
			From: make([]networking.NetworkPolicyPeer, observedPresence(rule.Peers)), Ports: rule.observationPorts(),
		}
	})
	out.Spec.Egress = mapItems(p.Egress, func(rule networkRule) networking.NetworkPolicyEgressRule {
		return networking.NetworkPolicyEgressRule{
			To: make([]networking.NetworkPolicyPeer, observedPresence(rule.Peers)), Ports: rule.observationPorts(),
		}
	})
	return out
}

type pdb struct {
	Identity           identity    `json:"identity"`
	Selector           *selector   `json:"selector,omitempty"`
	ObservedGeneration int64       `json:"observed_generation,omitempty"`
	Allowed            int32       `json:"allowed"`
	Healthy            int32       `json:"healthy,omitempty"`
	Desired            int32       `json:"desired_healthy,omitempty"`
	Expected           int32       `json:"expected_pods,omitempty"`
	Conditions         []condition `json:"conditions,omitempty"`
}

func describePDB(p policy.PodDisruptionBudget) pdb {
	return pdb{
		Identity: describe(p.ObjectMeta, false), Selector: describeSelector(p.Spec.Selector),
		ObservedGeneration: p.Status.ObservedGeneration, Allowed: p.Status.DisruptionsAllowed, Healthy: p.Status.CurrentHealthy,
		Desired: p.Status.DesiredHealthy, Expected: p.Status.ExpectedPods,
		Conditions: mapItems(p.Status.Conditions, func(c metav1.Condition) condition {
			return condition{Type: c.Type, Status: string(c.Status), Reason: c.Reason,
				ObservedGeneration: c.ObservedGeneration, TransitionAt: timestamp(c.LastTransitionTime.Time)}
		}),
	}
}

func (p pdb) observation() policy.PodDisruptionBudget {
	out := policy.PodDisruptionBudget{ObjectMeta: p.Identity.metadata()}
	out.Spec.Selector = p.Selector.meta()
	out.Status.ObservedGeneration, out.Status.DisruptionsAllowed = p.ObservedGeneration, p.Allowed
	out.Status.CurrentHealthy, out.Status.DesiredHealthy, out.Status.ExpectedPods = p.Healthy, p.Desired, p.Expected
	out.Status.Conditions = mapItems(p.Conditions, func(c condition) metav1.Condition {
		return metav1.Condition{Type: c.Type, Status: metav1.ConditionStatus(c.Status), Reason: c.Reason,
			ObservedGeneration: c.ObservedGeneration, LastTransitionTime: metaTime(c.TransitionAt)}
	})
	return out
}

type dnsConfig struct {
	UID         string `json:"uid,omitempty"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	HasCorefile bool   `json:"has_corefile"`
}

func describeDNSConfig(c model.DNSConfig) dnsConfig {
	return dnsConfig{UID: c.UID, Namespace: c.Namespace, Name: c.Name, HasCorefile: c.HasCorefile}
}

func (c dnsConfig) observation() model.DNSConfig {
	return model.DNSConfig{UID: c.UID, Namespace: c.Namespace, Name: c.Name, HasCorefile: c.HasCorefile}
}

// Verify an endpoint state has an unambiguous normalized time representation.
func validTimestamp(t *time.Time) bool {
	return t == nil || (!t.IsZero() && t.Year() >= 1 && t.Year() <= 9999)
}

func observedPresence(count int) int {
	if count > 0 {
		return 1
	}
	return 0
}

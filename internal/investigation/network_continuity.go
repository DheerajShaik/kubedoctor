package investigation

import (
	"maps"
	"slices"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const networkContinuityWorkLimit = 250_000

type networkContinuity struct {
	policyPorts map[model.Resource]map[namedIngressPort]bool
	dns         *dnsPodContinuity
}

type namedIngressPort struct {
	name     string
	protocol core.Protocol
}

type dnsPodContinuity struct {
	resource        model.Resource
	selector        map[string]string
	publishNotReady bool
	selectedReady   bool
}

func networkComparisonEvidence(s model.Snapshot) networkContinuity {
	return networkContinuity{policyPorts: observedPolicyPorts(s), dns: observedDNSPods(s)}
}

// Only positive observations are recorded. A vanished port, an empty Pod
// selection, and an exhausted budget cannot serve as recovery evidence.
func observedPolicyPorts(s model.Snapshot) map[model.Resource]map[namedIngressPort]bool {
	if s.Coverage(model.SourceNetworkPolicies) != model.Complete || s.Coverage(model.SourcePods) != model.Complete {
		return nil
	}
	active := make(map[string][]*core.Pod)
	for i := range s.Pods {
		pod := &s.Pods[i]
		if pod.DeletionTimestamp == nil && pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
			active[pod.Namespace] = append(active[pod.Namespace], pod)
		}
	}
	result := make(map[model.Resource]map[namedIngressPort]bool)
	remaining := networkContinuityWorkLimit
	for i := range s.NetworkPolicies {
		policy := &s.NetworkPolicies[i]
		pods := active[policy.Namespace]
		if len(pods) == 0 || policy.DeletionTimestamp != nil ||
			len(policy.Spec.PolicyTypes) != 0 && !slices.Contains(policy.Spec.PolicyTypes, networking.PolicyTypeIngress) {
			continue
		}
		ports := make(map[namedIngressPort]bool)
		for _, rule := range policy.Spec.Ingress {
			remaining -= 1 + len(rule.Ports)
			if remaining < 0 {
				return result
			}
			for _, p := range rule.Ports {
				if p.Port == nil || p.Port.Type != intstr.String || p.Port.StrVal == "" {
					continue
				}
				protocol := core.ProtocolTCP
				if p.Protocol != nil {
					protocol = *p.Protocol
				}
				ports[namedIngressPort{p.Port.StrVal, protocol}] = false
			}
		}
		if len(ports) == 0 {
			continue
		}
		work := 1 + len(policy.Spec.PodSelector.MatchLabels) + len(policy.Spec.PodSelector.MatchExpressions)
		remaining -= work
		for _, expression := range policy.Spec.PodSelector.MatchExpressions {
			remaining -= len(expression.Values)
		}
		if remaining < 0 {
			return result
		}
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if err != nil {
			continue
		}
		result[model.Identity("NetworkPolicy", policy)] = ports
		for _, pod := range pods {
			remaining -= work
			if remaining < 0 {
				return result
			}
			if selector.Matches(labels.Set(pod.Labels)) {
				remaining = observeNamedPorts(pod, ports, remaining)
				if remaining < 0 {
					return result
				}
			}
		}
	}
	return result
}

func observeNamedPorts(pod *core.Pod, observed map[namedIngressPort]bool, remaining int) int {
	for _, group := range []struct {
		containers []core.Container
		isInit     bool
	}{{pod.Spec.Containers, false}, {pod.Spec.InitContainers, true}} {
		for _, container := range group.containers {
			remaining--
			if remaining < 0 {
				return remaining
			}
			if group.isInit && (container.RestartPolicy == nil || *container.RestartPolicy != core.ContainerRestartPolicyAlways) {
				continue
			}
			remaining -= len(container.Ports)
			if remaining < 0 {
				return remaining
			}
			for _, port := range container.Ports {
				protocol := port.Protocol
				if protocol == "" {
					protocol = core.ProtocolTCP
				}
				key := namedIngressPort{port.Name, protocol}
				if _, declared := observed[key]; declared && port.ContainerPort > 0 {
					observed[key] = true
				}
			}
		}
	}
	return remaining
}

func observedDNSPods(s model.Snapshot) *dnsPodContinuity {
	if s.Coverage(model.SourceDNSServices) != model.Complete || s.Coverage(model.SourceDNSPods) != model.Complete {
		return nil
	}
	for i := range s.DNSServices {
		service := &s.DNSServices[i]
		if service.Namespace != "kube-system" || service.Name != "kube-dns" || service.DeletionTimestamp != nil ||
			service.Spec.Type == core.ServiceTypeExternalName {
			continue
		}
		facts := dnsPodContinuity{resource: model.Identity("Service", service), selector: maps.Clone(service.Spec.Selector),
			publishNotReady: service.Spec.PublishNotReadyAddresses}
		if len(facts.selector) == 0 {
			return &facts
		}
		selector := labels.SelectorFromSet(facts.selector)
		remaining := networkContinuityWorkLimit
		for i := range s.DNSPods {
			pod := &s.DNSPods[i]
			remaining -= 1 + len(service.Spec.Selector) + len(pod.Status.Conditions)
			if remaining < 0 {
				break
			}
			if pod.Namespace != service.Namespace || pod.DeletionTimestamp != nil || pod.Status.Phase != core.PodRunning ||
				!selector.Matches(labels.Set(pod.Labels)) {
				continue
			}
			count, ready := 0, false
			for _, c := range pod.Status.Conditions {
				if c.Type == core.PodReady {
					count++
					ready = c.Status == core.ConditionTrue
				}
			}
			if count == 1 && ready {
				facts.selectedReady = true
				break
			}
		}
		return &facts
	}
	return nil
}

func networkRecoveryEvidence(left, right compareSnapshot, finding model.Finding) (handled, current bool) {
	id := finding.Resource
	previous, next := left.inv.applicability.network, right.inv.applicability.network
	switch finding.RuleID {
	case "networkpolicy-named-ports":
		port := namedIngressPort{evidenceValue(finding, "named_port"), core.Protocol(evidenceValue(finding, "protocol"))}
		return true, next.policyPorts[id][port]
	case "dns.infrastructure":
		podReadiness := evidenceValue(finding, "inspected_ready_dns_pods") == "0"
		backendReadiness := evidenceValue(finding, "usable_backend_entries") == "0" || evidenceValue(finding, "draining_entries") != ""
		if !podReadiness && !backendReadiness {
			return false, false
		}
		before, after := previous.dns, next.dns
		if before == nil || after == nil || before.resource != id || after.resource != id {
			return true, false
		}
		if !podReadiness && !before.publishNotReady && !after.publishNotReady {
			return false, false
		}
		return true, maps.Equal(before.selector, after.selector) && after.selectedReady &&
			(podReadiness || right.inventory[id].Ready)
	default:
		return false, false
	}
}

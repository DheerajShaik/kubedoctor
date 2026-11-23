package analyze

import (
	"maps"
	"slices"
	"strconv"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	policy "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func analyzeIngressBackends(s model.Snapshot, services map[networkKey]*core.Service) ([]model.Finding, model.CheckResult) {
	check := model.CheckResult{ID: ingressBackendsID, Status: model.Checked}
	ingressesComplete := s.Coverage(model.SourceIngresses) == model.Complete
	servicesComplete := s.Coverage(model.SourceServices) == model.Complete
	if !ingressesComplete || (len(s.Ingresses) > 0 && !servicesComplete) {
		check.Status = model.Inconclusive
		check.Detail = "Ingress or Service inventory is incomplete."
	} else if len(s.Ingresses) == 0 {
		check.Status = model.Skipped
		check.Detail = "No Ingresses in this scope."
	}

	var findings []model.Finding
	for i := range s.Ingresses {
		ingress := &s.Ingresses[i]
		var backends []networking.IngressBackend
		if ingress.Spec.DefaultBackend != nil {
			backends = append(backends, *ingress.Spec.DefaultBackend)
		}
		for _, rule := range ingress.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				backends = append(backends, path.Backend)
			}
		}
		seen := make(map[networking.IngressServiceBackend]bool)
		missingServices := make(map[string]bool)
		for _, backend := range backends {
			if backend.Service == nil {
				check.Status = model.Inconclusive
				check.Detail = "One or more non-Service Ingress backend references are outside this check."
				continue
			}
			reference := *backend.Service
			if seen[reference] {
				continue
			}
			seen[reference] = true
			service := services[networkKey{ingress.Namespace, reference.Name}]
			if service == nil {
				if servicesComplete && !missingServices[reference.Name] {
					missingServices[reference.Name] = true
					findings = append(findings, networkFinding(ingressBackendsID, "Ingress", ingress, model.Warning, model.Observed,
						"An Ingress backend references a Service absent from its namespace.",
						[]model.Evidence{{Field: "backend_service", Value: reference.Name}},
						"Check the backend Service name and namespace because the reference resolves within the Ingress namespace."))
				}
				continue
			}
			matchesPort := func(port core.ServicePort) bool {
				if reference.Port.Name != "" {
					return reference.Port.Name == port.Name
				}
				return reference.Port.Number == port.Port
			}
			port := reference.Port.Name
			if port == "" {
				port = strconv.Itoa(int(reference.Port.Number))
			}
			evidence := []model.Evidence{{Field: "backend_service", Value: reference.Name}, {Field: "backend_port", Value: port}}
			if !slices.ContainsFunc(service.Spec.Ports, matchesPort) {
				findings = append(findings, networkFinding(ingressBackendsID, "Ingress", ingress, model.Warning, model.Observed,
					"An Ingress backend references a port absent from the Service.", evidence,
					"Compare the referenced port with the Service port name or exposed number. The targetPort is the backend mapping."))
			} else if !slices.ContainsFunc(service.Spec.Ports, func(port core.ServicePort) bool {
				return matchesPort(port) && networkProtocol(port.Protocol) == core.ProtocolTCP
			}) {
				findings = append(findings, networkFinding(ingressBackendsID, "Ingress", ingress, model.Warning, model.Observed,
					"The referenced Service port has no TCP declaration for an HTTP Ingress backend.", evidence,
					"Confirm the backend declares a TCP Service port that can serve the Ingress HTTP request."))
			}
		}
	}
	return findings, check
}

const (
	dnsNamespace = "kube-system"
	dnsService   = "kube-dns"
)

func analyzeDNS(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := model.CheckResult{ID: dnsID, Status: model.Checked, Detail: "Inspects conventional DNS infrastructure. Workload name resolution requires a runtime observation from the workload."}
	findings := dnsConfigurationFindings(s.DNSConfig)
	service := networkObservedDNSService(s)
	if service == nil {
		check.Status = model.Inconclusive
		check.Detail = "The conventional kube-system/kube-dns Service could not be inspected. The cluster may use another DNS configuration."
		return findings, check
	}

	missingPorts := dnsMissingStandardPorts(service)
	if len(missingPorts) > 0 {
		findings = append(findings, networkFinding(dnsID, "Service", service, model.Warning, model.Observed,
			"The conventional DNS Service lacks one or more standard port declarations.", missingPorts,
			"Check the DNS add-on configuration for both UDP and TCP port 53, including whether this cluster uses an alternate DNS service."))
	}

	inventory := networkBackendSource(s, true)
	groups, complete := inventory.forService(service)
	ready, draining, observed := networkEndpointCounts(groups)
	if !complete || networkIsFresh(service.CreationTimestamp.Time, s.CollectedAt) {
		check.Status = model.Inconclusive
		check.Detail = "Conventional DNS endpoint visibility is incomplete or the Service was created recently."
	} else if ready+draining == 0 {
		findings = append(findings, networkFinding(dnsID, "Service", service, model.Warning, model.Observed,
			"No ready or draining, serving endpoints were observed for the conventional DNS Service.",
			[]model.Evidence{{Field: "published_backend_entries", Value: strconv.Itoa(observed)}, {Field: "usable_backend_entries", Value: "0"}},
			"Compare the kube-dns Service selector, DNS workload readiness, and the published endpoints for the conventional DNS backend."))
	} else {
		if ready == 0 {
			findings = append(findings, networkFinding(dnsID, "Service", service, model.Info, model.Observed,
				"Only terminating endpoint entries remain eligible by their published conditions for the conventional DNS Service.",
				[]model.Evidence{{Field: "draining_entries", Value: strconv.Itoa(draining)}},
				"Check the DNS workload's rollout progress because the published endpoints are terminating."))
		}
		for _, port := range service.Spec.Ports {
			if port.Port != 53 {
				continue
			}
			switch networkBackendPortCoverage(groups, port, networkCheckNumericTarget(service)) {
			case model.Unavailable:
				findings = append(findings, networkFinding(dnsID, "Service", service, model.Warning, model.Observed,
					"Usable conventional DNS backend entries do not publish a matching DNS Service port.", networkServicePortEvidence(port),
					"Compare the DNS Service protocol and target port with the endpoint port mappings."))
			case model.NotInspected:
				check.Status = model.Inconclusive
				check.Detail = "Conventional DNS backend ports include unspecified values."
			}
		}
	}

	// Ready is a publication signal for these Services, so use Pod status for readiness evidence.
	if service.Spec.PublishNotReadyAddresses {
		selected, selectedReady := dnsSelectedPods(service, s.DNSPods)
		if selected > 0 && selectedReady == 0 && s.Coverage(model.SourceDNSPods) == model.Complete {
			findings = append(findings, networkFinding(dnsID, "Service", service, model.Warning, model.Observed,
				"The conventional DNS Service publishes unready addresses and no inspected selected DNS Pod was Ready.",
				[]model.Evidence{{Field: "inspected_active_dns_pods", Value: strconv.Itoa(selected)}, {Field: "inspected_ready_dns_pods", Value: "0"}},
				"Check readiness of the selected DNS Pods because published endpoints can precede workload readiness."))
		} else if selectedReady == 0 {
			check.Status = model.Inconclusive
			check.Detail = "The DNS Service publishes unready addresses and selected DNS Pod readiness is insufficiently observed."
		}
	}

	if s.Coverage(model.SourceDNSConfig) != model.Complete || s.Coverage(model.SourceDNSPods) != model.Complete {
		check.Status = model.Inconclusive
		if complete {
			check.Detail = "The DNS Service and endpoints were inspected with limited DNS Pod or CoreDNS configuration visibility."
		}
	}
	if inventory.legacy && check.Status == model.Checked {
		check.Detail += " Legacy Endpoints provide reduced readiness and termination detail."
	}
	return findings, check
}

func dnsConfigurationFindings(configs []model.DNSConfig) []model.Finding {
	var findings []model.Finding
	for _, config := range configs {
		if config.Namespace == dnsNamespace && config.Name == "coredns" && !config.HasCorefile {
			identity := &metav1.ObjectMeta{Namespace: config.Namespace, Name: config.Name}
			findings = append(findings, networkFinding(dnsID, "ConfigMap", identity, model.Info, model.Hypothesis,
				"The conventional CoreDNS ConfigMap lacks the Corefile key.",
				[]model.Evidence{{Field: "corefile_key_present", Value: "false"}},
				"Confirm the running DNS deployment's configuration source. The conventional CoreDNS mount reads a key named Corefile."))
		}
	}
	return findings
}

func networkObservedDNSService(s model.Snapshot) *core.Service {
	if s.Coverage(model.SourceDNSServices) != model.Complete {
		return nil
	}
	for i := range s.DNSServices {
		service := &s.DNSServices[i]
		if service.Namespace == dnsNamespace && service.Name == dnsService && service.Spec.Type != core.ServiceTypeExternalName {
			return service
		}
	}
	return nil
}

func dnsMissingStandardPorts(service *core.Service) []model.Evidence {
	var evidence []model.Evidence
	for _, protocol := range []core.Protocol{core.ProtocolUDP, core.ProtocolTCP} {
		if !slices.ContainsFunc(service.Spec.Ports, func(port core.ServicePort) bool {
			return port.Port == 53 && networkProtocol(port.Protocol) == protocol
		}) {
			evidence = append(evidence, model.Evidence{Field: "missing_standard_port", Value: "53/" + string(protocol)})
		}
	}
	return evidence
}

func dnsSelectedPods(service *core.Service, pods []core.Pod) (selected, ready int) {
	if len(service.Spec.Selector) == 0 {
		return 0, 0
	}
	selector := labels.SelectorFromSet(service.Spec.Selector)
	for i := range pods {
		pod := &pods[i]
		if pod.Namespace == service.Namespace && selector.Matches(labels.Set(pod.Labels)) && networkActivePod(pod) {
			selected++
			if networkPodReady(pod) {
				ready++
			}
		}
	}
	return selected, ready
}

type networkPolicyObservation struct {
	selector                  labels.Selector
	ingress, egress           bool
	ingressRules, egressRules int
}

type policyDirectionObservation struct {
	selected, hasRules bool
}

func analyzeNetworkPolicies(s model.Snapshot, pods map[string][]*core.Pod) ([]model.Finding, model.CheckResult) {
	check := model.CheckResult{ID: networkPolicyID, Status: model.Checked, Detail: "Static policy selection only. The networking implementation and live traffic are outside this check."}
	if s.Coverage(model.SourceNetworkPolicies) != model.Complete || s.Coverage(model.SourcePods) != model.Complete {
		check.Status = model.Inconclusive
		check.Detail = "NetworkPolicy or Pod inventory is incomplete. Additive rules cannot be evaluated reliably."
		return nil, check
	}
	if len(s.NetworkPolicies) == 0 {
		check.Status = model.Skipped
		check.Detail = "No Kubernetes NetworkPolicies in this scope."
		return nil, check
	}

	policies := make(map[string][]networkPolicyObservation)
	invalid := make(map[string]bool)
	for _, item := range s.NetworkPolicies {
		selector, err := metav1.LabelSelectorAsSelector(&item.Spec.PodSelector)
		if err != nil {
			invalid[item.Namespace] = true
			check.Status = model.Inconclusive
			check.Detail = "An invalid policy selector prevented additive evaluation in its namespace."
			continue
		}
		ingress, egress := policyDirections(item.Spec)
		policies[item.Namespace] = append(policies[item.Namespace], networkPolicyObservation{
			selector: selector, ingress: ingress, egress: egress,
			ingressRules: len(item.Spec.Ingress), egressRules: len(item.Spec.Egress),
		})
	}

	var findings []model.Finding
	for _, namespace := range slices.Sorted(maps.Keys(policies)) {
		namespacePolicies := policies[namespace]
		if invalid[namespace] {
			continue
		}
		var noIngress, noEgress int
		for _, pod := range pods[namespace] {
			if !networkActivePod(pod) || pod.Spec.HostNetwork {
				continue
			}
			var ingress, egress policyDirectionObservation
			for _, item := range namespacePolicies {
				if !item.selector.Matches(labels.Set(pod.Labels)) {
					continue
				}
				if item.ingress {
					ingress.selected = true
					ingress.hasRules = ingress.hasRules || item.ingressRules > 0
				}
				if item.egress {
					egress.selected = true
					egress.hasRules = egress.hasRules || item.egressRules > 0
				}
			}
			if ingress.selected && !ingress.hasRules {
				noIngress++
			}
			if egress.selected && !egress.hasRules {
				noEgress++
			}
		}
		for _, result := range []struct {
			direction string
			count     int
		}{{"ingress", noIngress}, {"egress", noEgress}} {
			if result.count == 0 {
				continue
			}
			identity := &metav1.ObjectMeta{Name: namespace}
			findings = append(findings, networkFinding(networkPolicyID, "Namespace", identity, model.Info, model.Hypothesis,
				"One or more Pods are selected by policies with no combined allow rules in this direction.",
				[]model.Evidence{{Field: "direction", Value: result.direction}, {Field: "selected_pods_without_allow_rules", Value: strconv.Itoa(result.count)}},
				"Confirm whether this isolation is intended and whether the networking implementation enforces these policies. Review intended traffic, including DNS for egress."))
		}
	}
	return findings, check
}

func policyDirections(spec networking.NetworkPolicySpec) (ingress, egress bool) {
	if len(spec.PolicyTypes) == 0 {
		return true, len(spec.Egress) > 0
	}
	return slices.Contains(spec.PolicyTypes, networking.PolicyTypeIngress), slices.Contains(spec.PolicyTypes, networking.PolicyTypeEgress)
}

func analyzePDBs(s model.Snapshot) ([]model.Finding, model.CheckResult) {
	check := model.CheckResult{ID: pdbID, Status: model.Checked}
	if s.Coverage(model.SourcePDBs) != model.Complete {
		check.Status = model.Inconclusive
		check.Detail = "PodDisruptionBudget inventory is incomplete."
	} else if len(s.PDBs) == 0 {
		check.Status = model.Skipped
		check.Detail = "No PodDisruptionBudgets in this scope."
	}
	var findings []model.Finding
	for i := range s.PDBs {
		pdb := &s.PDBs[i]
		status := pdb.Status
		if pdb.Generation <= 0 || pdb.Generation != status.ObservedGeneration || slices.ContainsFunc(status.Conditions, func(condition metav1.Condition) bool {
			return condition.Type == policy.DisruptionAllowedCondition && condition.Reason == policy.SyncFailedReason
		}) {
			check.Status = model.Inconclusive
			check.Detail = "One or more PodDisruptionBudget status observations are stale or failed to reconcile."
			continue
		}
		if status.ExpectedPods == 0 || status.DisruptionsAllowed > 0 {
			continue
		}
		evidence := []model.Evidence{
			{Field: "disruptions_allowed", Value: strconv.Itoa(int(status.DisruptionsAllowed))},
			{Field: "current_healthy", Value: strconv.Itoa(int(status.CurrentHealthy))},
			{Field: "desired_healthy", Value: strconv.Itoa(int(status.DesiredHealthy))},
			{Field: "expected_pods", Value: strconv.Itoa(int(status.ExpectedPods))},
		}
		findings = append(findings, networkFinding(pdbID, "PodDisruptionBudget", pdb, model.Info, model.Observed,
			"The current budget permits zero voluntary disruptions for its covered workload.", evidence,
			"Before draining nodes, confirm whether zero voluntary evictions is intentional and whether enough replicas are ready to satisfy the budget."))
	}
	return findings, check
}

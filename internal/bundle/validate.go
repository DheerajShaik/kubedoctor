package bundle

import (
	"encoding/hex"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func validateEvidence(p *payload) error {
	if !validScope(p.Scope) || !safeText(p.Producer, 160) || p.Producer == "" ||
		!simpleToken(p.AnalyzerVersion, 64) || p.AnalyzerVersion == "" ||
		p.StartedAt.IsZero() || p.FinishedAt.IsZero() || p.EvaluatedAt.IsZero() ||
		p.StartedAt.After(p.EvaluatedAt) || p.EvaluatedAt.After(p.FinishedAt) ||
		p.FinishedAt.Sub(p.StartedAt) > 24*time.Hour || !validProvenance(p.Scope, p.ClusterID, p.ClusterProvenance) ||
		!validSources(p.Sources) {
		return ErrFormat
	}
	for _, timing := range p.SourceTimings {
		if !knownSource(timing.Source) || !simpleToken(timing.Resource, 253) ||
			timing.StartedAt.Before(p.StartedAt) || timing.FinishedAt.Before(timing.StartedAt) ||
			timing.FinishedAt.After(p.FinishedAt) {
			return ErrFormat
		}
	}
	total := 0
	for _, count := range p.Evidence.counts() {
		total += count
		if count > maxResources || total > maxResources {
			return ErrLimit
		}
	}

	e := p.Evidence
	namespace := func(id identity) bool {
		return validIdentity(id) && namespaceInScope(p.Scope, id.Namespace)
	}
	global := func(id identity) bool {
		return validIdentity(id) && id.Namespace == ""
	}
	system := func(id identity) bool {
		return validIdentity(id) && id.Namespace == "kube-system"
	}
	if !uniqueEvidence(e) {
		return ErrFormat
	}
	hosts := make(map[string]bool)
	claims := make(map[string]bool)
	referenced := make(map[configMapKey]map[string]bool)
	for _, pod := range e.Pods {
		if !namespace(pod.Identity) || !validPod(pod) || pod.DNSNamedPorts != nil {
			return ErrFormat
		}
		if shouldContainNode(pod) {
			hosts[pod.HostNode] = true
		}
		rememberKeys(referenced, pod)
	}
	for _, class := range e.Claims {
		if !namespace(class.Identity) || !validClaim(class) {
			return ErrFormat
		}
		if class.Class != nil && class.Phase == "Pending" && class.Identity.DeletedAt == nil {
			claims[*class.Class] = true
		}
	}
	for _, n := range e.Nodes {
		if !global(n.Identity) || !validConditions(n.Conditions) || (isNamespaceScope(p.Scope) && !hosts[n.Identity.Name]) {
			return ErrFormat
		}
	}
	for _, class := range e.StorageClasses {
		if !global(class.Identity) || !oneOf(class.Binding, "", "Immediate", "WaitForFirstConsumer") ||
			(isNamespaceScope(p.Scope) && !claims[class.Identity.Name]) {
			return ErrFormat
		}
	}
	for _, source := range p.Sources {
		if !knownCoverageResource(p.Scope, source.Source, source.Resource, hosts, claims) {
			return ErrFormat
		}
	}
	for _, timing := range p.SourceTimings {
		if !knownCoverageResource(p.Scope, timing.Source, timing.Resource, hosts, claims) {
			return ErrFormat
		}
	}
	for _, cm := range e.ConfigMaps {
		keys := referenced[configMapKey{cm.Namespace, cm.Name}]
		if keys == nil || !namespaceInScope(p.Scope, cm.Namespace) || !validName(cm.Name) || !simpleToken(cm.UID, 253) ||
			!all(cm.Keys, func(key string) bool { return keys[key] }) ||
			!all(cm.Binary, func(key string) bool { return keys[key] }) {
			return ErrFormat
		}
	}
	if !all(e.ReplicaSets, func(r replicaSet) bool {
		return namespace(r.Identity) && r.ObservedGeneration >= 0 && r.Ready >= 0 && (r.Desired == nil || *r.Desired >= 0)
	}) || !all(e.Deployments, func(r deployment) bool {
		return namespace(r.Identity) && validLabels(r.TemplateLabels) && validConditions(r.Conditions)
	}) || !all(e.DaemonSets, func(r daemonSet) bool {
		return namespace(r.Identity) && validLabels(r.TemplateLabels) &&
			oneOf(r.Strategy, "", "OnDelete", "RollingUpdate")
	}) || !all(e.Services, func(r service) bool { return namespace(r.Identity) && validService(r) }) ||
		!all(e.EndpointSlices, func(r endpointSlice) bool { return namespace(r.Identity) && validSlice(r) }) ||
		!all(e.LegacyEndpoints, func(r legacyEndpoints) bool { return namespace(r.Identity) && validLegacy(r) }) ||
		!all(e.Ingresses, func(r ingress) bool { return namespace(r.Identity) && validIngress(r) }) ||
		!all(e.PDBs, func(r pdb) bool {
			return namespace(r.Identity) && validSelector(r.Selector) && validConditions(r.Conditions)
		}) ||
		!all(e.NetworkPolicies, func(r networkPolicy) bool { return namespace(r.Identity) && validNetworkPolicy(r) }) ||
		!all(e.Events, func(r classifiedEvent) bool { return validEvent(p.Scope, hosts, r) }) ||
		!all(e.Metrics, func(r currentUsage) bool { return namespaceInScope(p.Scope, r.Namespace) && validMetrics(r) }) {
		return ErrFormat
	}
	dnsPorts := namedDNSPortRequirements(e.DNSServices)
	for _, pod := range e.DNSPods {
		if !system(pod.Identity) || !podsAreDNSInfrastructure(pod.Identity.Namespace, pod.Identity.Labels) ||
			!validPod(pod) || pod.HostNode != "" || len(pod.Containers) != 0 || len(pod.Init) != 0 ||
			len(pod.Volumes) != 0 || len(pod.Statuses) != 0 || len(pod.InitStatuses) != 0 || pod.Resources != nil ||
			!validDNSNamedPorts(pod, dnsPorts) {
			return ErrFormat
		}
	}
	if !all(e.DNSServices, func(r service) bool {
		return system(r.Identity) && r.Identity.Name == "kube-dns" && validService(r)
	}) || !all(e.DNSEndpointSlices, func(r endpointSlice) bool {
		return system(r.Identity) && r.ServiceName == "kube-dns" && validSlice(r)
	}) || !all(e.DNSLegacyEndpoints, func(r legacyEndpoints) bool {
		return system(r.Identity) && r.Identity.Name == "kube-dns" && validLegacy(r)
	}) || !all(e.DNSConfig, func(r dnsConfig) bool {
		return r.Namespace == "kube-system" && oneOf(r.Name, "kube-dns", "coredns") && simpleToken(r.UID, 253)
	}) || !validExpansion(p.Scope, e.Expansion, hosts) || !validHistory(p) {
		return ErrFormat
	}
	return nil
}

func validateReport(p *payload) error {
	report := p.Stored
	if report.SchemaVersion != model.SchemaVersion || report.Scope != p.Scope ||
		!report.CollectedAt.Equal(p.EvaluatedAt) || !validSources(report.Sources) || !validSources(report.CollectionErrors) {
		return ErrFormat
	}
	if report.Complete != !slices.ContainsFunc(p.Sources, func(s model.SourceResult) bool {
		return s.Required && s.Status != model.Complete && s.Status != model.NotApplicable
	}) {
		return ErrFormat
	}
	if !sameSources(report.Sources, p.Sources) || !sameSources(report.CollectionErrors, errorsOnly(p.Sources)) {
		return ErrFormat
	}
	scope := func(r model.Resource) bool { return storedReferenceInScope(p, r) }
	for _, f := range report.Findings {
		if !simpleToken(f.RuleID, 128) || f.RuleID == "" || !scope(f.Resource) ||
			(f.Incident != nil && !scope(*f.Incident)) || f.Severity.Rank() == 0 ||
			!oneOf(string(f.Confidence), "observed", "likely", "hypothesis") ||
			!safeText(f.Symptom, 2048) || !safeText(f.ProbableCause, 2048) ||
			len(f.Evidence) > 128 || len(f.NextSteps) > 32 ||
			!all(f.NextSteps, func(s string) bool { return safeText(s, 2048) }) ||
			!all(f.Evidence, func(e model.Evidence) bool { return simpleToken(e.Field, 128) && safeText(e.Value, 2048) }) {
			return ErrFormat
		}
	}
	for _, c := range report.Checks {
		if !simpleToken(c.ID, 128) || c.ID == "" || !safeText(c.Detail, 2048) ||
			!oneOf(string(c.Status), "checked", "skipped", "inconclusive") {
			return ErrFormat
		}
	}
	return nil
}

func errorsOnly(sources []model.SourceResult) []model.SourceResult {
	var out []model.SourceResult
	for _, s := range sources {
		if s.Status != model.Complete && s.Status != model.NotApplicable {
			out = append(out, s)
		}
	}
	return out
}

func sameSources(a, b []model.SourceResult) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[model.SourceResult]int, len(a))
	for _, source := range a {
		counts[source]++
	}
	for _, source := range b {
		if counts[source] == 0 {
			return false
		}
		counts[source]--
	}
	return true
}

func storedReferenceInScope(p *payload, r model.Resource) bool {
	if !validResource(r) {
		return false
	}
	if namespaceInScope(p.Scope, r.Namespace) {
		return true
	}
	if r.Namespace == "" {
		switch r.Kind {
		case "Node":
			return p.Scope.Kind == model.ClusterScope || slices.ContainsFunc(p.Evidence.Nodes, func(n node) bool {
				return n.Identity.Name == r.Name && n.Identity.UID == r.UID
			})
		case "StorageClass":
			return p.Scope.Kind == model.ClusterScope || slices.ContainsFunc(p.Evidence.StorageClasses, func(s storageClass) bool {
				return s.Identity.Name == r.Name && s.Identity.UID == r.UID
			})
		case "Namespace":
			return p.Scope.Kind == model.ClusterScope || p.Scope.Namespace == r.Name
		}
	}
	if r.Namespace != "kube-system" {
		return false
	}
	switch r.Kind {
	case "Service":
		return r.Name == "kube-dns"
	case "ConfigMap":
		return oneOf(r.Name, "coredns", "kube-dns")
	case "DaemonSet":
		return r.Name == "node-local-dns"
	case "Pod":
		return slices.ContainsFunc(p.Evidence.DNSPods, func(p pod) bool {
			return p.Identity.Name == r.Name && p.Identity.UID == r.UID
		}) || slices.ContainsFunc(p.Evidence.Expansion.NodeLocalPods, func(p model.NodeLocalPodEvidence) bool {
			return p.Pod.Name == r.Name && p.Pod.UID == r.UID
		})
	}
	return false
}

func validScope(s model.Scope) bool {
	return safeText(s.Context, 253) &&
		((s.Kind == model.ClusterScope && s.Namespace == "") ||
			(s.Kind == model.NamespaceScope && validNamespace(s.Namespace)))
}

func namespaceInScope(s model.Scope, namespace string) bool {
	return validNamespace(namespace) && (s.Kind == model.ClusterScope || namespace == s.Namespace)
}

func validProvenance(scope model.Scope, id, method string) bool {
	if id == "" {
		return method == ""
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 32 || id != strings.ToLower(id) {
		return false
	}
	switch method {
	case model.KubeSystemUIDProvenance:
		return true
	case model.NamespaceUIDProvenance:
		return scope.Kind == model.NamespaceScope
	default:
		return false
	}
}

func validSources(sources []model.SourceResult) bool {
	return all(sources, func(s model.SourceResult) bool {
		return knownSource(s.Source) && simpleToken(s.Resource, 253) && knownCoverageDetail(s.Detail) &&
			oneOf(string(s.Status), "complete", "not_applicable", "permission_denied", "unavailable",
				"failed", "truncated", "not_inspected", "stale")
	})
}

func knownSource(s model.Source) bool {
	switch s {
	case model.SourceNamespace, model.SourceNodes, model.SourcePods, model.SourceReplicaSets, model.SourceDeployments,
		model.SourceDaemonSets, model.SourceEvents, model.SourceConfigMaps, model.SourcePVCs, model.SourceStorageClasses,
		model.SourceServices, model.SourceEndpointSlices, model.SourceLegacyEndpoints, model.SourceIngresses,
		model.SourceDNSPods, model.SourceDNSServices, model.SourceDNSConfig, model.SourceDNSEndpointSlices,
		model.SourceDNSLegacyEndpoints, model.SourceNetworkPolicies, model.SourcePDBs, model.SourceMetrics,
		model.SourceStatefulSets, model.SourceJobs, model.SourceCronJobs, model.SourceHPAs, model.SourceQuotas,
		model.SourceLimitRanges, model.SourceNodeLocalDNS, model.SourceNodeLocalPods, model.SourceProvenance,
		"scan", "configuration", "historical_metrics", "prometheus":
		return true
	}
	return false
}

func validIdentity(i identity) bool {
	return validName(i.Name) && (i.Namespace == "" || validNamespace(i.Namespace)) &&
		simpleToken(i.UID, 253) && i.Generation >= 0 && validTimestamp(i.CreatedAt) && validTimestamp(i.DeletedAt) &&
		len(i.Owners) <= 64 && all(i.Owners, func(o owner) bool {
		return o.Kind != "" && simpleToken(o.Kind, 128) && validName(o.Name) && simpleToken(o.UID, 253) &&
			(o.APIVersion == "" || o.APIVersion == safeOwnerVersion(metav1.OwnerReference{Kind: o.Kind, APIVersion: o.APIVersion}))
	}) && validLabels(i.Labels)
}

func validResource(r model.Resource) bool {
	return r.Kind != "" && simpleToken(r.Kind, 128) && validName(r.Name) &&
		(r.Namespace == "" || validNamespace(r.Namespace)) && simpleToken(r.UID, 253)
}

func validName(name string) bool {
	return name != "" && len(validation.IsDNS1123Subdomain(name)) == 0
}
func validNamespace(name string) bool {
	return name != "" && len(validation.IsDNS1123Label(name)) == 0
}
func safeText(s string, limit int) bool {
	return len(s) <= limit && strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) == -1
}
func simpleToken(s string, limit int) bool {
	return len(s) <= limit && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-')
	}) == -1
}

func validLabels(labels map[string]string) bool {
	for key, value := range labels {
		if !validLabelKey(key) || !validLabelFingerprint(key, value) {
			return false
		}
	}
	return true
}

func validLabelKey(key string) bool {
	return len(validation.IsQualifiedName(key)) == 0
}

func validSelector(s *selector) bool {
	if s == nil {
		return true
	}
	return validLabels(s.Labels) && all(s.Expressions, func(e expression) bool {
		// A marker represents an unsupported selector without its original text.
		if e.Key == "invalid" && e.Operator == "Invalid" && len(e.Values) == 0 {
			return true
		}
		if !validLabelKey(e.Key) || !oneOf(e.Operator, "In", "NotIn", "Exists", "DoesNotExist") {
			return false
		}
		any := len(e.Values) > 0
		return any == (e.Operator == "In" || e.Operator == "NotIn") &&
			all(e.Values, func(value string) bool { return validLabelFingerprint(e.Key, value) })
	})
}

func validConditions(conditions []condition) bool {
	return all(conditions, func(c condition) bool {
		return validConditionType(c.Type) && validConditionReason(c.Reason) &&
			oneOf(c.Status, "", "True", "False", "Unknown") &&
			validTimestamp(c.TransitionAt) && validTimestamp(c.UpdatedAt)
	})
}

func validClaim(c claim) bool {
	return (c.Class == nil || *c.Class == "" || validName(*c.Class)) &&
		(c.Volume == "" || validName(c.Volume)) && (c.SelectedNode == "" || validName(c.SelectedNode)) &&
		oneOf(c.Phase, "", "Pending", "Bound", "Lost")
}

func validPod(p pod) bool {
	return (p.HostNode == "" || validName(p.HostNode)) &&
		oneOf(p.Phase, "", "Pending", "Running", "Succeeded", "Failed", "Unknown") &&
		validPodReason(p.Reason) && validTimestamp(p.StartedAt) && validConditions(p.Conditions) &&
		(p.Resources == nil || validRequirements(*p.Resources)) &&
		all(p.Containers, validContainer) && all(p.Init, validContainer) &&
		all(p.Statuses, validContainerStatus) && all(p.InitStatuses, validContainerStatus) &&
		all(p.Volumes, validVolume)
}

func validContainer(c container) bool {
	return validName(c.Name) && validRequirements(c.Resources) && all(c.Ports, validPort) &&
		all(c.Environment, func(r configReference) bool { return validConfigRef(r) && len(r.Keys) == 0 }) &&
		all(c.Imports, func(r configReference) bool {
			return validConfigRef(r) && r.Key == "" && len(r.Keys) == 0 && r.Variable == ""
		})
}

func validConfigRef(r configReference) bool {
	return oneOf(r.Kind, "ConfigMap", "Secret") &&
		(r.Name == "" || validName(r.Name)) && simpleToken(r.Key, 253) && simpleToken(r.Variable, 253) &&
		all(r.Keys, func(key string) bool { return key != "" && simpleToken(key, 253) })
}

func validVolume(v volume) bool {
	return validName(v.Name) && (v.Claim == "" || validName(v.Claim)) && len(v.Direct) <= 2 &&
		all(v.Direct, validConfigRef) && all(v.Projection, validConfigRef)
}

func validRequirements(r requirements) bool {
	return validResourcePair(r.Requests) && validResourcePair(r.Limits)
}

func validResourcePair(r resourcePair) bool {
	return (r.CPU == "" || validQuantity(r.CPU)) && (r.Memory == "" || validQuantity(r.Memory))
}

func validQuantity(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	// Reject exponent ranges that could turn later quantity conversions into
	// oversized decimal allocations. E is also a legal suffix by itself.
	if exp := strings.IndexAny(s, "eE"); exp >= 0 && exp+1 < len(s) && s[exp+1:] != "i" {
		n, err := strconv.Atoi(s[exp+1:])
		if err != nil || n < -18 || n > 18 {
			return false
		}
	}
	q, err := resource.ParseQuantity(s)
	v := q.AsApproximateFloat64()
	return err == nil && !math.IsNaN(v) && v >= 0 && v < float64(math.MaxInt64)/1000
}

func validContainerStatus(s containerStatus) bool {
	return validName(s.Name) && s.RestartCount >= 0 && validState(s.Current) && validState(s.Previous)
}

func validState(s containerState) bool {
	if s.Waiting != nil {
		return !s.Running && s.RunningSince == nil && s.Terminated == nil && validWaitingReason(*s.Waiting)
	}
	if s.Running {
		return s.Terminated == nil && validTimestamp(s.RunningSince)
	}
	if s.RunningSince != nil {
		return false
	}
	t := s.Terminated
	return t == nil || (validTerminationReason(t.Reason) && validTimestamp(t.StartedAt) && validTimestamp(t.FinishedAt))
}

func validPort(p port) bool {
	return simpleToken(p.Name, 128) && p.Number >= 0 && p.Number <= 65535 &&
		oneOf(p.Protocol, "", "TCP", "UDP", "SCTP")
}

func validService(s service) bool {
	return oneOf(s.Type, "", "ClusterIP", "NodePort", "LoadBalancer", "ExternalName") &&
		validLabels(s.Selector) && all(s.Ports, func(p servicePort) bool {
		return validPort(p.Port) && p.Target >= 0 && p.Target <= 65535 && simpleToken(p.Named, 128) &&
			((p.IsNamed && p.Target == 0) || (!p.IsNamed && p.Named == ""))
	})
}

func validSlice(s endpointSlice) bool {
	return (s.ServiceName == "" || validName(s.ServiceName)) && oneOf(s.AddressType, "", "IPv4", "IPv6", "FQDN") &&
		all(s.Ports, func(p endpointPort) bool {
			return (p.Name == nil || simpleToken(*p.Name, 128)) && (p.Number == nil || (*p.Number >= 0 && *p.Number <= 65535)) &&
				(p.Protocol == nil || oneOf(*p.Protocol, "", "TCP", "UDP", "SCTP"))
		}) && all(s.Endpoints, func(e endpoint) bool {
		return e.AddressCount >= 0 && e.AddressCount <= 100 && validTarget(e.Target, s.Identity.Namespace)
	})
}

func validTarget(r *model.Resource, namespace string) bool {
	return r == nil || (validResource(*r) && (r.Namespace == "" || r.Namespace == namespace))
}

func validLegacy(s legacyEndpoints) bool {
	return all(s.Subsets, func(r legacySubset) bool {
		return all(r.Ports, validPort) && all(r.Ready, func(ref *model.Resource) bool {
			return validTarget(ref, s.Identity.Namespace)
		}) && all(r.NotReady, func(ref *model.Resource) bool {
			return validTarget(ref, s.Identity.Namespace)
		})
	})
}

func validIngress(s ingress) bool {
	valid := func(r ingressBackend) bool {
		return (r.Service == "" || validName(r.Service)) && simpleToken(r.PortName, 128) &&
			r.PortNumber >= 0 && r.PortNumber <= 65535 && simpleToken(r.ResourceKind, 128) &&
			(r.ResourceName == "" || validName(r.ResourceName))
	}
	return (s.Default == nil || valid(*s.Default)) && all(s.Rules, func(paths []ingressBackend) bool {
		return all(paths, valid)
	})
}

func validNetworkPolicy(s networkPolicy) bool {
	rule := func(r networkRule) bool {
		return r.Peers >= 0 && r.Peers <= 1024 && r.Ports >= 0 && r.Ports <= 1024 &&
			len(r.NamedPorts) <= r.Ports && all(r.NamedPorts, func(p port) bool {
			return p.Name != "" && p.Number == 0 && validPort(p)
		})
	}
	return validSelector(s.Selector) && all(s.Types, func(t string) bool { return oneOf(t, "Ingress", "Egress") }) &&
		all(s.Ingress, rule) && all(s.Egress, rule)
}

func validEvent(scope model.Scope, hosts map[string]bool, event classifiedEvent) bool {
	r := event.Resource
	reasonValid := validEventReason(event.Reason) || r.Kind == "HorizontalPodAutoscaler" && model.HPAWarningReason(event.Reason)
	if !validResource(r) || event.Count < 0 || !reasonValid ||
		!oneOf(event.TimeBasis, "", "series_last_observed", "last_reported", "event_time", "first_reported", "event_object_created") {
		return false
	}
	if r.Namespace == "" && r.Kind == "Node" {
		if scope.Kind == model.NamespaceScope && !hosts[r.Name] {
			return false
		}
	} else if !namespaceInScope(scope, r.Namespace) {
		return false
	}
	c := event.Reference
	return c == nil || (r.Kind == "Pod" && oneOf(c.Kind, "ConfigMap", "Secret") && validName(c.Name) &&
		simpleToken(c.Key, 253) && ((c.Missing == "object" && c.Key == "") || (c.Missing == "key" && c.Key != "")))
}

func validMetrics(r currentUsage) bool {
	return validName(r.Name) && r.Window >= 0 && all(r.Containers, func(c containerUsage) bool {
		return validName(c.Name) && validQuantity(c.Usage.CPU) && validQuantity(c.Usage.Memory)
	})
}

func uniqueIdentities[T any](values []T, identify func(T) identity) bool {
	seen := make(map[[2]string]bool, len(values))
	uids := make(map[string]bool, len(values))
	for _, v := range values {
		i := identify(v)
		key := [2]string{i.Namespace, i.Name}
		if seen[key] || (i.UID != "" && uids[i.UID]) {
			return false
		}
		seen[key] = true
		if i.UID != "" {
			uids[i.UID] = true
		}
	}
	return true
}

func all[T any](values []T, valid func(T) bool) bool {
	return !slices.ContainsFunc(values, func(v T) bool { return !valid(v) })
}

func oneOf(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
}

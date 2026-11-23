package bundle

import "github.com/DheerajShaik/kubedoctor/internal/model"

// Captured coverage descriptions are fixed product text. The bundle parser
// rejects arbitrary messages, including recomputed but unauthenticated payloads.
func knownCoverageDetail(detail string) bool {
	switch detail {
	case "",
		"API item budget reached",
		"API page budget reached",
		"API repeated a pagination token",
		"API response byte budget reached",
		"API retry budget reached",
		"API returned an unexpected namespace",
		"API returned an unexpected object",
		"API returned invalid or out-of-scope objects",
		"Kubernetes API read failed",
		"Kubernetes did not authorize this API read",
		"Kubernetes did not provide the requested API resource",
		"Kubernetes truncated at least one legacy Endpoints resource",
		"Kubernetes truncated the DNS legacy Endpoints resource",
		"NodeLocal DNSCache provider detection did not finish",
		"NodeLocal DNSCache provider detection was incomplete",
		"Scan timeout or cancellation interrupted collection.",
		"a pending claim has no explicit StorageClass and namespace scans do not list cluster defaults",
		"cluster inventory was selected",
		"collection timed out",
		"collection was canceled",
		"context canceled",
		"context deadline exceeded",
		"conventional DNS Service was unavailable",
		"conventional DNS configuration name was absent",
		"hosting nodes could not be determined for uncollected pods",
		"invalid scan scope",
		"named lookup budget reached",
		"namespace hosting nodes could not be determined",
		"no active namespaced Pod had an observed hosting node",
		"no observed active pods were assigned to a node",
		"no observed pending claims named a StorageClass",
		"stable EndpointSlices were selected",
		"stable EndpointSlices were unavailable, using legacy Endpoints",
		"the Pod Metrics API is not installed",
		"the conventional NodeLocal DNSCache DaemonSet was absent",
		"the conventional provider was not discovered",
		"the referenced StorageClass was absent",
		"uncollected claims may reference other storage classes":
		return true
	}
	return false
}

func knownCoverageResource(scope model.Scope, source model.Source, name string, hosts, claims map[string]bool) bool {
	if name == "" {
		return true
	}
	switch source {
	case model.SourceNamespace:
		return isNamespaceScope(scope) && name == scope.Namespace
	case model.SourceNodes:
		return isNamespaceScope(scope) && hosts[name]
	case model.SourceStorageClasses:
		return isNamespaceScope(scope) && claims[name]
	case model.SourceDNSConfig:
		return oneOf(name, "coredns", "kube-dns")
	case model.SourceDNSServices, model.SourceDNSLegacyEndpoints:
		return name == "kube-dns"
	case model.SourceNodeLocalDNS:
		return name == "node-local-dns"
	case model.SourceProvenance:
		return name == "kube-system" || (isNamespaceScope(scope) && name == scope.Namespace)
	case model.SourceEndpointSlices, model.SourceMetrics:
		return name == "api-discovery"
	}
	return false
}

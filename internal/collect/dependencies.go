package collect

import (
	"context"
	"errors"
	"slices"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	storage "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Collector) collectNodes(ctx context.Context, cluster, podsComplete bool, s *model.Snapshot) []model.SourceResult {
	if cluster {
		var result model.SourceResult
		s.Nodes, result = inventory(ctx, model.SourceNodes, c.core.Nodes().List,
			func(l *core.NodeList) []core.Node { return l.Items }, projectNode)
		return []model.SourceResult{result}
	}
	var names []string
	for _, pod := range s.Pods {
		if pod.Spec.NodeName != "" && pod.DeletionTimestamp == nil &&
			pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
			names = append(names, pod.Spec.NodeName)
		}
	}
	var results []model.SourceResult
	if !podsComplete {
		results = append(results, observed(model.SourceNodes, true, model.NotInspected, "hosting nodes could not be determined for uncollected pods"))
	}
	if len(names) == 0 {
		return append(results, observed(model.SourceNodes, true, model.NotApplicable, "no observed active pods were assigned to a node"))
	}

	for _, object := range boundedNames(names, model.SourceNodes, &results) {
		if err := ctx.Err(); err != nil {
			results = append(results, collectionError(model.SourceNodes, true, err))
			break
		}
		node, err := timedRead(ctx, model.SourceNodes, object, func() (*core.Node, error) {
			return c.core.Nodes().Get(requestContext(ctx), object, metav1.GetOptions{})
		})
		result := namedObservation(model.SourceNodes, true, object, err)
		if err == nil {
			if node.Name == object {
				s.Nodes = append(s.Nodes, projectNode(*node))
			} else {
				result = namedObservation(model.SourceNodes, true, object, errUnexpectedNamedObject)
			}
		}
		results = append(results, result)
	}
	return results
}

func (c *Collector) collectStorageClasses(ctx context.Context, cluster, claimsComplete bool, s *model.Snapshot) []model.SourceResult {
	if cluster {
		var result model.SourceResult
		s.StorageClasses, result = inventory(ctx, model.SourceStorageClasses, c.storage.StorageClasses().List,
			func(l *storage.StorageClassList) []storage.StorageClass { return l.Items }, projectStorageClass)
		return []model.SourceResult{result}
	}

	var names []string
	defaultUnknown := false
	for _, claim := range s.PVCs {
		if claim.Status.Phase != core.ClaimPending || claim.DeletionTimestamp != nil {
			continue
		}
		if class := claim.Spec.StorageClassName; class == nil {
			defaultUnknown = true
		} else if *class != "" {
			names = append(names, *class)
		}
	}
	var results []model.SourceResult
	if !claimsComplete {
		results = append(results, observed(model.SourceStorageClasses, true, model.NotInspected, "uncollected claims may reference other storage classes"))
	}
	if defaultUnknown {
		results = append(results, observed(model.SourceStorageClasses, false, model.NotInspected, "a pending claim has no explicit StorageClass and namespace scans do not list cluster defaults"))
	}
	if len(names) == 0 {
		return append(results, observed(model.SourceStorageClasses, true, model.NotApplicable, "no observed pending claims named a StorageClass"))
	}

	for _, object := range boundedNames(names, model.SourceStorageClasses, &results) {
		if err := ctx.Err(); err != nil {
			results = append(results, collectionError(model.SourceStorageClasses, true, err))
			break
		}
		class, err := timedRead(ctx, model.SourceStorageClasses, object, func() (*storage.StorageClass, error) {
			return c.storage.StorageClasses().Get(requestContext(ctx), object, metav1.GetOptions{})
		})
		result := namedObservation(model.SourceStorageClasses, true, object, err)
		switch {
		case storageClassNotFound(object, err):
			// Only an object-specific Kubernetes status establishes absence.
			result.Status = model.Complete
			result.Detail = "the referenced StorageClass was absent"
		case err == nil && class.Name == object:
			s.StorageClasses = append(s.StorageClasses, projectStorageClass(*class))
		case err == nil:
			result = namedObservation(model.SourceStorageClasses, true, object, errUnexpectedNamedObject)
		}
		results = append(results, result)
	}
	return results
}

func storageClassNotFound(name string, err error) bool {
	if !apierrors.IsNotFound(err) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	details := status.Status().Details
	return details != nil && details.Name == name &&
		details.Group == "storage.k8s.io" && details.Kind == "storageclasses"
}

func boundedNames(names []string, source model.Source, results *[]model.SourceResult) []string {
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) <= maxNamedLookups {
		return names
	}
	*results = append(*results, observed(source, true, model.Truncated, "named lookup budget reached"))
	return names[:maxNamedLookups]
}

var errUnexpectedNamedObject = errors.New("unexpected named object")

func namedObservation(source model.Source, required bool, object string, err error) model.SourceResult {
	result := observed(source, required, model.Complete, "")
	switch {
	case err == errUnexpectedNamedObject:
		result.Status, result.Detail = model.Failed, "API returned an unexpected object"
	case err != nil:
		result = collectionError(source, required, err)
	}
	result.Resource = object
	return result
}

func (c *Collector) collectDNSPods(ctx context.Context, s *model.Snapshot) []model.SourceResult {
	var result model.SourceResult
	s.DNSPods, result = readPages(ctx, model.SourceDNSPods, false, metav1.ListOptions{LabelSelector: dnsPodSelector},
		c.core.Pods(dnsNamespace).List, func(l *core.PodList) []core.Pod { return l.Items }, include(projectPod))
	return []model.SourceResult{result}
}

func (c *Collector) collectDNSConfig(ctx context.Context, s *model.Snapshot) []model.SourceResult {
	var results []model.SourceResult
	for _, object := range []string{"coredns", "kube-dns"} {
		configMap, err := timedRead(ctx, model.SourceDNSConfig, object, func() (*core.ConfigMap, error) {
			return c.core.ConfigMaps(dnsNamespace).Get(requestContext(ctx), object, metav1.GetOptions{})
		})
		result := namedObservation(model.SourceDNSConfig, false, object, err)
		switch {
		case apierrors.IsNotFound(err):
			// Either name is legitimate. Complete here means the named
			// object was checked and found absent, and conveys no DNS health.
			result.Status, result.Detail = model.Complete, "conventional DNS configuration name was absent"
		case err == nil && configMap.Name == object && configMap.Namespace == dnsNamespace:
			_, hasCorefile := configMap.Data["Corefile"]
			s.DNSConfig = append(s.DNSConfig, model.DNSConfig{
				Name: object, Namespace: dnsNamespace, UID: token(string(configMap.UID)), HasCorefile: hasCorefile,
			})
		case err == nil:
			result = namedObservation(model.SourceDNSConfig, false, object, errUnexpectedNamedObject)
		}
		results = append(results, result)
	}

	if len(s.DNSConfig) == 0 {
		for i := range results {
			if results[i].Status == model.Complete {
				results[i].Status = model.Unavailable
			}
		}
	}
	return results
}

func (c *Collector) collectDNSServices(ctx context.Context, slicesAbsent bool, s *model.Snapshot) []model.SourceResult {
	service, err := timedRead(ctx, model.SourceDNSServices, dnsService, func() (*core.Service, error) {
		return c.core.Services(dnsNamespace).Get(requestContext(ctx), dnsService, metav1.GetOptions{})
	})
	if err != nil || service.Name != dnsService || service.Namespace != dnsNamespace {
		if err == nil {
			err = errUnexpectedNamedObject
		}
		return []model.SourceResult{
			namedObservation(model.SourceDNSServices, false, dnsService, err),
			observed(model.SourceDNSEndpointSlices, false, model.NotInspected, "conventional DNS Service was unavailable"),
			observed(model.SourceDNSLegacyEndpoints, false, model.NotInspected, "conventional DNS Service was unavailable"),
		}
	}
	s.DNSServices = append(s.DNSServices, projectService(*service))
	result := namedObservation(model.SourceDNSServices, false, dnsService, nil)
	if !slicesAbsent {
		var sliceResult model.SourceResult
		s.DNSEndpointSlices, sliceResult = readPages(ctx, model.SourceDNSEndpointSlices, false,
			metav1.ListOptions{LabelSelector: dnsSliceSelector}, c.discovery.EndpointSlices(dnsNamespace).List,
			func(l *discovery.EndpointSliceList) []discovery.EndpointSlice { return l.Items }, include(projectEndpointSlice))
		return []model.SourceResult{result, sliceResult,
			observed(model.SourceDNSLegacyEndpoints, false, model.NotApplicable, "stable EndpointSlices were selected")}
	}

	endpoint, err := timedRead(ctx, model.SourceDNSLegacyEndpoints, dnsService, func() (*core.Endpoints, error) {
		return c.core.Endpoints(dnsNamespace).Get(requestContext(ctx), dnsService, metav1.GetOptions{})
	})
	fallback := namedObservation(model.SourceDNSLegacyEndpoints, false, dnsService, err)
	if err == nil {
		if endpoint.Name == dnsService && endpoint.Namespace == dnsNamespace {
			s.DNSLegacyEndpoints = append(s.DNSLegacyEndpoints, projectLegacyEndpoints(*endpoint))
			if endpoint.Annotations[overCapacity] == "truncated" {
				fallback.Status, fallback.Detail = model.Truncated, "Kubernetes truncated the DNS legacy Endpoints resource"
			}
		} else {
			fallback = namedObservation(model.SourceDNSLegacyEndpoints, false, dnsService, errUnexpectedNamedObject)
		}
	}
	return []model.SourceResult{result,
		observed(model.SourceDNSEndpointSlices, false, model.Unavailable, "stable EndpointSlices were unavailable, using legacy Endpoints"),
		fallback}
}

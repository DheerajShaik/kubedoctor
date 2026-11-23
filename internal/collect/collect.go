package collect

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	policy "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	metrics "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func (c *Collector) Collect(ctx context.Context, request model.ScanRequest) model.Snapshot {
	scope := request.Scope
	s := model.Snapshot{Scope: scope, CollectedAt: time.Now().UTC()}
	if (scope.Kind != model.NamespaceScope && scope.Kind != model.ClusterScope) ||
		(scope.Kind == model.NamespaceScope && !validNamespace(scope.Namespace)) ||
		(scope.Kind == model.ClusterScope && scope.Namespace != "") {
		s.Sources = []model.SourceResult{observed(model.SourceNamespace, true, model.Failed, "invalid scan scope")}
		return s
	}

	ns := scope.Namespace
	cluster := scope.Kind == model.ClusterScope
	// Scope client-go request logs away from output, including optional
	// high-verbosity fields such as raw HTTP bodies and pagination tokens.
	ctx = klog.NewContext(ctx, klog.Logger{})
	ctx = withAccess(ctx, scanAccess{namespace: ns, cluster: cluster})
	slicesAbsent := sync.OnceValue(func() bool {
		absent, _ := apiResourceAbsent(ctx, c.discovery.RESTClient(),
			"/apis/discovery.k8s.io/v1", "endpointslices")
		return absent
	})

	s.Sources = parallel(
		func() []model.SourceResult {
			if cluster {
				return []model.SourceResult{observed(model.SourceNamespace, false, model.NotApplicable, "cluster inventory was selected")}
			}
			found, err := c.core.Namespaces().Get(requestContext(ctx), ns, metav1.GetOptions{})
			if err != nil {
				return []model.SourceResult{collectionError(model.SourceNamespace, true, err)}
			}
			if found.Name != ns {
				return []model.SourceResult{observed(model.SourceNamespace, true, model.Failed, "API returned an unexpected namespace")}
			}
			return []model.SourceResult{observed(model.SourceNamespace, true, model.Complete, "")}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Pods, result = inventory(ctx, model.SourcePods, c.core.Pods(ns).List,
				func(l *core.PodList) []core.Pod { return l.Items }, projectPod)
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.ReplicaSets, result = inventory(ctx, model.SourceReplicaSets, c.apps.ReplicaSets(ns).List,
				func(l *apps.ReplicaSetList) []apps.ReplicaSet { return l.Items }, projectReplicaSet)
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Deployments, result = inventory(ctx, model.SourceDeployments, c.apps.Deployments(ns).List,
				func(l *apps.DeploymentList) []apps.Deployment { return l.Items }, projectDeployment)
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.DaemonSets, result = inventory(ctx, model.SourceDaemonSets, c.apps.DaemonSets(ns).List,
				func(l *apps.DaemonSetList) []apps.DaemonSet { return l.Items }, projectDaemonSet)
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.ConfigMaps, result = inventory(ctx, model.SourceConfigMaps, c.core.ConfigMaps(ns).List,
				func(l *core.ConfigMapList) []core.ConfigMap { return l.Items }, projectConfigMap)
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.PVCs, result = inventory(ctx, model.SourcePVCs, c.core.PersistentVolumeClaims(ns).List,
				func(l *core.PersistentVolumeClaimList) []core.PersistentVolumeClaim { return l.Items }, projectPVC)
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Services, result = inventory(ctx, model.SourceServices, c.core.Services(ns).List,
				func(l *core.ServiceList) []core.Service { return l.Items }, projectService)
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			return c.collectBackends(ctx, ns, slicesAbsent(), &s)
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Ingresses, result = inventory(ctx, model.SourceIngresses, c.networking.Ingresses(ns).List,
				func(l *networking.IngressList) []networking.Ingress { return l.Items }, projectIngress)
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.NetworkPolicies, result = inventory(ctx, model.SourceNetworkPolicies, c.networking.NetworkPolicies(ns).List,
				func(l *networking.NetworkPolicyList) []networking.NetworkPolicy { return l.Items }, projectNetworkPolicy)
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.PDBs, result = inventory(ctx, model.SourcePDBs, c.policy.PodDisruptionBudgets(ns).List,
				func(l *policy.PodDisruptionBudgetList) []policy.PodDisruptionBudget { return l.Items }, projectPDB)
			return []model.SourceResult{result}
		},
	)

	// Dependencies are derived from objects actually visible in the first phase.
	// Read coverage before the workers start so they never race with the append.
	podsComplete := s.Coverage(model.SourcePods) == model.Complete
	claimsComplete := s.Coverage(model.SourcePVCs) == model.Complete
	dependencies := parallel(
		func() []model.SourceResult {
			return c.collectNodes(ctx, cluster, podsComplete, &s)
		},
		func() []model.SourceResult {
			return c.collectStorageClasses(ctx, cluster, claimsComplete, &s)
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Events, result = readPages(ctx, model.SourceEvents, true, metav1.ListOptions{}, c.core.Events(ns).List,
				func(l *core.EventList) []core.Event { return l.Items }, eventProjector(s.Pods))
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			return c.collectMetrics(ctx, ns, &s)
		},
		func() []model.SourceResult {
			return c.collectDNSPods(ctx, &s)
		},
		func() []model.SourceResult {
			return c.collectDNSConfig(ctx, &s)
		},
		func() []model.SourceResult {
			return c.collectDNSServices(ctx, slicesAbsent(), &s)
		},
	)
	s.Sources = append(s.Sources, dependencies...)
	s.CollectedAt = time.Now().UTC()
	return s
}

// Probe only API group versions that affect collection strategy. A discovery
// denial does not imply that a concrete resource read is forbidden, so callers
// still try the typed read unless the resource was definitively absent.
func apiResourceAbsent(ctx context.Context, client rest.Interface, route, resource string) (bool, error) {
	var list metav1.APIResourceList
	err := client.Get().AbsPath(route).MaxRetries(1).Do(requestContext(ctx)).Into(&list)
	if err != nil {
		return apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err), err
	}
	return !slices.ContainsFunc(list.APIResources, func(item metav1.APIResource) bool {
		return item.Name == resource
	}), nil
}

func (c *Collector) collectBackends(ctx context.Context, ns string, slicesAbsent bool, s *model.Snapshot) []model.SourceResult {
	if !slicesAbsent {
		var result model.SourceResult
		s.EndpointSlices, result = inventory(ctx, model.SourceEndpointSlices, c.discovery.EndpointSlices(ns).List,
			func(l *discovery.EndpointSliceList) []discovery.EndpointSlice { return l.Items }, projectEndpointSlice)
		return []model.SourceResult{result, observed(model.SourceLegacyEndpoints, false, model.NotApplicable, "stable EndpointSlices were selected")}
	}

	var result model.SourceResult
	s.LegacyEndpoints, result = inventory(ctx, model.SourceLegacyEndpoints, c.core.Endpoints(ns).List,
		func(l *core.EndpointsList) []core.Endpoints { return l.Items }, projectLegacyEndpoints)
	if result.Status == model.Complete && slices.ContainsFunc(s.LegacyEndpoints, func(endpoint core.Endpoints) bool {
		return endpoint.Annotations[overCapacity] == "truncated"
	}) {
		result.Status, result.Detail = model.Truncated, "Kubernetes truncated at least one legacy Endpoints resource"
	}
	return []model.SourceResult{
		observed(model.SourceEndpointSlices, false, model.Unavailable, "stable EndpointSlices were unavailable, using legacy Endpoints"),
		result,
	}
}

func (c *Collector) collectMetrics(ctx context.Context, ns string, s *model.Snapshot) []model.SourceResult {
	absent, discoveryErr := apiResourceAbsent(ctx, c.metrics.RESTClient(), "/apis/metrics.k8s.io/v1beta1", "pods")
	if absent {
		return []model.SourceResult{observed(model.SourceMetrics, false, model.Unavailable, "the Pod Metrics API is not installed")}
	}
	if apierrors.IsServiceUnavailable(discoveryErr) {
		return []model.SourceResult{collectionError(model.SourceMetrics, false, discoveryErr)}
	}
	var result model.SourceResult
	s.Metrics, result = readPages(
		ctx, model.SourceMetrics, false, metav1.ListOptions{}, c.metrics.PodMetricses(ns).List,
		func(l *metrics.PodMetricsList) []metrics.PodMetrics { return l.Items }, include(projectMetrics))
	return []model.SourceResult{result}
}

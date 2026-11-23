package collect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	autoscaling "k8s.io/api/autoscaling/v2"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Optional sources share a limited part of the remaining legacy scan budget.
// Their coverage is preserved when the narrower deadline interrupts a read.
func optionalCollectionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := 4 * time.Second
	if deadline, set := ctx.Deadline(); set {
		budget = min(budget, time.Until(deadline)/2)
	}
	return context.WithTimeout(ctx, budget)
}

func (c *Collector) collectExpansion(ctx context.Context, namespace string, s *model.Snapshot) []model.SourceResult {
	return parallel(
		func() []model.SourceResult {
			var result model.SourceResult
			s.Expansion.StatefulSets, result = readPages(ctx, model.SourceStatefulSets, false, metav1.ListOptions{}, c.apps.StatefulSets(namespace).List,
				func(list *apps.StatefulSetList) []apps.StatefulSet { return list.Items }, include(projectStatefulSet))
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Expansion.Jobs, result = readPages(ctx, model.SourceJobs, false, metav1.ListOptions{}, c.batch.Jobs(namespace).List,
				func(list *batch.JobList) []batch.Job { return list.Items }, include(projectJob))
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Expansion.CronJobs, result = readPages(ctx, model.SourceCronJobs, false, metav1.ListOptions{}, c.batch.CronJobs(namespace).List,
				func(list *batch.CronJobList) []batch.CronJob { return list.Items }, include(projectCronJob))
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Expansion.HPAs, result = readPages(ctx, model.SourceHPAs, false, metav1.ListOptions{}, c.autoscaling.HorizontalPodAutoscalers(namespace).List,
				func(list *autoscaling.HorizontalPodAutoscalerList) []autoscaling.HorizontalPodAutoscaler {
					return list.Items
				}, include(projectHPA))
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Expansion.Quotas, result = readPages(ctx, model.SourceQuotas, false, metav1.ListOptions{}, c.core.ResourceQuotas(namespace).List,
				func(list *core.ResourceQuotaList) []core.ResourceQuota { return list.Items }, include(projectQuota))
			return []model.SourceResult{result}
		},
		func() []model.SourceResult {
			var result model.SourceResult
			s.Expansion.LimitRanges, result = readPages(ctx, model.SourceLimitRanges, false, metav1.ListOptions{}, c.core.LimitRanges(namespace).List,
				func(list *core.LimitRangeList) []core.LimitRange { return list.Items }, include(projectLimitRange))
			return []model.SourceResult{result}
		},
		func() []model.SourceResult { return c.collectNodeLocalDNS(ctx, namespace, s) },
	)
}

func (c *Collector) collectNodeLocalDNS(ctx context.Context, namespace string, s *model.Snapshot) []model.SourceResult {
	if err := ctx.Err(); err != nil {
		return []model.SourceResult{collectionError(model.SourceNodeLocalDNS, false, err),
			observed(model.SourceNodeLocalPods, false, model.NotInspected, "NodeLocal DNSCache provider detection did not finish")}
	}
	configuration, err := timedRead(ctx, model.SourceNodeLocalDNS, nodeLocalDNS, func() (*apps.DaemonSet, error) {
		return c.apps.DaemonSets(dnsNamespace).Get(requestContext(ctx), nodeLocalDNS, metav1.GetOptions{})
	})
	result := namedObservation(model.SourceNodeLocalDNS, false, nodeLocalDNS, err)
	if apierrors.IsNotFound(err) {
		result.Status, result.Detail = model.NotApplicable, "the conventional NodeLocal DNSCache DaemonSet was absent"
		return []model.SourceResult{result, observed(model.SourceNodeLocalPods, false, model.NotApplicable, "the conventional provider was not discovered")}
	}
	if err == nil && (configuration.Namespace != dnsNamespace || configuration.Name != nodeLocalDNS) {
		err = errUnexpectedNamedObject
		result = namedObservation(model.SourceNodeLocalDNS, false, nodeLocalDNS, err)
	}
	if err != nil {
		return []model.SourceResult{result, observed(model.SourceNodeLocalPods, false, model.NotInspected, "NodeLocal DNSCache provider detection was incomplete")}
	}
	s.Expansion.NodeLocalDNS = &model.NodeLocalDNSEvidence{
		Resource: projectedIdentity("DaemonSet", configuration), Created: configuration.CreationTimestamp.Time,
		Generation: configuration.Generation, ObservedGeneration: configuration.Status.ObservedGeneration,
		Desired: configuration.Status.DesiredNumberScheduled, Ready: configuration.Status.NumberReady,
		Deleting: configuration.DeletionTimestamp != nil,
	}
	hosts := map[string]bool{}
	if namespace != "" {
		for _, pod := range s.Pods {
			if pod.Spec.NodeName != "" && pod.DeletionTimestamp == nil && pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
				hosts[pod.Spec.NodeName] = true
			}
		}
		if len(hosts) == 0 {
			if s.Coverage(model.SourcePods) != model.Complete {
				return []model.SourceResult{result, observed(model.SourceNodeLocalPods, false, model.NotInspected, "namespace hosting nodes could not be determined")}
			}
			return []model.SourceResult{result, observed(model.SourceNodeLocalPods, false, model.NotApplicable, "no active namespaced Pod had an observed hosting node")}
		}
	}
	var pods model.SourceResult
	s.Expansion.NodeLocalPods, pods = readPages(ctx, model.SourceNodeLocalPods, false, metav1.ListOptions{LabelSelector: nodeLocalSelector},
		c.core.Pods(dnsNamespace).List, func(list *core.PodList) []core.Pod { return list.Items }, func(pod core.Pod) (model.NodeLocalPodEvidence, bool) {
			if namespace != "" && !hosts[pod.Spec.NodeName] {
				return model.NodeLocalPodEvidence{}, false
			}
			projected := projectNodeLocalPod(pod)
			return projected, projected.Owner != nil && *projected.Owner == s.Expansion.NodeLocalDNS.Resource
		})
	return []model.SourceResult{result, pods}
}

func (c *Collector) collectClusterProvenance(ctx context.Context, s *model.Snapshot) model.SourceResult {
	if err := ctx.Err(); err != nil {
		return collectionError(model.SourceProvenance, false, err)
	}
	found, err := timedRead(ctx, model.SourceProvenance, dnsNamespace, func() (*core.Namespace, error) {
		return c.core.Namespaces().Get(requestContext(ctx), dnsNamespace, metav1.GetOptions{})
	})
	if err != nil {
		return namedObservation(model.SourceProvenance, false, dnsNamespace, err)
	}
	return captureProvenance(s, dnsNamespace, model.KubeSystemUIDProvenance, found)
}

func captureProvenance(s *model.Snapshot, namespace, method string, observedNamespace *core.Namespace) model.SourceResult {
	if observedNamespace == nil || observedNamespace.Name != namespace || token(string(observedNamespace.UID)) == "" {
		return namedObservation(model.SourceProvenance, false, namespace, errUnexpectedNamedObject)
	}
	digest := sha256.Sum256([]byte("k8s-namespace-uid:" + namespace + ":" + string(observedNamespace.UID)))
	s.ClusterID = hex.EncodeToString(digest[:])
	s.ClusterProvenance = method
	return namedObservation(model.SourceProvenance, false, namespace, nil)
}

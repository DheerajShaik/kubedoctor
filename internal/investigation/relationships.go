package investigation

import (
	"cmp"
	"slices"
	"strconv"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	maxRelationships       = 16_000
	maxSelectorEvaluations = 200_000
)

func addExpansionInventory(s model.Snapshot, index *inventory) {
	for _, statefulSet := range s.Expansion.StatefulSets {
		index.add(statefulSet.Resource, model.SourceStatefulSets, statefulSet.Created, statefulSet.Desired > 0 && !statefulSet.Deleting,
			statefulSet.Generation > 0 && statefulSet.ObservedGeneration == statefulSet.Generation, statefulSet.Ready > 0)
		index.expectPods(statefulSet.Resource, statefulSet.Desired)
	}
	for _, job := range s.Expansion.Jobs {
		terminal := slices.ContainsFunc(job.Conditions, func(condition model.BriefCondition) bool {
			return (condition.Type == "Complete" || condition.Type == "Failed") && condition.Status == "True"
		})
		index.add(job.Resource, model.SourceJobs, job.Created, !terminal && !job.Deleting && !job.Suspended,
			!job.Started.IsZero() || terminal || job.Suspended, !job.Completed.IsZero())
	}
	for _, cronJob := range s.Expansion.CronJobs {
		index.add(cronJob.Resource, model.SourceCronJobs, cronJob.Created, !cronJob.Suspended && !cronJob.Deleting, true, false)
	}
	for _, hpa := range s.Expansion.HPAs {
		index.add(hpa.Resource, model.SourceHPAs, hpa.Created, !hpa.Deleting,
			hpa.Generation > 0 && hpa.ObservedGeneration == hpa.Generation, false)
	}
	for _, quota := range s.Expansion.Quotas {
		index.add(quota.Resource, model.SourceQuotas, quota.Created, true, true, false)
	}
	for _, limit := range s.Expansion.LimitRanges {
		index.add(limit.Resource, model.SourceLimitRanges, limit.Created, true, true, false)
	}
	if dns := s.Expansion.NodeLocalDNS; dns != nil {
		if _, present := index.resolve(dns.Resource); !present {
			index.add(dns.Resource, model.SourceNodeLocalDNS, dns.Created, dns.Desired > 0,
				dns.Generation > 0 && dns.ObservedGeneration == dns.Generation, dns.Ready > 0)
		}
	}
	for _, pod := range s.Expansion.NodeLocalPods {
		if _, present := index.resolve(pod.Pod); !present {
			index.add(pod.Pod, model.SourceNodeLocalPods, time.Time{}, !pod.Deleting, pod.Ready != "", pod.Ready == "True")
		}
	}
}

type graphBuilder struct {
	inventory     *inventory
	relationships []Relationship
	clipped       bool
	selectors     int
}

func buildRelationships(s model.Snapshot, inv *inventory, output *Investigation) []Relationship {
	graph := graphBuilder{inventory: inv, relationships: []Relationship{}}
	graph.addOwners(s)
	graph.addPodDependencies(s.Pods, model.SourcePods)
	graph.addPodDependencies(s.DNSPods, model.SourceDNSPods)
	for _, pod := range s.Expansion.NodeLocalPods {
		if pod.Node != "" {
			graph.add(PodNode, pod.Pod, model.Resource{Kind: "Node", Name: pod.Node}, model.SourceNodeLocalPods, MatchName, evidence("field", "spec.nodeName"))
		}
	}
	graph.addBackends(s.EndpointSlices, model.SourceEndpointSlices)
	graph.addBackends(s.DNSEndpointSlices, model.SourceDNSEndpointSlices)
	graph.addLegacyBackends(s.LegacyEndpoints, model.SourceLegacyEndpoints)
	graph.addLegacyBackends(s.DNSLegacyEndpoints, model.SourceDNSLegacyEndpoints)
	for i := range s.Ingresses {
		ingress := &s.Ingresses[i]
		resource := model.Identity("Ingress", ingress)
		addBackend := func(backend networking.IngressBackend, field string) {
			if backend.Service == nil {
				return
			}
			reference := backend.Service
			port := reference.Port.Name
			if port == "" {
				port = strconv.Itoa(int(reference.Port.Number))
			}
			graph.add(IngressService, resource, model.Resource{Kind: "Service", Namespace: ingress.Namespace, Name: reference.Name},
				model.SourceIngresses, MatchName, evidence("field", field), evidence("port", port))
		}
		if ingress.Spec.DefaultBackend != nil {
			addBackend(*ingress.Spec.DefaultBackend, "spec.defaultBackend.service")
		}
		for _, rule := range ingress.Spec.Rules {
			if rule.HTTP != nil {
				for _, path := range rule.HTTP.Paths {
					addBackend(path.Backend, "spec.rules.http.paths.backend.service")
				}
			}
		}
	}
	for _, hpa := range s.Expansion.HPAs {
		graph.add(AutoscalingTarget, hpa.Resource, hpa.Target, model.SourceHPAs, MatchName, evidence("field", "spec.scaleTargetRef"))
	}
	for _, cronJob := range s.Expansion.CronJobs {
		for _, job := range cronJob.ActiveJobs {
			graph.add(CronJobActiveJob, cronJob.Resource, job, model.SourceCronJobs, basisOf(job), evidence("field", "status.active"))
		}
	}
	graph.selectorsFor(s)
	if graph.clipped {
		output.Truncated = true
		output.Limitations = append(output.Limitations, "Relationship or selector evaluation reached its budget. Some links could not be represented.")
	}
	slices.SortFunc(graph.relationships, compareRelationship)
	return slices.CompactFunc(graph.relationships, func(a, b Relationship) bool { return compareRelationship(a, b) == 0 })
}

func (graph *graphBuilder) add(kind RelationshipKind, from, to model.Resource, source model.Source, basis MatchBasis, facts ...model.Evidence) {
	if from.Name == "" || to.Name == "" || to.Kind == "" {
		return
	}
	if len(graph.relationships) >= maxRelationships {
		graph.clipped = true
		return
	}
	if resolved, ok := graph.inventory.resolve(from); ok {
		from = resolved
	}
	resolved, observed := graph.inventory.resolve(to)
	graph.relationships = append(graph.relationships, Relationship{Kind: kind, From: from, To: resolved,
		Source: source, Basis: basis, TargetObserved: observed, Evidence: facts})
}

func basisOf(resource model.Resource) MatchBasis {
	if resource.UID != "" {
		return MatchUID
	}
	return MatchName
}

func (graph *graphBuilder) owner(from, to model.Resource, source model.Source, controller bool) {
	graph.add(Owns, from, to, source, basisOf(to), evidence("field", "metadata.ownerReferences"), evidence("controller", strconv.FormatBool(controller)))
	if controller && from.UID != "" && to.UID != "" && from.Namespace == to.Namespace {
		if observed, ok := graph.inventory.resolve(from); ok && observed.UID == from.UID {
			if validated, ok := graph.inventory.resolve(to); ok && validated.UID == to.UID {
				graph.inventory.owners[from] = validated
			}
		}
	}
}

func (graph *graphBuilder) addOwners(s model.Snapshot) {
	addObject := func(kind string, source model.Source, object metav1.Object) {
		from := model.Identity(kind, object)
		for _, ref := range object.GetOwnerReferences() {
			to := model.Resource{Kind: ref.Kind, Namespace: object.GetNamespace(), Name: ref.Name, UID: string(ref.UID)}
			graph.owner(from, to, source, ref.Controller != nil && *ref.Controller)
		}
	}
	for i := range s.Pods {
		addObject("Pod", model.SourcePods, &s.Pods[i])
	}
	for i := range s.DNSPods {
		addObject("Pod", model.SourceDNSPods, &s.DNSPods[i])
	}
	for i := range s.ReplicaSets {
		addObject("ReplicaSet", model.SourceReplicaSets, &s.ReplicaSets[i])
	}
	for i := range s.PVCs {
		addObject("PersistentVolumeClaim", model.SourcePVCs, &s.PVCs[i])
	}
	for _, job := range s.Expansion.Jobs {
		if job.Owner != nil {
			graph.owner(job.Resource, *job.Owner, model.SourceJobs, true)
		}
	}
	for _, pod := range s.Expansion.NodeLocalPods {
		if pod.Owner != nil {
			graph.owner(pod.Pod, *pod.Owner, model.SourceNodeLocalPods, true)
		}
	}
}

func (graph *graphBuilder) addPodDependencies(pods []core.Pod, source model.Source) {
	for i := range pods {
		pod := &pods[i]
		resource := model.Identity("Pod", pod)
		if pod.Spec.NodeName != "" {
			graph.add(PodNode, resource, model.Resource{Kind: "Node", Name: pod.Spec.NodeName}, source, MatchName, evidence("field", "spec.nodeName"))
		}
		addConfig := func(kind, name, field string, optional *bool) {
			graph.add(PodConfig, resource, model.Resource{Kind: kind, Namespace: pod.Namespace, Name: name}, source, MatchName,
				evidence("field", field), evidence("optional", strconv.FormatBool(optional != nil && *optional)))
		}
		for _, volume := range pod.Spec.Volumes {
			if item := volume.PersistentVolumeClaim; item != nil {
				graph.add(PodStorage, resource, model.Resource{Kind: "PersistentVolumeClaim", Namespace: pod.Namespace, Name: item.ClaimName},
					source, MatchName, evidence("field", "spec.volumes.persistentVolumeClaim"))
			}
			if item := volume.ConfigMap; item != nil {
				addConfig("ConfigMap", item.Name, "spec.volumes.configMap", item.Optional)
			}
			if item := volume.Secret; item != nil {
				addConfig("Secret", item.SecretName, "spec.volumes.secret", item.Optional)
			}
			if item := volume.Projected; item != nil {
				for _, projected := range item.Sources {
					if cm := projected.ConfigMap; cm != nil {
						addConfig("ConfigMap", cm.Name, "spec.volumes.projected.configMap", cm.Optional)
					}
					if secret := projected.Secret; secret != nil {
						addConfig("Secret", secret.Name, "spec.volumes.projected.secret", secret.Optional)
					}
				}
			}
		}
		for _, group := range [][]core.Container{pod.Spec.Containers, pod.Spec.InitContainers} {
			for _, container := range group {
				for _, from := range container.EnvFrom {
					if item := from.ConfigMapRef; item != nil {
						addConfig("ConfigMap", item.Name, "container.envFrom.configMapRef", item.Optional)
					}
					if item := from.SecretRef; item != nil {
						addConfig("Secret", item.Name, "container.envFrom.secretRef", item.Optional)
					}
				}
				for _, item := range container.Env {
					if item.ValueFrom == nil {
						continue
					}
					if ref := item.ValueFrom.ConfigMapKeyRef; ref != nil {
						addConfig("ConfigMap", ref.Name, "container.env.valueFrom.configMapKeyRef", ref.Optional)
					}
					if ref := item.ValueFrom.SecretKeyRef; ref != nil {
						addConfig("Secret", ref.Name, "container.env.valueFrom.secretKeyRef", ref.Optional)
					}
				}
			}
		}
	}
}

func (graph *graphBuilder) addBackends(endpointSlices []discovery.EndpointSlice, source model.Source) {
	for i := range endpointSlices {
		item := &endpointSlices[i]
		resource := model.Identity("EndpointSlice", item)
		declared := false
		for _, ref := range item.OwnerReferences {
			if ref.Kind == "Service" && ref.UID != "" {
				declared = true
				graph.add(ServiceBackend, resource, model.Resource{Kind: "Service", Namespace: item.Namespace, Name: ref.Name, UID: string(ref.UID)},
					source, MatchUID, evidence("field", "metadata.ownerReferences"))
			}
		}
		if !declared {
			graph.add(ServiceBackend, resource, model.Resource{Kind: "Service", Namespace: item.Namespace, Name: item.Labels[discovery.LabelServiceName]},
				source, MatchLabel, evidence("field", discovery.LabelServiceName))
		}
		for _, endpoint := range item.Endpoints {
			if endpoint.TargetRef == nil {
				continue
			}
			target := objectReference(*endpoint.TargetRef, item.Namespace)
			graph.add(EndpointTarget, resource, target, source, basisOf(target),
				evidence("field", "endpoints.targetRef"), evidence("ready", triState(endpoint.Conditions.Ready)),
				evidence("serving", triState(endpoint.Conditions.Serving)), evidence("terminating", triState(endpoint.Conditions.Terminating)))
		}
	}
}

func (graph *graphBuilder) addLegacyBackends(endpoints []core.Endpoints, source model.Source) {
	for i := range endpoints {
		item := &endpoints[i]
		resource := model.Identity("Endpoints", item)
		graph.add(ServiceBackend, resource, model.Resource{Kind: "Service", Namespace: item.Namespace, Name: item.Name},
			source, MatchLabel, evidence("field", "legacy Endpoints name"))
		for _, subset := range item.Subsets {
			for _, addresses := range []struct {
				ready bool
				items []core.EndpointAddress
			}{{true, subset.Addresses}, {false, subset.NotReadyAddresses}} {
				for _, address := range addresses.items {
					if address.TargetRef == nil {
						continue
					}
					target := objectReference(*address.TargetRef, item.Namespace)
					graph.add(EndpointTarget, resource, target, source, basisOf(target), evidence("field", "subsets.addresses.targetRef"), evidence("ready", strconv.FormatBool(addresses.ready)))
				}
			}
		}
	}
}

func triState(value *bool) string {
	if value == nil {
		return "unspecified"
	}
	return strconv.FormatBool(*value)
}

func objectReference(ref core.ObjectReference, namespace string) model.Resource {
	if ref.Namespace != "" {
		namespace = ref.Namespace
	}
	return model.Resource{Kind: ref.Kind, Namespace: namespace, Name: ref.Name, UID: string(ref.UID)}
}

func (graph *graphBuilder) selectorsFor(s model.Snapshot) {
	pods := make(map[string][]*core.Pod)
	for i := range s.Pods {
		pod := &s.Pods[i]
		pods[pod.Namespace] = append(pods[pod.Namespace], pod)
	}
	for _, namespacePods := range pods {
		slices.SortFunc(namespacePods, func(a, b *core.Pod) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.UID, b.UID)) })
	}
	services := make([]*core.Service, len(s.Services))
	for i := range s.Services {
		services[i] = &s.Services[i]
	}
	slices.SortFunc(services, func(a, b *core.Service) int {
		return compareResource(model.Identity("Service", a), model.Identity("Service", b))
	})
	for _, service := range services {
		if len(service.Spec.Selector) == 0 || service.Spec.Type == core.ServiceTypeExternalName {
			continue
		}
		if !graph.addSelector(ServiceSelectsPod, model.Identity("Service", service), model.SourceServices, labels.SelectorFromSet(service.Spec.Selector), pods[service.Namespace]) {
			return
		}
	}
	for i := range s.PDBs {
		budget := &s.PDBs[i]
		selector, err := metav1.LabelSelectorAsSelector(budget.Spec.Selector)
		if err == nil && !graph.addSelector(DisruptionTarget, model.Identity("PodDisruptionBudget", budget), model.SourcePDBs, selector, pods[budget.Namespace]) {
			return
		}
	}
}

func (graph *graphBuilder) addSelector(kind RelationshipKind, from model.Resource, source model.Source, selector labels.Selector, pods []*core.Pod) bool {
	for _, pod := range pods {
		if graph.selectors == maxSelectorEvaluations || len(graph.relationships) == maxRelationships {
			graph.clipped = true
			return false
		}
		graph.selectors++
		if selector.Matches(labels.Set(pod.Labels)) {
			graph.add(kind, from, model.Identity("Pod", pod), source, MatchSelector, evidence("field", "spec.selector"))
		}
	}
	return true
}

func compareRelationship(a, b Relationship) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), compareResource(a.From, b.From), compareResource(a.To, b.To), cmp.Compare(a.Source, b.Source),
		cmp.Compare(a.Basis, b.Basis), slices.CompareFunc(a.Evidence, b.Evidence, func(a, b model.Evidence) int {
			return cmp.Or(cmp.Compare(a.Field, b.Field), cmp.Compare(a.Value, b.Value))
		}))
}

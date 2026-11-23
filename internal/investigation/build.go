package investigation

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"slices"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/history"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const maxIncidentReferences = 64_000

func Build(snapshot model.Snapshot, report model.Report) Investigation {
	inv := Investigation{
		SchemaVersion: SchemaVersion, RuleVersion: model.AnalyzerVersion, CaptureRuleVersion: model.AnalyzerVersion,
		AnalysisMode: "replay", ClusterID: snapshot.ClusterID, ClusterProvenance: snapshot.ClusterProvenance, Scope: report.Scope,
		CaptureStartedAt: snapshot.StartedAt.UTC(), CaptureCompletedAt: snapshot.FinishedAt.UTC(),
		EvaluatedAt: snapshot.CollectedAt.UTC(), SourceTimings: append([]model.SourceTiming{}, snapshot.SourceTimings...),
		Report: report, Limitations: []string{
			"Resource observations may come from different requests. The capture is an interval, not an atomic cluster snapshot.",
			"Relationships and timestamps establish association. Confirm application impact and cause with independent evidence.",
		},
	}
	slices.SortFunc(inv.SourceTimings, func(a, b model.SourceTiming) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Resource, b.Resource), a.StartedAt.Compare(b.StartedAt), a.FinishedAt.Compare(b.FinishedAt))
	})
	if inv.ClusterID == "" {
		inv.Limitations = append(inv.Limitations, "Strong cluster provenance was unavailable. Cross-capture findings cannot be compared safely.")
	}
	inv.History = history.Analyze(snapshot)
	if inv.History == nil {
		inv.Limitations = append(inv.Limitations, "Historical resource evidence was not configured or captured.")
	}
	inventory := newInventory(snapshot)
	inv.Relationships = buildRelationships(snapshot, inventory, &inv)
	inv.Resources = inventory.observations()
	inv.Timeline = buildTimeline(snapshot, inventory, &inv)
	inv.Incidents = groupIncidents(inv)
	linkIncidentEvidence(&inv)
	return inv
}

type resourceName struct{ kind, namespace, name string }

func nameOf(resource model.Resource) resourceName {
	return resourceName{resource.Kind, resource.Namespace, resource.Name}
}

type inventory struct {
	byName map[resourceName]ResourceObservation
	owners map[model.Resource]model.Resource
}

func newInventory(s model.Snapshot) *inventory {
	index := &inventory{byName: make(map[resourceName]ResourceObservation), owners: make(map[model.Resource]model.Resource)}
	add := func(kind string, source model.Source, object metav1.Object, active, fresh, ready bool) {
		resource := model.Identity(kind, object)
		index.add(resource, source, object.GetCreationTimestamp().Time, active, fresh, ready)
	}
	for i := range s.Nodes {
		node := &s.Nodes[i]
		fresh, ready := false, false
		for _, condition := range node.Status.Conditions {
			if condition.Type == core.NodeReady {
				fresh, ready = condition.Status != core.ConditionUnknown, condition.Status == core.ConditionTrue
			}
		}
		add("Node", model.SourceNodes, node, true, fresh, ready)
	}
	for _, podSource := range []struct {
		source model.Source
		pods   []core.Pod
	}{{model.SourceDNSPods, s.DNSPods}, {model.SourcePods, s.Pods}} {
		for i := range podSource.pods {
			pod := &podSource.pods[i]
			active := pod.DeletionTimestamp == nil && pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed
			fresh := pod.Status.Phase != "" && pod.Status.Phase != core.PodUnknown
			ready := slices.ContainsFunc(pod.Status.Conditions, func(condition core.PodCondition) bool {
				return condition.Type == core.PodReady && condition.Status == core.ConditionTrue
			})
			add("Pod", podSource.source, pod, active, fresh, ready && active)
		}
	}
	for i := range s.ReplicaSets {
		item := &s.ReplicaSets[i]
		add("ReplicaSet", model.SourceReplicaSets, item, item.Spec.Replicas == nil || *item.Spec.Replicas > 0,
			item.Generation > 0 && item.Status.ObservedGeneration == item.Generation, item.Status.ReadyReplicas > 0)
		index.expectPods(model.Identity("ReplicaSet", item), replicas(item.Spec.Replicas))
	}
	for i := range s.Deployments {
		item := &s.Deployments[i]
		add("Deployment", model.SourceDeployments, item, item.Spec.Replicas == nil || *item.Spec.Replicas > 0,
			item.Generation > 0 && item.Status.ObservedGeneration == item.Generation, item.Status.AvailableReplicas > 0)
		index.expectPods(model.Identity("Deployment", item), replicas(item.Spec.Replicas))
	}
	for i := range s.DaemonSets {
		item := &s.DaemonSets[i]
		add("DaemonSet", model.SourceDaemonSets, item, item.Status.DesiredNumberScheduled > 0,
			item.Generation > 0 && item.Status.ObservedGeneration == item.Generation, item.Status.NumberAvailable > 0)
		index.expectPods(model.Identity("DaemonSet", item), item.Status.DesiredNumberScheduled)
	}
	for i := range s.PVCs {
		item := &s.PVCs[i]
		add("PersistentVolumeClaim", model.SourcePVCs, item, item.DeletionTimestamp == nil, item.Status.Phase != "", item.Status.Phase == core.ClaimBound)
	}
	for _, cm := range s.ConfigMaps {
		index.add(model.Resource{Kind: "ConfigMap", Namespace: cm.Namespace, Name: cm.Name}, model.SourceConfigMaps, time.Time{}, true, true, true)
	}
	for _, cm := range s.DNSConfig {
		resource := model.Resource{Kind: "ConfigMap", Namespace: cm.Namespace, Name: cm.Name}
		if _, known := index.byName[nameOf(resource)]; !known {
			index.add(resource, model.SourceDNSConfig, time.Time{}, true, true, cm.HasCorefile)
		}
	}
	for i := range s.DNSServices {
		item := &s.DNSServices[i]
		add("Service", model.SourceDNSServices, item, true, true, false)
	}
	for i := range s.Services {
		item := &s.Services[i]
		add("Service", model.SourceServices, item, true, true, false)
	}
	for i := range s.DNSEndpointSlices {
		add("EndpointSlice", model.SourceDNSEndpointSlices, &s.DNSEndpointSlices[i], true, true, false)
	}
	for i := range s.EndpointSlices {
		add("EndpointSlice", model.SourceEndpointSlices, &s.EndpointSlices[i], true, true, false)
	}
	for i := range s.DNSLegacyEndpoints {
		add("Endpoints", model.SourceDNSLegacyEndpoints, &s.DNSLegacyEndpoints[i], true, true, false)
	}
	for i := range s.LegacyEndpoints {
		add("Endpoints", model.SourceLegacyEndpoints, &s.LegacyEndpoints[i], true, true, false)
	}
	for i := range s.Ingresses {
		add("Ingress", model.SourceIngresses, &s.Ingresses[i], true, true, false)
	}
	for i := range s.NetworkPolicies {
		add("NetworkPolicy", model.SourceNetworkPolicies, &s.NetworkPolicies[i], true, true, false)
	}
	for i := range s.PDBs {
		item := &s.PDBs[i]
		add("PodDisruptionBudget", model.SourcePDBs, item, item.Status.ExpectedPods > 0,
			item.Generation > 0 && item.Status.ObservedGeneration == item.Generation, item.Status.DisruptionsAllowed > 0)
	}
	addExpansionInventory(s, index)
	return index
}

func replicas(value *int32) int32 {
	if value == nil {
		return 1
	}
	return *value
}

func (index *inventory) expectPods(resource model.Resource, count int32) {
	key := nameOf(resource)
	if item, ok := index.byName[key]; ok && item.Resource == resource {
		item.ExpectedPods = &count
		index.byName[key] = item
	}
}

func (index *inventory) add(resource model.Resource, source model.Source, created time.Time, active, fresh, ready bool) {
	if resource.Name == "" {
		return
	}
	item := ResourceObservation{Resource: resource, Source: source, Active: active, StatusFresh: fresh, Ready: ready}
	if !created.IsZero() {
		item.CreatedAt = new(created.UTC())
	}
	existing, ok := index.byName[nameOf(resource)]
	if ok && existing.Resource.UID != "" && resource.UID != existing.Resource.UID {
		// The ordinary and conventional DNS passes can see a replacement
		// during one capture. Eliminate ambiguous identity rather than joining.
		item.Resource.UID = ""
		item.StatusFresh = false
	}
	index.byName[nameOf(resource)] = item
}

func (index *inventory) resolve(resource model.Resource) (model.Resource, bool) {
	observed, ok := index.byName[nameOf(resource)]
	if !ok || resource.UID != "" && resource.UID != observed.Resource.UID {
		return resource, false
	}
	return observed.Resource, true
}

func (index *inventory) observations() []ResourceObservation {
	items := make([]ResourceObservation, 0, len(index.byName))
	for _, item := range index.byName {
		if workload, ok := index.workload(item.Resource); ok {
			item.Workload = &workload
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b ResourceObservation) int { return compareResource(a.Resource, b.Resource) })
	return items
}

func (index *inventory) workload(resource model.Resource) (model.Resource, bool) {
	var anchor model.Resource
	current := resource
	for depth := 0; depth < 8; depth++ {
		if current.UID == "" {
			break
		}
		if supportedWorkload(current.Kind) {
			anchor = current
		}
		parent, ok := index.owners[current]
		if !ok {
			return anchor, anchor.UID != ""
		}
		current = parent
	}
	// Cycles and unexpectedly deep ownership chains do not establish an anchor.
	return model.Resource{}, false
}

func supportedWorkload(kind string) bool {
	switch kind {
	case "Deployment", "DaemonSet", "StatefulSet", "CronJob", "Job", "ReplicaSet":
		return true
	}
	return false
}

func compareResource(a, b model.Resource) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name), cmp.Compare(a.UID, b.UID))
}

func evidence(field, value string) model.Evidence {
	return model.Evidence{Field: field, Value: value}
}

func evidenceValue(f model.Finding, field string) string {
	for _, item := range f.Evidence {
		if item.Field == field {
			return item.Value
		}
	}
	return ""
}

func incidentID(anchor model.Resource) string {
	digest := sha256.Sum256(fmt.Appendf(nil, "%q/%q/%q/%q", anchor.Kind, anchor.Namespace, anchor.Name, anchor.UID))
	return fmt.Sprintf("incident-%x", digest[:12])
}

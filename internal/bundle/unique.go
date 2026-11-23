package bundle

import "github.com/DheerajShaik/kubedoctor/internal/model"

func uniqueEvidence(e evidence) bool {
	x := e.Expansion
	return uniqueIdentities(e.Nodes, func(r node) identity { return r.Identity }) &&
		uniqueIdentities(e.Pods, func(r pod) identity { return r.Identity }) &&
		uniqueIdentities(e.ReplicaSets, func(r replicaSet) identity { return r.Identity }) &&
		uniqueIdentities(e.Deployments, func(r deployment) identity { return r.Identity }) &&
		uniqueIdentities(e.DaemonSets, func(r daemonSet) identity { return r.Identity }) &&
		uniqueIdentities(e.ConfigMaps, func(r configMap) identity { return identity{Namespace: r.Namespace, Name: r.Name} }) &&
		uniqueIdentities(e.Claims, func(r claim) identity { return r.Identity }) &&
		uniqueIdentities(e.StorageClasses, func(r storageClass) identity { return r.Identity }) &&
		uniqueIdentities(e.Services, func(r service) identity { return r.Identity }) &&
		uniqueIdentities(e.EndpointSlices, func(r endpointSlice) identity { return r.Identity }) &&
		uniqueIdentities(e.LegacyEndpoints, func(r legacyEndpoints) identity { return r.Identity }) &&
		uniqueIdentities(e.Ingresses, func(r ingress) identity { return r.Identity }) &&
		uniqueIdentities(e.DNSPods, func(r pod) identity { return r.Identity }) &&
		uniqueIdentities(e.DNSServices, func(r service) identity { return r.Identity }) &&
		uniqueIdentities(e.DNSConfig, func(r dnsConfig) identity { return identity{Namespace: r.Namespace, Name: r.Name} }) &&
		uniqueIdentities(e.DNSEndpointSlices, func(r endpointSlice) identity { return r.Identity }) &&
		uniqueIdentities(e.DNSLegacyEndpoints, func(r legacyEndpoints) identity { return r.Identity }) &&
		uniqueIdentities(e.NetworkPolicies, func(r networkPolicy) identity { return r.Identity }) &&
		uniqueIdentities(e.PDBs, func(r pdb) identity { return r.Identity }) &&
		uniqueIdentities(e.Metrics, func(r currentUsage) identity { return identity{Namespace: r.Namespace, Name: r.Name} }) &&
		uniqueIdentities(x.StatefulSets, func(r model.StatefulSetEvidence) identity { return resourceKey(r.Resource) }) &&
		uniqueIdentities(x.Jobs, func(r model.JobEvidence) identity { return resourceKey(r.Resource) }) &&
		uniqueIdentities(x.CronJobs, func(r model.CronJobEvidence) identity { return resourceKey(r.Resource) }) &&
		uniqueIdentities(x.HPAs, func(r model.HPAEvidence) identity { return resourceKey(r.Resource) }) &&
		uniqueIdentities(x.Quotas, func(r model.QuotaEvidence) identity { return resourceKey(r.Resource) }) &&
		uniqueIdentities(x.LimitRanges, func(r model.LimitRangeEvidence) identity { return resourceKey(r.Resource) }) &&
		uniqueIdentities(x.Probes, func(r model.PodProbeEvidence) identity { return resourceKey(r.Pod) }) &&
		uniqueIdentities(x.NodeLocalPods, func(r model.NodeLocalPodEvidence) identity { return resourceKey(r.Pod) })
}

func resourceKey(r model.Resource) identity { return identity{Name: r.Name, Namespace: r.Namespace} }

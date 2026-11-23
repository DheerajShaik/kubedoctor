package bundle

import (
	"crypto/sha256"
	"encoding/base32"
	"slices"
	"strings"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

func (e *evidence) minimize(scope model.Scope) {
	var hosts = make(map[string]bool)
	for _, pod := range e.Pods {
		if shouldContainNode(pod) {
			hosts[pod.HostNode] = true
		}
	}
	e.Events = slices.DeleteFunc(e.Events, func(event classifiedEvent) bool {
		return !validEvent(scope, hosts, event)
	})
	e.Expansion.NodeLocalPods = slices.DeleteFunc(slices.Clone(e.Expansion.NodeLocalPods), func(pod model.NodeLocalPodEvidence) bool {
		return (isNamespaceScope(scope) && !hosts[pod.Node]) ||
			(pod.Owner != nil && pod.Owner.Name != "node-local-dns")
	})
	needed := make(map[string]map[string]bool)
	remember := func(namespace string, keys map[string]string, expressions []expression) {
		if needed[namespace] == nil {
			needed[namespace] = make(map[string]bool)
		}
		for key := range keys {
			needed[namespace][key] = true
		}
		for _, expr := range expressions {
			needed[namespace][expr.Key] = true
		}
	}
	for _, services := range [][]service{e.Services, e.DNSServices} {
		for _, service := range services {
			remember(service.Identity.Namespace, service.Selector, nil)
		}
	}
	for _, p := range e.NetworkPolicies {
		if p.Selector != nil {
			remember(p.Identity.Namespace, p.Selector.Labels, p.Selector.Expressions)
		}
	}
	for _, p := range e.PDBs {
		if p.Selector != nil {
			remember(p.Identity.Namespace, p.Selector.Labels, p.Selector.Expressions)
		}
	}
	// Keep the standard identity for the deliberately label-scoped DNS probes.
	remember("kube-system", map[string]string{"k8s-app": "kube-dns"}, nil)

	configKeys := make(map[configMapKey]map[string]bool)
	for i := range e.Pods {
		pod := &e.Pods[i]
		pod.Identity.Labels = relevantLabels(pod.Identity.Labels, needed[pod.Identity.Namespace])
		rememberKeys(configKeys, *pod)
	}
	for i := range e.DNSPods {
		pod := &e.DNSPods[i]
		pod.Identity.Labels = relevantLabels(pod.Identity.Labels, needed[pod.Identity.Namespace])
		pod.HostNode, pod.Resources = "", nil
		pod.Containers, pod.Init, pod.Volumes = nil, nil, nil
		pod.Statuses, pod.InitStatuses = nil, nil
	}
	for _, services := range [][]service{e.Services, e.DNSServices} {
		for i := range services {
			services[i].Selector = fingerprintLabels(services[i].Selector, nil)
		}
	}
	for i := range e.NetworkPolicies {
		e.NetworkPolicies[i].Selector = fingerprintSelector(e.NetworkPolicies[i].Selector)
	}
	for i := range e.PDBs {
		e.PDBs[i].Selector = fingerprintSelector(e.PDBs[i].Selector)
	}
	for i := range e.Deployments {
		d := &e.Deployments[i]
		d.TemplateLabels = relevantLabels(d.TemplateLabels, needed[d.Identity.Namespace])
	}
	for i := range e.DaemonSets {
		d := &e.DaemonSets[i]
		d.TemplateLabels = relevantLabels(d.TemplateLabels, needed[d.Identity.Namespace])
	}
	e.ConfigMaps = slices.DeleteFunc(e.ConfigMaps, func(value configMap) bool {
		return configKeys[configMapKey{value.Namespace, value.Name}] == nil
	})
	for i := range e.ConfigMaps {
		value := &e.ConfigMaps[i]
		keys := configKeys[configMapKey{value.Namespace, value.Name}]
		value.Keys = referencedKeys(value.Keys, keys)
		value.Binary = referencedKeys(value.Binary, keys)
	}
}

type configMapKey struct{ Namespace, Name string }

func rememberKeys(keys map[configMapKey]map[string]bool, p pod) {
	remember := func(ref configReference) {
		if ref.Kind != "ConfigMap" {
			return
		}
		object := configMapKey{p.Identity.Namespace, ref.Name}
		if keys[object] == nil {
			keys[object] = make(map[string]bool)
		}
		if ref.Key != "" {
			keys[object][ref.Key] = true
		}
		for _, key := range ref.Keys {
			keys[object][key] = true
		}
	}
	for _, containers := range [][]container{p.Containers, p.Init} {
		for _, c := range containers {
			for _, ref := range c.Environment {
				remember(ref)
			}
			for _, ref := range c.Imports {
				remember(ref)
			}
		}
	}
	for _, v := range p.Volumes {
		for _, ref := range v.Direct {
			remember(ref)
		}
		for _, ref := range v.Projection {
			remember(ref)
		}
	}
}

func referencedKeys(original []string, references map[string]bool) []string {
	var kept []string
	for _, key := range original {
		if references[key] {
			kept = append(kept, key)
		}
	}
	return kept
}

func relevantLabels(labels map[string]string, keys map[string]bool) map[string]string {
	if len(keys) == 0 {
		return nil
	}
	return fingerprintLabels(labels, keys)
}

func fingerprintLabels(labels map[string]string, keys map[string]bool) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for key, value := range labels {
		if keys == nil || keys[key] {
			out[key] = fingerprintLabel(key, value)
		}
	}
	return out
}

var labelEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func fingerprintLabel(key, value string) string {
	if key == "k8s-app" && value == "kube-dns" {
		return value
	}
	sum := sha256.Sum256([]byte("kubedoctor-label-v1\x00" + key + "\x00" + value))
	return "sha256-" + strings.ToLower(labelEncoding.EncodeToString(sum[:]))
}

func validLabelFingerprint(key, value string) bool {
	if key == "k8s-app" && value == "kube-dns" {
		return true
	}
	encoded, found := strings.CutPrefix(value, "sha256-")
	if !found || len(encoded) != 52 || encoded != strings.ToLower(encoded) {
		return false
	}
	bytes, err := labelEncoding.DecodeString(strings.ToUpper(encoded))
	return err == nil && len(bytes) == sha256.Size
}

func podsAreDNSInfrastructure(namespace string, labels map[string]string) bool {
	return namespace == "kube-system" && labels["k8s-app"] == "kube-dns"
}

func fingerprintSelector(original *selector) *selector {
	if original == nil {
		return nil
	}
	out := &selector{Labels: fingerprintLabels(original.Labels, nil)}
	out.Expressions = mapItems(original.Expressions, func(e expression) expression {
		return expression{
			Key: e.Key, Operator: e.Operator,
			Values: mapItems(e.Values, func(v string) string { return fingerprintLabel(e.Key, v) }),
		}
	})
	return out
}

func shouldContainNode(p pod) bool {
	return p.HostNode != "" && p.Identity.DeletedAt == nil && p.Phase != "Succeeded" && p.Phase != "Failed"
}

func isNamespaceScope(s model.Scope) bool { return s.Kind == model.NamespaceScope }

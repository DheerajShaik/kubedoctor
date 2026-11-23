package history

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type sourceTarget struct {
	target  model.HistoryTarget
	runtime string
}

type targetKey struct {
	namespace, pod, uid, container, runtime string
}

func (t sourceTarget) key() targetKey {
	return targetKey{t.target.Pod.Namespace, t.target.Pod.Name, t.target.Pod.UID, t.target.Container, t.runtime}
}

// Enrich records historical source timing as part of the capture envelope
// while preserving the Kubernetes evaluation time.
func (p *Provider) Enrich(ctx context.Context, s *model.Snapshot) {
	s.History = p.Capture(ctx, *s)
	if s.History.FinishedAt.After(s.FinishedAt) {
		s.FinishedAt = s.History.FinishedAt
	}
}

// Capture queries the configured provider after Kubernetes collection. Provider
// errors become evidence about this source. They leave Kubernetes coverage intact.
func (p *Provider) Capture(ctx context.Context, s model.Snapshot) *model.HistoryCapture {
	end := s.CollectedAt.UTC().Truncate(time.Second)
	capture := &model.HistoryCapture{
		SchemaVersion: model.HistorySchemaVersion, StartedAt: time.Now().UTC(),
		WindowStart: end.Add(-p.window), WindowEnd: end,
		ScrapeSeconds: int64(p.scrape / time.Second), StepSeconds: int64(p.step / time.Second),
		Targets: []model.HistoryTargetCapture{},
	}
	defer func() { capture.FinishedAt = time.Now().UTC() }()
	if p.grafana != nil {
		dashboard := *p.grafana
		capture.Grafana = &dashboard
	}
	targets, incomplete, truncated := p.selectTargets(s)
	if len(targets) == 0 {
		capture.Status, capture.Reason = model.HistoryUnavailable, model.HistoryNoTargets
		return capture
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	cpu := p.readMetric(ctx, p.cpu, targets, capture.WindowStart, capture.WindowEnd)
	memory := p.readMetric(ctx, p.memory, targets, capture.WindowStart, capture.WindowEnd)
	for _, target := range targets {
		capture.Targets = append(capture.Targets, model.HistoryTargetCapture{
			Target: target.target, CPUCounter: cpu[target.key()], Memory: memory[target.key()],
		})
	}
	capture.Status, capture.Reason = summarizeCapture(capture.Targets)
	if incomplete && capture.Status == model.HistoryComplete {
		capture.Status, capture.Reason = model.HistoryPartial, model.HistoryNoTargets
	}
	if truncated {
		capture.Status, capture.Reason = model.HistoryTruncated, model.HistoryTargetsLimited
	}
	return capture
}

func (p *Provider) selectTargets(s model.Snapshot) ([]sourceTarget, bool, bool) {
	if s.Scope.Kind != model.ClusterScope && s.Scope.Kind != model.NamespaceScope {
		return nil, true, false
	}
	index := indexOwners(s)
	pods := make([]*core.Pod, len(s.Pods))
	for i := range s.Pods {
		pods[i] = &s.Pods[i]
	}
	slices.SortFunc(pods, func(a, b *core.Pod) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name), cmp.Compare(a.UID, b.UID))
	})
	seen := make(map[model.HistoryTarget]bool, maxTargets)
	var matches []sourceTarget
	incomplete := s.Coverage(model.SourcePods) != model.Complete
	truncated := false
	for _, policy := range p.targets {
		if s.Scope.Kind == model.NamespaceScope && s.Scope.Namespace != policy.Namespace {
			continue
		}
		matched := false
		for _, pod := range pods {
			if pod.Namespace != policy.Namespace || !dnsName(pod.Name) || !simpleID.MatchString(string(pod.UID)) ||
				!slices.ContainsFunc(pod.Spec.Containers, func(c core.Container) bool { return c.Name == policy.Container }) {
				continue
			}
			var workload model.Resource
			for _, ancestor := range index.ancestors(pod) {
				if ancestor.Kind == policy.Kind && ancestor.Name == policy.Name {
					workload = ancestor
					break
				}
			}
			if workload.UID == "" {
				continue
			}
			matched = true
			podIdentity := model.Identity("Pod", pod)
			identity := model.HistoryTarget{
				Pod: podIdentity, Workload: workload, Container: policy.Container, VPADisabled: policy.VPADisabled,
			}
			key := model.HistoryTarget{Pod: podIdentity, Container: policy.Container}
			if seen[key] {
				incomplete = true
				continue
			}
			if len(matches) == maxTargets {
				truncated = true
				continue
			}
			seen[key] = true
			target := sourceTarget{target: identity}
			if i := containerStatus(pod, policy.Container); i >= 0 {
				id := pod.Status.ContainerStatuses[i].ContainerID
				if len(id) <= 100 && runtimeID.MatchString(id) {
					target.runtime = id
				}
			}
			matches = append(matches, target)
		}
		if !matched {
			incomplete = true
		}
	}
	slices.SortFunc(matches, func(a, b sourceTarget) int {
		return compareTarget(a.target, b.target)
	})
	return matches, incomplete, truncated
}

func compareTarget(a, b model.HistoryTarget) int {
	return cmp.Or(cmp.Compare(a.Pod.Namespace, b.Pod.Namespace), cmp.Compare(a.Pod.Name, b.Pod.Name),
		cmp.Compare(a.Pod.UID, b.Pod.UID), cmp.Compare(a.Container, b.Container))
}

func summarizeCapture(targets []model.HistoryTargetCapture) (model.HistoryStatus, model.HistoryReason) {
	firstFault, reason := model.HistoryStatus(""), model.HistoryReason("")
	someAvailable := false
	for _, target := range targets {
		for _, series := range []model.HistorySeries{target.CPUCounter, target.Memory} {
			if series.Status == model.HistoryComplete || len(series.Points) > 0 {
				someAvailable = true
			}
			if series.Status != model.HistoryComplete && firstFault == "" {
				firstFault, reason = series.Status, series.Reason
			}
		}
	}
	if firstFault != "" {
		if someAvailable {
			return model.HistoryPartial, reason
		}
		return firstFault, reason
	}
	return model.HistoryComplete, ""
}

type ownerIndex map[model.Resource][]metav1.OwnerReference

func indexOwners(s model.Snapshot) ownerIndex {
	index := make(ownerIndex, len(s.ReplicaSets))
	for i := range s.ReplicaSets {
		rs := &s.ReplicaSets[i]
		index[model.Identity("ReplicaSet", rs)] = rs.OwnerReferences
	}
	return index
}

func (index ownerIndex) ancestors(pod *core.Pod) []model.Resource {
	identities := []model.Resource{model.Identity("Pod", pod)}
	owner, ok := controller(pod.Namespace, pod.OwnerReferences)
	if !ok {
		return identities
	}
	identities = append(identities, owner)
	if owner.Kind == "ReplicaSet" {
		if parent, ok := controller(pod.Namespace, index[owner]); ok && parent.Kind == "Deployment" {
			identities = append(identities, parent)
		}
	}
	return identities
}

func controller(namespace string, owners []metav1.OwnerReference) (model.Resource, bool) {
	for _, ref := range owners {
		if ref.Controller != nil && *ref.Controller && builtInOwner(ref) && dnsName(ref.Name) && simpleID.MatchString(string(ref.UID)) {
			return model.Resource{Kind: ref.Kind, Namespace: namespace, Name: ref.Name, UID: string(ref.UID)}, true
		}
	}
	return model.Resource{}, false
}

func builtInOwner(ref metav1.OwnerReference) bool {
	switch ref.Kind {
	case "Deployment", "ReplicaSet", "DaemonSet", "StatefulSet":
		return ref.APIVersion == "apps/v1"
	case "Job":
		return ref.APIVersion == "batch/v1"
	default:
		return false
	}
}

func containerStatus(pod *core.Pod, name string) int {
	return slices.IndexFunc(pod.Status.ContainerStatuses, func(s core.ContainerStatus) bool { return s.Name == name })
}

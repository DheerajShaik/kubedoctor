package investigation

import (
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
)

type containerIdentity struct{ name, role string }

type podContainerContinuity struct {
	declared map[model.Resource]map[containerIdentity]bool
	members  map[model.Resource]map[containerIdentity]int32
}

func podContainerEvidence(s model.Snapshot, index *inventory) podContainerContinuity {
	facts := podContainerContinuity{
		declared: make(map[model.Resource]map[containerIdentity]bool, len(s.Pods)),
		members:  make(map[model.Resource]map[containerIdentity]int32),
	}
	for i := range s.Pods {
		pod := &s.Pods[i]
		id := model.Identity("Pod", pod)
		if observed := index.byName[nameOf(id)]; id.UID == "" || observed.Resource != id || !observed.Active {
			continue
		}
		declared := make(map[containerIdentity]bool, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
		for _, group := range []struct {
			role       string
			containers []core.Container
		}{{"application", pod.Spec.Containers}, {"init", pod.Spec.InitContainers}} {
			for _, container := range group.containers {
				if container.Name != "" {
					declared[containerIdentity{container.Name, group.role}] = true
				}
			}
		}
		facts.declared[id] = declared
		if owner, ok := index.workload(id); ok {
			if facts.members[owner] == nil {
				facts.members[owner] = make(map[containerIdentity]int32)
			}
			for identity := range declared {
				facts.members[owner][identity]++
			}
		}
	}
	return facts
}

func podContainerRecoveryLimitation(left, right compareSnapshot, subject model.Resource, finding model.Finding) string {
	// Object-level Pod continuity is already pinned by the immutable Pod UID.
	// Workload continuity allows replica replacement, including new Pod specs.
	if finding.Resource.Kind != "Pod" || !supportedWorkload(subject.Kind) ||
		(finding.RuleID != "pod-containers" && finding.RuleID != "container-resources") {
		return ""
	}
	identity := containerIdentity{evidenceValue(finding, "container"), evidenceValue(finding, "container_role")}
	if identity.name != "" && identity.role == "" {
		// Older or stored reports can omit the role. Infer it only when a
		// single baseline declaration unambiguously identifies the container.
		declared := left.inv.applicability.pods.declared[finding.Resource]
		application := declared[containerIdentity{identity.name, "application"}]
		init := declared[containerIdentity{identity.name, "init"}]
		if application && !init {
			identity.role = "application"
		} else if init && !application {
			identity.role = "init"
		}
	}
	if identity.name == "" || identity.role != "application" && identity.role != "init" {
		return "The baseline did not identify the implicated container and its role."
	}
	active := right.podHealth[subject].active
	if active == 0 || right.inv.applicability.pods.members[subject][identity] != active {
		return "The implicated container name and role are missing from one or more of the later Ready workload Pods."
	}
	return ""
}

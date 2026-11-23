package investigation

import (
	"strings"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestWorkloadRecoveryRetainsImplicatedContainerIdentity(t *testing.T) {
	for _, rule := range []string{"pod-containers", "container-resources"} {
		t.Run(rule, func(t *testing.T) {
			before, _ := fixtureSnapshot(baselineTime, true, "original-pod")
			original := &before.Pods[0]
			original.Spec.Containers = []core.Container{continuityContainer("api")}
			original.Status.ContainerStatuses = []core.ContainerStatus{readyContainer("api")}
			if rule == "pod-containers" {
				original.Status.Conditions[0].Status = core.ConditionFalse
				original.Status.ContainerStatuses[0].Ready = false
				original.Status.ContainerStatuses[0].State = core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
			} else {
				original.Spec.Containers[0].Resources.Requests = nil
			}
			baseline := Build(before, engine.Analyze(before))
			for _, scenario := range []struct {
				name   string
				modify func(*model.Snapshot)
				want   Classification
			}{
				{"same container after replica churn", func(*model.Snapshot) {}, Resolved},
				{"renamed application container", func(s *model.Snapshot) {
					s.Pods[0].Spec.Containers[0].Name = "replacement"
					s.Pods[0].Status.ContainerStatuses[0].Name = "replacement"
				}, NoLongerObservable},
				{"same name now belongs to an init container", func(s *model.Snapshot) {
					s.Pods[0].Spec.InitContainers = []core.Container{continuityContainer("api")}
					s.Pods[0].Status.InitContainerStatuses = []core.ContainerStatus{{Name: "api",
						State: core.ContainerState{Terminated: &core.ContainerStateTerminated{Reason: "Completed"}}}}
					s.Pods[0].Spec.Containers[0].Name = "replacement"
					s.Pods[0].Status.ContainerStatuses[0].Name = "replacement"
				}, NoLongerObservable},
				{"only part of the Ready workload population retains it", func(s *model.Snapshot) {
					second := s.Pods[0].DeepCopy()
					second.Name, second.UID = "different-replica", "different-pod"
					second.Spec.Containers[0].Name = "replacement"
					second.Status.ContainerStatuses[0].Name = "replacement"
					s.Pods = append(s.Pods, *second)
					s.Deployments[0].Spec.Replicas = new(int32(2))
					s.Deployments[0].Status.AvailableReplicas = 2
					s.Deployments[0].Status.UpdatedReplicas = 2
				}, NoLongerObservable},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					after, _ := fixtureSnapshot(baselineTime.Add(time.Minute), true, "replacement-pod")
					after.Pods[0].Name = "checkout-new"
					after.Pods[0].Spec.Containers = []core.Container{continuityContainer("api")}
					after.Pods[0].Status.ContainerStatuses = []core.ContainerStatus{readyContainer("api")}
					scenario.modify(&after)
					result := Compare(baseline, Build(after, engine.Analyze(after)))
					change := pickChange(t, result.WorkloadChanges, rule, "deployment-uid")
					if !result.Compatible || change.Classification != scenario.want {
						t.Fatalf("workload comparison expected %s, got %+v", scenario.want, change)
					}
					if scenario.want == NoLongerObservable && (result.Complete || !strings.Contains(change.Reason, "container name and role")) {
						t.Fatalf("workload comparison should explain missing container continuity: %+v", change)
					}
					if scenario.want == Resolved && !result.Complete {
						t.Fatalf("valid replica churn should still establish comparable recovery: %+v", result.Limitations)
					}
					if original := pickChange(t, result.ResourceChanges, rule, "original-pod"); original.Classification != NoLongerObservable {
						t.Fatalf("original Pod UID no longer exists: %+v", original)
					}
				})
			}
		})
	}
}

func continuityContainer(name string) core.Container {
	limits := core.ResourceList{core.ResourceCPU: resource.MustParse("100m"), core.ResourceMemory: resource.MustParse("64Mi")}
	return core.Container{Name: name, Resources: core.ResourceRequirements{Requests: limits.DeepCopy(), Limits: limits}}
}

func readyContainer(name string) core.ContainerStatus {
	return core.ContainerStatus{Name: name, Ready: true, State: core.ContainerState{Running: &core.ContainerStateRunning{}}}
}

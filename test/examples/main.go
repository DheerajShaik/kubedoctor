// Command examples writes two reproducible, entirely synthetic incident bundles.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/bundle"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func fixture(repaired bool) model.Snapshot {
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	at := base.Add(time.Hour)
	if repaired {
		at = at.Add(10 * time.Minute)
	}
	const ns = "example-shop"
	meta := func(name, uid string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(uid), CreationTimestamp: metav1.NewTime(base), Generation: 1}
	}
	owner := func(kind string, m metav1.ObjectMeta) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: m.Name, UID: m.UID, Controller: new(true)}
	}
	dep := apps.Deployment{ObjectMeta: meta("checkout", "90000000-0000-4000-8000-000000000001"), Spec: apps.DeploymentSpec{Replicas: new(int32(1))}, Status: apps.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1}}
	rs := apps.ReplicaSet{ObjectMeta: meta("checkout-blue", "90000000-0000-4000-8000-000000000002")}
	rs.OwnerReferences = []metav1.OwnerReference{owner("Deployment", dep.ObjectMeta)}
	pod := core.Pod{ObjectMeta: meta("checkout-blue-one", "90000000-0000-4000-8000-000000000003")}
	pod.OwnerReferences = []metav1.OwnerReference{owner("ReplicaSet", rs.ObjectMeta)}
	pod.Spec.Containers = []core.Container{{Name: "checkout"}}
	status := core.ContainerStatus{Name: "checkout", State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}
	pod.Status.Phase = core.PodPending
	pod.Status.Conditions = []core.PodCondition{{Type: core.PodReady, Status: core.ConditionFalse, LastTransitionTime: metav1.NewTime(base)}}
	if repaired {
		dep.Status.ReadyReplicas = 1
		dep.Status.AvailableReplicas = 1
		pod.Status.Phase = core.PodRunning
		pod.Status.Conditions[0].Status = core.ConditionTrue
		pod.Status.Conditions[0].LastTransitionTime = metav1.NewTime(at.Add(-time.Minute))
		status.Ready = true
		status.State = core.ContainerState{Running: &core.ContainerStateRunning{StartedAt: metav1.NewTime(at.Add(-time.Minute))}}
	} else {
		dep.Status.UnavailableReplicas = 1
		dep.Status.Conditions = []apps.DeploymentCondition{{Type: apps.DeploymentProgressing, Status: core.ConditionFalse, Reason: "ProgressDeadlineExceeded", LastUpdateTime: metav1.NewTime(at.Add(-time.Minute)), LastTransitionTime: metav1.NewTime(at.Add(-time.Minute))}}
	}
	pod.Status.ContainerStatuses = []core.ContainerStatus{status}
	digest := sha256.Sum256([]byte("synthetic example-shop namespace identity"))
	s := model.Snapshot{Scope: model.Scope{Kind: model.NamespaceScope, Namespace: ns, Context: "synthetic-example"}, ClusterID: hex.EncodeToString(digest[:]), ClusterProvenance: model.NamespaceUIDProvenance, CollectedAt: at, StartedAt: at.Add(-time.Second), FinishedAt: at, Pods: []core.Pod{pod}, ReplicaSets: []apps.ReplicaSet{rs}, Deployments: []apps.Deployment{dep}}
	for _, source := range []model.Source{model.SourceNamespace, model.SourcePods, model.SourceReplicaSets, model.SourceDeployments, model.SourceDaemonSets, model.SourceEvents, model.SourceNodes, model.SourceConfigMaps, model.SourcePVCs, model.SourceServices, model.SourceEndpointSlices, model.SourceIngresses, model.SourceNetworkPolicies, model.SourcePDBs, model.SourceStatefulSets, model.SourceJobs, model.SourceCronJobs, model.SourceHPAs, model.SourceQuotas, model.SourceLimitRanges, model.SourceProvenance} {
		s.Sources = append(s.Sources, model.SourceResult{Source: source, Status: model.Complete, Required: source == model.SourcePods})
		s.SourceTimings = append(s.SourceTimings, model.SourceTiming{Source: source, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt})
	}
	s.Sources = append(s.Sources, model.SourceResult{Source: model.SourceMetrics, Status: model.Unavailable, Detail: "the Pod Metrics API is not installed"})
	return s
}
func run(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, item := range []struct {
		name     string
		repaired bool
	}{{"before.json", false}, {"after.json", true}} {
		b, err := bundle.New(fixture(item.repaired), "v0.2.0-example")
		if err != nil {
			return err
		}
		if err := b.Save(filepath.Join(dir, item.name)); err != nil {
			return err
		}
	}
	return nil
}
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./test/examples NEW_DIRECTORY")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

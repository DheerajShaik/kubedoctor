package collect

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/bundle"
	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/investigation"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStandaloneReplicaSetFreshnessThroughCaptureReplayAndComparison(t *testing.T) {
	const (
		replicaSetUID = "standalone-rs-uid"
		podUID        = "standalone-pod-uid"
		generation    = 2
	)
	scope := model.Scope{Kind: model.NamespaceScope, Namespace: "payments", Context: "test"}
	created := metav1.NewTime(time.Now().Add(-time.Hour).UTC())
	capture := func(isReady bool, observedGeneration int64) investigation.Investigation {
		t.Helper()
		var readyCount int32
		state := core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "ErrImagePull"}}
		phase, ready := core.PodPending, core.ConditionFalse
		if isReady {
			readyCount = 1
			state = core.ContainerState{Running: &core.ContainerStateRunning{}}
			phase, ready = core.PodRunning, core.ConditionTrue
		}
		pod := core.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "standalone-pod", Namespace: "payments", UID: podUID, CreationTimestamp: created,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "standalone", UID: replicaSetUID, Controller: new(true),
				}},
			},
			Spec: core.PodSpec{
				NodeName: "node-a",
				Containers: []core.Container{{
					Name: "app",
					Resources: core.ResourceRequirements{
						Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("100m"), core.ResourceMemory: resource.MustParse("100Mi")},
						Limits:   core.ResourceList{core.ResourceCPU: resource.MustParse("500m"), core.ResourceMemory: resource.MustParse("200Mi")},
					},
				}},
			},
			Status: core.PodStatus{
				Phase: phase, Conditions: []core.PodCondition{{Type: core.PodReady, Status: ready}},
				ContainerStatuses: []core.ContainerStatus{{Name: "app", State: state, Ready: isReady}},
			},
		}
		replicaSet := apps.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "standalone", Namespace: "payments", UID: replicaSetUID, Generation: generation, CreationTimestamp: created,
			},
			Spec:   apps.ReplicaSetSpec{Replicas: new(int32(1))},
			Status: apps.ReplicaSetStatus{ObservedGeneration: observedGeneration, ReadyReplicas: readyCount},
		}
		collector, fixture := fixtureCollector(t, func(w http.ResponseWriter, req *http.Request) bool {
			var response any
			switch req.URL.Path {
			case "/api/v1/namespaces/payments":
				response = core.Namespace{
					TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
					ObjectMeta: metav1.ObjectMeta{Name: "payments", UID: "payments-uid"},
				}
			case "/api/v1/namespaces/payments/pods":
				response = core.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []core.Pod{pod}}
			case "/api/v1/nodes/node-a":
				response = core.Node{
					TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
					ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid"},
					Status:     core.NodeStatus{Conditions: []core.NodeCondition{{Type: core.NodeReady, Status: core.ConditionTrue}}},
				}
			case "/apis/apps/v1/namespaces/payments/replicasets":
				response = apps.ReplicaSetList{
					TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSetList"}, Items: []apps.ReplicaSet{replicaSet},
				}
			default:
				return false
			}
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Errorf("encode typed API fixture: %v", err)
			}
			return true
		}, 0)
		snapshot := engine.New(collector).Capture(t.Context(), model.ScanRequest{Scope: scope})
		if len(snapshot.ReplicaSets) != 1 {
			t.Fatalf("expected one typed ReplicaSet collection: %+v", snapshot.Sources)
		}
		item := snapshot.ReplicaSets[0]
		if item.Generation != generation || item.Status.ObservedGeneration != observedGeneration || item.Status.ReadyReplicas != readyCount ||
			item.Spec.Replicas == nil || *item.Spec.Replicas != 1 {
			t.Fatalf("ReplicaSet status did not survive the typed collector: %+v", item)
		}
		out, err := bundle.New(snapshot, "replicaset-test")
		if err != nil {
			t.Fatal(err)
		}
		var raw bytes.Buffer
		if err := out.Encode(&raw); err != nil {
			t.Fatal(err)
		}
		replay, err := bundle.Decode(&raw)
		if err != nil {
			t.Fatal(err)
		}
		inv, err := investigation.FromBundle(replay, bundle.Replay)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(inv.Resources, func(resource investigation.ResourceObservation) bool {
			return resource.Resource.UID == replicaSetUID && resource.StatusFresh == (observedGeneration == generation) &&
				resource.Ready == isReady && resource.ExpectedPods != nil && *resource.ExpectedPods == 1
		}) {
			t.Fatalf("ReplicaSet status did not survive bundle replay: %+v", inv.Resources)
		}
		if !fixture.called("/apis/apps/v1/namespaces/payments/replicasets") {
			t.Fatal("test did not exercise the actual typed ReplicaSet API")
		}
		return inv
	}

	baseline := capture(true, generation)
	broken := capture(false, generation)
	repaired := capture(true, generation)
	stale := capture(true, generation-1)

	for _, scenario := range []struct {
		name   string
		before investigation.Investigation
		after  investigation.Investigation
		want   investigation.Classification
		exit   int
	}{
		{"new pod failure", baseline, broken, investigation.New, 1},
		{"repaired pod", broken, repaired, investigation.Resolved, 0},
		{"repair with stale owner observations", broken, stale, investigation.NoLongerObservable, 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			comparison := investigation.Compare(scenario.before, scenario.after)
			found := slices.ContainsFunc(comparison.WorkloadChanges, func(change investigation.Change) bool {
				return change.Subject.Kind == "ReplicaSet" && change.Subject.UID == replicaSetUID &&
					change.RuleID == "pod-containers" && change.Classification == scenario.want
			})
			if !comparison.Compatible || !found || investigation.ExitCode(comparison, model.Warning) != scenario.exit {
				t.Fatalf("expected %s at standalone ReplicaSet with exit %d: %+v", scenario.want, scenario.exit, comparison)
			}
		})
	}
}

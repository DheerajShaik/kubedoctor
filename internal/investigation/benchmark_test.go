package investigation

import (
	"fmt"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
)

func representativeCapture(at time.Time) (model.Snapshot, model.Report) {
	snapshot, report := fixtureSnapshot(at, false, "pod-uid-a")
	for i := range 499 {
		pod := *snapshot.Pods[0].DeepCopy()
		pod.ObjectMeta = fixtureMeta(fmt.Sprintf("pod-%d", i), fmt.Sprintf("pod-uid-%d", i))
		pod.Labels["partition"] = fmt.Sprint(i % 50)
		snapshot.Pods = append(snapshot.Pods, pod)
		id := model.Identity("Pod", &pod)
		snapshot.Events = append(snapshot.Events, model.Event{Resource: id, Time: at.Add(-time.Minute), Reason: "Failed", Count: 1, TimeBasis: "event_time"})
		if i%5 == 0 {
			finding := report.Findings[0]
			finding.Resource = id
			report.Findings = append(report.Findings, finding)
		}
	}
	for i := range 50 {
		service := core.Service{ObjectMeta: fixtureMeta(fmt.Sprintf("service-%d", i), fmt.Sprintf("service-uid-%d", i)),
			Spec: core.ServiceSpec{Selector: map[string]string{"partition": fmt.Sprint(i)}}}
		snapshot.Services = append(snapshot.Services, service)
	}
	return snapshot, report
}

func BenchmarkInvestigationBuild(b *testing.B) {
	snapshot, report := representativeCapture(baselineTime)
	b.ReportAllocs()
	for b.Loop() {
		view := Build(snapshot, report)
		if len(view.Resources) == 0 {
			b.Fatal("missing investigation")
		}
	}
}

func BenchmarkInvestigationCompare(b *testing.B) {
	snapshot, report := representativeCapture(baselineTime)
	before := Build(snapshot, report)
	snapshot, report = representativeCapture(baselineTime.Add(time.Minute))
	after := Build(snapshot, report)
	b.ReportAllocs()
	for b.Loop() {
		changes := Compare(before, after)
		if !changes.Complete {
			b.Fatal("incomplete comparison")
		}
	}
}

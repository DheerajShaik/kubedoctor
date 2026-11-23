package investigation

import (
	"cmp"
	"container/heap"
	"slices"
	"strconv"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
)

const (
	maxTimelineEntries  = 2_000
	timelineEventWindow = time.Hour
	clockTolerance      = 5 * time.Minute
)

type observedTimeline []TimelineEntry

func (entries observedTimeline) Len() int { return len(entries) }
func (entries observedTimeline) Less(i, j int) bool {
	return compareTimeline(entries[i], entries[j]) < 0
}
func (entries observedTimeline) Swap(i, j int)  { entries[i], entries[j] = entries[j], entries[i] }
func (entries *observedTimeline) Push(item any) { *entries = append(*entries, item.(TimelineEntry)) }
func (entries *observedTimeline) Pop() any {
	index := len(*entries) - 1
	item := (*entries)[index]
	(*entries)[index] = TimelineEntry{}
	*entries = (*entries)[:index]
	return item
}

type timelineBuilder struct {
	evaluation time.Time
	entries    observedTimeline
	truncated  bool
	uncertain  bool
}

func (timeline *timelineBuilder) add(at time.Time, kind TimelineTimeKind, resource *model.Resource, source model.Source, summary string, uncertain bool, facts ...model.Evidence) {
	if at.IsZero() {
		return
	}
	uncertain = uncertain || kind != CollectionTime && !timeline.evaluation.IsZero() && at.After(timeline.evaluation)
	if uncertain && at.After(timeline.evaluation.Add(clockTolerance)) {
		timeline.uncertain = true
	}
	entry := TimelineEntry{Time: at.UTC(), TimeKind: kind, Resource: resource, Source: source, Summary: summary, Evidence: facts, Uncertain: uncertain}
	if len(timeline.entries) < maxTimelineEntries-3 {
		heap.Push(&timeline.entries, entry)
		return
	}
	timeline.truncated = true
	if compareTimeline(entry, timeline.entries[0]) > 0 {
		timeline.entries[0] = entry
		heap.Fix(&timeline.entries, 0)
	}
}

func buildTimeline(s model.Snapshot, index *inventory, output *Investigation) []TimelineEntry {
	builder := timelineBuilder{evaluation: s.CollectedAt, entries: make(observedTimeline, 0, min(64, maxTimelineEntries))}
	for _, timing := range output.SourceTimings {
		builder.add(timing.StartedAt, CollectionTime, nil, timing.Source, "Source collection started.", false, evidence("resource", timing.Resource))
		builder.add(timing.FinishedAt, CollectionTime, nil, timing.Source, "Source collection finished.", false, evidence("resource", timing.Resource))
	}
	for _, event := range s.Events {
		if event.Time.IsZero() || !s.CollectedAt.IsZero() && event.Time.Before(s.CollectedAt.Add(-timelineEventWindow)) {
			continue
		}
		resolved, observed := index.resolve(event.Resource)
		if observed {
			item := index.byName[nameOf(resolved)]
			if item.CreatedAt != nil && event.Time.Before(*item.CreatedAt) {
				continue
			}
		}
		anchored := observed && event.Resource.UID != "" && event.Resource.UID == resolved.UID
		if !anchored {
			resolved = event.Resource
		}
		kind := ObservationTime
		summary := "Kubernetes Event reported a classified condition."
		uncertain := !anchored
		switch event.TimeBasis {
		case "event_time", "first_reported":
			kind = OccurrenceTime
		case "event_object_created":
			kind = CollectionTime
			uncertain = true
		case "series_last_observed":
			summary = "Kubernetes Event series reported its latest captured observation."
		case "last_reported":
			summary = "Kubernetes Event records its last reported time."
		default:
			uncertain = true
		}
		builder.add(event.Time, kind, &resolved, model.SourceEvents, summary, uncertain,
			evidence("reason", event.Reason), evidence("reported_count", strconv.Itoa(int(event.Count))), evidence("time_basis", event.TimeBasis))
	}
	for i := range s.Nodes {
		node := &s.Nodes[i]
		resource := model.Identity("Node", node)
		for _, condition := range node.Status.Conditions {
			builder.condition(resource, model.SourceNodes, condition.LastTransitionTime.Time, string(condition.Type), string(condition.Status), condition.Reason)
		}
	}
	for i := range s.Pods {
		pod := &s.Pods[i]
		resource := model.Identity("Pod", pod)
		for _, condition := range pod.Status.Conditions {
			builder.condition(resource, model.SourcePods, condition.LastTransitionTime.Time, string(condition.Type), string(condition.Status), condition.Reason)
		}
		for _, statuses := range [][]core.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
			for _, status := range statuses {
				if status.State.Running != nil {
					builder.add(status.State.Running.StartedAt.Time, OccurrenceTime, &resource, model.SourcePods, "Container status reports a start.", false,
						evidence("container", status.Name))
				}
				for _, occurrence := range []struct {
					state *core.ContainerStateTerminated
					basis string
				}{{status.State.Terminated, "current_state"}, {status.LastTerminationState.Terminated, "last_termination_state"}} {
					if occurrence.state != nil {
						builder.add(occurrence.state.FinishedAt.Time, OccurrenceTime, &resource, model.SourcePods, "Container status reports a termination.", false,
							evidence("container", status.Name), evidence("state", occurrence.basis), evidence("reason", occurrence.state.Reason))
					}
				}
			}
		}
	}
	for i := range s.Deployments {
		item := &s.Deployments[i]
		resource := model.Identity("Deployment", item)
		for _, condition := range item.Status.Conditions {
			builder.condition(resource, model.SourceDeployments, condition.LastTransitionTime.Time, string(condition.Type), string(condition.Status), condition.Reason)
		}
	}
	for _, job := range s.Expansion.Jobs {
		for _, condition := range job.Conditions {
			builder.condition(job.Resource, model.SourceJobs, condition.Transition, condition.Type, condition.Status, condition.Reason)
		}
		builder.add(job.Started, OccurrenceTime, &job.Resource, model.SourceJobs, "Job status reports a start.", false)
		builder.add(job.Completed, OccurrenceTime, &job.Resource, model.SourceJobs, "Job status reports successful completion.", false)
	}
	for _, cronJob := range s.Expansion.CronJobs {
		builder.add(cronJob.LastScheduled, OccurrenceTime, &cronJob.Resource, model.SourceCronJobs, "CronJob status reports its last schedule.", false)
		builder.add(cronJob.LastSuccessful, OccurrenceTime, &cronJob.Resource, model.SourceCronJobs, "CronJob status reports its last successful Job.", false)
	}
	for _, hpa := range s.Expansion.HPAs {
		for _, condition := range hpa.Conditions {
			builder.condition(hpa.Resource, model.SourceHPAs, condition.Transition, condition.Type, condition.Status, condition.Reason)
		}
	}
	for i := range s.PDBs {
		pdb := &s.PDBs[i]
		resource := model.Identity("PodDisruptionBudget", pdb)
		for _, condition := range pdb.Status.Conditions {
			builder.condition(resource, model.SourcePDBs, condition.LastTransitionTime.Time, condition.Type, string(condition.Status), condition.Reason)
		}
	}
	if builder.truncated {
		output.Truncated = true
		output.Limitations = append(output.Limitations, "Timeline budget retained the most recent observations and capture markers. Earlier entries may be omitted.")
	}
	if builder.uncertain {
		output.Limitations = append(output.Limitations, "One or more reported timestamps exceed the evaluation clock by over five minutes. Their timing is uncertain.")
	}
	for _, boundary := range []struct {
		at      time.Time
		kind    TimelineTimeKind
		summary string
	}{
		{s.StartedAt, CollectionTime, "Capture started."},
		{s.FinishedAt, CollectionTime, "Capture finished."},
		{s.CollectedAt, ObservationTime, "Rule evaluation used this capture timestamp."},
	} {
		if !boundary.at.IsZero() {
			builder.entries = append(builder.entries, TimelineEntry{Time: boundary.at.UTC(), TimeKind: boundary.kind, Source: "capture", Summary: boundary.summary, Evidence: []model.Evidence{}})
		}
	}
	slices.SortFunc(builder.entries, compareTimeline)
	return builder.entries
}

func (timeline *timelineBuilder) condition(resource model.Resource, source model.Source, at time.Time, kind, status, reason string) {
	timeline.add(at, TransitionTime, &resource, source, "Kubernetes status reports a condition transition.", false,
		evidence("condition", kind), evidence("status", status), evidence("reason", reason))
}

func compareTimeline(a, b TimelineEntry) int {
	var left, right model.Resource
	if a.Resource != nil {
		left = *a.Resource
	}
	if b.Resource != nil {
		right = *b.Resource
	}
	return cmp.Or(a.Time.Compare(b.Time), cmp.Compare(a.TimeKind, b.TimeKind), compareResource(left, right), cmp.Compare(a.Source, b.Source),
		cmp.Compare(a.Summary, b.Summary), slices.CompareFunc(a.Evidence, b.Evidence, func(a, b model.Evidence) int {
			return cmp.Or(cmp.Compare(a.Field, b.Field), cmp.Compare(a.Value, b.Value))
		}))
}

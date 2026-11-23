package collect

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
)

type sourceTimingKey struct{}

type sourceTimings struct {
	mu      sync.Mutex
	windows []model.SourceTiming
}

func (s *sourceTimings) observations() []model.SourceTiming {
	slices.SortFunc(s.windows, func(a, b model.SourceTiming) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Resource, b.Resource), a.StartedAt.Compare(b.StartedAt))
	})
	return s.windows
}

func recordSourceTiming(ctx context.Context, source model.Source, object string, start time.Time) {
	if timings, ok := ctx.Value(sourceTimingKey{}).(*sourceTimings); ok && !start.IsZero() {
		timing := model.SourceTiming{Source: source, Resource: object, StartedAt: start.UTC(), FinishedAt: time.Now().UTC()}
		timings.mu.Lock()
		timings.windows = append(timings.windows, timing)
		timings.mu.Unlock()
	}
}

func timedRead[T any](ctx context.Context, source model.Source, object string, read func() (T, error)) (T, error) {
	defer recordSourceTiming(ctx, source, object, time.Now())
	return read()
}

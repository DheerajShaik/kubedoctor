package collect

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	pageSize           = 200
	maxPages           = 25
	maxItems           = 5000
	maxNamedLookups    = 64
	maxParallelSources = 4
)

func observed(source model.Source, required bool, status model.Coverage, detail string) model.SourceResult {
	return model.SourceResult{Source: source, Required: required, Status: status, Detail: detail}
}

func collectionError(source model.Source, required bool, err error) model.SourceResult {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return observed(source, required, model.PermissionDenied, "Kubernetes did not authorize this API read")
	case apierrors.IsNotFound(err), apierrors.IsMethodNotSupported(err), apierrors.IsServiceUnavailable(err):
		return observed(source, required, model.Unavailable, "Kubernetes did not provide the requested API resource")
	case errors.Is(err, errRequestBudget):
		return observed(source, required, model.Failed, "API retry budget reached")
	case errors.Is(err, errResponseBudget):
		return observed(source, required, model.Truncated, "API response byte budget reached")
	case errors.Is(err, context.Canceled):
		return observed(source, required, model.Failed, "collection was canceled")
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return observed(source, required, model.Failed, "collection timed out")
	default:
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return observed(source, required, model.Failed, "collection timed out")
		}
		return observed(source, required, model.Failed, "Kubernetes API read failed")
	}
}

func readPages[T any, L metav1.ListInterface, O any](
	ctx context.Context, source model.Source, required bool, options metav1.ListOptions,
	get func(context.Context, metav1.ListOptions) (L, error),
	items func(L) []T, project func(T) (O, bool),
) ([]O, model.SourceResult) {
	options.Limit = pageSize
	var output []O
	seenTokens := make(map[string]bool)
	itemCount := 0
	var started time.Time
	defer func() { recordSourceTiming(ctx, source, "", started) }()
	unexpectedObjects := false
	access, checkScope := ctx.Value(scanAccessKey{}).(scanAccess)
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return output, collectionError(source, required, err)
		}
		if started.IsZero() {
			started = time.Now()
		}
		list, err := get(requestContext(ctx), options)
		if err != nil {
			return output, collectionError(source, required, err)
		}
		next := list.GetContinue()
		if next != "" && seenTokens[next] {
			return output, observed(source, required, model.Truncated, "API repeated a pagination token")
		}
		for _, item := range items(list) {
			if itemCount == maxItems {
				return output, observed(source, required, model.Truncated, "API item budget reached")
			}
			itemCount++
			if object, isObject := any(&item).(metav1.Object); checkScope && isObject && !inScope(source, access, object) {
				unexpectedObjects = true
				continue
			}
			if projection, keep := project(item); keep {
				output = append(output, projection)
			}
		}
		if next == "" {
			if unexpectedObjects {
				return output, observed(source, required, model.Failed, "API returned invalid or out-of-scope objects")
			}
			return output, observed(source, required, model.Complete, "")
		}
		if itemCount == maxItems {
			return output, observed(source, required, model.Truncated, "API item budget reached")
		}
		seenTokens[next] = true
		options.Continue = next
	}
	return output, observed(source, required, model.Truncated, "API page budget reached")
}

func inScope(source model.Source, access scanAccess, item metav1.Object) bool {
	if !validResourceName(item.GetName()) {
		return false
	}
	namespace := item.GetNamespace()
	switch source {
	case model.SourceNodes, model.SourceStorageClasses:
		return namespace == ""
	case model.SourceDNSPods:
		return namespace == dnsNamespace && item.GetLabels()["k8s-app"] == dnsService
	case model.SourceNodeLocalPods:
		return namespace == dnsNamespace && item.GetLabels()["k8s-app"] == nodeLocalDNS
	case model.SourceDNSEndpointSlices:
		return namespace == dnsNamespace && item.GetLabels()["kubernetes.io/service-name"] == dnsService
	default:
		if access.cluster {
			return validNamespace(namespace)
		}
		return namespace == access.namespace
	}
}

func inventory[T any, L metav1.ListInterface, O any](
	ctx context.Context, source model.Source,
	get func(context.Context, metav1.ListOptions) (L, error),
	items func(L) []T, project func(T) O,
) ([]O, model.SourceResult) {
	return readPages(ctx, source, true, metav1.ListOptions{}, get, items, include(project))
}

func include[T, O any](project func(T) O) func(T) (O, bool) {
	return func(value T) (O, bool) {
		return project(value), true
	}
}

type sourceTask func() []model.SourceResult

// Each task owns distinct snapshot fields. Return coverage in task order even
// when requests finish in a different order.
func parallel(tasks ...sourceTask) []model.SourceResult {
	results := make([][]model.SourceResult, len(tasks))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for i := 0; i < min(maxParallelSources, len(tasks)); i++ {
		workers.Go(func() {
			for index := range jobs {
				results[index] = tasks[index]()
			}
		})
	}
	for i := range tasks {
		jobs <- i
	}
	close(jobs)
	workers.Wait()

	var output []model.SourceResult
	for _, result := range results {
		output = append(output, result...)
	}
	return output
}

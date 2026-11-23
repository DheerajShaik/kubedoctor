// Package collect reads bounded Kubernetes snapshots through a GET-only transport.
package collect

import (
	"errors"
	"net/http"
	"time"

	apps "k8s.io/client-go/kubernetes/typed/apps/v1"
	autoscaling "k8s.io/client-go/kubernetes/typed/autoscaling/v2"
	batch "k8s.io/client-go/kubernetes/typed/batch/v1"
	core "k8s.io/client-go/kubernetes/typed/core/v1"
	discovery "k8s.io/client-go/kubernetes/typed/discovery/v1"
	networking "k8s.io/client-go/kubernetes/typed/networking/v1"
	policy "k8s.io/client-go/kubernetes/typed/policy/v1"
	storage "k8s.io/client-go/kubernetes/typed/storage/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
)

const maxRequestTime = 8 * time.Second

type Collector struct {
	core        *core.CoreV1Client
	apps        *apps.AppsV1Client
	autoscaling *autoscaling.AutoscalingV2Client
	batch       *batch.BatchV1Client
	discovery   *discovery.DiscoveryV1Client
	networking  *networking.NetworkingV1Client
	policy      *policy.PolicyV1Client
	storage     *storage.StorageV1Client
	metrics     *metrics.MetricsV1beta1Client
}

func New(input *rest.Config) (*Collector, error) {
	if input == nil {
		return nil, errors.New("missing Kubernetes client configuration")
	}
	config := rest.CopyConfig(input)
	if config.Timeout <= 0 || config.Timeout > maxRequestTime {
		config.Timeout = maxRequestTime
	}
	// Warning headers are arbitrary server text. Keep them outside logs and
	// reports, just as we do with API status messages and event text.
	config.WarningHandler = nil
	config.WarningHandlerWithContext = rest.NoWarnings{}
	config.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(20, 30)
	server, _, err := rest.DefaultServerUrlFor(config)
	if err != nil || server.User != nil || server.RawQuery != "" || server.Fragment != "" ||
		(server.Scheme != "http" && server.Scheme != "https") {
		return nil, errors.New("invalid Kubernetes API server URL")
	}

	client, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, errors.New("could not configure Kubernetes authentication or transport")
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	// Guard the authenticated client from the outside, including URLs redirected
	// by a server. Copy the client rather than modifying a shared default.
	client = &http.Client{
		Transport: diagnosticTransport{base: base, server: server},
		Timeout:   config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	c := &Collector{}
	if c.core, err = core.NewForConfigAndClient(config, client); err != nil {
		return nil, errors.New("could not configure the core Kubernetes client")
	}
	if c.apps, err = apps.NewForConfigAndClient(config, client); err != nil {
		return nil, errors.New("could not configure the apps Kubernetes client")
	}
	if c.autoscaling, err = autoscaling.NewForConfigAndClient(config, client); err != nil {
		return nil, errors.New("could not configure the autoscaling Kubernetes client")
	}
	if c.batch, err = batch.NewForConfigAndClient(config, client); err != nil {
		return nil, errors.New("could not configure the batch Kubernetes client")
	}
	if c.discovery, err = discovery.NewForConfigAndClient(config, client); err != nil {
		return nil, errors.New("could not configure the discovery Kubernetes client")
	}
	if c.networking, err = networking.NewForConfigAndClient(config, client); err != nil {
		return nil, errors.New("could not configure the networking Kubernetes client")
	}
	if c.policy, err = policy.NewForConfigAndClient(config, client); err != nil {
		return nil, errors.New("could not configure the policy Kubernetes client")
	}
	if c.storage, err = storage.NewForConfigAndClient(config, client); err != nil {
		return nil, errors.New("could not configure the storage Kubernetes client")
	}
	if c.metrics, err = metrics.NewForConfigAndClient(config, client); err != nil {
		return nil, errors.New("could not configure the metrics Kubernetes client")
	}
	return c, nil
}

package investigation

import (
	"github.com/DheerajShaik/kubedoctor/internal/model"
	discovery "k8s.io/api/discovery/v1"
)

type serviceEndpointSelectorContinuity struct {
	conflicting bool
	ready       bool
}

func (observation *serviceEndpointSelectorContinuity) observe(verified, ready bool) {
	observation.conflicting = observation.conflicting || !verified
	observation.ready = observation.ready || verified && ready
}

func relevantSelectorEndpoint(endpoint discovery.Endpoint) bool {
	if serviceEndpointHasAddress(endpoint) {
		return true
	}
	// A terminating backend can still participate in fallback routing. Exclude
	// it only when its published conditions rule out both readiness and serving.
	conditions := endpoint.Conditions
	return len(endpoint.Addresses) != 0 &&
		(conditions.Ready == nil || *conditions.Ready || conditions.Serving == nil || *conditions.Serving)
}

func serviceSelectorRecoveryLimitation(after ResourceObservation, finding model.Finding) string {
	if finding.RuleID != "service.backends" || evidenceValue(finding, "mismatched_pod_uids") == "" {
		return ""
	}
	if after.Service != nil && after.Service.identities != nil {
		observed := after.Service.identities.selectorEndpoints
		if observed.ready && !observed.conflicting {
			return ""
		}
	}
	return "The later capture needs a Ready, controller-managed endpoint and current selector-matching Pod UIDs for all eligible managed targets."
}

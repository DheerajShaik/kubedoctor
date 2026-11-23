package investigation

import (
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestIngressRecoveryRequiresObservedBackend(t *testing.T) {
	for _, test := range []struct {
		name, backend string
		want          Classification
	}{
		{"original backend repaired", "checkout", Resolved},
		{"backend removed", "", NoLongerObservable},
		{"backend retargeted", "other", NoLongerObservable},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := eventComparisonSnapshot()
			ref := &networking.IngressServiceBackend{Name: "checkout", Port: networking.ServiceBackendPort{Number: 80}}
			s.Ingresses = []networking.Ingress{{ObjectMeta: fixtureMeta("checkout", "ingress-uid"), Spec: networking.IngressSpec{
				DefaultBackend: &networking.IngressBackend{Service: ref},
			}}}
			before := Build(s, engine.Analyze(s))
			after := advanceObservation(s, time.Minute)
			after.Services = []core.Service{
				{ObjectMeta: fixtureMeta("checkout", "service-uid"), Spec: core.ServiceSpec{Ports: []core.ServicePort{{Port: 80}}}},
				{ObjectMeta: fixtureMeta("other", "other-uid"), Spec: core.ServiceSpec{Ports: []core.ServicePort{{Port: 80}}}},
			}
			if test.backend == "" {
				// Another valid route remains inspected.
				after.Ingresses[0].Spec.DefaultBackend = nil
				after.Ingresses[0].Spec.Rules = []networking.IngressRule{{IngressRuleValue: networking.IngressRuleValue{
					HTTP: &networking.HTTPIngressRuleValue{Paths: []networking.HTTPIngressPath{{Backend: networking.IngressBackend{
						Service: &networking.IngressServiceBackend{Name: "other", Port: networking.ServiceBackendPort{Number: 80}},
					}}}},
				}}}
			} else {
				after.Ingresses[0].Spec.DefaultBackend.Service.Name = test.backend
			}
			assertApplicableChange(t, before, after, "ingress.backends", "ingress-uid", test.want)
		})
	}
}

func TestQuotaRecoveryRequiresSameResourceHeadroom(t *testing.T) {
	s := eventComparisonSnapshot()
	quota := quotaAtHardValue()
	s.Expansion.Quotas = []model.QuotaEvidence{quota}
	before := Build(s, engine.Analyze(s))
	for _, test := range []struct {
		name, resource string
		want           Classification
	}{
		{"same resource below hard", "pods", Resolved},
		{"resource replaced", "services", NoLongerObservable},
		{"resource removed", "", NoLongerObservable},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := advanceObservation(s, time.Minute)
			after.Expansion.Quotas = []model.QuotaEvidence{quota}
			after.Expansion.Quotas[0].Capacities = nil
			if test.resource != "" {
				after.Expansion.Quotas[0].Capacities = []model.QuotaCapacity{
					{Resource: test.resource, Hard: "2", Used: "1", Current: true},
				}
			}
			assertApplicableChange(t, before, after, "namespace-admission", "quota-uid", test.want)
		})
	}
}

func TestServicePortRecoveryRequiresReadyPublicationForSamePort(t *testing.T) {
	s := eventComparisonSnapshot()
	service := core.Service{ObjectMeta: fixtureMeta("checkout", "service-uid"), Spec: core.ServiceSpec{
		Selector: map[string]string{"app": "checkout"},
		Ports:    []core.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)}},
	}}
	s.Services = []core.Service{service}
	s.EndpointSlices = []discovery.EndpointSlice{publishedFixture(&service, "existing", 9090, false)}
	before := Build(s, engine.Analyze(s))
	for _, test := range []struct {
		name     string
		draining bool
		want     Classification
	}{
		{"same-port Ready backend", false, Resolved},
		{"correct port only on draining slice", true, NoLongerObservable},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := advanceObservation(s, time.Minute)
			after.EndpointSlices = append(after.EndpointSlices, publishedFixture(&service, "added", 8080, test.draining))
			assertApplicableChange(t, before, after, "service.ports", "service-uid", test.want)
		})
	}
}

func publishedFixture(service *core.Service, name string, number int32, draining bool) discovery.EndpointSlice {
	metadata := fixtureMeta(name, name+"-uid")
	metadata.Namespace = service.Namespace
	metadata.Labels[discovery.LabelServiceName] = service.Name
	metadata.OwnerReferences = []metav1.OwnerReference{owner("Service", service)}
	return discovery.EndpointSlice{
		ObjectMeta: metadata, AddressType: discovery.AddressTypeIPv4,
		Ports: []discovery.EndpointPort{{Name: new(service.Spec.Ports[0].Name), Port: &number}},
		Endpoints: []discovery.Endpoint{{Addresses: []string{""}, Conditions: discovery.EndpointConditions{
			Ready: new(!draining), Terminating: &draining, Serving: new(true),
		}}},
	}
}

func TestDNSRecoveryRequiresCurrentBackendPublication(t *testing.T) {
	s := eventComparisonSnapshot()
	addApplicableSources(&s, model.SourceDNSServices, model.SourceDNSConfig, model.SourceDNSPods, model.SourceDNSEndpointSlices)
	service := core.Service{ObjectMeta: fixtureMeta("kube-dns", "dns-uid"), Spec: core.ServiceSpec{
		Selector: map[string]string{"k8s-app": "kube-dns"}, Ports: []core.ServicePort{
			{Name: "dns-tcp", Port: 53, Protocol: core.ProtocolTCP},
			{Name: "dns-udp", Port: 53, Protocol: core.ProtocolUDP},
		},
	}}
	service.Namespace = "kube-system"
	s.DNSServices = []core.Service{service}
	s.DNSEndpointSlices = []discovery.EndpointSlice{publishedFixture(&service, "dns", 53, false)}
	s.DNSEndpointSlices[0].Ports = append(s.DNSEndpointSlices[0].Ports, discovery.EndpointPort{
		Name: new("dns-udp"), Port: new(int32(53)), Protocol: new(core.ProtocolUDP),
	})
	s.DNSEndpointSlices[0].Endpoints[0].Conditions = discovery.EndpointConditions{Ready: new(false)}
	before := Build(s, engine.Analyze(s))
	after := advanceObservation(s, time.Minute)
	after.DNSEndpointSlices[0].Endpoints[0].Conditions = discovery.EndpointConditions{Ready: new(false), Serving: new(true), Terminating: new(true)}
	assertApplicableChange(t, before, after, "dns.infrastructure", "dns-uid", NoLongerObservable)
	after.DNSEndpointSlices[0].Endpoints[0].Conditions = discovery.EndpointConditions{Ready: new(true), Serving: new(true), Terminating: new(false)}
	assertApplicableChange(t, before, after, "dns.infrastructure", "dns-uid", Resolved)
}

func TestDNSPortRecoveryUsesDedicatedPublicationAfterBundleReplay(t *testing.T) {
	s := conventionalDNSSnapshot(false, true)
	s.Scope.Kind, s.Scope.Namespace = model.ClusterScope, ""
	s.ClusterProvenance = model.KubeSystemUIDProvenance
	s.Services = []core.Service{*s.DNSServices[0].DeepCopy()}
	s.EndpointSlices = []discovery.EndpointSlice{*s.DNSEndpointSlices[0].DeepCopy()}
	s.EndpointSlices[0].Name, s.EndpointSlices[0].UID = "primary-dns", "primary-dns-uid"
	// Primary inventory has a healthy mapping throughout. DNS findings
	// are based on the independently collected DNS supplementary slices.
	s.DNSEndpointSlices[0].Ports[0].Port = new(int32(5353))
	before := replayNetworkContinuity(t, s)
	after := advanceObservation(s, time.Minute)
	current := publishedFixture(&after.DNSServices[0], "new-dns", 53, true)
	current.Ports[0].Protocol = new(core.ProtocolUDP)
	after.DNSEndpointSlices = append(after.DNSEndpointSlices, current)
	requireNetworkClassification(t, before, after, "dns.infrastructure", "dns-uid", NoLongerObservable)
	after.DNSEndpointSlices[1].Endpoints[0].Conditions.Ready = new(true)
	after.DNSEndpointSlices[1].Endpoints[0].Conditions.Terminating = new(false)
	requireNetworkClassification(t, before, after, "dns.infrastructure", "dns-uid", Resolved)
}

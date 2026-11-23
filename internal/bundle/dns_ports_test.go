package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func dnsNamedPortBundle(t *testing.T) (*Bundle, []byte) {
	t.Helper()
	s := sampleSnapshot()
	s.DNSServices[0].Spec.Ports[0].TargetPort = intstr.FromString("dns-target")
	s.DNSServices[0].Spec.Selector["role"] = "selected"
	s.DNSPods[0].Labels["role"] = "selected"
	s.DNSPods[0].Spec.Containers = []core.Container{{
		Name: "dns-process", Image: marker, Env: []core.EnvVar{{Name: "TOKEN", Value: marker}},
		SecurityContext: &core.SecurityContext{Privileged: new(true)},
		Ports: []core.ContainerPort{
			{Name: "dns-target", Protocol: core.ProtocolUDP, ContainerPort: 1053, HostPort: 853, HostIP: "192.0.2.51"},
			{Name: "dns-target", Protocol: core.ProtocolTCP, ContainerPort: 2053},
			{Name: "unused-dns", Protocol: core.ProtocolUDP, ContainerPort: 3153},
		},
	}}
	s.DNSPods[0].Spec.InitContainers = []core.Container{
		{Name: "regular-init", Ports: []core.ContainerPort{{Name: "dns-target", Protocol: core.ProtocolUDP, ContainerPort: 3053}}},
		{Name: "dns-sidecar", RestartPolicy: new(core.ContainerRestartPolicyAlways),
			Ports: []core.ContainerPort{{Name: "dns-target", Protocol: core.ProtocolUDP, ContainerPort: 4053}}},
	}
	unselected := *s.DNSPods[0].DeepCopy()
	unselected.Name, unselected.UID = "not-selected", "not-selected-uid"
	unselected.Labels["role"] = "different"
	s.DNSPods = append(s.DNSPods, unselected)
	b, err := New(s, "dns-port-test")
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if err := b.Encode(&raw); err != nil {
		t.Fatal(err)
	}
	return b, raw.Bytes()
}

func TestDNSNamedPortEvidenceIsMinimalAndRestoresRole(t *testing.T) {
	_, encoded := dnsNamedPortBundle(t)
	for _, forbidden := range []string{
		"dns-process", "dns-sidecar", "unused-dns", "regular-init", marker, "192.0.2.51", "2053", "3053", "3153",
		"securityContext", "hostPort",
	} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Errorf("unneeded DNS Pod detail persisted: %s", forbidden)
		}
	}
	if got := bytes.Count(encoded, []byte(`"dns_named_ports":`)); got != 1 {
		t.Fatalf("wanted port observations only for the selected DNS Pod, found %d", got)
	}
	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	s, err := decoded.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	application, init := s.DNSPods[0].Spec.Containers, s.DNSPods[0].Spec.InitContainers
	if len(application) != 1 || application[0].Name != "" || len(application[0].Ports) != 1 ||
		application[0].Ports[0] != (core.ContainerPort{Name: "dns-target", Protocol: core.ProtocolUDP, ContainerPort: 1053}) {
		t.Fatalf("application port evidence was not reconstructed: %+v", application)
	}
	if len(init) != 1 || init[0].Name != "" || init[0].RestartPolicy == nil ||
		*init[0].RestartPolicy != core.ContainerRestartPolicyAlways || len(init[0].Ports) != 1 ||
		init[0].Ports[0] != (core.ContainerPort{Name: "dns-target", Protocol: core.ProtocolUDP, ContainerPort: 4053}) {
		t.Fatalf("restartable init port evidence was not reconstructed: %+v", init)
	}
	if len(s.DNSPods[1].Spec.Containers) != 0 || len(s.DNSPods[1].Spec.InitContainers) != 0 {
		t.Fatal("unselected DNS Pod retained port observations")
	}
}

func TestOlderDNSBundleReplaysWithoutClaimingMissingPortEvidence(t *testing.T) {
	b, _ := dnsNamedPortBundle(t)
	old := rewritePayload(t, b, func(p *payload) {
		p.Evidence.DNSPods[0].DNSNamedPorts = nil
	})
	if bytes.Contains(old, []byte(`"dns_named_ports":`)) {
		t.Fatal("fixture unexpectedly has the new observation field")
	}
	decoded, err := Decode(bytes.NewReader(old))
	if err != nil {
		t.Fatalf("older schema-1 bundle failed to decode: %v", err)
	}
	replayed, err := decoded.Analyze(Replay)
	if err != nil || !sameReport(replayed.Report, b.data.Stored) {
		t.Fatalf("older schema-1 report failed to replay: %v", err)
	}
	s, err := decoded.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.DNSPods[0].Spec.Containers) != 0 || len(s.DNSPods[0].Spec.InitContainers) != 0 ||
		s.Coverage(model.SourceDNSPods) != model.Complete {
		t.Fatal("a legacy namespace Pod list must not synthesize individual port evidence")
	}
}

func TestDecodeRejectsUnauthorizedDNSNamedPortEvidence(t *testing.T) {
	b, _ := dnsNamedPortBundle(t)
	for _, tt := range []struct {
		name string
		edit func(*payload)
	}{
		{"wrong port name", func(p *payload) { p.Evidence.DNSPods[0].DNSNamedPorts[0].Name = "unrelated" }},
		{"wrong protocol", func(p *payload) { p.Evidence.DNSPods[0].DNSNamedPorts[0].Protocol = "TCP" }},
		{"invalid numeric port", func(p *payload) { p.Evidence.DNSPods[0].DNSNamedPorts[0].Number = 65536 }},
		{"unsupported container role", func(p *payload) { p.Evidence.DNSPods[0].DNSNamedPorts[0].Role = "init" }},
		{"duplicate declaration", func(p *payload) {
			dns := &p.Evidence.DNSPods[0]
			dns.DNSNamedPorts = append(dns.DNSNamedPorts, dns.DNSNamedPorts[0])
		}},
		{"kept after Service stopped naming target", func(p *payload) {
			port := &p.Evidence.DNSServices[0].Ports[0]
			port.IsNamed, port.Named = false, ""
		}},
		{"attached to ordinary Pod", func(p *payload) {
			p.Evidence.Pods[0].DNSNamedPorts = p.Evidence.DNSPods[0].DNSNamedPorts
		}},
		{"attached to unselected DNS Pod", func(p *payload) {
			p.Evidence.DNSPods[1].DNSNamedPorts = p.Evidence.DNSPods[0].DNSNamedPorts
		}},
		{"DNS Pod outside supplement scope", func(p *payload) {
			p.Evidence.DNSPods[0].Identity.Namespace = "unrelated"
		}},
		{"regular container details", func(p *payload) {
			p.Evidence.DNSPods[0].Containers = []container{{Name: "unexpected"}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(rewritePayload(t, b, tt.edit))); !errors.Is(err, ErrFormat) {
				t.Fatalf("invalid named-port evidence accepted: %v", err)
			}
		})
	}
	entry, err := json.Marshal(b.data.Evidence.DNSPods[0].DNSNamedPorts[0])
	if err != nil {
		t.Fatal(err)
	}
	changed := append(bytes.Clone(entry[:len(entry)-1]), []byte(`,"env":"`+marker+`"}`)...)
	invalid := bytes.Replace(b.encoded, entry, changed, 1)
	if _, err := Decode(bytes.NewReader(writeEnvelope(invalid))); !errors.Is(err, ErrFormat) {
		t.Fatalf("unknown field accepted in port observation: %v", err)
	}
}

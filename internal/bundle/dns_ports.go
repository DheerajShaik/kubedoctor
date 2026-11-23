package bundle

import (
	"cmp"
	"slices"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	dnsApplicationPort = "application"
	dnsInitSidecarPort = "restartable_init"
)

// Supplemental DNS Pods do not retain container identities or configuration.
// These declarations are sufficient to verify a named targetPort.
type dnsNamedPort struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Number   int32  `json:"number"`
	Role     string `json:"role"`
}

type dnsPortTarget struct {
	name     string
	protocol string
}

type dnsPortRequirements struct {
	selector labelSet
	targets  map[dnsPortTarget]bool
}

func namedDNSPortRequirements(services []service) dnsPortRequirements {
	for _, svc := range services {
		if svc.Identity.Namespace != "kube-system" || svc.Identity.Name != "kube-dns" ||
			svc.Type == "ExternalName" || svc.Headless || svc.Identity.DeletedAt != nil || len(svc.Selector) == 0 {
			continue
		}
		requirements := dnsPortRequirements{selector: svc.Selector, targets: make(map[dnsPortTarget]bool)}
		for _, declared := range svc.Ports {
			if declared.IsNamed {
				key := dnsPortTarget{declared.Named, dnsPortProtocol(declared.Port.Protocol)}
				requirements.targets[key] = true
			}
		}
		return requirements
	}
	return dnsPortRequirements{}
}

func (r dnsPortRequirements) selects(identity identity) bool {
	if len(r.targets) == 0 || !podsAreDNSInfrastructure(identity.Namespace, identity.Labels) {
		return false
	}
	for key, value := range r.selector {
		if observed, present := identity.Labels[key]; !present || observed != value {
			return false
		}
	}
	return true
}

func dnsPortProtocol(protocol string) string {
	if protocol == "" {
		return string(core.ProtocolTCP)
	}
	return protocol
}

func retainDNSNamedPorts(p pod, requirements dnsPortRequirements) []dnsNamedPort {
	if !requirements.selects(p.Identity) {
		return nil
	}
	var ports []dnsNamedPort
	seen := make(map[dnsNamedPort]bool)
	for _, group := range []struct {
		containers []container
		role       string
	}{{p.Containers, dnsApplicationPort}, {p.Init, dnsInitSidecarPort}} {
		for _, c := range group.containers {
			if group.role == dnsInitSidecarPort && !c.Sidecar {
				continue
			}
			for _, declared := range c.Ports {
				evidence := dnsNamedPort{
					Name: declared.Name, Protocol: dnsPortProtocol(declared.Protocol), Number: declared.Number, Role: group.role,
				}
				if !seen[evidence] && validDNSNamedPort(evidence, requirements.targets) {
					seen[evidence] = true
					ports = append(ports, evidence)
				}
			}
		}
	}
	slices.SortFunc(ports, func(a, b dnsNamedPort) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Protocol, b.Protocol), cmp.Compare(a.Number, b.Number), cmp.Compare(a.Role, b.Role))
	})
	return ports
}

func validDNSNamedPorts(p pod, requirements dnsPortRequirements) bool {
	// Older bundles omit these declarations. Replay can use the old Pod
	// state, while named targetPort recovery lacks corroboration.
	if len(p.DNSNamedPorts) == 0 {
		return true
	}
	if !requirements.selects(p.Identity) {
		return false
	}
	seen := make(map[dnsNamedPort]bool, len(p.DNSNamedPorts))
	for _, declared := range p.DNSNamedPorts {
		if seen[declared] || !validDNSNamedPort(declared, requirements.targets) {
			return false
		}
		seen[declared] = true
	}
	return true
}

func validDNSNamedPort(p dnsNamedPort, targets map[dnsPortTarget]bool) bool {
	return targets[dnsPortTarget{p.Name, p.Protocol}] && len(validation.IsValidPortName(p.Name)) == 0 &&
		p.Number > 0 && p.Number <= 65535 && oneOf(p.Protocol, "TCP", "UDP", "SCTP") &&
		oneOf(p.Role, dnsApplicationPort, dnsInitSidecarPort)
}

func (p pod) dnsObservation() core.Pod {
	out := p.observation()
	var app, sidecar core.Container
	for _, declared := range p.DNSNamedPorts {
		port := core.ContainerPort{Name: declared.Name, ContainerPort: declared.Number, Protocol: core.Protocol(declared.Protocol)}
		if declared.Role == dnsInitSidecarPort {
			sidecar.Ports = append(sidecar.Ports, port)
		} else {
			app.Ports = append(app.Ports, port)
		}
	}
	// Only the port declaration and role are observed inputs here.
	if len(app.Ports) != 0 {
		out.Spec.Containers = []core.Container{app}
	}
	if len(sidecar.Ports) != 0 {
		sidecar.RestartPolicy = new(core.ContainerRestartPolicyAlways)
		out.Spec.InitContainers = []core.Container{sidecar}
	}
	return out
}

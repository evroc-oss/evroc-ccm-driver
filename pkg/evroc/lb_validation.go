// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"fmt"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// annotationLBType selects the load balancer's addressing. Only "external"
	// is supported today; see lbTypeInternal.
	annotationLBType = "ccm.evroc.com/lb-type"

	// annotationProxyProtocol enables PROXY protocol on the backend services.
	annotationProxyProtocol = "ccm.evroc.com/proxy-protocol"

	// annotationHealthCheckPort names a Service port (by name or number) whose
	// node port the load balancer health checks instead of each traffic port.
	annotationHealthCheckPort = "ccm.evroc.com/health-check-port"

	// annotationHealthCheckPath switches the health check from TCP to an HTTP
	// GET of this path.
	annotationHealthCheckPath = "ccm.evroc.com/health-check-path"

	// annotationHealthCheckHost overrides the HTTP probe Host header.
	annotationHealthCheckHost = "ccm.evroc.com/health-check-host"

	// lbTypeExternal is a load balancer reachable from outside the VPC through
	// a public IP. This is the default.
	lbTypeExternal = "external"

	// lbTypeInternal is a load balancer reachable only from within the VPC.
	// The evroc LoadBalancer API requires a publicIPRef on every load balancer
	// and offers no internal-only mode, so this is rejected rather than
	// silently provisioning a public address for a service asking to be
	// private. When the API gains support, this is where it hooks in.
	lbTypeInternal = "internal"
)

// unsupportedServiceError describes a Service this provider cannot implement
// faithfully. It is returned rather than approximated, so that a service never
// appears healthy while silently behaving differently from what it asked for.
type unsupportedServiceError struct {
	field  string
	reason string
}

func (e *unsupportedServiceError) Error() string {
	return fmt.Sprintf("%s is not supported by the evroc load balancer: %s", e.field, e.reason)
}

// validateService rejects services requesting behaviour the evroc load
// balancer cannot provide.
func validateService(service *v1.Service) error {
	if err := validatePorts(service); err != nil {
		return err
	}
	if err := validateLBType(service); err != nil {
		return err
	}
	if err := validateTrafficPolicy(service); err != nil {
		return err
	}
	if err := validateHealthCheck(service); err != nil {
		return err
	}
	if err := validateSessionAffinity(service); err != nil {
		return err
	}
	return validateIPFamilies(service)
}

func validatePorts(service *v1.Service) error {
	for _, port := range service.Spec.Ports {
		// Protocol is defaulted to TCP by the Kubernetes API. Accept the empty
		// value as TCP as well so validation remains useful for objects built by
		// clients before API defaulting has run.
		if port.Protocol != "" && port.Protocol != v1.ProtocolTCP {
			return &unsupportedServiceError{
				field:  fmt.Sprintf("spec.ports[%q].protocol: %s", port.Name, port.Protocol),
				reason: "the evroc load balancer API currently supports TCP listeners only",
			}
		}
		if port.NodePort == 0 {
			return &unsupportedServiceError{
				field:  fmt.Sprintf("spec.ports[%q].nodePort: 0", port.Name),
				reason: "evroc load balancers forward to node ports; enable load balancer node-port allocation",
			}
		}
	}
	return nil
}

func validateLBType(service *v1.Service) error {
	switch t := service.Annotations[annotationLBType]; t {
	case "", lbTypeExternal:
		return nil
	case lbTypeInternal:
		return &unsupportedServiceError{
			field: fmt.Sprintf("annotation %s=%s", annotationLBType, lbTypeInternal),
			reason: "the evroc load balancer API requires a public IP on every load balancer, " +
				"so an internal-only load balancer cannot be created",
		}
	default:
		return &unsupportedServiceError{
			field:  fmt.Sprintf("annotation %s=%s", annotationLBType, t),
			reason: fmt.Sprintf("must be %q or %q", lbTypeExternal, lbTypeInternal),
		}
	}
}

// validateTrafficPolicy checks that externalTrafficPolicy: Local can be
// honoured.
//
// Under Local, kube-proxy only forwards to pods on the same node, so the load
// balancer must health check kube-proxy's healthCheckNodePort to avoid sending
// traffic to nodes that would drop it. Kubernetes allocates that port together
// with the node ports; without it the service cannot be routed correctly.
//
// Note that Local does not preserve the client source address on evroc, since
// the load balancer is a proxy. Backends that need the client address must
// opt in to the PROXY protocol.
func validateTrafficPolicy(service *v1.Service) error {
	if service.Spec.ExternalTrafficPolicy == v1.ServiceExternalTrafficPolicyLocal && service.Spec.HealthCheckNodePort == 0 {
		return &unsupportedServiceError{
			field:  "spec.healthCheckNodePort: 0",
			reason: "externalTrafficPolicy: Local requires a health check node port; enable load balancer node-port allocation",
		}
	}
	return nil
}

// validateHealthCheck checks the health check annotations. Under
// externalTrafficPolicy: Local the check must stay on kube-proxy's
// healthCheckNodePort, or nodes without a local pod would keep receiving
// traffic, so the annotations are rejected there rather than ignored.
func validateHealthCheck(service *v1.Service) error {
	port, path := service.Annotations[annotationHealthCheckPort], service.Annotations[annotationHealthCheckPath]
	host := service.Annotations[annotationHealthCheckHost]
	if port == "" && path == "" && host == "" {
		return nil
	}
	if service.Spec.ExternalTrafficPolicy == v1.ServiceExternalTrafficPolicyLocal {
		return &unsupportedServiceError{
			field:  fmt.Sprintf("annotation %s", annotationHealthCheckPort),
			reason: "externalTrafficPolicy: Local always health checks spec.healthCheckNodePort",
		}
	}
	if host != "" && path == "" {
		return &unsupportedServiceError{
			field:  fmt.Sprintf("annotation %s", annotationHealthCheckHost),
			reason: "requires ccm.evroc.com/health-check-path for an HTTP health check",
		}
	}
	// Health-check hosts must follow the DNS1123 subdomain format (max 253
	// characters), without a scheme or port.
	if host != "" && len(validation.IsDNS1123Subdomain(host)) != 0 {
		return &unsupportedServiceError{
			field:  fmt.Sprintf("annotation %s=%s", annotationHealthCheckHost, host),
			reason: "must be a lowercase hostname or IPv4 address without a scheme or port",
		}
	}
	if port != "" && findServicePort(service, port) == nil {
		return &unsupportedServiceError{
			field:  fmt.Sprintf("annotation %s=%s", annotationHealthCheckPort, port),
			reason: "must be the name or number of a port in spec.ports",
		}
	}
	if path != "" && !strings.HasPrefix(path, "/") {
		return &unsupportedServiceError{
			field:  fmt.Sprintf("annotation %s=%s", annotationHealthCheckPath, path),
			reason: "must be an absolute path",
		}
	}
	return nil
}

// findServicePort returns the Service port matching the given name or number.
func findServicePort(service *v1.Service, nameOrNumber string) *v1.ServicePort {
	for i := range service.Spec.Ports {
		p := &service.Spec.Ports[i]
		if p.Name == nameOrNumber || strconv.Itoa(int(p.Port)) == nameOrNumber {
			return p
		}
	}
	return nil
}

// healthCheck returns the node port and HTTP path the load balancer must
// health check, or 0 and "" for a TCP check of each traffic port. Under
// externalTrafficPolicy: Local this is kube-proxy's healthCheckNodePort;
// otherwise it comes from the health check annotations.
func healthCheck(service *v1.Service) (port int32, path string) {
	if service.Spec.ExternalTrafficPolicy == v1.ServiceExternalTrafficPolicyLocal {
		return service.Spec.HealthCheckNodePort, localHealthCheckPath
	}
	if p := findServicePort(service, service.Annotations[annotationHealthCheckPort]); p != nil {
		port = p.NodePort
	}
	return port, service.Annotations[annotationHealthCheckPath]
}

func validateSessionAffinity(service *v1.Service) error {
	if service.Spec.SessionAffinity == v1.ServiceAffinityClientIP {
		return &unsupportedServiceError{
			field:  "spec.sessionAffinity: ClientIP",
			reason: "the evroc load balancer backend services have no session affinity setting",
		}
	}
	return nil
}

// validateIPFamilies is the admission boundary for frontend address-family
// support. Reject any Service assigned an IPv6 family rather than silently
// creating only the IPv4 half of a requested dual-stack load balancer. PublicIP
// allocation and status extraction have matching boundaries in
// newPublicIPRequest and publicIPAddress. When the LoadBalancer and PublicIP APIs
// support IPv6, update those three places together.
//
// Reaching backends over IPv6 is a separate concern, configured cluster-wide
// by loadbalancers.stackType rather than per Service.
func validateIPFamilies(service *v1.Service) error {
	for _, family := range service.Spec.IPFamilies {
		if family == v1.IPv6Protocol {
			return &unsupportedServiceError{
				field:  fmt.Sprintf("spec.ipFamilies: %v", service.Spec.IPFamilies),
				reason: "evroc load balancer frontends support IPv4 only",
			}
		}
	}
	return nil
}

// wantsProxyProtocol reports whether the service opted in to the PROXY
// protocol. It defaults to off: enabling it prepends a header that a backend
// which cannot parse it will read as application data, breaking every
// connection, so it is never turned on implicitly.
func wantsProxyProtocol(service *v1.Service) bool {
	return service.Annotations[annotationProxyProtocol] == "true"
}

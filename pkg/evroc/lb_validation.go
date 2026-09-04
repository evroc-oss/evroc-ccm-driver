// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
)

const (
	// annotationLBType selects the load balancer's addressing. Only "external"
	// is supported today; see lbTypeInternal.
	annotationLBType = "ccm.evroc.com/lb-type"

	// annotationProxyProtocol enables PROXY protocol on the backend services.
	annotationProxyProtocol = "ccm.evroc.com/proxy-protocol"

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

// validateTrafficPolicy rejects externalTrafficPolicy: Local.
//
// Local requires the load balancer to preserve the client source address,
// which the evroc load balancer does not do.
func validateTrafficPolicy(service *v1.Service) error {
	if service.Spec.ExternalTrafficPolicy == v1.ServiceExternalTrafficPolicyLocal {
		return &unsupportedServiceError{
			field:  "spec.externalTrafficPolicy: Local",
			reason: "Local requires preserving the client source address, which the evroc load balancer does not support",
		}
	}
	return nil
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

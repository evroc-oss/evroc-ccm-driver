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
	annotationLBType = "evroc.com/lb-type"

	// annotationProxyProtocol enables PROXY protocol on the backend services.
	annotationProxyProtocol = "evroc.com/proxy-protocol"

	// lbTypeExternal is a load balancer reachable from outside the VPC through
	// a public IP. This is the default.
	lbTypeExternal = "external"

	// lbTypeInternal is a load balancer reachable only from within the VPC.
	// The evroc LoadBalancer API requires a publicIPRef on every load balancer
	// and offers no internal-only mode, so this is rejected rather than
	// silently provisioning a public address for a service asking to be
	// private. When the API gains support, this is where it hooks in.
	lbTypeInternal = "internal"

	// LoadBalancerClass is the loadBalancerClass this provider serves. A
	// service that names a different class belongs to another controller.
	LoadBalancerClass = "evroc.com/lb"
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

// shouldServe reports whether this provider owns the given service. Kubernetes
// only routes services to a provider whose loadBalancerClass matches, but the
// field is optional: a service with no class set is served by the default
// provider, which is this one.
func shouldServe(service *v1.Service) bool {
	if service.Spec.LoadBalancerClass == nil {
		return true
	}
	return *service.Spec.LoadBalancerClass == LoadBalancerClass
}

// validateService rejects services requesting behaviour the evroc load
// balancer cannot provide.
func validateService(service *v1.Service) error {
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
// The evroc load balancer's backends are whole VMs, not individual pods, so
// traffic always lands on a node and is forwarded from there by kube-proxy.
// That is exactly Cluster semantics. Honouring Local would mean registering
// only nodes that host a ready pod and health checking them on kube-proxy's
// healthz node port, which this provider does not do, so a service asking for
// Local would silently get Cluster behaviour: the client source IP it expects
// to preserve would still be lost to the second hop.
func validateTrafficPolicy(service *v1.Service) error {
	if service.Spec.ExternalTrafficPolicy == v1.ServiceExternalTrafficPolicyLocal {
		return &unsupportedServiceError{
			field: "spec.externalTrafficPolicy: Local",
			reason: "load balancer backends are whole VMs rather than pods, so traffic is always " +
				"forwarded by kube-proxy and the client source IP is not preserved; use " +
				"Cluster, and set the " + annotationProxyProtocol + " annotation if the " +
				"backend can read the PROXY protocol header",
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
// support. PublicIP allocation and status extraction have matching boundaries
// in newPublicIPRequest and publicIPAddress. When the LoadBalancer and PublicIP
// APIs support IPv6, update those three places together.
//
// Reaching backends over IPv6 is a separate concern, configured cluster-wide
// by network.stackType rather than per Service.
func validateIPFamilies(service *v1.Service) error {
	for _, family := range service.Spec.IPFamilies {
		if family == v1.IPv6Protocol && len(service.Spec.IPFamilies) == 1 {
			return &unsupportedServiceError{
				field:  "spec.ipFamilies: [IPv6]",
				reason: "the evroc load balancer frontend is always IPv4",
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

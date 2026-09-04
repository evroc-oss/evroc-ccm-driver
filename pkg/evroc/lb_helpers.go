// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	"github.com/evroc-oss/evroc-go-sdk/loadbalancer"
	lbtypes "github.com/evroc-oss/evroc-go-sdk/types/loadbalancer"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

func lbPoolName(lbName string) string {
	return lbName + "-pool"
}

func lbBackendServiceName(lbName, listenerName string) string {
	return lbName + "-" + listenerName + "-svc"
}

func lbRouteName(lbName, listenerName string) string {
	return lbName + "-" + listenerName + "-route"
}

type listenerInput struct {
	Name         string
	FrontendPort int32
	BackendPort  int32
}

// lbEnsureOptions carries everything needed to reconcile the load balancer
// and its sub-resources.
type lbEnsureOptions struct {
	Name        string
	PublicIPRef string
	Listeners   []listenerInput
	BackendRefs []string

	// ProxyProtocol enables the PROXY protocol on every backend service, so
	// backends can recover the client's address. Off unless the service opts in.
	ProxyProtocol bool

	// BackendStackType is the cluster's IP stack. When "ipv6-only" the backend
	// services select IPv6 so the load balancer can reach backends that have no
	// IPv4 address. This is deliberately separate from the frontend address
	// family, which is determined by the Service and PublicIP capabilities.
	BackendStackType string

	// BackendNetwork pins the load balancer to the explicitly configured VPC
	// and zone/subnet pairs. When nil the platform applies its defaults.
	BackendNetwork *lbtypes.LoadbalancerSpecBackendNetwork
}

func lbEnsure(ctx context.Context, client *loadbalancer.Client, opts lbEnsureOptions) (*lbtypes.Loadbalancer, error) {
	lbName := opts.Name
	poolName := lbPoolName(lbName)

	_, err := loadbalancer.NewBackendPoolBuilder(poolName).
		WithBackendRefs(opts.BackendRefs).
		Create(ctx, client.BackendPools())
	if errors.Is(err, evroc.ErrConflict) {
		_, err = client.BackendPools().Patch(ctx, poolName, map[string]any{
			"spec": map[string]any{"backendRefs": opts.BackendRefs},
		})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile backend pool: %w", err)
	}

	var lbListeners []lbtypes.LoadbalancerSpecListenersItem
	for _, l := range opts.Listeners {
		svcName := lbBackendServiceName(lbName, l.Name)
		svcReq := loadbalancer.NewBackendServiceBuilder(svcName).
			WithPort(l.BackendPort).
			WithBackendPoolRef(client.BackendPoolRef(poolName)).
			WithTCPHealthCheck().
			Build()
		proxy := opts.ProxyProtocol
		svcReq.Spec.ProxyProtocol = &proxy
		// Only ipv6-only clusters need this: on dual-stack the backends still
		// have IPv4 addresses, so the default path reaches them.
		if opts.BackendStackType == ccmconfig.StackTypeIPv6Only {
			proto := lbtypes.IPv6
			svcReq.Spec.IpProtocolSelection = &proto
		}
		_, err := client.BackendServices().Create(ctx, svcReq)
		if errors.Is(err, evroc.ErrConflict) {
			_, err = client.BackendServices().Patch(ctx, svcName, map[string]any{
				"spec": map[string]any{
					"backendPoolRef":      svcReq.Spec.BackendPoolRef,
					"healthCheck":         svcReq.Spec.HealthCheck,
					"ipProtocolSelection": svcReq.Spec.IpProtocolSelection,
					"port":                svcReq.Spec.Port,
					"proxyProtocol":       svcReq.Spec.ProxyProtocol,
				},
			})
		}
		if err != nil {
			return nil, fmt.Errorf("failed to reconcile backend service %q: %w", svcName, err)
		}

		routeName := lbRouteName(lbName, l.Name)
		routeReq := loadbalancer.NewL4RouteBuilder(routeName).
			WithBackendServiceRef(client.BackendServiceRef(svcName)).
			Build()
		_, err = client.L4Routes().Create(ctx, routeReq)
		if errors.Is(err, evroc.ErrConflict) {
			_, err = client.L4Routes().Patch(ctx, routeName, map[string]any{
				"spec": routeReq.Spec,
			})
		}
		if err != nil {
			return nil, fmt.Errorf("failed to reconcile L4 route %q: %w", routeName, err)
		}

		name := l.Name
		routeRefs := []string{client.L4RouteRef(routeName)}
		lbListeners = append(lbListeners, lbtypes.LoadbalancerSpecListenersItem{
			Name:      &name,
			Protocol:  lbtypes.TCP,
			Port:      l.FrontendPort,
			RouteRefs: &routeRefs,
		})
	}

	builder := loadbalancer.NewLoadBalancerBuilder(lbName).
		WithPublicIPRef(opts.PublicIPRef)
	for _, l := range lbListeners {
		builder = builder.WithListener(l)
	}

	// The builder has no backend network setter, so it is applied to the built
	// request directly.
	lbReq := builder.Build()
	if opts.BackendNetwork != nil {
		lbReq.Spec.BackendNetwork = opts.BackendNetwork
	}
	created, err := client.LoadBalancers().Create(ctx, lbReq)
	if errors.Is(err, evroc.ErrConflict) {
		// backendNetwork is immutable and may only be set by the create above.
		created, err = client.LoadBalancers().Patch(ctx, lbName, map[string]any{
			"spec": map[string]any{
				"listeners":   lbListeners,
				"publicIPRef": opts.PublicIPRef,
			},
		})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile load balancer: %w", err)
	}

	// Returning a cleanup error makes the Kubernetes service controller retry
	// the full, idempotent reconciliation, including stale-resource discovery.
	if err := lbDeleteStaleListeners(ctx, client, lbName, opts.Listeners); err != nil {
		return nil, err
	}
	return created, nil
}

// lbDeleteStaleListeners removes resources left by deleted or renamed Service
// ports. Discovery also catches resources from a partially completed reconcile.
func lbDeleteStaleListeners(ctx context.Context, client *loadbalancer.Client, lbName string, listeners []listenerInput) error {
	desiredRoutes := make(map[string]bool, len(listeners))
	desiredServices := make(map[string]bool, len(listeners))
	for _, listener := range listeners {
		desiredRoutes[lbRouteName(lbName, listener.Name)] = true
		desiredServices[lbBackendServiceName(lbName, listener.Name)] = true
	}

	var errs []error
	routes, err := client.L4Routes().List(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to list L4 routes: %w", err))
	} else {
		for _, route := range routes.Items {
			name := route.Metadata.Id
			if isOwnedListenerResource(name, lbName, "-route") && !desiredRoutes[name] {
				if err := client.L4Routes().Delete(ctx, name); err != nil && !errors.Is(err, evroc.ErrNotFound) {
					errs = append(errs, fmt.Errorf("failed to delete stale L4 route %q: %w", name, err))
				}
			}
		}
	}

	services, err := client.BackendServices().List(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to list backend services: %w", err))
	} else {
		for _, service := range services.Items {
			name := service.Metadata.Id
			if isOwnedListenerResource(name, lbName, "-svc") && !desiredServices[name] {
				if err := client.BackendServices().Delete(ctx, name); err != nil && !errors.Is(err, evroc.ErrNotFound) {
					errs = append(errs, fmt.Errorf("failed to delete stale backend service %q: %w", name, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

func isOwnedListenerResource(name, lbName, suffix string) bool {
	return strings.HasPrefix(name, lbName+"-") && strings.HasSuffix(name, suffix)
}

// lbCleanup tears down the load balancer and discovers and removes all of its
// sub-resources, including fragments not present in the Service specification.
//
// Discovery is by deterministic name rather than by walking the load
// balancer's listeners. A partially created load balancer is the common case
// that leaks: if creation failed after the backend pool, service, or route
// were made but before the load balancer itself, walking the (missing) load
// balancer would find nothing and the sub-resources would be orphaned forever,
// still costing money. The names are all derived from the load balancer name
// and the service's listeners, so they can be reconstructed here without the
// load balancer being present.
//
// listenerNames are the per-port listener names (see listenerName). They let
// deletion attempt the known resource names even if collection discovery
// fails; successful discovery adds any other fragments that actually exist.
func lbCleanup(ctx context.Context, client *loadbalancer.Client, lbName string, listenerNames []string) error {
	// Delete top-down: each resource is referenced by the one above it. Every
	// delete tolerates NotFound, so tearing down a partially created or
	// already-partially-deleted load balancer is safe and idempotent.
	var errs []error
	recordError := func(operation string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to %s: %w", operation, err))
		}
	}
	deleteResource := func(what string, err error) {
		if !errors.Is(err, evroc.ErrNotFound) {
			recordError("delete "+what, err)
		}
	}

	deleteResource(fmt.Sprintf("load balancer %q", lbName), client.LoadBalancers().Delete(ctx, lbName))

	// Seed the deletion sets with names that can be reconstructed from the
	// Service. List failures are still returned, but do not prevent these
	// known resources from being deleted on the same attempt.
	routesToDelete := make(map[string]bool, len(listenerNames))
	servicesToDelete := make(map[string]bool, len(listenerNames))
	for _, listenerName := range listenerNames {
		routesToDelete[lbRouteName(lbName, listenerName)] = true
		servicesToDelete[lbBackendServiceName(lbName, listenerName)] = true
	}

	// Include discovered resources so ports removed from the Service, or a
	// reconcile interrupted before the LoadBalancer was created, are cleaned up.
	if routes, err := client.L4Routes().List(ctx); err != nil {
		recordError("list L4 routes for discovery", err)
	} else {
		for _, route := range routes.Items {
			if isOwnedListenerResource(route.Metadata.Id, lbName, "-route") {
				routesToDelete[route.Metadata.Id] = true
			}
		}
	}
	if services, err := client.BackendServices().List(ctx); err != nil {
		recordError("list backend services for discovery", err)
	} else {
		for _, service := range services.Items {
			if isOwnedListenerResource(service.Metadata.Id, lbName, "-svc") {
				servicesToDelete[service.Metadata.Id] = true
			}
		}
	}

	for routeName := range routesToDelete {
		deleteResource(fmt.Sprintf("L4 route %q", routeName), client.L4Routes().Delete(ctx, routeName))
	}
	for serviceName := range servicesToDelete {
		deleteResource(fmt.Sprintf("backend service %q", serviceName), client.BackendServices().Delete(ctx, serviceName))
	}

	poolName := lbPoolName(lbName)
	deleteResource(fmt.Sprintf("backend pool %q", poolName), client.BackendPools().Delete(ctx, poolName))

	return errors.Join(errs...)
}

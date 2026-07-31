// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"errors"
	"fmt"
	"path"

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

// lbCreateOptions carries everything needed to build the load balancer and its
// sub-resources.
type lbCreateOptions struct {
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

	// BackendNetwork pins the load balancer to a VPC and its per-zone subnets.
	// When nil the load balancer attaches to the bootstrap subnet in each zone.
	BackendNetwork *lbtypes.LoadbalancerSpecBackendNetwork
}

// ignoreExists discards an AlreadyExists error.
//
// Creating a load balancer means creating five resources in sequence, which is
// not atomic: a failure partway through leaves the earlier ones behind. The
// service controller then retries, and without this every retry would fail on
// the first resource it had already created, wedging the service permanently.
// Each resource is named deterministically from the service, so an existing
// one is always this load balancer's own.
func ignoreExists(err error, what string) error {
	if err == nil || errors.Is(err, evroc.ErrConflict) {
		return nil
	}
	return fmt.Errorf("failed to create %s: %w", what, err)
}

func lbCreate(ctx context.Context, client *loadbalancer.Client, opts lbCreateOptions) (*lbtypes.Loadbalancer, error) {
	lbName := opts.Name
	poolName := lbPoolName(lbName)

	_, err := loadbalancer.NewBackendPoolBuilder(poolName).
		WithBackendRefs(opts.BackendRefs).
		Create(ctx, client.BackendPools())
	if err := ignoreExists(err, "backend pool"); err != nil {
		return nil, err
	}

	var lbListeners []lbtypes.LoadbalancerSpecListenersItem
	for _, l := range opts.Listeners {
		svcName := lbBackendServiceName(lbName, l.Name)
		svcReq := loadbalancer.NewBackendServiceBuilder(svcName).
			WithPort(l.BackendPort).
			WithBackendPoolRef(client.BackendPoolRef(poolName)).
			WithTCPHealthCheck().
			Build()
		if opts.ProxyProtocol {
			proxy := true
			svcReq.Spec.ProxyProtocol = &proxy
		}
		// Only ipv6-only clusters need this: on dual-stack the backends still
		// have IPv4 addresses, so the default path reaches them.
		if opts.BackendStackType == ccmconfig.StackTypeIPv6Only {
			proto := lbtypes.IPv6
			svcReq.Spec.IpProtocolSelection = &proto
		}
		_, err := client.BackendServices().Create(ctx, svcReq)
		if err := ignoreExists(err, fmt.Sprintf("backend service %q", svcName)); err != nil {
			return nil, err
		}

		routeName := lbRouteName(lbName, l.Name)
		_, err = loadbalancer.NewL4RouteBuilder(routeName).
			WithBackendServiceRef(client.BackendServiceRef(svcName)).
			Create(ctx, client.L4Routes())
		if err := ignoreExists(err, fmt.Sprintf("L4 route %q", routeName)); err != nil {
			return nil, err
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
		// Created by an earlier attempt that failed later on; adopt it.
		return client.LoadBalancers().Get(ctx, lbName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer: %w", err)
	}
	return created, nil
}

// lbDelete tears down the load balancer and all of its sub-resources.
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
// listenerNames are the per-port listener names (see listenerName); they are
// needed because backend service and route names embed them.
func lbDelete(ctx context.Context, client *loadbalancer.Client, lbName string, listenerNames []string) error {
	// Delete top-down: each resource is referenced by the one above it. Every
	// delete tolerates NotFound, so tearing down a partially created or
	// already-partially-deleted load balancer is safe and idempotent.
	var errs []error
	del := func(what string, err error) {
		if err != nil && !errors.Is(err, evroc.ErrNotFound) {
			errs = append(errs, fmt.Errorf("failed to delete %s: %w", what, err))
		}
	}

	del(fmt.Sprintf("load balancer %q", lbName), client.LoadBalancers().Delete(ctx, lbName))

	for _, ln := range listenerNames {
		routeName := lbRouteName(lbName, ln)
		del(fmt.Sprintf("L4 route %q", routeName), client.L4Routes().Delete(ctx, routeName))

		svcName := lbBackendServiceName(lbName, ln)
		del(fmt.Sprintf("backend service %q", svcName), client.BackendServices().Delete(ctx, svcName))
	}

	poolName := lbPoolName(lbName)
	del(fmt.Sprintf("backend pool %q", poolName), client.BackendPools().Delete(ctx, poolName))

	return errors.Join(errs...)
}

func lbSyncBackends(ctx context.Context, client *loadbalancer.Client, lbName string, desiredRefs []string) error {
	lb, err := client.LoadBalancers().Get(ctx, lbName)
	if err != nil {
		return fmt.Errorf("failed to get load balancer: %w", err)
	}

	var poolName string
	if lb.Spec.Listeners != nil {
		for _, l := range *lb.Spec.Listeners {
			if l.RouteRefs == nil {
				continue
			}
			for _, routeRef := range *l.RouteRefs {
				route, err := client.L4Routes().Get(ctx, path.Base(routeRef))
				if err != nil {
					continue
				}
				svc, err := client.BackendServices().Get(ctx, path.Base(route.Spec.DefaultBackendServiceRef))
				if err != nil {
					continue
				}
				if svc.Spec.BackendPoolRef != nil {
					poolName = path.Base(*svc.Spec.BackendPoolRef)
					break
				}
			}
			if poolName != "" {
				break
			}
		}
	}

	if poolName == "" {
		return fmt.Errorf("no backend pool found for load balancer %q", lbName)
	}

	_, err = client.BackendPools().Patch(ctx, poolName, map[string]interface{}{
		"spec": lbtypes.BackendpoolSpec{BackendRefs: &desiredRefs},
	})
	return err
}

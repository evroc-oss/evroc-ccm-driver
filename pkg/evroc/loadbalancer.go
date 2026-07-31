// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	"github.com/evroc-oss/evroc-go-sdk/loadbalancer"
	"github.com/evroc-oss/evroc-go-sdk/networking"
	lbtypes "github.com/evroc-oss/evroc-go-sdk/types/loadbalancer"
	networkingtypes "github.com/evroc-oss/evroc-go-sdk/types/networking"
	v1 "k8s.io/api/core/v1"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

const (
	annotationPublicIPRef = "evroc.com/public-ip-ref"

	publicIPWaitTimeout = 5 * time.Minute
)

// lbClient abstracts the LoadBalancer operations for testability.
type lbClient interface {
	Create(ctx context.Context, opts lbCreateOptions) (*lbtypes.Loadbalancer, error)
	Delete(ctx context.Context, lbName string, listenerNames []string) error
	SyncBackends(ctx context.Context, lbName string, desiredVMRefs []string) error
	GetLB(ctx context.Context, name string) (*lbtypes.Loadbalancer, error)
}

// publicIPService abstracts PublicIP operations for testability.
type publicIPService interface {
	Create(ctx context.Context, request *networkingtypes.PublicIPRequest) (*networkingtypes.PublicIP, error)
	Get(ctx context.Context, name string) (*networkingtypes.PublicIP, error)
	Delete(ctx context.Context, name string) error
	WaitForReady(ctx context.Context, name string, timeout time.Duration, opts ...networking.WaiterOption) (*networkingtypes.PublicIP, error)
}

// loadBalancer implements cloudprovider.LoadBalancer.
type loadBalancer struct {
	lb     lbClient
	ips    publicIPService
	config *ccmconfig.Config
	logger *slog.Logger
}

func newLoadBalancer(lbCl *loadbalancer.Client, netCl *evroc.Client, cfg *ccmconfig.Config, logger *slog.Logger) *loadBalancer {
	return &loadBalancer{
		lb:     &sdkLBClient{client: lbCl},
		ips:    netCl.Networking().PublicIPs(),
		config: cfg,
		logger: logger.With("subsystem", "loadbalancer"),
	}
}

// sdkLBClient wraps loadbalancer.Client to satisfy the lbClient interface.
type sdkLBClient struct {
	client *loadbalancer.Client
}

func (s *sdkLBClient) Create(ctx context.Context, opts lbCreateOptions) (*lbtypes.Loadbalancer, error) {
	return lbCreate(ctx, s.client, opts)
}

func (s *sdkLBClient) Delete(ctx context.Context, lbName string, listenerNames []string) error {
	return lbDelete(ctx, s.client, lbName, listenerNames)
}

func (s *sdkLBClient) SyncBackends(ctx context.Context, lbName string, desiredVMRefs []string) error {
	return lbSyncBackends(ctx, s.client, lbName, desiredVMRefs)
}

func (s *sdkLBClient) GetLB(ctx context.Context, name string) (*lbtypes.Loadbalancer, error) {
	return s.client.LoadBalancers().Get(ctx, name)
}

// GetLoadBalancer returns the load balancer status for the given service.
func (lb *loadBalancer) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (*v1.LoadBalancerStatus, bool, error) {
	lbName := lb.GetLoadBalancerName(ctx, clusterName, service)
	lb.logger.Debug("getting load balancer", "name", lbName, "service", service.Name)

	evrocLB, err := lb.lb.GetLB(ctx, lbName)
	if err != nil {
		if errors.Is(err, evroc.ErrNotFound) {
			lb.logger.Info("load balancer not found", "name", lbName)
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to get load balancer %q: %w", lbName, err)
	}

	status, err := lb.toLBStatus(ctx, evrocLB)
	if err != nil {
		return nil, true, fmt.Errorf("failed to resolve load balancer status: %w", err)
	}
	return status, true, nil
}

// GetLoadBalancerName returns the name of the load balancer for the given service.
func (lb *loadBalancer) GetLoadBalancerName(_ context.Context, _ string, service *v1.Service) string {
	return fmt.Sprintf("%s-%s", service.Namespace, service.Name)
}

// EnsureLoadBalancer creates or updates a load balancer for the given service.
func (lb *loadBalancer) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	lbName := lb.GetLoadBalancerName(ctx, clusterName, service)
	lb.logger.Info("ensuring load balancer", "name", lbName, "service", service.Name, "nodes", len(nodes))

	if err := validateService(service); err != nil {
		lb.logger.Error("service is not supported", "name", lbName, "service", service.Name, "err", err)
		return nil, err
	}

	evrocLB, err := lb.lb.GetLB(ctx, lbName)
	if err != nil && !errors.Is(err, evroc.ErrNotFound) {
		return nil, fmt.Errorf("failed to check existing load balancer: %w", err)
	}

	if evrocLB == nil {
		evrocLB, err = lb.createLoadBalancer(ctx, lbName, service, nodes)
		if err != nil {
			return nil, fmt.Errorf("failed to create load balancer: %w", err)
		}
	} else {
		if err := lb.lb.SyncBackends(ctx, lbName, lb.nodeVMRefs(nodes)); err != nil {
			return nil, fmt.Errorf("failed to update load balancer backends: %w", err)
		}
	}

	status, err := lb.toLBStatus(ctx, evrocLB)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve load balancer status: %w", err)
	}
	return status, nil
}

// UpdateLoadBalancer updates the backends of an existing load balancer.
func (lb *loadBalancer) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) error {
	lbName := lb.GetLoadBalancerName(ctx, clusterName, service)
	lb.logger.Info("updating load balancer", "name", lbName, "nodes", len(nodes))

	if err := validateService(service); err != nil {
		return err
	}

	if err := lb.lb.SyncBackends(ctx, lbName, lb.nodeVMRefs(nodes)); err != nil {
		return fmt.Errorf("failed to update load balancer: %w", err)
	}
	return nil
}

// EnsureLoadBalancerDeleted tears down the full LB resource stack and the managed PublicIP.
func (lb *loadBalancer) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, service *v1.Service) error {
	lbName := lb.GetLoadBalancerName(ctx, clusterName, service)
	lb.logger.Info("deleting load balancer", "name", lbName, "service", service.Name)

	if err := lb.lb.Delete(ctx, lbName, serviceListenerNames(service)); err != nil {
		return fmt.Errorf("failed to delete load balancer %q: %w", lbName, err)
	}

	if _, hasAnnotation := service.Annotations[annotationPublicIPRef]; !hasAnnotation {
		ipName := lb.publicIPName(lbName)
		if err := lb.ips.Delete(ctx, ipName); err != nil && !errors.Is(err, evroc.ErrNotFound) {
			return fmt.Errorf("failed to delete public IP %q: %w", ipName, err)
		}
	}

	lb.logger.Info("load balancer deleted", "name", lbName)
	return nil
}

// createLoadBalancer allocates a PublicIP (or uses an annotated one) and creates
// the LB with its BackendPool, BackendServices, and L4Routes.
func (lb *loadBalancer) createLoadBalancer(ctx context.Context, lbName string, service *v1.Service, nodes []*v1.Node) (*lbtypes.Loadbalancer, error) {
	publicIPRef, err := lb.resolvePublicIP(ctx, lbName, service)
	if err != nil {
		return nil, err
	}

	listeners := make([]listenerInput, 0, len(service.Spec.Ports))
	for _, port := range service.Spec.Ports {
		listeners = append(listeners, listenerInput{
			Name:         listenerName(port),
			FrontendPort: port.Port,
			BackendPort:  port.NodePort,
		})
	}

	evrocLB, err := lb.lb.Create(ctx, lbCreateOptions{
		Name:             lbName,
		PublicIPRef:      publicIPRef,
		Listeners:        listeners,
		BackendRefs:      lb.nodeVMRefs(nodes),
		ProxyProtocol:    wantsProxyProtocol(service),
		BackendStackType: lb.config.Network.StackType,
		BackendNetwork:   lb.backendNetwork(nodes),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer: %w", err)
	}

	lb.logger.Info("load balancer created", "name", lbName)
	return evrocLB, nil
}

// resolvePublicIP returns the PublicIP ref to use for the LB.
func (lb *loadBalancer) resolvePublicIP(ctx context.Context, lbName string, service *v1.Service) (string, error) {
	if ref, ok := service.Annotations[annotationPublicIPRef]; ok && ref != "" {
		lb.logger.Info("using annotated public IP", "ref", ref)
		return ref, nil
	}

	ipName := lb.publicIPName(lbName)

	// Reuse the IP if it is already there. Creating a load balancer is not
	// atomic, so an earlier attempt may have allocated the IP and then failed
	// on a later step. Treating that as an error would wedge the service
	// permanently: every retry would fail with AlreadyExists and the load
	// balancer would never be created.
	if existing, err := lb.ips.Get(ctx, ipName); err == nil && existing != nil {
		lb.logger.Info("reusing public IP from an earlier attempt", "name", ipName)
	} else {
		lb.logger.Info("allocating public IP", "name", ipName)

		// Built via the SDK builder so the request carries the API version the
		// SDK actually posts to. Hardcoding it here meant the body still said
		// networking/v1beta1 after the SDK moved to v1beta2, and the API
		// rejected every public IP with a version mismatch.
		if _, err := lb.ips.Create(ctx, newPublicIPRequest(ipName)); err != nil {
			return "", fmt.Errorf("failed to create public IP: %w", err)
		}
	}

	ip, err := lb.ips.WaitForReady(ctx, ipName, publicIPWaitTimeout)
	if err != nil {
		return "", fmt.Errorf("public IP %q did not become ready: %w", ipName, err)
	}

	return string(ip.Ref()), nil
}

func (lb *loadBalancer) publicIPName(lbName string) string {
	return fmt.Sprintf("%s-ip", lbName)
}

// listenerName derives a load balancer listener name from a service port.
//
// The port's name is optional: a single-port service usually has none, and
// "%s-%d" would then produce "-80", which the API rejects for not being a
// valid DNS label. The port number alone is unique within a service, so it is
// used when there is no name to qualify it with.
func listenerName(port v1.ServicePort) string {
	if port.Name == "" {
		return fmt.Sprintf("port-%d", port.Port)
	}
	return fmt.Sprintf("%s-%d", port.Name, port.Port)
}

// serviceListenerNames returns the listener name for each of the service's
// ports. Teardown uses these to reconstruct the backend service and route
// names deterministically, so it can clean up even a load balancer that was
// only partially created.
func serviceListenerNames(service *v1.Service) []string {
	names := make([]string, 0, len(service.Spec.Ports))
	for _, port := range service.Spec.Ports {
		names = append(names, listenerName(port))
	}
	return names
}

// backendNetwork builds the load balancer's backend network from the
// configured VPC and the zones the given nodes occupy. It returns nil when no
// VPC is configured, which leaves the load balancer attached to the bootstrap
// subnet in each zone.
func (lb *loadBalancer) backendNetwork(nodes []*v1.Node) *lbtypes.LoadbalancerSpecBackendNetwork {
	if lb.config.Network.VPCRef == "" {
		return nil
	}

	// Attach to one subnet per zone the backends actually occupy.
	seen := make(map[string]bool)
	var zones []string
	for _, node := range nodes {
		zone := node.Labels[v1.LabelTopologyZone]
		if zone == "" || seen[zone] {
			continue
		}
		seen[zone] = true
		zones = append(zones, zone)
	}
	sort.Strings(zones)

	network := &lbtypes.LoadbalancerSpecBackendNetwork{
		VpcRef: lb.config.VPCRef(),
	}
	for _, zone := range zones {
		network.Subnets = append(network.Subnets, struct {
			SubnetRef string                                            `json:"subnetRef"`
			Zone      lbtypes.LoadbalancerSpecBackendNetworkSubnetsZone `json:"zone"`
		}{
			SubnetRef: lb.config.SubnetRefForZone(zone),
			Zone:      lbtypes.LoadbalancerSpecBackendNetworkSubnetsZone(zone),
		})
	}
	return network
}

// nodeVMRefs converts Kubernetes nodes to fully-qualified VM refs.
func (lb *loadBalancer) nodeVMRefs(nodes []*v1.Node) []string {
	refs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ref := fmt.Sprintf("/compute/projects/%s/regions/%s/virtualMachines/%s",
			lb.config.Context.Project, lb.config.Context.Region, node.Name)
		refs = append(refs, ref)
	}
	return refs
}

// toLBStatus resolves the ingress IP from the PublicIP ref on the LB spec.
func (lb *loadBalancer) toLBStatus(ctx context.Context, evrocLB *lbtypes.Loadbalancer) (*v1.LoadBalancerStatus, error) {
	status := &v1.LoadBalancerStatus{}

	if evrocLB.Spec.PublicIPRef == "" {
		return status, nil
	}

	ipName := lb.publicIPName(evrocLB.Metadata.Id)
	ip, err := lb.ips.Get(ctx, ipName)
	if err != nil {
		return nil, fmt.Errorf("failed to get public IP %q: %w", ipName, err)
	}

	addr := publicIPAddress(ip)
	if addr != "" {
		status.Ingress = []v1.LoadBalancerIngress{{IP: addr}}
	}

	return status, nil
}

// newPublicIPRequest is the single allocation boundary for load balancer
// frontend addresses. PublicIP only supports IPv4 today. When the API and SDK
// expose an address-family selector, apply the Service's requested family here
// rather than coupling it to the backend stack type.
func newPublicIPRequest(name string) *networkingtypes.PublicIPRequest {
	return networking.NewPublicIPBuilder(name).Build()
}

// publicIPAddress is the single status boundary for load balancer frontend
// addresses. Extend this when PublicIP status exposes IPv6; callers should not
// depend directly on the current IPv4-only SDK helper.
func publicIPAddress(ip *networkingtypes.PublicIP) string {
	return networking.GetPublicIPAddress(ip)
}

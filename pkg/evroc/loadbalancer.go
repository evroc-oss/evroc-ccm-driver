// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
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
	annotationPublicIPRef = "ccm.evroc.com/public-ip-ref"
	managedLBPrefix       = "ccm-"

	publicIPWaitTimeout     = 5 * time.Minute
	loadBalancerWaitTimeout = 5 * time.Minute
)

// lbClient abstracts the LoadBalancer operations for testability.
type lbClient interface {
	Ensure(ctx context.Context, opts lbEnsureOptions) (*lbtypes.Loadbalancer, error)
	WaitForReady(ctx context.Context, name string, timeout time.Duration) (*lbtypes.Loadbalancer, error)
	Delete(ctx context.Context, lbName string, listenerNames []string) error
	GetLB(ctx context.Context, name string) (*lbtypes.Loadbalancer, error)
	HasDependents(ctx context.Context, name string) (bool, error)
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
		lb:     &sdkLBClient{client: lbCl, owner: resourceOwner(cfg.CCMIdentifier())},
		ips:    netCl.Networking().PublicIPs(),
		config: cfg,
		logger: logger.With("subsystem", "loadbalancer"),
	}
}

// sdkLBClient wraps loadbalancer.Client to satisfy the lbClient interface.
type sdkLBClient struct {
	owner  resourceOwner
	client *loadbalancer.Client
}

func (s *sdkLBClient) Ensure(ctx context.Context, opts lbEnsureOptions) (*lbtypes.Loadbalancer, error) {
	opts.Owner = s.owner
	return lbEnsure(ctx, s.client, opts)
}

func (s *sdkLBClient) WaitForReady(ctx context.Context, name string, timeout time.Duration) (*lbtypes.Loadbalancer, error) {
	return s.client.LoadBalancers().WaitForReady(ctx, name, timeout)
}

func (s *sdkLBClient) Delete(ctx context.Context, lbName string, listenerNames []string) error {
	return lbCleanup(ctx, s.client, lbName, listenerNames, s.owner)
}

func (s *sdkLBClient) GetLB(ctx context.Context, name string) (*lbtypes.Loadbalancer, error) {
	r, err := s.client.LoadBalancers().Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := s.owner.check(name, resourceLabels(r.Metadata.UserLabels)); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *sdkLBClient) HasDependents(ctx context.Context, name string) (bool, error) {
	return lbHasDependents(ctx, s.client, name, s.owner)
}

// GetLoadBalancer returns the load balancer status for the given service.
func (lb *loadBalancer) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (*v1.LoadBalancerStatus, bool, error) {
	lbName := lb.GetLoadBalancerName(ctx, clusterName, service)
	lb.logger.Debug("getting load balancer", "name", lbName, "service", service.Name)

	evrocLB, err := lb.lb.GetLB(ctx, lbName)
	if err != nil {
		if errors.Is(err, evroc.ErrNotFound) {
			// The service controller skips cleanup and removes its finalizer when
			// exists is false. A missing frontend must not hide a partial stack.
			exists, err := lb.hasManagedDependents(ctx, lbName)
			return &v1.LoadBalancerStatus{}, exists, err
		}
		return nil, false, fmt.Errorf("failed to get load balancer %q: %w", lbName, err)
	}

	return toLBStatus(evrocLB), true, nil
}

// GetLoadBalancerName returns the name of the load balancer for the given service.
func (lb *loadBalancer) GetLoadBalancerName(_ context.Context, clusterName string, service *v1.Service) string {
	identity := clusterName + "/" + string(service.UID)
	if service.UID == "" {
		// Services received from Kubernetes always have a UID. The fallback keeps
		// tests and pre-persistence callers deterministic without reintroducing
		// namespace/name collisions between clusters.
		identity += "/" + service.Namespace + "/" + service.Name
	}
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%s%x", managedLBPrefix, sum[:12])
}

// EnsureLoadBalancer creates or updates a load balancer for the given service.
func (lb *loadBalancer) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	lbName := lb.GetLoadBalancerName(ctx, clusterName, service)
	lb.logger.Info("ensuring load balancer", "name", lbName, "service", service.Name, "nodes", len(nodes))

	if err := validateService(service); err != nil {
		lb.logger.Error("service is not supported", "name", lbName, "service", service.Name, "err", err)
		return nil, err
	}

	_, err := lb.lb.GetLB(ctx, lbName)
	if err != nil && !errors.Is(err, evroc.ErrNotFound) {
		return nil, fmt.Errorf("failed to check existing load balancer: %w", err)
	}

	evrocLB, err := lb.ensureLoadBalancer(ctx, lbName, service, nodes)
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile load balancer: %w", err)
	}

	return toLBStatus(evrocLB), nil
}

// UpdateLoadBalancer updates the backends of an existing load balancer.
func (lb *loadBalancer) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) error {
	lbName := lb.GetLoadBalancerName(ctx, clusterName, service)
	lb.logger.Info("updating load balancer", "name", lbName, "nodes", len(nodes))

	if err := validateService(service); err != nil {
		return err
	}

	if _, err := lb.ensureLoadBalancer(ctx, lbName, service, nodes); err != nil {
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

	// The managed IP has a name only the CCM allocates, so it is deleted even
	// when the service now carries an annotated IP: the annotation may have
	// been added after the managed IP was allocated, and skipping the delete
	// would leak it. The annotated IP is never touched.
	ipName := lb.publicIPName(lbName)
	if err := lb.deleteManagedIP(ctx, ipName); err != nil && !errors.Is(err, evroc.ErrNotFound) {
		return fmt.Errorf("failed to delete public IP %q: %w", ipName, err)
	}

	// Cloud deletes are asynchronous. Keep the Service finalizer until the
	// entire stack is absent, and let the service controller retry meanwhile.
	if _, exists, err := lb.GetLoadBalancer(ctx, clusterName, service); err != nil {
		return fmt.Errorf("failed to verify load balancer deletion: %w", err)
	} else if exists {
		return fmt.Errorf("load balancer %q resources are still deleting", lbName)
	}

	lb.logger.Info("load balancer deleted", "name", lbName)
	return nil
}

func (lb *loadBalancer) hasManagedDependents(ctx context.Context, name string) (bool, error) {
	if exists, err := lb.lb.HasDependents(ctx, name); err != nil || exists {
		return exists, err
	}
	// Only inspect the deterministic managed IP, never an annotated external IP.
	ip, err := lb.ips.Get(ctx, lb.publicIPName(name))
	if errors.Is(err, evroc.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check managed public IP: %w", err)
	}
	return true, lb.checkManagedIP(ip)
}

// ensureLoadBalancer resolves the PublicIP and declaratively reconciles the LB,
// BackendPool, BackendServices, and L4Routes.
func (lb *loadBalancer) ensureLoadBalancer(ctx context.Context, lbName string, service *v1.Service, nodes []*v1.Node) (*lbtypes.Loadbalancer, error) {
	publicIPRef, err := lb.ensurePublicIP(ctx, lbName, service)
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

	_, err = lb.lb.Ensure(ctx, lbEnsureOptions{
		Name:                lbName,
		PublicIPRef:         publicIPRef,
		Listeners:           listeners,
		BackendRefs:         lb.nodeVMRefs(nodes),
		ProxyProtocol:       wantsProxyProtocol(service),
		HealthCheckNodePort: localHealthCheckNodePort(service),
		BackendStackType:    lb.config.LoadBalancers.StackType,
		BackendNetwork:      lb.backendNetwork(),
	})
	if err != nil {
		return nil, err
	}

	// Load balancer creation is asynchronous. The create/patch response can
	// precede the Ready condition and therefore contain no frontend address.
	// Returning that response would make Kubernetes persist an empty Service
	// status with no subsequent event to trigger another reconciliation.
	evrocLB, err := lb.lb.WaitForReady(ctx, lbName, loadBalancerWaitTimeout)
	if err != nil {
		return nil, fmt.Errorf("load balancer %q did not become ready: %w", lbName, err)
	}
	if evrocLB == nil {
		return nil, fmt.Errorf("load balancer %q became ready without a resource", lbName)
	}

	lb.logger.Info("load balancer reconciled", "name", lbName)
	return evrocLB, nil
}

// ensurePublicIP ensures the selected PublicIP exists and returns its ref.
func (lb *loadBalancer) ensurePublicIP(ctx context.Context, lbName string, service *v1.Service) (string, error) {
	if ref := strings.TrimSpace(service.Annotations[annotationPublicIPRef]); ref != "" {
		ipName := path.Base(strings.TrimSuffix(ref, "/"))
		if ipName == "." || ipName == "/" || ipName == "" {
			return "", fmt.Errorf("invalid annotated public IP ref %q", ref)
		}
		if _, err := lb.ips.Get(ctx, ipName); err != nil {
			return "", fmt.Errorf("failed to get annotated public IP %q: %w", ipName, err)
		}
		lb.logger.Info("using annotated public IP", "ref", ref)
		return ref, nil
	}

	ipName := lb.publicIPName(lbName)

	// Reuse the IP if it is already there. Creating a load balancer is not
	// atomic, so an earlier attempt may have allocated the IP and then failed
	// on a later step. Treating that as an error would wedge the service
	// permanently: every retry would fail with AlreadyExists and the load
	// balancer would never be created.
	existing, err := lb.ips.Get(ctx, ipName)
	if err == nil {
		if err := lb.checkManagedIP(existing); err != nil {
			return "", err
		}
		lb.logger.Info("reusing public IP from an earlier attempt", "name", ipName)
	} else if errors.Is(err, evroc.ErrNotFound) {
		lb.logger.Info("allocating public IP", "name", ipName)

		// The SDK builder supplies the request's required API metadata.
		if _, err := lb.ips.Create(ctx, newPublicIPRequest(ipName, resourceOwner(lb.config.CCMIdentifier()))); err != nil && !errors.Is(err, evroc.ErrConflict) {
			return "", fmt.Errorf("failed to create public IP: %w", err)
		}
	} else {
		return "", fmt.Errorf("failed to get public IP %q: %w", ipName, err)
	}

	ip, err := lb.ips.WaitForReady(ctx, ipName, publicIPWaitTimeout)
	if err != nil {
		return "", fmt.Errorf("public IP %q did not become ready: %w", ipName, err)
	}
	if ip == nil || ip.Ref() == "" {
		return "", fmt.Errorf("public IP %q became ready without a resource reference", ipName)
	}

	if err := lb.checkManagedIP(ip); err != nil {
		return "", err
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

// backendNetwork builds the load balancer API's backend network from the
// configured zone/subnet pairs. Only explicitly configured zones are sent. It
// returns nil when no network is configured, allowing the platform to apply
// its default behavior.
func (lb *loadBalancer) backendNetwork() *lbtypes.LoadbalancerSpecBackendNetwork {
	configured := lb.config.LoadBalancers.BackendNetwork
	if configured == nil {
		return nil
	}

	network := &lbtypes.LoadbalancerSpecBackendNetwork{
		VpcRef: lb.config.VPCRef(),
	}
	for _, subnet := range configured.Subnets {
		network.Subnets = append(network.Subnets, struct {
			SubnetRef string                                            `json:"subnetRef"`
			Zone      lbtypes.LoadbalancerSpecBackendNetworkSubnetsZone `json:"zone"`
		}{
			SubnetRef: lb.config.SubnetRef(subnet.SubnetID),
			Zone:      lbtypes.LoadbalancerSpecBackendNetworkSubnetsZone(subnet.Zone),
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

// toLBStatus reads the assigned frontend address from the load balancer status.
func toLBStatus(evrocLB *lbtypes.Loadbalancer) *v1.LoadBalancerStatus {
	status := &v1.LoadBalancerStatus{}
	if evrocLB.Status.Networking != nil && evrocLB.Status.Networking.PublicIPv4Address != nil {
		if address := *evrocLB.Status.Networking.PublicIPv4Address; address != "" {
			status.Ingress = []v1.LoadBalancerIngress{{IP: address}}
		}
	}
	return status
}

// newPublicIPRequest is the single allocation boundary for load balancer
// frontend addresses. PublicIP only supports IPv4 today. When the API and SDK
// expose an address-family selector, apply the Service's requested family here
// rather than coupling it to the backend stack type.
func newPublicIPRequest(name string, owner resourceOwner) *networkingtypes.PublicIPRequest {
	return networking.NewPublicIPBuilder(name).WithLabels(owner.labels()).Build()
}

func (lb *loadBalancer) checkManagedIP(ip *networkingtypes.PublicIP) error {
	if ip == nil {
		return fmt.Errorf("managed public IP lookup returned no resource")
	}
	return resourceOwner(lb.config.CCMIdentifier()).check(ip.Metadata.Id, resourceLabels(ip.Metadata.UserLabels))
}

func (lb *loadBalancer) deleteManagedIP(ctx context.Context, name string) error {
	ip, err := lb.ips.Get(ctx, name)
	if err != nil {
		return err
	}
	if err := lb.checkManagedIP(ip); err != nil {
		return err
	}
	return lb.ips.Delete(ctx, name)
}

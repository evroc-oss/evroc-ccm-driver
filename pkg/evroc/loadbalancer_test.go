// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	"github.com/evroc-oss/evroc-go-sdk/networking"
	lbtypes "github.com/evroc-oss/evroc-go-sdk/types/loadbalancer"
	networkingtypes "github.com/evroc-oss/evroc-go-sdk/types/networking"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

// --- recording mocks ---

type recordingLBClient struct {
	lb                   *lbtypes.Loadbalancer
	err                  error
	getLBErr             error
	createdName          string
	createdIPRef         string
	createdListeners     []listenerInput
	createdVMRefs        []string
	syncedLBName         string
	syncedRefs           []string
	deletedLBName        string
	deletedListenerNames []string
	createdOpts          lbCreateOptions
}

func (m *recordingLBClient) Create(_ context.Context, opts lbCreateOptions) (*lbtypes.Loadbalancer, error) {
	m.createdName = opts.Name
	m.createdIPRef = opts.PublicIPRef
	m.createdListeners = opts.Listeners
	m.createdVMRefs = opts.BackendRefs
	m.createdOpts = opts
	return m.lb, m.err
}

func (m *recordingLBClient) Delete(_ context.Context, name string, listenerNames []string) error {
	m.deletedLBName = name
	m.deletedListenerNames = listenerNames
	return m.err
}

func (m *recordingLBClient) SyncBackends(_ context.Context, lbName string, refs []string) error {
	m.syncedLBName = lbName
	m.syncedRefs = refs
	return m.err
}

func (m *recordingLBClient) GetLB(_ context.Context, _ string) (*lbtypes.Loadbalancer, error) {
	if m.getLBErr != nil {
		return nil, m.getLBErr
	}
	return m.lb, m.err
}

type recordingPublicIPService struct {
	ip             *networkingtypes.PublicIP
	err            error
	created        bool
	deleted        bool
	createdRequest *networkingtypes.PublicIPRequest
	getErr         error
}

func (m *recordingPublicIPService) Create(_ context.Context, req *networkingtypes.PublicIPRequest) (*networkingtypes.PublicIP, error) {
	m.created = true
	m.createdRequest = req
	return m.ip, m.err
}

func (m *recordingPublicIPService) Get(_ context.Context, _ string) (*networkingtypes.PublicIP, error) {
	// getErr applies only until the IP is created, mirroring reality: the
	// lookup misses before allocation and succeeds afterwards.
	if m.getErr != nil && !m.created {
		return nil, m.getErr
	}
	return m.ip, m.err
}

func (m *recordingPublicIPService) Delete(_ context.Context, _ string) error {
	m.deleted = true
	return m.err
}

func (m *recordingPublicIPService) WaitForReady(_ context.Context, _ string, _ time.Duration, _ ...networking.WaiterOption) (*networkingtypes.PublicIP, error) {
	return m.ip, m.err
}

// --- test helpers ---

func testLBConfig() *ccmconfig.Config {
	return &ccmconfig.Config{
		Context: ccmconfig.ContextConfig{
			Project: "test-project",
			Region:  "se-sto",
		},
	}
}

func testService(name, namespace string, ports ...v1.ServicePort) *v1.Service {
	if len(ports) == 0 {
		ports = []v1.ServicePort{{
			Name:       "http",
			Port:       80,
			NodePort:   30080,
			TargetPort: intstr.FromInt32(8080),
		}}
	}
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1.ServiceSpec{
			Type:  v1.ServiceTypeLoadBalancer,
			Ports: ports,
		},
	}
}

func testServiceWithAnnotation(name, namespace, ipRef string) *v1.Service {
	svc := testService(name, namespace)
	svc.Annotations = map[string]string{
		annotationPublicIPRef: ipRef,
	}
	return svc
}

func testNodes(names ...string) []*v1.Node {
	nodes := make([]*v1.Node, len(names))
	for i, name := range names {
		nodes[i] = &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
	}
	return nodes
}

func testPublicIP(name, address string) *networkingtypes.PublicIP {
	project := "test-project"
	region := "se-sto"
	return &networkingtypes.PublicIP{
		Metadata: networkingtypes.RegionalMetadataResponse{
			Id:      name,
			Project: &project,
			Region:  &region,
		},
		Status: networkingtypes.PublicIPStatus{
			PublicIPv4Address: &address,
		},
	}
}

func testEvrocLB(name string) *lbtypes.Loadbalancer {
	return &lbtypes.Loadbalancer{
		Metadata: lbtypes.RegionalMetadataResponse{
			Id: name,
		},
		Spec: lbtypes.LoadbalancerSpec{
			PublicIPRef: "/networking/projects/test-project/regions/se-sto/publicIPs/" + name + "-ip",
		},
	}
}

func newTestLB(lbMock lbClient, ipMock publicIPService) *loadBalancer {
	return &loadBalancer{
		lb:     lbMock,
		ips:    ipMock,
		config: testLBConfig(),
		logger: slog.Default(),
	}
}

func vmRef(name string) string {
	return fmt.Sprintf("/compute/projects/test-project/regions/se-sto/virtualMachines/%s", name)
}

// --- GetLoadBalancerName ---

func TestGetLoadBalancerName(t *testing.T) {
	lb := newTestLB(&mockLBClient{}, &mockPublicIPService{})
	svc := testService("my-svc", "default")
	got := lb.GetLoadBalancerName(context.Background(), "cluster", svc)
	if got != "default-my-svc" {
		t.Errorf("GetLoadBalancerName() = %q, want %q", got, "default-my-svc")
	}
}

// --- GetLoadBalancer ---

func TestGetLoadBalancer_Found(t *testing.T) {
	ip := testPublicIP("default-my-svc-ip", "203.0.113.1")
	evrocLB := testEvrocLB("default-my-svc")

	lb := newTestLB(
		&recordingLBClient{lb: evrocLB},
		&recordingPublicIPService{ip: ip},
	)

	status, exists, err := lb.GetLoadBalancer(context.Background(), "cluster", testService("my-svc", "default"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("exists = false, want true")
	}
	if len(status.Ingress) != 1 || status.Ingress[0].IP != "203.0.113.1" {
		t.Errorf("unexpected ingress: %+v", status.Ingress)
	}
}

func TestGetLoadBalancer_NotFound(t *testing.T) {
	lb := newTestLB(
		&recordingLBClient{getLBErr: evroc.ErrNotFound},
		&recordingPublicIPService{},
	)

	_, exists, err := lb.GetLoadBalancer(context.Background(), "cluster", testService("my-svc", "default"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("exists = true, want false")
	}
}

func TestGetLoadBalancer_APIError(t *testing.T) {
	apiErr := fmt.Errorf("server error")
	lb := newTestLB(
		&recordingLBClient{getLBErr: apiErr},
		&recordingPublicIPService{},
	)

	_, _, err := lb.GetLoadBalancer(context.Background(), "cluster", testService("my-svc", "default"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, apiErr) {
		t.Errorf("error should wrap original: got %v", err)
	}
}

// --- EnsureLoadBalancer (create path) ---

func TestEnsureLoadBalancer_Creates(t *testing.T) {
	evrocLB := testEvrocLB("default-my-svc")
	ip := testPublicIP("default-my-svc-ip", "203.0.113.1")

	lbMock := &recordingLBClient{
		lb:       evrocLB,
		getLBErr: evroc.ErrNotFound,
	}
	// Get misses, so the allocation path runs rather than reusing an IP.
	ipMock := &recordingPublicIPService{ip: ip, getErr: evroc.ErrNotFound}
	lb := newTestLB(lbMock, ipMock)

	svc := testService("my-svc", "default",
		v1.ServicePort{Name: "http", Port: 80, NodePort: 30080},
		v1.ServicePort{Name: "https", Port: 443, NodePort: 30443},
	)
	nodes := testNodes("node-1", "node-2")

	status, err := lb.EnsureLoadBalancer(context.Background(), "cluster", svc, nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !ipMock.created {
		t.Error("PublicIP should have been created")
	}

	if lbMock.createdName != "default-my-svc" {
		t.Errorf("created LB name = %q, want %q", lbMock.createdName, "default-my-svc")
	}

	if len(lbMock.createdListeners) != 2 {
		t.Fatalf("listener count = %d, want 2", len(lbMock.createdListeners))
	}

	l0 := lbMock.createdListeners[0]
	if l0.FrontendPort != 80 || l0.BackendPort != 30080 {
		t.Errorf("listener[0] = %d->%d, want 80->30080", l0.FrontendPort, l0.BackendPort)
	}

	if len(lbMock.createdVMRefs) != 2 {
		t.Fatalf("backend count = %d, want 2", len(lbMock.createdVMRefs))
	}
	if lbMock.createdVMRefs[0] != vmRef("node-1") {
		t.Errorf("vmRef[0] = %q, want %q", lbMock.createdVMRefs[0], vmRef("node-1"))
	}

	if len(status.Ingress) != 1 || status.Ingress[0].IP != "203.0.113.1" {
		t.Errorf("unexpected ingress: %+v", status.Ingress)
	}
}

func TestEnsureLoadBalancer_UsesAnnotatedPublicIP(t *testing.T) {
	evrocLB := testEvrocLB("default-my-svc")
	ip := testPublicIP("default-my-svc-ip", "203.0.113.99")

	lbMock := &recordingLBClient{
		lb:       evrocLB,
		getLBErr: evroc.ErrNotFound,
	}
	ipMock := &recordingPublicIPService{ip: ip}
	lb := newTestLB(lbMock, ipMock)

	customRef := "/networking/projects/test-project/regions/se-sto/publicIPs/my-static-ip"
	svc := testServiceWithAnnotation("my-svc", "default", customRef)
	nodes := testNodes("node-1")

	_, err := lb.EnsureLoadBalancer(context.Background(), "cluster", svc, nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ipMock.created {
		t.Error("PublicIP should NOT have been created when annotation is present")
	}
	if lbMock.createdIPRef != customRef {
		t.Errorf("publicIPRef = %q, want %q", lbMock.createdIPRef, customRef)
	}
}

// --- EnsureLoadBalancer (update path) ---

func TestEnsureLoadBalancer_SyncsExisting(t *testing.T) {
	evrocLB := testEvrocLB("default-my-svc")
	ip := testPublicIP("default-my-svc-ip", "203.0.113.1")

	lbMock := &recordingLBClient{
		lb: evrocLB,
	}
	ipMock := &recordingPublicIPService{ip: ip}
	lb := newTestLB(lbMock, ipMock)

	nodes := testNodes("node-1", "node-2")
	_, err := lb.EnsureLoadBalancer(context.Background(), "cluster", testService("my-svc", "default"), nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lbMock.syncedLBName != "default-my-svc" {
		t.Errorf("synced LB = %q, want %q", lbMock.syncedLBName, "default-my-svc")
	}
	if len(lbMock.syncedRefs) != 2 {
		t.Fatalf("synced refs = %d, want 2", len(lbMock.syncedRefs))
	}
	if lbMock.syncedRefs[0] != vmRef("node-1") {
		t.Errorf("syncedRefs[0] = %q, want %q", lbMock.syncedRefs[0], vmRef("node-1"))
	}
}

// --- UpdateLoadBalancer ---

func TestUpdateLoadBalancer_SyncsBackends(t *testing.T) {
	lbMock := &recordingLBClient{}
	lb := newTestLB(lbMock, &recordingPublicIPService{})

	nodes := testNodes("node-1", "node-2")
	err := lb.UpdateLoadBalancer(context.Background(), "cluster", testService("my-svc", "default"), nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lbMock.syncedLBName != "default-my-svc" {
		t.Errorf("synced LB = %q, want %q", lbMock.syncedLBName, "default-my-svc")
	}
	if len(lbMock.syncedRefs) != 2 {
		t.Errorf("expected 2 synced refs, got %d", len(lbMock.syncedRefs))
	}
}

// --- EnsureLoadBalancerDeleted ---

func TestEnsureLoadBalancerDeleted_DeletesLBAndIP(t *testing.T) {
	lbMock := &recordingLBClient{}
	ipMock := &recordingPublicIPService{}
	lb := newTestLB(lbMock, ipMock)

	err := lb.EnsureLoadBalancerDeleted(context.Background(), "cluster", testService("my-svc", "default"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lbMock.deletedLBName != "default-my-svc" {
		t.Errorf("deleted LB = %q, want %q", lbMock.deletedLBName, "default-my-svc")
	}
	if !ipMock.deleted {
		t.Error("PublicIP should have been deleted")
	}
}

// TestEnsureLoadBalancerDeleted_PassesListenerNames guards the teardown fix:
// delete must reconstruct sub-resource names from the service's ports rather
// than by walking the load balancer, so a partially created load balancer
// (which the walk cannot see) is still cleaned up. The backend service and
// route names embed the listener name, so those names must reach the delete.
func TestEnsureLoadBalancerDeleted_PassesListenerNames(t *testing.T) {
	lbMock := &recordingLBClient{}
	lb := newTestLB(lbMock, &recordingPublicIPService{})

	// An unnamed port and a named port, the two shapes teardown must handle.
	svc := testService("my-svc", "default",
		v1.ServicePort{Port: 80, NodePort: 30080},
		v1.ServicePort{Name: "https", Port: 443, NodePort: 30443},
	)

	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "cluster", svc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"port-80", "https-443"}
	if len(lbMock.deletedListenerNames) != len(want) {
		t.Fatalf("listener names = %v, want %v", lbMock.deletedListenerNames, want)
	}
	for i, w := range want {
		if lbMock.deletedListenerNames[i] != w {
			t.Errorf("listener[%d] = %q, want %q", i, lbMock.deletedListenerNames[i], w)
		}
	}
}

func TestEnsureLoadBalancerDeleted_SkipsIPDeleteForAnnotated(t *testing.T) {
	lbMock := &recordingLBClient{}
	ipMock := &recordingPublicIPService{}
	lb := newTestLB(lbMock, ipMock)

	svc := testServiceWithAnnotation("my-svc", "default", "/networking/projects/p/regions/r/publicIPs/static")
	err := lb.EnsureLoadBalancerDeleted(context.Background(), "cluster", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lbMock.deletedLBName != "default-my-svc" {
		t.Errorf("deleted LB = %q, want %q", lbMock.deletedLBName, "default-my-svc")
	}
	if ipMock.deleted {
		t.Error("PublicIP should NOT have been deleted when annotation is present")
	}
}

// --- nodeVMRefs ---

func TestNodeVMRefs(t *testing.T) {
	lb := newTestLB(&mockLBClient{}, &mockPublicIPService{})
	nodes := testNodes("node-a", "node-b")
	refs := lb.nodeVMRefs(nodes)

	if len(refs) != 2 {
		t.Fatalf("refs count = %d, want 2", len(refs))
	}
	want := "/compute/projects/test-project/regions/se-sto/virtualMachines/node-a"
	if refs[0] != want {
		t.Errorf("refs[0] = %q, want %q", refs[0], want)
	}
}

// --- toLBStatus ---

func TestToLBStatus_ResolvesIP(t *testing.T) {
	ip := testPublicIP("default-my-svc-ip", "203.0.113.5")
	lb := newTestLB(&mockLBClient{}, &recordingPublicIPService{ip: ip})
	evrocLB := testEvrocLB("default-my-svc")

	status, err := lb.toLBStatus(context.Background(), evrocLB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(status.Ingress) != 1 || status.Ingress[0].IP != "203.0.113.5" {
		t.Errorf("unexpected ingress: %+v", status.Ingress)
	}
}

func TestToLBStatus_EmptyPublicIPRef(t *testing.T) {
	lb := newTestLB(&mockLBClient{}, &recordingPublicIPService{})
	evrocLB := &lbtypes.Loadbalancer{
		Metadata: lbtypes.RegionalMetadataResponse{Id: "test"},
		Spec:     lbtypes.LoadbalancerSpec{},
	}

	status, err := lb.toLBStatus(context.Background(), evrocLB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(status.Ingress) != 0 {
		t.Errorf("expected no ingress, got %+v", status.Ingress)
	}
}

func TestPublicIPFrontendBoundaries(t *testing.T) {
	request := newPublicIPRequest("test-ip")
	if request.Metadata.Id != "test-ip" {
		t.Errorf("request ID = %q, want test-ip", request.Metadata.Id)
	}

	ip := testPublicIP("test-ip", "203.0.113.5")
	if got := publicIPAddress(ip); got != "203.0.113.5" {
		t.Errorf("publicIPAddress() = %q, want 203.0.113.5", got)
	}
}

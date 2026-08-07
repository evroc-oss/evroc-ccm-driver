// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	computetypes "github.com/evroc-oss/evroc-go-sdk/types/compute"
	"github.com/google/uuid"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	cloudprovider "k8s.io/cloud-provider"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

// mockVMGetter implements vmGetter for testing.
type mockVMGetter struct {
	vm  *computetypes.VirtualMachine
	err error
}

func (m *mockVMGetter) Get(_ context.Context, _ string) (*computetypes.VirtualMachine, error) {
	return m.vm, m.err
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func testConfig() *ccmconfig.Config {
	return &ccmconfig.Config{
		Context: ccmconfig.ContextConfig{
			Project: "test-project",
			Region:  "se-sto",
		},
	}
}

func testNode(name string) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

func testVM() *computetypes.VirtualMachine {
	return &computetypes.VirtualMachine{
		Metadata: computetypes.RegionalMetadataResponse{
			Id:  "vm-123",
			Uid: uuid.MustParse("01234567-89ab-cdef-0123-456789abcdef"),
		},
		Spec: computetypes.VirtualMachineSpec{
			ComputeProfileRef: "c1a.m",
			Running:           boolPtr(true),
			Placement: computetypes.VirtualMachineSpecPlacement{
				Zone: strPtr("a"),
			},
		},
		Status: computetypes.VirtualMachineStatus{
			Networking: &computetypes.VirtualMachineStatusNetworking{
				PrivateIPv4Address: strPtr("10.0.0.5"),
				PublicIPv4Address:  strPtr("203.0.113.10"),
			},
		},
	}
}

func testInstances(mock *mockVMGetter) *instances {
	return &instances{
		vms:    mock,
		config: testConfig(),
		logger: slog.Default(),
	}
}

// --- InstanceExists ---

func TestInstanceExists_Found(t *testing.T) {
	inst := testInstances(&mockVMGetter{vm: testVM()})
	exists, err := inst.InstanceExists(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("InstanceExists() = false, want true")
	}
}

func TestInstanceExists_NotFound(t *testing.T) {
	inst := testInstances(&mockVMGetter{err: evroc.ErrNotFound})
	exists, err := inst.InstanceExists(context.Background(), testNode("gone-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("InstanceExists() = true, want false")
	}
}

func TestInstanceExists_APIError(t *testing.T) {
	apiErr := fmt.Errorf("connection refused")
	inst := testInstances(&mockVMGetter{err: apiErr})
	_, err := inst.InstanceExists(context.Background(), testNode("my-vm"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, apiErr) {
		t.Errorf("error should wrap original: got %v", err)
	}
}

// --- InstanceShutdown ---

func TestInstanceShutdown_Running(t *testing.T) {
	inst := testInstances(&mockVMGetter{vm: testVM()})
	shutdown, err := inst.InstanceShutdown(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shutdown {
		t.Error("InstanceShutdown() = true, want false for running VM")
	}
}

func TestInstanceShutdown_SpecNotRunning(t *testing.T) {
	vm := testVM()
	vm.Spec.Running = boolPtr(false)
	inst := testInstances(&mockVMGetter{vm: vm})
	shutdown, err := inst.InstanceShutdown(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !shutdown {
		t.Error("InstanceShutdown() = false, want true for spec.running=false")
	}
}

func TestInstanceShutdown_StatusStopped(t *testing.T) {
	vm := testVM()
	vm.Status.VirtualMachineStatus = strPtr("Stopped")
	inst := testInstances(&mockVMGetter{vm: vm})
	shutdown, err := inst.InstanceShutdown(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !shutdown {
		t.Error("InstanceShutdown() = false, want true for status=Stopped")
	}
}

func TestInstanceShutdown_StatusStopping(t *testing.T) {
	vm := testVM()
	vm.Status.VirtualMachineStatus = strPtr("Stopping")
	inst := testInstances(&mockVMGetter{vm: vm})
	shutdown, err := inst.InstanceShutdown(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !shutdown {
		t.Error("InstanceShutdown() = false, want true for status=Stopping")
	}
}

func TestInstanceShutdown_StatusRunning(t *testing.T) {
	vm := testVM()
	vm.Status.VirtualMachineStatus = strPtr("Running")
	inst := testInstances(&mockVMGetter{vm: vm})
	shutdown, err := inst.InstanceShutdown(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shutdown {
		t.Error("InstanceShutdown() = true, want false for status=Running")
	}
}

func TestInstanceShutdown_NotFound(t *testing.T) {
	inst := testInstances(&mockVMGetter{err: evroc.ErrNotFound})
	_, err := inst.InstanceShutdown(context.Background(), testNode("gone-vm"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, cloudprovider.InstanceNotFound) {
		t.Errorf("error should be InstanceNotFound, got %v", err)
	}
}

func TestInstanceShutdown_APIError(t *testing.T) {
	apiErr := fmt.Errorf("timeout")
	inst := testInstances(&mockVMGetter{err: apiErr})
	_, err := inst.InstanceShutdown(context.Background(), testNode("my-vm"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, apiErr) {
		t.Errorf("error should wrap original: got %v", err)
	}
}

func TestInstanceShutdown_NilRunning(t *testing.T) {
	vm := testVM()
	vm.Spec.Running = nil // IsVMRunning defaults to true when nil
	inst := testInstances(&mockVMGetter{vm: vm})
	shutdown, err := inst.InstanceShutdown(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shutdown {
		t.Error("InstanceShutdown() = true, want false when spec.running is nil (default running)")
	}
}

// --- InstanceMetadata ---

func TestInstanceMetadata_FullVM(t *testing.T) {
	inst := testInstances(&mockVMGetter{vm: testVM()})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantProviderID := "evroc://01234567-89ab-cdef-0123-456789abcdef"
	if meta.ProviderID != wantProviderID {
		t.Errorf("ProviderID = %q, want %q", meta.ProviderID, wantProviderID)
	}

	if meta.InstanceType != "c1a.m" {
		t.Errorf("InstanceType = %q, want %q", meta.InstanceType, "c1a.m")
	}

	if meta.Region != "se-sto" {
		t.Errorf("Region = %q, want %q", meta.Region, "se-sto")
	}

	if meta.Zone != "a" {
		t.Errorf("Zone = %q, want %q", meta.Zone, "a")
	}

	// Check addresses
	var gotHostname, gotInternal, gotExternal string
	for _, addr := range meta.NodeAddresses {
		switch addr.Type {
		case v1.NodeHostName:
			gotHostname = addr.Address
		case v1.NodeInternalIP:
			gotInternal = addr.Address
		case v1.NodeExternalIP:
			gotExternal = addr.Address
		}
	}

	if gotHostname != "my-vm" {
		t.Errorf("hostname = %q, want %q", gotHostname, "my-vm")
	}
	if gotInternal != "10.0.0.5" {
		t.Errorf("internal IP = %q, want %q", gotInternal, "10.0.0.5")
	}
	if gotExternal != "203.0.113.10" {
		t.Errorf("external IP = %q, want %q", gotExternal, "203.0.113.10")
	}
}

func TestInstanceMetadata_NoZone(t *testing.T) {
	vm := testVM()
	vm.Spec.Placement.Zone = nil
	inst := testInstances(&mockVMGetter{vm: vm})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.Zone != "" {
		t.Errorf("Zone = %q, want empty string when zone is nil", meta.Zone)
	}
}

func TestInstanceMetadata_NoNetworking(t *testing.T) {
	vm := testVM()
	vm.Status.Networking = nil
	inst := testInstances(&mockVMGetter{vm: vm})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only have hostname address
	if len(meta.NodeAddresses) != 1 {
		t.Fatalf("NodeAddresses count = %d, want 1", len(meta.NodeAddresses))
	}
	if meta.NodeAddresses[0].Type != v1.NodeHostName {
		t.Errorf("address type = %v, want NodeHostName", meta.NodeAddresses[0].Type)
	}
}

func TestInstanceMetadata_PartialNetworking_PrivateOnly(t *testing.T) {
	vm := testVM()
	vm.Status.Networking = &computetypes.VirtualMachineStatusNetworking{
		PrivateIPv4Address: strPtr("10.0.0.5"),
	}
	inst := testInstances(&mockVMGetter{vm: vm})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(meta.NodeAddresses) != 2 {
		t.Fatalf("NodeAddresses count = %d, want 2 (hostname + internal)", len(meta.NodeAddresses))
	}
}

func TestInstanceMetadata_PartialNetworking_PublicOnly(t *testing.T) {
	vm := testVM()
	vm.Status.Networking = &computetypes.VirtualMachineStatusNetworking{
		PublicIPv4Address: strPtr("203.0.113.10"),
	}
	inst := testInstances(&mockVMGetter{vm: vm})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(meta.NodeAddresses) != 2 {
		t.Fatalf("NodeAddresses count = %d, want 2 (hostname + external)", len(meta.NodeAddresses))
	}
}

func TestInstanceMetadata_EmptyIPStrings(t *testing.T) {
	vm := testVM()
	vm.Status.Networking = &computetypes.VirtualMachineStatusNetworking{
		PrivateIPv4Address: strPtr(""),
		PublicIPv4Address:  strPtr(""),
	}
	inst := testInstances(&mockVMGetter{vm: vm})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Empty strings should be excluded
	if len(meta.NodeAddresses) != 1 {
		t.Fatalf("NodeAddresses count = %d, want 1 (hostname only)", len(meta.NodeAddresses))
	}
}

func TestInstanceMetadata_NotFound(t *testing.T) {
	inst := testInstances(&mockVMGetter{err: evroc.ErrNotFound})
	_, err := inst.InstanceMetadata(context.Background(), testNode("gone-vm"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, cloudprovider.InstanceNotFound) {
		t.Errorf("error should be InstanceNotFound, got %v", err)
	}
}

func TestInstanceMetadata_APIError(t *testing.T) {
	apiErr := fmt.Errorf("server error")
	inst := testInstances(&mockVMGetter{err: apiErr})
	_, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, apiErr) {
		t.Errorf("error should wrap original: got %v", err)
	}
}

func TestNodeNameFromNode(t *testing.T) {
	node := testNode("test-node-name")
	if got := nodeNameFromNode(node); got != "test-node-name" {
		t.Errorf("nodeNameFromNode() = %q, want %q", got, "test-node-name")
	}
}

// TestInstanceMetadata_InstanceTypeIsAValidLabelValue guards against sending
// the compute profile's fully-qualified ref as the instance type. It becomes
// the node.kubernetes.io/instance-type label value, and the API server rejects
// the entire node update if it contains a slash, leaving the node stuck with
// the uninitialized taint.
func TestInstanceMetadata_InstanceTypeIsAValidLabelValue(t *testing.T) {
	vm := testVM()
	vm.Spec.ComputeProfileRef = "/compute/global/computeProfiles/a1a.m"
	inst := testInstances(&mockVMGetter{vm: vm})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.InstanceType != "a1a.m" {
		t.Errorf("InstanceType = %q, want %q", meta.InstanceType, "a1a.m")
	}
	for _, errMsg := range validation.IsValidLabelValue(meta.InstanceType) {
		t.Errorf("InstanceType %q is not a valid label value: %s", meta.InstanceType, errMsg)
	}
	// Zone and region become label values too.
	for _, errMsg := range validation.IsValidLabelValue(meta.Zone) {
		t.Errorf("Zone %q is not a valid label value: %s", meta.Zone, errMsg)
	}
	for _, errMsg := range validation.IsValidLabelValue(meta.Region) {
		t.Errorf("Region %q is not a valid label value: %s", meta.Region, errMsg)
	}
}

func TestInstanceMetadata_IPv6Only(t *testing.T) {
	vm := testVM()
	// An ipv6-only node has no IPv4 address at all: the IPv6 address is its
	// only InternalIP, so dropping it would leave the node with none.
	vm.Status.Networking = &computetypes.VirtualMachineStatusNetworking{
		Ipv6Address: strPtr("2001:db8::5"),
	}
	inst := testInstances(&mockVMGetter{vm: vm})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var internal []string
	for _, a := range meta.NodeAddresses {
		if a.Type == v1.NodeInternalIP {
			internal = append(internal, a.Address)
		}
	}
	if len(internal) != 1 || internal[0] != "2001:db8::5" {
		t.Errorf("internal IPs = %v, want [2001:db8::5]", internal)
	}
}

func TestInstanceMetadata_DualStack(t *testing.T) {
	vm := testVM()
	vm.Status.Networking = &computetypes.VirtualMachineStatusNetworking{
		PrivateIPv4Address: strPtr("10.0.0.5"),
		Ipv6Address:        strPtr("2001:db8::5"),
	}
	inst := testInstances(&mockVMGetter{vm: vm})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var internal []string
	for _, a := range meta.NodeAddresses {
		if a.Type == v1.NodeInternalIP {
			internal = append(internal, a.Address)
		}
	}
	if len(internal) != 2 {
		t.Fatalf("internal IPs = %v, want both IPv4 and IPv6", internal)
	}
	// IPv4 first: kubelet treats the first InternalIP as the node's primary.
	if internal[0] != "10.0.0.5" {
		t.Errorf("first internal IP = %q, want the IPv4 address", internal[0])
	}
	if internal[1] != "2001:db8::5" {
		t.Errorf("second internal IP = %q, want the IPv6 address", internal[1])
	}
}

func TestInstanceMetadata_TopologyLabels(t *testing.T) {
	// Zone comes from VM placement, region from config; together these become
	// the node's topology.kubernetes.io/{zone,region} labels.
	vm := testVM()
	vm.Spec.Placement.Zone = strPtr("b")
	inst := testInstances(&mockVMGetter{vm: vm})
	meta, err := inst.InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.Zone != "b" {
		t.Errorf("Zone = %q, want %q", meta.Zone, "b")
	}
	if meta.Region != "se-sto" {
		t.Errorf("Region = %q, want %q", meta.Region, "se-sto")
	}
}

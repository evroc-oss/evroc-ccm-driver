// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	computetypes "github.com/evroc-oss/evroc-go-sdk/types/compute"
	"github.com/google/uuid"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cloudprovider "k8s.io/cloud-provider"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

type mockVMGetter struct {
	vm      *computetypes.VirtualMachine
	err     error
	gotName string
}

func (m *mockVMGetter) Get(_ context.Context, name string) (*computetypes.VirtualMachine, error) {
	m.gotName = name
	return m.vm, m.err
}

func strPtr(value string) *string { return &value }
func boolPtr(value bool) *bool    { return &value }

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
		vms: mock,
		config: &ccmconfig.Config{Context: ccmconfig.ContextConfig{
			Project: "test-project", Region: "se-sto",
		}},
		logger: slog.Default(),
	}
}

func testNode(name string) *v1.Node {
	return &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestInstanceExists(t *testing.T) {
	apiErr := errors.New("connection refused")
	for _, tt := range []struct {
		name    string
		getErr  error
		want    bool
		wantErr error
	}{
		{
			name: "found",
			want: true,
		},
		{
			name:   "not found",
			getErr: evroc.ErrNotFound,
		},
		{
			name:    "API error",
			getErr:  apiErr,
			wantErr: apiErr,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockVMGetter{vm: testVM(), err: tt.getErr}
			got, err := testInstances(mock).InstanceExists(context.Background(), testNode("my-vm"))
			if got != tt.want || !sameError(err, tt.wantErr) {
				t.Errorf("InstanceExists() = %v, %v; want %v, %v", got, err, tt.want, tt.wantErr)
			}
			if mock.gotName != "my-vm" {
				t.Errorf("VM lookup name = %q, want my-vm", mock.gotName)
			}
		})
	}
}

func TestInstanceShutdown(t *testing.T) {
	apiErr := errors.New("timeout")
	for _, tt := range []struct {
		name    string
		mutate  func(*computetypes.VirtualMachine)
		getErr  error
		want    bool
		wantErr error
	}{
		{
			name: "running",
		},
		{
			name: "spec not running",
			mutate: func(vm *computetypes.VirtualMachine) {
				vm.Spec.Running = boolPtr(false)
			},
			want: true,
		},
		{
			name: "status stopped",
			mutate: func(vm *computetypes.VirtualMachine) {
				vm.Status.VirtualMachineStatus = strPtr("Stopped")
			},
			want: true,
		},
		{
			name: "status stopping",
			mutate: func(vm *computetypes.VirtualMachine) {
				vm.Status.VirtualMachineStatus = strPtr("Stopping")
			},
			want: true,
		},
		{
			name: "status running",
			mutate: func(vm *computetypes.VirtualMachine) {
				vm.Status.VirtualMachineStatus = strPtr("Running")
			},
		},
		{
			name: "nil spec defaults to running",
			mutate: func(vm *computetypes.VirtualMachine) {
				vm.Spec.Running = nil
			},
		},
		{
			name:    "not found",
			getErr:  evroc.ErrNotFound,
			wantErr: cloudprovider.InstanceNotFound,
		},
		{
			name:    "API error",
			getErr:  apiErr,
			wantErr: apiErr,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			vm := testVM()
			if tt.mutate != nil {
				tt.mutate(vm)
			}
			got, err := testInstances(&mockVMGetter{vm: vm, err: tt.getErr}).InstanceShutdown(
				context.Background(), testNode("my-vm"),
			)
			if got != tt.want || !sameError(err, tt.wantErr) {
				t.Errorf("InstanceShutdown() = %v, %v; want %v, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestInstanceMetadata(t *testing.T) {
	metadata, err := testInstances(&mockVMGetter{vm: testVM()}).InstanceMetadata(
		context.Background(), testNode("my-vm"),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := &cloudprovider.InstanceMetadata{
		ProviderID:   "evroc://01234567-89ab-cdef-0123-456789abcdef",
		InstanceType: "c1a.m",
		NodeAddresses: []v1.NodeAddress{
			{Type: v1.NodeHostName, Address: "my-vm"},
			{Type: v1.NodeInternalIP, Address: "10.0.0.5"},
			{Type: v1.NodeExternalIP, Address: "203.0.113.10"},
		},
		Zone:   "a",
		Region: "se-sto",
	}
	if !reflect.DeepEqual(metadata, want) {
		t.Errorf("InstanceMetadata() = %#v, want %#v", metadata, want)
	}
}

func TestInstanceMetadataAddresses(t *testing.T) {
	for _, tt := range []struct {
		name       string
		networking *computetypes.VirtualMachineStatusNetworking
		want       []v1.NodeAddress
	}{
		{
			name: "none",
			want: nodeAddresses(),
		},
		{
			name: "private IPv4",
			networking: &computetypes.VirtualMachineStatusNetworking{
				PrivateIPv4Address: strPtr("10.0.0.5"),
			},
			want: nodeAddresses("internal", "10.0.0.5"),
		},
		{
			name: "public IPv4",
			networking: &computetypes.VirtualMachineStatusNetworking{
				PublicIPv4Address: strPtr("203.0.113.10"),
			},
			want: nodeAddresses("external", "203.0.113.10"),
		},
		{
			name: "empty addresses",
			networking: &computetypes.VirtualMachineStatusNetworking{
				PrivateIPv4Address: strPtr(""),
				PublicIPv4Address:  strPtr(""),
			},
			want: nodeAddresses(),
		},
		{
			name: "IPv6 only",
			networking: &computetypes.VirtualMachineStatusNetworking{
				Ipv6Address: strPtr("2001:db8::5"),
			},
			want: nodeAddresses(
				"internal", "2001:db8::5",
				"external", "2001:db8::5",
			),
		},
		{
			name: "dual stack",
			networking: &computetypes.VirtualMachineStatusNetworking{
				PrivateIPv4Address: strPtr("10.0.0.5"),
				Ipv6Address:        strPtr("2001:db8::5"),
			},
			want: nodeAddresses(
				"internal", "10.0.0.5",
				"internal", "2001:db8::5",
				"external", "2001:db8::5",
			),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			vm := testVM()
			vm.Status.Networking = tt.networking
			got, err := testInstances(&mockVMGetter{vm: vm}).InstanceMetadata(context.Background(), testNode("my-vm"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.NodeAddresses, tt.want) {
				t.Errorf("NodeAddresses = %#v, want %#v", got.NodeAddresses, tt.want)
			}
		})
	}
}

func TestInstanceMetadataMissingZone(t *testing.T) {
	vm := testVM()
	vm.Spec.Placement.Zone = nil
	got, err := testInstances(&mockVMGetter{vm: vm}).InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil || got.Zone != "" {
		t.Errorf("InstanceMetadata() zone = %q, error = %v; want empty zone", got.Zone, err)
	}
}

func TestInstanceMetadataTopologyLabels(t *testing.T) {
	vm := testVM()
	vm.Spec.ComputeProfileRef = "/compute/global/computeProfiles/a1a.m"
	vm.Spec.Placement.Zone = strPtr("b")
	got, err := testInstances(&mockVMGetter{vm: vm}).InstanceMetadata(context.Background(), testNode("my-vm"))
	if err != nil {
		t.Fatal(err)
	}
	if got.InstanceType != "a1a.m" || got.Zone != "b" || got.Region != "se-sto" {
		t.Errorf("topology metadata = %#v", got)
	}
}

func TestInstanceMetadataNotFound(t *testing.T) {
	_, err := testInstances(&mockVMGetter{err: evroc.ErrNotFound}).InstanceMetadata(
		context.Background(), testNode("gone-vm"),
	)
	if !errors.Is(err, cloudprovider.InstanceNotFound) {
		t.Errorf("InstanceMetadata() error = %v, want %v", err, cloudprovider.InstanceNotFound)
	}
}

func TestInstanceMetadataAPIError(t *testing.T) {
	apiErr := errors.New("server error")
	_, err := testInstances(&mockVMGetter{err: apiErr}).InstanceMetadata(
		context.Background(), testNode("my-vm"),
	)
	if !errors.Is(err, apiErr) {
		t.Errorf("InstanceMetadata() error = %v, want %v", err, apiErr)
	}
}

func nodeAddresses(kindAndAddress ...string) []v1.NodeAddress {
	addresses := []v1.NodeAddress{{Type: v1.NodeHostName, Address: "my-vm"}}
	for index := 0; index < len(kindAndAddress); index += 2 {
		addressType := v1.NodeInternalIP
		if kindAndAddress[index] == "external" {
			addressType = v1.NodeExternalIP
		}
		addresses = append(addresses, v1.NodeAddress{Type: addressType, Address: kindAndAddress[index+1]})
	}
	return addresses
}

func sameError(got, want error) bool {
	return (got == nil && want == nil) || (got != nil && want != nil && errors.Is(got, want))
}

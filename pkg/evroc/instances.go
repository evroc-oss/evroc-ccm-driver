// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	"github.com/evroc-oss/evroc-go-sdk/compute"
	computetypes "github.com/evroc-oss/evroc-go-sdk/types/compute"
	v1 "k8s.io/api/core/v1"
	cloudprovider "k8s.io/cloud-provider"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

// vmGetter abstracts VM lookups for testability.
type vmGetter interface {
	Get(ctx context.Context, name string) (*computetypes.VirtualMachine, error)
}

// instances implements cloudprovider.InstancesV2.
type instances struct {
	vms    vmGetter
	config *ccmconfig.Config
	logger *slog.Logger
}

func newInstances(client *evroc.Client, cfg *ccmconfig.Config, logger *slog.Logger) *instances {
	return &instances{
		vms:    client.Compute().VirtualMachines(),
		config: cfg,
		logger: logger.With("subsystem", "instances"),
	}
}

// InstanceExists checks whether a node still exists in the cloud provider.
func (i *instances) InstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	name := nodeNameFromNode(node)
	i.logger.Debug("checking if instance exists", "node", name)

	_, err := i.vms.Get(ctx, name)
	if err != nil {
		if errors.Is(err, evroc.ErrNotFound) {
			i.logger.Info("instance not found", "node", name)
			return false, nil
		}
		return false, fmt.Errorf("failed to get VM %s: %w", name, err)
	}

	return true, nil
}

// InstanceShutdown checks whether a node is shut down (not running).
func (i *instances) InstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	name := nodeNameFromNode(node)
	i.logger.Debug("checking if instance is shutdown", "node", name)

	vm, err := i.vms.Get(ctx, name)
	if err != nil {
		if errors.Is(err, evroc.ErrNotFound) {
			return false, cloudprovider.InstanceNotFound
		}
		return false, fmt.Errorf("failed to get VM %s: %w", name, err)
	}

	// Check if VM is not running
	if !compute.IsVMRunning(vm) {
		return true, nil
	}

	// Also check the actual status field
	if vm.Status.VirtualMachineStatus != nil {
		status := *vm.Status.VirtualMachineStatus
		if status == "Stopped" || status == "Stopping" {
			return true, nil
		}
	}

	return false, nil
}

// InstanceMetadata returns the metadata for the node, including provider ID,
// instance type, node addresses, and topology (region/zone).
func (i *instances) InstanceMetadata(ctx context.Context, node *v1.Node) (*cloudprovider.InstanceMetadata, error) {
	name := nodeNameFromNode(node)
	i.logger.Debug("fetching instance metadata", "node", name)

	vm, err := i.vms.Get(ctx, name)
	if err != nil {
		if errors.Is(err, evroc.ErrNotFound) {
			return nil, cloudprovider.InstanceNotFound
		}
		return nil, fmt.Errorf("failed to get VM %s: %w", name, err)
	}

	// Match the provider ID assigned by cluster-api-provider-evroc. The VM UID
	// is globally unique and immutable, unlike the user-selected resource name.
	providerID := fmt.Sprintf("evroc://%s", vm.Metadata.Uid.String())

	// Determine zone from VM placement. The zone becomes the node's
	// topology.kubernetes.io/zone label, which schedulers and volume topology
	// rely on, so a VM without one is reported rather than silently labelled "".
	zone := ""
	if vm.Spec.Placement.Zone != nil {
		zone = *vm.Spec.Placement.Zone
	}
	if zone == "" {
		i.logger.Warn("VM has no placement zone; node will not be labelled with a topology zone",
			"node", name)
	}

	// Build node addresses
	addresses := []v1.NodeAddress{
		{
			Type:    v1.NodeHostName,
			Address: name,
		},
	}
	if vm.Status.Networking != nil {
		if vm.Status.Networking.PrivateIPv4Address != nil && *vm.Status.Networking.PrivateIPv4Address != "" {
			addresses = append(addresses, v1.NodeAddress{
				Type:    v1.NodeInternalIP,
				Address: *vm.Status.Networking.PrivateIPv4Address,
			})
		}
		// On dual-stack the IPv6 address is an additional InternalIP; on
		// ipv6-only it is the only one, so omitting it would leave the node
		// with no InternalIP at all.
		if vm.Status.Networking.Ipv6Address != nil && *vm.Status.Networking.Ipv6Address != "" {
			addresses = append(addresses, v1.NodeAddress{
				Type:    v1.NodeInternalIP,
				Address: *vm.Status.Networking.Ipv6Address,
			})
		}
		if vm.Status.Networking.PublicIPv4Address != nil && *vm.Status.Networking.PublicIPv4Address != "" {
			addresses = append(addresses, v1.NodeAddress{
				Type:    v1.NodeExternalIP,
				Address: *vm.Status.Networking.PublicIPv4Address,
			})
		}
	}

	// The compute profile is a fully-qualified ref such as
	// /compute/global/computeProfiles/a1a.m, but this becomes the
	// node.kubernetes.io/instance-type label value, and label values may not
	// contain slashes. Use the profile name alone: the API server rejects the
	// whole node update otherwise, leaving the node permanently uninitialized.
	instanceType := path.Base(vm.Spec.ComputeProfileRef)

	metadata := &cloudprovider.InstanceMetadata{
		ProviderID:    providerID,
		InstanceType:  instanceType,
		NodeAddresses: addresses,
		Zone:          zone,
		Region:        i.config.Context.Region,
	}

	i.logger.Info("instance metadata resolved",
		"node", name,
		"providerID", providerID,
		"instanceType", instanceType,
		"zone", zone,
		"region", i.config.Context.Region,
	)

	return metadata, nil
}

// nodeNameFromNode extracts the node name used to look up the VM in the evroc API.
func nodeNameFromNode(node *v1.Node) string {
	return node.Name
}

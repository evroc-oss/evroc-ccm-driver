// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package basic

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/evroc-oss/evroc-ccm-driver/test/e2e/framework"
)

// evrocProviderID matches the provider ID shared with cluster-api-provider-evroc:
// evroc://<immutable VM UUID>.
var evrocProviderID = regexp.MustCompile(`^evroc://[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNodesAreInitializedByTheCCM checks that every node was initialized by
// this provider rather than by the distribution's built-in one. A k3s or RKE2
// cluster that still runs its own cloud controller assigns a k3s:// provider
// ID and pre-labels the node, which silently hides whether the CCM works.
func TestNodesAreInitializedByTheCCM(t *testing.T) {
	f := framework.New(t)
	ctx := context.Background()

	nodes := f.Nodes(ctx)
	if len(nodes) == 0 {
		t.Fatal("cluster has no nodes")
	}

	for _, node := range nodes {
		t.Run(node.Name, func(t *testing.T) {
			if node.HasUninitializedTaint() {
				t.Error("node still carries the uninitialized taint, so the CCM has not initialized it")
			}
			if got := node.ProviderID(); !evrocProviderID.MatchString(got) {
				t.Errorf("providerID = %q, want it to match %s", got, evrocProviderID)
			}
		})
	}
}

// TestNodeTopologyLabels is the node zone labelling feature: the zone comes
// from the VM's placement and the region from the CCM's configuration.
//
// The instance type is derived from a fully-qualified compute profile ref, and
// sending that ref unmodified makes the API server reject the whole node
// update, leaving the node uninitialized.
func TestNodeTopologyLabels(t *testing.T) {
	f := framework.New(t)
	ctx := context.Background()

	for _, node := range f.Nodes(ctx) {
		t.Run(node.Name, func(t *testing.T) {
			zone := node.Zone()
			if zone == "" {
				t.Error("node has no topology.kubernetes.io/zone label")
			}
			region := node.Region()
			if region == "" {
				t.Error("node has no topology.kubernetes.io/region label")
			}
			instanceType := node.InstanceType()
			if instanceType == "" {
				t.Error("node has no node.kubernetes.io/instance-type label")
			}

			// A compute profile ref would still be slash-separated here.
			if strings.Contains(instanceType, "/") {
				t.Errorf("instance-type = %q, want the profile name rather than its fully-qualified ref", instanceType)
			}
		})
	}
}

// TestNodeAddresses checks the CCM populated node addresses from the VM. A
// node with no internal address cannot be reached by the control plane, which
// is the failure mode when an ipv6-only VM's address is dropped.
func TestNodeAddresses(t *testing.T) {
	f := framework.New(t)
	ctx := context.Background()

	for _, node := range f.Nodes(ctx) {
		t.Run(node.Name, func(t *testing.T) {
			if len(node.InternalIPs()) == 0 {
				t.Error("node has no InternalIP")
			}
		})
	}
}

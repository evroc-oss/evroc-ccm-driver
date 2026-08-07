// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package framework

import v1 "k8s.io/api/core/v1"

// corev1Node wraps a node with accessors for the fields the CCM populates.
type corev1Node struct{ *v1.Node }

// Zone returns the node's topology zone label.
func (n corev1Node) Zone() string { return n.Labels[v1.LabelTopologyZone] }

// Region returns the node's topology region label.
func (n corev1Node) Region() string { return n.Labels[v1.LabelTopologyRegion] }

// InstanceType returns the node's instance type label.
func (n corev1Node) InstanceType() string { return n.Labels[v1.LabelInstanceTypeStable] }

// ProviderID returns the node's provider ID.
func (n corev1Node) ProviderID() string { return n.Spec.ProviderID }

// HasUninitializedTaint reports whether the node is still waiting for a cloud
// provider to initialize it.
func (n corev1Node) HasUninitializedTaint() bool {
	for _, taint := range n.Spec.Taints {
		if taint.Key == "node.cloudprovider.kubernetes.io/uninitialized" {
			return true
		}
	}
	return false
}

// InternalIPs returns the node's internal addresses.
func (n corev1Node) InternalIPs() []string { return n.addresses(v1.NodeInternalIP) }

// ExternalIPs returns the node's external addresses.
func (n corev1Node) ExternalIPs() []string { return n.addresses(v1.NodeExternalIP) }

func (n corev1Node) addresses(t v1.NodeAddressType) []string {
	var out []string
	for _, a := range n.Status.Addresses {
		if a.Type == t {
			out = append(out, a.Address)
		}
	}
	return out
}

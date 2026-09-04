// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"log/slog"
	"testing"

	cloudprovider "k8s.io/cloud-provider"
)

func TestCloudProviderInterface(t *testing.T) {
	var _ cloudprovider.Interface = (*Cloud)(nil)

	c := &Cloud{
		instances:    &instances{},
		loadBalancer: &loadBalancer{},
		logger:       slog.Default(),
	}

	if c.ProviderName() != ProviderName || ProviderName != "evroc" {
		t.Errorf("provider name = %q, want evroc", c.ProviderName())
	}
	if c.HasClusterID() {
		t.Error("HasClusterID() = true, want false")
	}
	if got, ok := c.LoadBalancer(); !ok || got != c.loadBalancer {
		t.Errorf("LoadBalancer() = %v, %v", got, ok)
	}
	if got, ok := c.InstancesV2(); !ok || got != c.instances {
		t.Errorf("InstancesV2() = %v, %v", got, ok)
	}

	if got, ok := c.Instances(); ok || got != nil {
		t.Errorf("Instances() = %v, %v; want nil, false", got, ok)
	}
	if got, ok := c.Zones(); ok || got != nil {
		t.Errorf("Zones() = %v, %v; want nil, false", got, ok)
	}
	if got, ok := c.Clusters(); ok || got != nil {
		t.Errorf("Clusters() = %v, %v; want nil, false", got, ok)
	}
	if got, ok := c.Routes(); ok || got != nil {
		t.Errorf("Routes() = %v, %v; want nil, false", got, ok)
	}

	c.Initialize(nil, make(chan struct{}))
}

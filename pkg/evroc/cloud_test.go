// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/evroc-oss/evroc-go-sdk/networking"
	lbtypes "github.com/evroc-oss/evroc-go-sdk/types/loadbalancer"
	networkingtypes "github.com/evroc-oss/evroc-go-sdk/types/networking"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

func newTestCloud() *Cloud {
	cfg := &ccmconfig.Config{
		Auth: ccmconfig.AuthConfig{
			ServiceAccountID:     "ccm-agent",
			ServiceAccountSecret: "c2VjcmV0",
		},
		API: ccmconfig.APIConfig{BaseURL: "https://api.evroc.com"},
		Context: ccmconfig.ContextConfig{
			Organization: "test-org",
			Project:      "test-project",
			Region:       "se-sto",
		},
	}

	return &Cloud{
		config:       cfg,
		instances:    newTestInstances(cfg),
		loadBalancer: newTestLoadBalancer(cfg),
		logger:       slog.Default(),
	}
}

func newTestInstances(cfg *ccmconfig.Config) *instances {
	return &instances{
		vms:    &mockVMGetter{},
		config: cfg,
		logger: slog.Default(),
	}
}

func newTestLoadBalancer(cfg *ccmconfig.Config) *loadBalancer {
	return &loadBalancer{
		lb:     &mockLBClient{},
		ips:    &mockPublicIPService{},
		config: cfg,
		logger: slog.Default(),
	}
}

// --- LB mocks ---

type mockLBClient struct {
	lb  *lbtypes.Loadbalancer
	err error
}

func (m *mockLBClient) Create(_ context.Context, _ lbCreateOptions) (*lbtypes.Loadbalancer, error) {
	return m.lb, m.err
}

func (m *mockLBClient) Delete(_ context.Context, _ string, _ []string) error {
	return m.err
}

func (m *mockLBClient) SyncBackends(_ context.Context, _ string, _ []string) error {
	return m.err
}

func (m *mockLBClient) GetLB(_ context.Context, _ string) (*lbtypes.Loadbalancer, error) {
	return m.lb, m.err
}

type mockPublicIPService struct {
	ip  *networkingtypes.PublicIP
	err error
}

func (m *mockPublicIPService) Create(_ context.Context, _ *networkingtypes.PublicIPRequest) (*networkingtypes.PublicIP, error) {
	return m.ip, m.err
}

func (m *mockPublicIPService) Get(_ context.Context, _ string) (*networkingtypes.PublicIP, error) {
	return m.ip, m.err
}

func (m *mockPublicIPService) Delete(_ context.Context, _ string) error {
	return m.err
}

func (m *mockPublicIPService) WaitForReady(_ context.Context, _ string, _ time.Duration, _ ...networking.WaiterOption) (*networkingtypes.PublicIP, error) {
	return m.ip, m.err
}

// --- Cloud interface tests ---

func TestProviderName(t *testing.T) {
	c := newTestCloud()
	if got := c.ProviderName(); got != "evroc" {
		t.Errorf("ProviderName() = %q, want %q", got, "evroc")
	}
}

func TestProviderNameConstant(t *testing.T) {
	if ProviderName != "evroc" {
		t.Errorf("ProviderName constant = %q, want %q", ProviderName, "evroc")
	}
}

func TestHasClusterID(t *testing.T) {
	c := newTestCloud()
	if c.HasClusterID() {
		t.Error("HasClusterID() = true, want false")
	}
}

func TestLoadBalancer(t *testing.T) {
	c := newTestCloud()
	lb, ok := c.LoadBalancer()
	if !ok {
		t.Error("LoadBalancer() ok = false, want true")
	}
	if lb == nil {
		t.Error("LoadBalancer() should not return nil")
	}
}

func TestInstances(t *testing.T) {
	c := newTestCloud()
	inst, ok := c.Instances()
	if ok {
		t.Error("Instances() ok = true, want false")
	}
	if inst != nil {
		t.Error("Instances() should return nil")
	}
}

func TestInstancesV2(t *testing.T) {
	c := newTestCloud()
	inst, ok := c.InstancesV2()
	if !ok {
		t.Error("InstancesV2() ok = false, want true")
	}
	if inst == nil {
		t.Error("InstancesV2() should not return nil")
	}
}

func TestZones(t *testing.T) {
	c := newTestCloud()
	z, ok := c.Zones()
	if ok {
		t.Error("Zones() ok = true, want false")
	}
	if z != nil {
		t.Error("Zones() should return nil")
	}
}

func TestClusters(t *testing.T) {
	c := newTestCloud()
	cl, ok := c.Clusters()
	if ok {
		t.Error("Clusters() ok = true, want false")
	}
	if cl != nil {
		t.Error("Clusters() should return nil")
	}
}

func TestRoutes(t *testing.T) {
	c := newTestCloud()
	r, ok := c.Routes()
	if ok {
		t.Error("Routes() ok = true, want false")
	}
	if r != nil {
		t.Error("Routes() should return nil")
	}
}

func TestInitialize(t *testing.T) {
	c := newTestCloud()
	stop := make(chan struct{})
	defer close(stop)
	c.Initialize(nil, stop)
}

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	cloudprovider "k8s.io/cloud-provider"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

const (
	// ProviderName is the name registered with the Kubernetes cloud provider framework.
	ProviderName = "evroc"
)

// Cloud implements cloudprovider.Interface for the evroc platform.
type Cloud struct {
	client       *evroc.Client
	config       *ccmconfig.Config
	instances    cloudprovider.InstancesV2
	loadBalancer cloudprovider.LoadBalancer
	logger       *slog.Logger
}

func init() {
	cloudprovider.RegisterCloudProvider(ProviderName, newCloud)
}

// newCloud is the factory function registered with the cloud provider framework.
// The framework opens the --cloud-config file and passes its contents via configReader.
func newCloud(configReader io.Reader) (cloudprovider.Interface, error) {
	data, err := io.ReadAll(configReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read cloud config: %w", err)
	}

	cfg, err := ccmconfig.ParseBytes(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cloud config: %w", err)
	}

	logger := slog.Default().With("component", "evroc-ccm")

	client, err := evroc.New(context.Background(), cfg.SDKConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create evroc SDK client: %w", err)
	}

	logger.Info("evroc cloud provider initialized",
		"region", cfg.Context.Region,
		"project", cfg.Context.Project,
	)

	cloud := &Cloud{
		client: client,
		config: cfg,
		logger: logger,
	}
	cloud.instances = newInstances(client, cfg, logger)
	cloud.loadBalancer = newLoadBalancer(client.LoadBalancer(), client, cfg, logger)

	return cloud, nil
}

// Initialize is called after the cloud provider is created.
func (c *Cloud) Initialize(clientBuilder cloudprovider.ControllerClientBuilder, stop <-chan struct{}) {
	c.logger.Info("cloud provider initialized with kubernetes client")
}

// LoadBalancer returns the load balancer interface.
func (c *Cloud) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	return c.loadBalancer, true
}

// Instances returns the deprecated Instances interface. Not implemented.
func (c *Cloud) Instances() (cloudprovider.Instances, bool) {
	return nil, false
}

// InstancesV2 returns the InstancesV2 interface for node metadata.
func (c *Cloud) InstancesV2() (cloudprovider.InstancesV2, bool) {
	return c.instances, true
}

// Zones returns the deprecated Zones interface. Not implemented.
func (c *Cloud) Zones() (cloudprovider.Zones, bool) {
	return nil, false
}

// Clusters returns the Clusters interface. Not implemented.
func (c *Cloud) Clusters() (cloudprovider.Clusters, bool) {
	return nil, false
}

// Routes returns the Routes interface. Not implemented.
func (c *Cloud) Routes() (cloudprovider.Routes, bool) {
	return nil, false
}

// ProviderName returns the cloud provider name.
func (c *Cloud) ProviderName() string {
	return ProviderName
}

// HasClusterID returns false because evroc does not require or use a Kubernetes
// cluster ID. The method is required by cloudprovider.Interface.
func (c *Cloud) HasClusterID() bool {
	return false
}

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package main

import (
	"os"

	"k8s.io/apimachinery/pkg/util/wait"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/cloud-provider/app"
	"k8s.io/cloud-provider/app/config"
	"k8s.io/cloud-provider/names"
	"k8s.io/cloud-provider/options"
	"k8s.io/component-base/cli"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"

	// Import evroc cloud provider to trigger init() registration.
	_ "github.com/evroc-oss/evroc-ccm-driver/pkg/evroc"
)

// Set via -ldflags at build time.
var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

func main() {
	klog.Infof("evroc-ccm %s (commit: %s, built: %s)", version, gitCommit, buildDate)

	ccmOptions, err := options.NewCloudControllerManagerOptions()
	if err != nil {
		klog.Fatalf("unable to initialize command options: %v", err)
	}

	controllerInitializers := app.DefaultInitFuncConstructors
	controllerAliases := names.CCMControllerAliases()

	// We don't have routes support.
	delete(controllerInitializers, "route")

	fss := cliflag.NamedFlagSets{}

	command := app.NewCloudControllerManagerCommand(
		ccmOptions,
		cloudInitializer,
		controllerInitializers,
		controllerAliases,
		fss,
		wait.NeverStop,
	)

	code := cli.Run(command)
	os.Exit(code)
}

func cloudInitializer(cfg *config.CompletedConfig) cloudprovider.Interface {
	cloudConfig := cfg.ComponentConfig.KubeCloudShared.CloudProvider

	cloud, err := cloudprovider.InitCloudProvider(cloudConfig.Name, cloudConfig.CloudConfigFile)
	if err != nil {
		klog.Fatalf("cloud provider could not be initialized: %v", err)
	}
	if cloud == nil {
		klog.Fatal("cloud provider is nil")
	}

	if !cloud.HasClusterID() {
		if cfg.ComponentConfig.KubeCloudShared.AllowUntaggedCloud {
			klog.Warning("detected a cluster without a ClusterID. A ClusterID will be required in the future. Please tag your cluster to avoid any future issues")
		} else {
			klog.Info("no ClusterID found, proceeding without one (evroc does not use ClusterID)")
		}
	}

	return cloud
}

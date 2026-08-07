// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

// Package framework provides helpers for the evroc CCM end-to-end tests.
package framework

import (
	"context"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// DefaultPollInterval is how often conditions are re-checked while waiting.
	DefaultPollInterval = 2 * time.Second

	// CCMNamespace is where the CCM is deployed.
	CCMNamespace = "evroc-system"
)

// Framework holds the cluster connection shared by the e2e tests.
type Framework struct {
	T              *testing.T
	ClientSet      kubernetes.Interface
	KubeconfigPath string
	cleanup        []func(context.Context)
}

// New connects to the cluster named by KUBECONFIG.
func New(t *testing.T) *Framework {
	t.Helper()

	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		t.Fatal("KUBECONFIG is not set; it must be an absolute path to the test cluster's kubeconfig")
	}
	if _, err := os.Stat(kubeconfigPath); err != nil {
		t.Fatalf("kubeconfig %s is not readable: %v", kubeconfigPath, err)
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		t.Fatalf("failed to load kubeconfig %s: %v", kubeconfigPath, err)
	}
	// Polling tests exceed the default 5 QPS.
	cfg.QPS = 50
	cfg.Burst = 100

	clientSet, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create kubernetes client: %v", err)
	}

	return &Framework{T: t, ClientSet: clientSet, KubeconfigPath: kubeconfigPath}
}

// Config returns a rest config built from the same kubeconfig.
func Config(t *testing.T) *rest.Config {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatalf("failed to load kubeconfig: %v", err)
	}
	return cfg
}

// DeferCleanup registers a function to run when Cleanup is called. The
// function is given a fresh context, because cleanup must still run when the
// test's own context has been cancelled.
func (f *Framework) DeferCleanup(fn func(context.Context)) {
	f.cleanup = append(f.cleanup, fn)
}

// Cleanup runs the registered cleanup functions in reverse order.
func (f *Framework) Cleanup() {
	ctx := context.Background()
	for i := len(f.cleanup) - 1; i >= 0; i-- {
		f.cleanup[i](ctx)
	}
	f.cleanup = nil
}

// Nodes returns all nodes in the cluster.
func (f *Framework) Nodes(ctx context.Context) []corev1Node {
	f.T.Helper()
	list, err := f.ClientSet.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		f.T.Fatalf("failed to list nodes: %v", err)
	}
	nodes := make([]corev1Node, 0, len(list.Items))
	for i := range list.Items {
		nodes = append(nodes, corev1Node{&list.Items[i]})
	}
	return nodes
}

// WaitFor polls until condition returns true, or fails the test on timeout.
func (f *Framework) WaitFor(ctx context.Context, timeout time.Duration, describe string, condition func(context.Context) bool) {
	f.T.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition(ctx) {
			return
		}
		time.Sleep(DefaultPollInterval)
	}
	f.T.Fatalf("timed out after %s waiting for %s", timeout, describe)
}

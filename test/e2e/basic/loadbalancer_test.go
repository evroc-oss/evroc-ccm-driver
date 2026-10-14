// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package basic

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/evroc-oss/evroc-ccm-driver/test/e2e/framework"
)

const (
	lbProvisionTimeout = 5 * time.Minute
	lbDeleteTimeout    = 3 * time.Minute
)

// deployEcho creates a deployment serving HTTP on 8080 and returns its labels.
func deployEcho(ctx context.Context, f *framework.Framework, name string) map[string]string {
	f.T.Helper()
	labels := map[string]string{"app": name}
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name:  "echo",
						Image: "registry.k8s.io/e2e-test-images/agnhost:2.53",
						Args:  []string{"netexec", "--http-port=8080"},
						Ports: []v1.ContainerPort{{ContainerPort: 8080}},
					}},
				},
			},
		},
	}

	if _, err := f.ClientSet.AppsV1().Deployments("default").Create(ctx, deployment, metav1.CreateOptions{}); err != nil {
		f.T.Fatalf("failed to create deployment %s: %v", name, err)
	}
	f.T.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = f.ClientSet.AppsV1().Deployments("default").Delete(cleanupCtx, name, metav1.DeleteOptions{})
	})

	f.WaitFor(ctx, 3*time.Minute, "deployment "+name+" to become available", func(ctx context.Context) bool {
		d, err := f.ClientSet.AppsV1().Deployments("default").Get(ctx, name, metav1.GetOptions{})
		return err == nil && d.Status.ReadyReplicas >= 1
	})
	return labels
}

func createService(ctx context.Context, f *framework.Framework, svc *v1.Service) *v1.Service {
	f.T.Helper()
	created, err := f.ClientSet.CoreV1().Services("default").Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		f.T.Fatalf("failed to create service %s: %v", svc.Name, err)
	}
	f.T.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = f.ClientSet.CoreV1().Services("default").Delete(cleanupCtx, svc.Name, metav1.DeleteOptions{})
	})
	return created
}

// TestNodePortService checks a NodePort service works. The CCM is not involved
// in NodePort itself, but a type: LoadBalancer service is built on top of one,
// so this establishes the layer the load balancer forwards to.
func TestNodePortService(t *testing.T) {
	f := framework.New(t)
	ctx := context.Background()

	labels := deployEcho(ctx, f, "e2e-nodeport")
	svc := createService(ctx, f, &v1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-nodeport", Namespace: "default"},
		Spec: v1.ServiceSpec{
			Type:     v1.ServiceTypeNodePort,
			Selector: labels,
			Ports: []v1.ServicePort{{
				Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080),
			}},
		},
	})

	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].NodePort == 0 {
		t.Fatalf("service did not get a node port: %+v", svc.Spec.Ports)
	}
	t.Logf("NodePort service allocated port %d", svc.Spec.Ports[0].NodePort)
}

// TestLoadBalancerService is the load balancer feature end to end: the CCM
// allocates a public IP, builds the backend pool, service, and route, and
// publishes the address back onto the service.
func TestLoadBalancerService(t *testing.T) {
	f := framework.New(t)
	ctx := context.Background()

	labels := deployEcho(ctx, f, "e2e-lb")
	svc := createService(ctx, f, &v1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-lb", Namespace: "default"},
		Spec: v1.ServiceSpec{
			Type:     v1.ServiceTypeLoadBalancer,
			Selector: labels,
			// Leave Name empty to cover the shape produced by kubectl expose.
			Ports: []v1.ServicePort{{
				Port: 80, TargetPort: intstr.FromInt32(8080),
			}},
		},
	})

	// A type: LoadBalancer service is layered on a node port, which is what
	// the evroc backend service targets.
	if svc.Spec.Ports[0].NodePort == 0 {
		t.Error("LoadBalancer service should also allocate a node port")
	}

	var ingressIP string
	f.WaitFor(ctx, lbProvisionTimeout, "load balancer to publish an ingress IP", func(ctx context.Context) bool {
		current, err := f.ClientSet.CoreV1().Services("default").Get(ctx, "e2e-lb", metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, ing := range current.Status.LoadBalancer.Ingress {
			if ing.IP != "" {
				ingressIP = ing.IP
				return true
			}
		}
		return false
	})

	t.Logf("load balancer ingress IP: %s", ingressIP)
	if net.ParseIP(ingressIP) == nil {
		t.Errorf("ingress %q is not a valid IP", ingressIP)
	}
	// The frontend is always IPv4, whatever the cluster's stack type.
	if net.ParseIP(ingressIP).To4() == nil {
		t.Errorf("ingress %q is not IPv4; the evroc load balancer frontend is always IPv4", ingressIP)
	}
}

// deployHealthCheckBackend keeps traffic and health on separate listeners and
// records the Host and response status of the platform's actual HTTP probes.
func deployHealthCheckBackend(ctx context.Context, f *framework.Framework, name string) map[string]string {
	f.T.Helper()
	config := `events {}
http {
    log_format probe '$server_port $request_uri $http_host $status';
    access_log /dev/stdout probe;
    server {
        listen 8080;
        location / { return 200 "traffic-ok\n"; }
    }
    server {
        listen 8081;
        if ($http_host != "health.example.test") { return 400; }
        location = /healthz { return 200 "healthy\n"; }
        location / { return 404; }
    }
}`
	_, err := f.ClientSet.CoreV1().ConfigMaps("default").Create(ctx, &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data:       map[string]string{"nginx.conf": config},
	}, metav1.CreateOptions{})
	if err != nil {
		f.T.Fatalf("create health-check config: %v", err)
	}
	f.T.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = f.ClientSet.CoreV1().ConfigMaps("default").Delete(cleanupCtx, name, metav1.DeleteOptions{})
	})
	labels := map[string]string{"app": name}
	replicas := int32(1)
	_, err = f.ClientSet.AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name: "nginx", Image: "nginx:1.28-alpine",
						VolumeMounts: []v1.VolumeMount{{Name: "config", MountPath: "/etc/nginx/nginx.conf", SubPath: "nginx.conf", ReadOnly: true}},
					}},
					Volumes: []v1.Volume{{Name: "config", VolumeSource: v1.VolumeSource{ConfigMap: &v1.ConfigMapVolumeSource{LocalObjectReference: v1.LocalObjectReference{Name: name}}}}},
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		f.T.Fatalf("create health-check deployment: %v", err)
	}
	f.T.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = f.ClientSet.AppsV1().Deployments("default").Delete(cleanupCtx, name, metav1.DeleteOptions{})
	})
	f.WaitFor(ctx, 3*time.Minute, "health-check backend to become available", func(ctx context.Context) bool {
		d, err := f.ClientSet.AppsV1().Deployments("default").Get(ctx, name, metav1.GetOptions{})
		return err == nil && d.Status.ReadyReplicas >= 1
	})
	return labels
}

// TestLoadBalancerHealthCheckAnnotations verifies that real HTTP probes reach
// a separate health listener with a valid Host. Frontend traffic alone cannot
// prove this: the platform forwards to unhealthy backends when all are unhealthy.
func TestLoadBalancerHealthCheckAnnotations(t *testing.T) {
	f := framework.New(t)
	ctx := context.Background()

	labels := deployHealthCheckBackend(ctx, f, "e2e-lb-healthcheck")
	createService(ctx, f, &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-lb-healthcheck",
			Namespace: "default",
			Annotations: map[string]string{
				"ccm.evroc.com/health-check-port": "health",
				"ccm.evroc.com/health-check-path": "/healthz",
				"ccm.evroc.com/health-check-host": "health.example.test",
			},
		},
		Spec: v1.ServiceSpec{
			Type:     v1.ServiceTypeLoadBalancer,
			Selector: labels,
			Ports: []v1.ServicePort{
				{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)},
				{Name: "health", Port: 8081, TargetPort: intstr.FromInt32(8081)},
			},
		},
	})

	var ingressIP string
	f.WaitFor(ctx, lbProvisionTimeout, "load balancer to publish an ingress IP", func(ctx context.Context) bool {
		current, err := f.ClientSet.CoreV1().Services("default").Get(ctx, "e2e-lb-healthcheck", metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, ing := range current.Status.LoadBalancer.Ingress {
			if ing.IP != "" {
				ingressIP = ing.IP
				return true
			}
		}
		return false
	})
	t.Logf("load balancer ingress IP: %s", ingressIP)

	// Only the load balancer sends requests to /healthz. Check actual server
	// responses, so an invalid Host or a TCP-only check cannot pass through
	// the platform's all-unhealthy fallback.
	f.WaitFor(ctx, 3*time.Minute, "HTTP probes with the annotated Host to succeed on the health listener", func(ctx context.Context) bool {
		pods, err := f.ClientSet.CoreV1().Pods("default").List(ctx, metav1.ListOptions{LabelSelector: "app=e2e-lb-healthcheck"})
		if err != nil || len(pods.Items) != 1 {
			return false
		}
		logs, err := f.ClientSet.CoreV1().Pods("default").GetLogs(pods.Items[0].Name, &v1.PodLogOptions{Container: "nginx"}).DoRaw(ctx)
		if err != nil {
			return false
		}
		return strings.Count(string(logs), "8081 /healthz health.example.test 200\n") >= 2
	})

	client := &http.Client{Timeout: 5 * time.Second}
	f.WaitFor(ctx, 3*time.Minute, "the frontend to serve traffic from a backend passing the annotated check", func(ctx context.Context) bool {
		resp, err := client.Get("http://" + ingressIP + "/hostname")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

// TestLoadBalancerDeletionCompletes checks that the CCM finishes its teardown
// path and releases the Service finalizer. Unit tests verify the individual
// evroc API deletion calls.
func TestLoadBalancerDeletionCompletes(t *testing.T) {
	f := framework.New(t)
	ctx := context.Background()

	labels := deployEcho(ctx, f, "e2e-lb-delete")
	createService(ctx, f, &v1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-lb-delete", Namespace: "default"},
		Spec: v1.ServiceSpec{
			Type:     v1.ServiceTypeLoadBalancer,
			Selector: labels,
			Ports:    []v1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	})

	f.WaitFor(ctx, lbProvisionTimeout, "load balancer to be provisioned", func(ctx context.Context) bool {
		current, err := f.ClientSet.CoreV1().Services("default").Get(ctx, "e2e-lb-delete", metav1.GetOptions{})
		return err == nil && len(current.Status.LoadBalancer.Ingress) > 0
	})

	if err := f.ClientSet.CoreV1().Services("default").Delete(ctx, "e2e-lb-delete", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("failed to delete service: %v", err)
	}

	// The CCM finalizer holds the Service until its deletion handler returns.
	f.WaitFor(ctx, lbDeleteTimeout, "service to be fully removed after cloud cleanup", func(ctx context.Context) bool {
		_, err := f.ClientSet.CoreV1().Services("default").Get(ctx, "e2e-lb-delete", metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	})
}

// TestUnsupportedServiceIsRejected checks that a service asking to be internal
// is refused rather than quietly given a public address. Silently handing a
// public IP to a service that asked to be private is the failure worth
// preventing.
func TestUnsupportedServiceIsRejected(t *testing.T) {
	f := framework.New(t)
	ctx := context.Background()

	labels := deployEcho(ctx, f, "e2e-lb-internal")
	createService(ctx, f, &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "e2e-lb-internal",
			Namespace:   "default",
			Annotations: map[string]string{"ccm.evroc.com/lb-type": "internal"},
		},
		Spec: v1.ServiceSpec{
			Type:     v1.ServiceTypeLoadBalancer,
			Selector: labels,
			Ports:    []v1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	})

	// A SyncLoadBalancerFailed event naming the reason should appear, and no
	// ingress address should ever be published.
	var sawFailure bool
	f.WaitFor(ctx, 2*time.Minute, "the service to be reported as unsupported", func(ctx context.Context) bool {
		events, err := f.ClientSet.CoreV1().Events("default").List(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.name=%s", "e2e-lb-internal"),
		})
		if err != nil {
			return false
		}
		for _, e := range events.Items {
			if e.Reason == "SyncLoadBalancerFailed" {
				sawFailure = true
				t.Logf("reported: %s", e.Message)
				return true
			}
		}
		return false
	})
	if !sawFailure {
		t.Error("no SyncLoadBalancerFailed event was recorded for an internal load balancer")
	}

	current, err := f.ClientSet.CoreV1().Services("default").Get(ctx, "e2e-lb-internal", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to read service: %v", err)
	}
	if len(current.Status.LoadBalancer.Ingress) > 0 {
		t.Errorf("a service that asked to be internal was given ingress %+v", current.Status.LoadBalancer.Ingress)
	}
}

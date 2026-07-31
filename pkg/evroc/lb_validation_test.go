// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"regexp"
	"strings"
	"testing"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	"github.com/evroc-oss/evroc-go-sdk/networking"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

func TestShouldServe(t *testing.T) {
	otherClass := "acme.io/lb"
	ourClass := LoadBalancerClass

	tests := []struct {
		name  string
		class *string
		want  bool
	}{
		{"unset class is the default provider", nil, true},
		{"our class", &ourClass, true},
		{"another provider's class", &otherClass, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := testService("my-svc", "default")
			svc.Spec.LoadBalancerClass = tt.class
			if got := shouldServe(svc); got != tt.want {
				t.Errorf("shouldServe() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateService_Accepts(t *testing.T) {
	tests := []struct {
		name  string
		mutit func(*v1.Service)
	}{
		{"plain service", func(*v1.Service) {}},
		{"explicit external", func(s *v1.Service) {
			s.Annotations = map[string]string{annotationLBType: lbTypeExternal}
		}},
		{"explicit Cluster traffic policy", func(s *v1.Service) {
			s.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyCluster
		}},
		{"dual stack", func(s *v1.Service) {
			s.Spec.IPFamilies = []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol}
		}},
		{"proxy protocol opt-in", func(s *v1.Service) {
			s.Annotations = map[string]string{annotationProxyProtocol: "true"}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := testService("my-svc", "default")
			tt.mutit(svc)
			if err := validateService(svc); err != nil {
				t.Errorf("validateService() = %v, want nil", err)
			}
		})
	}
}

func TestValidateService_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		mutit   func(*v1.Service)
		wantErr string
	}{
		{
			name: "internal load balancer",
			mutit: func(s *v1.Service) {
				s.Annotations = map[string]string{annotationLBType: lbTypeInternal}
			},
			wantErr: "requires a public IP",
		},
		{
			name: "unknown lb-type",
			mutit: func(s *v1.Service) {
				s.Annotations = map[string]string{annotationLBType: "sideways"}
			},
			wantErr: "must be",
		},
		{
			name: "externalTrafficPolicy Local",
			mutit: func(s *v1.Service) {
				s.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyLocal
			},
			wantErr: "externalTrafficPolicy",
		},
		{
			name: "sessionAffinity ClientIP",
			mutit: func(s *v1.Service) {
				s.Spec.SessionAffinity = v1.ServiceAffinityClientIP
			},
			wantErr: "sessionAffinity",
		},
		{
			name: "IPv6 single stack",
			mutit: func(s *v1.Service) {
				s.Spec.IPFamilies = []v1.IPFamily{v1.IPv6Protocol}
			},
			wantErr: "always IPv4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := testService("my-svc", "default")
			tt.mutit(svc)
			err := validateService(svc)
			if err == nil {
				t.Fatalf("validateService() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestEnsureLoadBalancer_RejectsUnsupported checks that an unsupported service
// is refused before any cloud resource is created: a service asking to be
// internal must never end up with a public IP.
func TestEnsureLoadBalancer_RejectsUnsupported(t *testing.T) {
	lbMock := &recordingLBClient{getLBErr: evroc.ErrNotFound}
	ipMock := &recordingPublicIPService{ip: testPublicIP("x", "203.0.113.1")}
	lb := newTestLB(lbMock, ipMock)

	svc := testService("my-svc", "default")
	svc.Annotations = map[string]string{annotationLBType: lbTypeInternal}

	_, err := lb.EnsureLoadBalancer(context.Background(), "cluster", svc, testNodes("node-1"))
	if err == nil {
		t.Fatal("expected an error for an internal load balancer")
	}
	if ipMock.created {
		t.Error("a public IP was allocated for a service that asked to be internal")
	}
	if lbMock.createdName != "" {
		t.Error("a load balancer was created for an unsupported service")
	}
}

func TestWantsProxyProtocol(t *testing.T) {
	svc := testService("my-svc", "default")
	if wantsProxyProtocol(svc) {
		t.Error("proxy protocol should be off unless explicitly enabled")
	}

	svc.Annotations = map[string]string{annotationProxyProtocol: "true"}
	if !wantsProxyProtocol(svc) {
		t.Error("proxy protocol should be on when the annotation is true")
	}
}

// --- create options threading ---

func nodeInZone(name, zone string) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{v1.LabelTopologyZone: zone},
		},
	}
}

func TestCreateLoadBalancer_ThreadsOptions(t *testing.T) {
	lbMock := &recordingLBClient{lb: testEvrocLB("default-my-svc"), getLBErr: evroc.ErrNotFound}
	ipMock := &recordingPublicIPService{ip: testPublicIP("default-my-svc-ip", "203.0.113.1")}
	lb := newTestLB(lbMock, ipMock)
	lb.config.Network.StackType = ccmconfig.StackTypeIPv6Only
	lb.config.Network.VPCRef = "vpc-prod"
	lb.config.Network.SubnetRefs = map[string]string{"a": "subnet-a"}

	svc := testService("my-svc", "default")
	svc.Annotations = map[string]string{annotationProxyProtocol: "true"}

	nodes := []*v1.Node{nodeInZone("node-1", "a"), nodeInZone("node-2", "b")}
	if _, err := lb.EnsureLoadBalancer(context.Background(), "cluster", svc, nodes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	opts := lbMock.createdOpts
	if !opts.ProxyProtocol {
		t.Error("ProxyProtocol should be set when the service opts in")
	}
	if opts.BackendStackType != ccmconfig.StackTypeIPv6Only {
		t.Errorf("BackendStackType = %q, want %q", opts.BackendStackType, ccmconfig.StackTypeIPv6Only)
	}
	if opts.BackendNetwork == nil {
		t.Fatal("BackendNetwork should be set when a VPC is configured")
	}
	wantVPC := "/networking/projects/test-project/regions/se-sto/virtualPrivateClouds/vpc-prod"
	if opts.BackendNetwork.VpcRef != wantVPC {
		t.Errorf("VpcRef = %q, want the fully-qualified %q", opts.BackendNetwork.VpcRef, wantVPC)
	}
	if len(opts.BackendNetwork.Subnets) != 2 {
		t.Fatalf("subnet count = %d, want one per zone the nodes occupy", len(opts.BackendNetwork.Subnets))
	}
	// Zone a is configured explicitly; zone b falls back to the default name.
	const subnetPfx = "/networking/projects/test-project/regions/se-sto/subnets/"
	if got := opts.BackendNetwork.Subnets[0].SubnetRef; got != subnetPfx+"subnet-a" {
		t.Errorf("zone a subnet = %q, want %q", got, subnetPfx+"subnet-a")
	}
	if got := opts.BackendNetwork.Subnets[1].SubnetRef; got != subnetPfx+"default-se-sto-b" {
		t.Errorf("zone b subnet = %q, want %q", got, subnetPfx+"default-se-sto-b")
	}
}

func TestBackendNetwork_NilWithoutVPC(t *testing.T) {
	lb := newTestLB(&recordingLBClient{}, &recordingPublicIPService{})
	if got := lb.backendNetwork([]*v1.Node{nodeInZone("node-1", "a")}); got != nil {
		t.Errorf("backendNetwork() = %+v, want nil when no VPC is configured", got)
	}
}

func TestBackendNetwork_IgnoresUnlabelledNodes(t *testing.T) {
	lb := newTestLB(&recordingLBClient{}, &recordingPublicIPService{})
	lb.config.Network.VPCRef = "vpc-prod"

	// A node that the CCM has not labelled yet has no zone to attach to.
	nodes := []*v1.Node{nodeInZone("node-1", "a"), {ObjectMeta: metav1.ObjectMeta{Name: "pending"}}}
	got := lb.backendNetwork(nodes)
	if got == nil {
		t.Fatal("backendNetwork() = nil, want a network for the labelled node")
	}
	if len(got.Subnets) != 1 {
		t.Errorf("subnet count = %d, want only the labelled node's zone", len(got.Subnets))
	}
}

// TestResolvePublicIP_RequestMatchesSDKVersion guards against the request body
// carrying a different API version than the path the SDK posts to. The version
// was previously hardcoded here and went stale when the SDK moved networking
// from v1beta1 to v1beta2, so every public IP was rejected with
// "apiVersion ... does not match path version".
func TestResolvePublicIP_RequestMatchesSDKVersion(t *testing.T) {
	// Get misses, so a new public IP is allocated and its request recorded.
	ipMock := &recordingPublicIPService{ip: testPublicIP("default-my-svc-ip", "203.0.113.1"), getErr: evroc.ErrNotFound}
	lb := newTestLB(&recordingLBClient{}, ipMock)

	if _, err := lb.resolvePublicIP(context.Background(), "default-my-svc", testService("my-svc", "default")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ipMock.createdRequest == nil {
		t.Fatal("no public IP request was recorded")
	}
	want := networking.NewPublicIPBuilder("x").Build().ApiVersion
	if got := ipMock.createdRequest.ApiVersion; got != want {
		t.Errorf("request apiVersion = %q, want %q (the version the SDK posts to)", got, want)
	}
}

// TestListenerName covers unnamed service ports. A single-port service usually
// has no port name, and "%s-%d" then yields "-80", which the API rejects for
// not being a valid DNS label, so the load balancer is never created.
func TestListenerName(t *testing.T) {
	// From k8s.io/apimachinery: the name must be a DNS label.
	valid := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

	tests := []struct {
		name string
		port v1.ServicePort
		want string
	}{
		{"named port", v1.ServicePort{Name: "http", Port: 80}, "http-80"},
		{"unnamed port", v1.ServicePort{Port: 80}, "port-80"},
		{"unnamed high port", v1.ServicePort{Port: 8443}, "port-8443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listenerName(tt.port)
			if got != tt.want {
				t.Errorf("listenerName() = %q, want %q", got, tt.want)
			}
			if !valid.MatchString(got) {
				t.Errorf("listenerName() = %q, which the API rejects as a listener name", got)
			}
		})
	}
}

// TestResolvePublicIP_ReusesExistingIP covers retrying after a partially
// completed create. Creating a load balancer is not atomic, so an attempt can
// allocate the public IP and then fail on a later step. If the retry treated
// the existing IP as an error, every subsequent attempt would fail with
// AlreadyExists and the service would never get a load balancer.
func TestResolvePublicIP_ReusesExistingIP(t *testing.T) {
	ipMock := &recordingPublicIPService{ip: testPublicIP("default-my-svc-ip", "203.0.113.1")}
	lb := newTestLB(&recordingLBClient{}, ipMock)

	ref, err := lb.resolvePublicIP(context.Background(), "default-my-svc", testService("my-svc", "default"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == "" {
		t.Error("no public IP ref returned")
	}
	if ipMock.created {
		t.Error("a second public IP was allocated even though one already existed")
	}
}

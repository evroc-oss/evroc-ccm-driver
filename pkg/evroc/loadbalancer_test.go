// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package evroc

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	evroc "github.com/evroc-oss/evroc-go-sdk"
	"github.com/evroc-oss/evroc-go-sdk/networking"
	lbtypes "github.com/evroc-oss/evroc-go-sdk/types/loadbalancer"
	networkingtypes "github.com/evroc-oss/evroc-go-sdk/types/networking"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

type recordingLBClient struct {
	existing     *lbtypes.Loadbalancer
	address      string
	getErr       error
	ensureErr    error
	waitErr      error
	ensured      []lbEnsureOptions
	waitedName   string
	waitTimeout  time.Duration
	deletedName  string
	deletedPorts []string
	deleteErr    error
}

func (c *recordingLBClient) Ensure(_ context.Context, opts lbEnsureOptions) (*lbtypes.Loadbalancer, error) {
	c.ensured = append(c.ensured, opts)
	if c.ensureErr != nil {
		return nil, c.ensureErr
	}
	return &lbtypes.Loadbalancer{
		Metadata: lbtypes.RegionalMetadataResponse{Id: opts.Name},
		Spec:     lbtypes.LoadbalancerSpec{PublicIPRef: opts.PublicIPRef},
	}, nil
}

func (c *recordingLBClient) WaitForReady(_ context.Context, name string, timeout time.Duration) (*lbtypes.Loadbalancer, error) {
	c.waitedName = name
	c.waitTimeout = timeout
	if c.waitErr != nil {
		return nil, c.waitErr
	}
	result := &lbtypes.Loadbalancer{Metadata: lbtypes.RegionalMetadataResponse{Id: name}}
	if c.address != "" {
		result.Status.Networking = &lbtypes.LoadbalancerStatusNetworking{PublicIPv4Address: &c.address}
	}
	return result, nil
}

func (c *recordingLBClient) Delete(_ context.Context, name string, ports []string) error {
	c.deletedName = name
	c.deletedPorts = append([]string(nil), ports...)
	return c.deleteErr
}

func (c *recordingLBClient) GetLB(_ context.Context, _ string) (*lbtypes.Loadbalancer, error) {
	return c.existing, c.getErr
}

type recordingPublicIPs struct {
	ip        *networkingtypes.PublicIP
	getErr    error
	createErr error
	waitErr   error
	created   bool
	deleted   bool
	getNames  []string
}

func (c *recordingPublicIPs) Create(_ context.Context, _ *networkingtypes.PublicIPRequest) (*networkingtypes.PublicIP, error) {
	c.created = true
	return c.ip, c.createErr
}

func (c *recordingPublicIPs) Get(_ context.Context, name string) (*networkingtypes.PublicIP, error) {
	c.getNames = append(c.getNames, name)
	if c.getErr != nil && !c.created {
		return nil, c.getErr
	}
	return c.ip, nil
}

func (c *recordingPublicIPs) Delete(_ context.Context, _ string) error {
	c.deleted = true
	return nil
}

func (c *recordingPublicIPs) WaitForReady(
	_ context.Context,
	_ string,
	_ time.Duration,
	_ ...networking.WaiterOption,
) (*networkingtypes.PublicIP, error) {
	return c.ip, c.waitErr
}

func testLBConfig() *ccmconfig.Config {
	return &ccmconfig.Config{
		Context:       ccmconfig.ContextConfig{Project: "test-project", Region: "se-sto"},
		LoadBalancers: ccmconfig.LoadBalancerConfig{StackType: ccmconfig.StackTypeIPv4Only},
	}
}

func testService(name, namespace string, ports ...v1.ServicePort) *v1.Service {
	if len(ports) == 0 {
		ports = []v1.ServicePort{{
			Name: "http", Port: 80, NodePort: 30080, TargetPort: intstr.FromInt32(8080),
		}}
	}
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace, UID: types.UID(namespace + "/" + name + "/uid"),
		},
		Spec: v1.ServiceSpec{Type: v1.ServiceTypeLoadBalancer, Ports: ports},
	}
}

func testNodes(names ...string) []*v1.Node {
	nodes := make([]*v1.Node, len(names))
	for i, name := range names {
		nodes[i] = &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	return nodes
}

func nodeInZone(name, zone string) *v1.Node {
	return &v1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: name, Labels: map[string]string{v1.LabelTopologyZone: zone},
	}}
}

func testPublicIP(name, address string) *networkingtypes.PublicIP {
	project, region := "test-project", "se-sto"
	return &networkingtypes.PublicIP{
		Metadata: networkingtypes.RegionalMetadataResponse{Id: name, Project: &project, Region: &region},
		Status:   networkingtypes.PublicIPStatus{PublicIPv4Address: &address},
	}
}

func newTestLB(client lbClient, ips publicIPService) *loadBalancer {
	return &loadBalancer{lb: client, ips: ips, config: testLBConfig(), logger: slog.Default()}
}

func TestGetLoadBalancerName(t *testing.T) {
	lb := newTestLB(&recordingLBClient{}, &recordingPublicIPs{})
	service := testService("api", "default")
	name := lb.GetLoadBalancerName(context.Background(), "cluster-a", service)

	if !regexp.MustCompile(`^ccm-[a-f0-9]{24}$`).MatchString(name) {
		t.Fatalf("GetLoadBalancerName() = %q, want bounded hashed name", name)
	}
	if again := lb.GetLoadBalancerName(context.Background(), "cluster-a", service); again != name {
		t.Errorf("name is not deterministic: %q then %q", name, again)
	}
	if other := lb.GetLoadBalancerName(context.Background(), "cluster-b", service); other == name {
		t.Error("different clusters produced the same load balancer name")
	}
	copy := service.DeepCopy()
	copy.UID = "replacement-uid"
	if other := lb.GetLoadBalancerName(context.Background(), "cluster-a", copy); other == name {
		t.Error("replacement Service produced the same load balancer name")
	}
}

func TestValidateService(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*v1.Service)
		wantErr string
	}{
		{
			name: "plain TCP service is supported",
		},
		{
			name: "dual-stack frontend is rejected because it includes IPv6",
			mutate: func(s *v1.Service) {
				s.Spec.IPFamilies = []v1.IPFamily{
					v1.IPv4Protocol,
					v1.IPv6Protocol,
				}
			},
			wantErr: "support IPv4 only",
		},
		{
			name: "UDP listener is rejected",
			mutate: func(s *v1.Service) {
				s.Spec.Ports[0].Protocol = v1.ProtocolUDP
			},
			wantErr: "TCP listeners only",
		},
		{
			name: "service without a node port is rejected",
			mutate: func(s *v1.Service) {
				s.Spec.Ports[0].NodePort = 0
			},
			wantErr: "node-port allocation",
		},
		{
			name: "internal load balancer is rejected",
			mutate: func(s *v1.Service) {
				s.Annotations = map[string]string{annotationLBType: lbTypeInternal}
			},
			wantErr: "requires a public IP",
		},
		{
			name: "local traffic policy is rejected",
			mutate: func(s *v1.Service) {
				s.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyLocal
			},
			wantErr: "externalTrafficPolicy",
		},
		{
			name: "client IP session affinity is rejected",
			mutate: func(s *v1.Service) {
				s.Spec.SessionAffinity = v1.ServiceAffinityClientIP
			},
			wantErr: "sessionAffinity",
		},
		{
			name: "IPv6-only frontend is rejected",
			mutate: func(s *v1.Service) {
				s.Spec.IPFamilies = []v1.IPFamily{v1.IPv6Protocol}
			},
			wantErr: "support IPv4 only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := testService("api", "default")
			if tt.mutate != nil {
				tt.mutate(service)
			}
			err := validateService(service)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validateService() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("validateService() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestEnsureLoadBalancer(t *testing.T) {
	t.Run("managed public IP", func(t *testing.T) {
		service := testService("api", "default",
			v1.ServicePort{Name: "http", Port: 80, NodePort: 30080},
			v1.ServicePort{Name: "https", Port: 443, NodePort: 30443},
		)
		client := &recordingLBClient{getErr: evroc.ErrNotFound, address: "203.0.113.1"}
		lb := newTestLB(client, &recordingPublicIPs{})
		lbName := lb.GetLoadBalancerName(context.Background(), "cluster", service)
		ips := &recordingPublicIPs{
			ip: testPublicIP(lb.publicIPName(lbName), "203.0.113.1"), getErr: evroc.ErrNotFound,
		}
		lb.ips = ips

		status, err := lb.EnsureLoadBalancer(context.Background(), "cluster", service, testNodes("node-a", "node-b"))
		if err != nil {
			t.Fatalf("EnsureLoadBalancer() error = %v", err)
		}
		if !ips.created || len(client.ensured) != 1 {
			t.Fatalf("created IP = %v, reconciles = %d; want true, 1", ips.created, len(client.ensured))
		}
		opts := client.ensured[0]
		if opts.Name != lbName || len(opts.Listeners) != 2 || len(opts.BackendRefs) != 2 {
			t.Errorf("unexpected reconcile options: %#v", opts)
		}
		if len(status.Ingress) != 1 || status.Ingress[0].IP != "203.0.113.1" {
			t.Errorf("status = %#v, want managed ingress IP", status)
		}
		if client.waitedName != lbName || client.waitTimeout != loadBalancerWaitTimeout {
			t.Errorf("ready wait = (%q, %s), want (%q, %s)",
				client.waitedName, client.waitTimeout, lbName, loadBalancerWaitTimeout)
		}
	})

	t.Run("annotated public IP", func(t *testing.T) {
		service := testService("api", "default")
		const ref = "/networking/projects/test-project/regions/se-sto/publicIPs/static-ip"
		service.Annotations = map[string]string{annotationPublicIPRef: ref}
		client := &recordingLBClient{getErr: evroc.ErrNotFound, address: "203.0.113.9"}
		ips := &recordingPublicIPs{ip: testPublicIP("static-ip", "203.0.113.9")}
		lb := newTestLB(client, ips)

		status, err := lb.EnsureLoadBalancer(context.Background(), "cluster", service, testNodes("node-a"))
		if err != nil {
			t.Fatalf("EnsureLoadBalancer() error = %v", err)
		}
		if ips.created || client.ensured[0].PublicIPRef != ref {
			t.Errorf("annotated IP was not preserved: created=%v opts=%#v", ips.created, client.ensured[0])
		}
		if len(ips.getNames) != 1 || ips.getNames[0] != "static-ip" {
			t.Errorf("annotated IP lookups = %v, want one existence check for static-ip", ips.getNames)
		}
		if status.Ingress[0].IP != "203.0.113.9" {
			t.Errorf("ingress = %#v, want annotated IP", status.Ingress)
		}
	})

	t.Run("existing resources are reconciled", func(t *testing.T) {
		service := testService("api", "default", v1.ServicePort{Name: "web", Port: 8080, NodePort: 32080})
		service.Annotations = map[string]string{annotationProxyProtocol: "true"}
		client := &recordingLBClient{existing: &lbtypes.Loadbalancer{}}
		lb := newTestLB(client, &recordingPublicIPs{})
		lbName := lb.GetLoadBalancerName(context.Background(), "cluster", service)
		lb.ips = &recordingPublicIPs{ip: testPublicIP(lb.publicIPName(lbName), "203.0.113.2")}

		if _, err := lb.EnsureLoadBalancer(context.Background(), "cluster", service, testNodes("node-a")); err != nil {
			t.Fatalf("EnsureLoadBalancer() error = %v", err)
		}
		if len(client.ensured) != 1 || !client.ensured[0].ProxyProtocol || client.ensured[0].Listeners[0].BackendPort != 32080 {
			t.Errorf("existing LB was not fully reconciled: %#v", client.ensured)
		}
	})

	t.Run("load balancer readiness error is returned", func(t *testing.T) {
		service := testService("api", "default")
		waitErr := errors.New("ready wait timed out")
		client := &recordingLBClient{getErr: evroc.ErrNotFound, waitErr: waitErr}
		lb := newTestLB(client, &recordingPublicIPs{})
		lbName := lb.GetLoadBalancerName(context.Background(), "cluster", service)
		lb.ips = &recordingPublicIPs{
			ip: testPublicIP(lb.publicIPName(lbName), "203.0.113.10"), getErr: evroc.ErrNotFound,
		}

		_, err := lb.EnsureLoadBalancer(context.Background(), "cluster", service, testNodes("node-a"))
		if !errors.Is(err, waitErr) {
			t.Fatalf("EnsureLoadBalancer() error = %v, want readiness error", err)
		}
	})
}

func TestUpdateLoadBalancerUsesConfiguredBackendNetwork(t *testing.T) {
	service := testService("api", "default")
	client := &recordingLBClient{}
	lb := newTestLB(client, &recordingPublicIPs{})
	lb.config.LoadBalancers.BackendNetwork = &ccmconfig.LoadBalancerBackendNetworkConfig{
		VPCID: "vpc-prod",
		Subnets: []ccmconfig.LoadBalancerBackendSubnetConfig{
			{Zone: "a", SubnetID: "subnet-a"},
		},
	}
	lbName := lb.GetLoadBalancerName(context.Background(), "cluster", service)
	lb.ips = &recordingPublicIPs{ip: testPublicIP(lb.publicIPName(lbName), "203.0.113.2")}

	nodes := []*v1.Node{nodeInZone("node-a", "a"), nodeInZone("node-b", "b")}
	if err := lb.UpdateLoadBalancer(context.Background(), "cluster", service, nodes); err != nil {
		t.Fatalf("UpdateLoadBalancer() error = %v", err)
	}
	network := client.ensured[0].BackendNetwork
	if network == nil || len(network.Subnets) != 1 || string(network.Subnets[0].Zone) != "a" {
		t.Fatalf("backend network = %#v, want only configured zone a", network)
	}
}

func TestEnsurePublicIPErrorsAndRaces(t *testing.T) {
	service := testService("api", "default")

	t.Run("annotated public IP must exist", func(t *testing.T) {
		annotated := service.DeepCopy()
		annotated.Annotations = map[string]string{
			annotationPublicIPRef: "/networking/projects/test-project/regions/se-sto/publicIPs/missing",
		}
		ips := &recordingPublicIPs{getErr: evroc.ErrNotFound}
		lb := newTestLB(&recordingLBClient{}, ips)
		_, err := lb.ensurePublicIP(context.Background(), "lb", annotated)
		if !errors.Is(err, evroc.ErrNotFound) || ips.created {
			t.Fatalf("ensurePublicIP() error = %v, created=%v", err, ips.created)
		}
	})

	t.Run("lookup error is preserved", func(t *testing.T) {
		boom := errors.New("temporary API failure")
		ips := &recordingPublicIPs{getErr: boom}
		lb := newTestLB(&recordingLBClient{}, ips)
		_, err := lb.ensurePublicIP(context.Background(), "lb", service)
		if !errors.Is(err, boom) || ips.created {
			t.Fatalf("ensurePublicIP() error = %v, created=%v", err, ips.created)
		}
	})

	t.Run("create conflict is adopted", func(t *testing.T) {
		ips := &recordingPublicIPs{
			ip: testPublicIP("lb-ip", "203.0.113.3"), getErr: evroc.ErrNotFound, createErr: evroc.ErrConflict,
		}
		lb := newTestLB(&recordingLBClient{}, ips)
		ref, err := lb.ensurePublicIP(context.Background(), "lb", service)
		if err != nil || !strings.HasSuffix(ref, "/publicIPs/lb-ip") {
			t.Fatalf("ensurePublicIP() = %q, %v", ref, err)
		}
	})
}

func TestEnsureLoadBalancerDeletedAlwaysDeletesManagedIP(t *testing.T) {
	for _, tt := range []struct {
		name       string
		annotation *string
		wantDelete bool
	}{
		{
			name:       "absent annotation deletes managed IP",
			wantDelete: true,
		},
		{
			name:       "empty annotation deletes managed IP",
			annotation: ptr(""),
			wantDelete: true,
		},
		{
			// The annotation may have been added after a managed IP was
			// allocated; the managed IP must not leak.
			name:       "annotated service still deletes the managed IP",
			annotation: ptr("/networking/projects/p/regions/r/publicIPs/static"),
			wantDelete: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := testService("api", "default")
			if tt.annotation != nil {
				service.Annotations = map[string]string{annotationPublicIPRef: *tt.annotation}
			}
			client, ips := &recordingLBClient{}, &recordingPublicIPs{}
			lb := newTestLB(client, ips)
			if err := lb.EnsureLoadBalancerDeleted(context.Background(), "cluster", service); err != nil {
				t.Fatal(err)
			}
			if ips.deleted != tt.wantDelete {
				t.Errorf("managed IP deleted = %v, want %v", ips.deleted, tt.wantDelete)
			}
		})
	}
}

func TestListenerAndBackendHelpers(t *testing.T) {
	for _, tt := range []struct {
		name string
		port v1.ServicePort
		want string
	}{
		{
			name: "named port includes its name and number",
			port: v1.ServicePort{
				Name: "http",
				Port: 80,
			},
			want: "http-80",
		},
		{
			name: "unnamed port uses a valid prefix",
			port: v1.ServicePort{
				Port: 8443,
			},
			want: "port-8443",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := listenerName(tt.port); got != tt.want {
				t.Errorf("listenerName(%#v) = %q, want %q", tt.port, got, tt.want)
			}
		})
	}

	lb := newTestLB(&recordingLBClient{}, &recordingPublicIPs{})
	lb.config.LoadBalancers.BackendNetwork = &ccmconfig.LoadBalancerBackendNetworkConfig{
		VPCID: "vpc-prod",
		Subnets: []ccmconfig.LoadBalancerBackendSubnetConfig{
			{Zone: "a", SubnetID: "subnet-a"},
		},
	}
	network := lb.backendNetwork()
	wantSubnetRef := "/networking/projects/test-project/regions/se-sto/subnets/subnet-a"
	if got := network.Subnets[0].SubnetRef; got != wantSubnetRef {
		t.Errorf("subnet ref = %q, want %q", got, wantSubnetRef)
	}
}

func ptr[T any](value T) *T { return &value }

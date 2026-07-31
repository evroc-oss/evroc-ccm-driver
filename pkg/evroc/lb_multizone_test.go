package evroc

import (
	"context"
	"testing"

	evroc "github.com/evroc-oss/evroc-go-sdk"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ccmconfig "github.com/evroc-oss/evroc-ccm-driver/pkg/config"
)

func haNode(name, zone string) *v1.Node {
	return &v1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{v1.LabelTopologyZone: zone},
	}}
}

// TestHAControlPlaneAcrossThreeZones models the shape of an HA cluster: three
// control-plane nodes spread over zones a, b, and c. The load balancer must
// attach to a subnet in every zone that holds a backend, or the nodes in the
// unattached zones are unreachable and the cluster is not actually HA.
func TestHAControlPlaneAcrossThreeZones(t *testing.T) {
	lbMock := &recordingLBClient{lb: testEvrocLB("kube-system-cp"), getLBErr: evroc.ErrNotFound}
	lb := newTestLB(lbMock, &recordingPublicIPService{ip: testPublicIP("cp-ip", "203.0.113.9")})
	lb.config.Network.VPCRef = "default-se-sto"

	nodes := []*v1.Node{
		haNode("cp-1", "a"), haNode("cp-2", "b"), haNode("cp-3", "c"),
	}

	svc := testService("cp", "kube-system")
	if _, err := lb.EnsureLoadBalancer(context.Background(), "cluster", svc, nodes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	opts := lbMock.createdOpts
	if opts.BackendNetwork == nil {
		t.Fatal("no backend network for a VPC-pinned load balancer")
	}
	if len(opts.BackendNetwork.Subnets) != 3 {
		t.Fatalf("attached to %d subnets, want one per zone (a, b, c)", len(opts.BackendNetwork.Subnets))
	}
	// Fully-qualified refs: the load balancer admission webhook rejects bare
	// names in spec.backendNetwork. These are the real subnets in se-sto.
	const pfx = "/networking/projects/test-project/regions/se-sto/subnets/"
	want := map[string]string{"a": pfx + "default-se-sto-a", "b": pfx + "default-se-sto-b", "c": pfx + "default-se-sto-c"}
	for _, s := range opts.BackendNetwork.Subnets {
		zone := string(s.Zone)
		if want[zone] != s.SubnetRef {
			t.Errorf("zone %s attached to %q, want %q", zone, s.SubnetRef, want[zone])
		}
		delete(want, zone)
	}
	if len(want) != 0 {
		t.Errorf("zones missing a subnet: %v", want)
	}

	// All three control-plane nodes must be registered as backends.
	if len(opts.BackendRefs) != 3 {
		t.Errorf("registered %d backends, want all 3 control-plane nodes", len(opts.BackendRefs))
	}
}

// TestBackendChurnOnScaling covers a node joining and later being removed,
// which is how an HA control plane is repaired after losing a member.
func TestBackendChurnOnScaling(t *testing.T) {
	lbMock := &recordingLBClient{lb: testEvrocLB("kube-system-cp")}
	lb := newTestLB(lbMock, &recordingPublicIPService{})
	svc := testService("cp", "kube-system")

	// Scale out: a third node joins.
	nodes := []*v1.Node{haNode("cp-1", "a"), haNode("cp-2", "b"), haNode("cp-3", "c")}
	if err := lb.UpdateLoadBalancer(context.Background(), "cluster", svc, nodes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lbMock.syncedRefs) != 3 {
		t.Errorf("synced %d backends after scale-out, want 3", len(lbMock.syncedRefs))
	}

	// Lose a zone: the node in zone b is removed.
	nodes = []*v1.Node{haNode("cp-1", "a"), haNode("cp-3", "c")}
	if err := lb.UpdateLoadBalancer(context.Background(), "cluster", svc, nodes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lbMock.syncedRefs) != 2 {
		t.Errorf("synced %d backends after losing a node, want 2", len(lbMock.syncedRefs))
	}
	for _, ref := range lbMock.syncedRefs {
		if ref == vmRef("cp-2") {
			t.Error("the removed node is still registered as a backend")
		}
	}
}

// TestIPv6OnlyHACluster combines the ipv6-only stack type with a multi-zone
// control plane, since an HA cluster is where both features meet.
func TestIPv6OnlyHACluster(t *testing.T) {
	lbMock := &recordingLBClient{lb: testEvrocLB("kube-system-cp"), getLBErr: evroc.ErrNotFound}
	lb := newTestLB(lbMock, &recordingPublicIPService{ip: testPublicIP("cp-ip", "203.0.113.9")})
	lb.config.Network.StackType = ccmconfig.StackTypeIPv6Only
	lb.config.Network.VPCRef = "k3s-ha-vpc"
	lb.config.Network.SubnetRefs = map[string]string{"a": "k3s-ha-subnet"}

	nodes := []*v1.Node{haNode("cp-1", "a"), haNode("cp-2", "b")}
	if _, err := lb.EnsureLoadBalancer(context.Background(), "cluster", testService("cp", "kube-system"), nodes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	opts := lbMock.createdOpts
	if opts.BackendStackType != ccmconfig.StackTypeIPv6Only {
		t.Errorf("BackendStackType = %q, want ipv6-only so backends are reached over IPv6", opts.BackendStackType)
	}
	// Zone a is pinned explicitly; zone b falls back to the default name.
	got := map[string]string{}
	for _, s := range opts.BackendNetwork.Subnets {
		got[string(s.Zone)] = s.SubnetRef
	}
	const pfx = "/networking/projects/test-project/regions/se-sto/subnets/"
	if got["a"] != pfx+"k3s-ha-subnet" {
		t.Errorf("zone a subnet = %q, want the configured k3s-ha-subnet", got["a"])
	}
	if got["b"] != pfx+"default-se-sto-b" {
		t.Errorf("zone b subnet = %q, want the default fallback", got["b"])
	}
}

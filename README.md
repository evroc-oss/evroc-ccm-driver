<div align="center">
  <img src="docs/images/evroc-logo.png" alt="evroc" width="300"/>
</div>

# evroc Cloud Controller Manager

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![golangci-lint](https://img.shields.io/github/actions/workflow/status/evroc-oss/evroc-ccm-driver/ci.yml?branch=main&label=golangci-lint&logo=go)](https://github.com/evroc-oss/evroc-ccm-driver/actions/workflows/ci.yml)

Kubernetes [Cloud Controller Manager](https://kubernetes.io/docs/concepts/architecture/cloud-controller/) (CCM) for the [evroc](https://evroc.com) cloud platform. The evroc CCM labels nodes with topology
metadata, syncs addresses and provider IDs, and reconciles `type: LoadBalancer`
services into evroc load balancers.

## Table of Contents

- [Quickstart](#quickstart)
- [Features](#features)
- [Load balancers](#load-balancers)
  - [Enabling load balancer access](#enabling-load-balancer-access)
  - [Annotations](#annotations)
  - [External traffic policy](#external-traffic-policy)
  - [Unsupported service fields](#unsupported-service-fields)
- [Security considerations](#security-considerations)
- [License](#license)
- [Support](#support)

## Quickstart

Requires Kubernetes 1.29+ and evroc credentials with compute, loadbalancer, and networking access.

### 1. Create a service account

The CCM authenticates as an evroc service account bound to the
`kubernetesCCMAgent` role, which grants access to the compute, loadbalancer, and
networking APIs.

```bash
export EVROC_PROJECT="$(evroc config current-project)"

evroc iam serviceaccount create ccm-agent
evroc iam serviceaccount credential create ccm-agent-key --service-account ccm-agent

SA_PRINCIPAL="/iam/projects/${EVROC_PROJECT}/serviceAccounts/ccm-agent"
evroc iam rolebinding assign --principal "$SA_PRINCIPAL" --role /iam/roles/kubernetesCCMAgent
```

The credential's private key is printed only once, at creation.

### 2. Create the config Secret

The CCM reads a YAML config file mounted as a Kubernetes Secret. The `auth`,
`api`, and `context` sections use the same evroc SDK schema as the
[evroc CSI driver](https://github.com/evroc-oss/evroc-csi-driver).

A minimal example is shown here:

```yaml
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "<base64-encoded-jwk>"

context:
  organization: "my-org"
  project: "my-project"
  region: "se-sto"
```

The `service_account_secret` is the base64 JWK printed by
`evroc iam serviceaccount credential create` above.

See [deploy/evroc-config-example.yaml](deploy/evroc-config-example.yaml) for the
full example, and [advanced configuration](docs/advanced-configuration.md) for
optional settings such as `api.base_url`/`auth.token_url` (private cloud),
`ccm.identifier` (multi-cluster), and `loadbalancers.backendNetwork`.

```bash
kubectl create namespace evroc-system
cp deploy/evroc-config-example.yaml my-config.yaml
# Edit my-config.yaml with your credentials and context.
kubectl create secret generic evroc-ccm-config \
  --namespace evroc-system \
  --from-file=config.yaml=my-config.yaml
```

### 3. Install the chart

```bash
helm install evroc-ccm oci://ghcr.io/evroc-oss/charts/evroc-ccm \
  --version 0.4.0 \
  --namespace evroc-system \
  --set evroc.existingConfigSecret=evroc-ccm-config
```

To install from a local checkout, or to deploy via static manifests instead of
Helm, see [`docs/deploying-ccm.md`](docs/deploying-ccm.md).

## Features

| Feature | Status |
|---------|--------|
| Node topology labeling (region/zone) | Supported |
| Node provider ID | Supported |
| Node address sync (internal/external IP) | Supported |
| Instance type labeling | Supported |
| Node lifecycle (shutdown detection) | Supported |
| Load Balancer | Supported |
| Routes | Not planned |

## Node topology labelling

The CCM automatically labels Kubernetes nodes with:
- `topology.kubernetes.io/region` — evroc region (e.g. `se-sto`)
- `topology.kubernetes.io/zone` — evroc availability zone (e.g. `a`)
- `node.kubernetes.io/instance-type` — compute profile (e.g. `c1a.m`)

Two requirements apply for this to work:

- **Kubernetes node names must equal the evroc VM names.** The CCM looks VMs
  up by node name for node initialization and for load balancer backends.
- **Kubelets must run with `--cloud-provider=external`.** Otherwise the
  distribution's own cloud controller initializes nodes first and the CCM is
  never consulted. On k3s and RKE2, set `disable-cloud-controller: true` and
  `kubelet-arg: ["cloud-provider=external"]`

## Load balancers

When a `type: LoadBalancer` Service is created, the CCM provisions an evroc
load balancer with a public IP and a backend pool containing **every node in the
cluster**. The load balancer forwards traffic to each node on the Service's node
port, where kube-proxy routes it to a pod:

```
client -> load balancer :80 -> VM :32061 (node port) -> kube-proxy -> pod :8080
```

### Enabling load balancer access

The node port is allocated from 30000-32767 and is served on **all nodes**,
regardless of whether they run a pod for the Service. The whole range must be
reachable from the load balancer; without it the load balancer is created and
reports healthy while no traffic arrives.

```console
# Node ports, so the load balancer can reach service backends.
$ evroc networking securitygroup addrule <security-group> \
    --name nodeports --direction Ingress --protocol TCP \
    --port 30000 --end-port 32767 \
    --remote-ip-or-cidr <vpc-cidr>
```

Apply this rule to every node's security group.

Use the VPC CIDR by default. For a tighter rule, add one rule for each backend
subnet CIDR used by the load balancer. Avoid `0.0.0.0/0`, which exposes the
node-port range more broadly than the CCM requires.

No other security-group rules are required specifically by the CCM. Cluster
networking and node-access rules depend on the Kubernetes distribution and CNI
and should be configured as part of cluster provisioning.

### Annotations

Annotations on `Service` objects of `type: LoadBalancer` alter the behaviour of
the CCM in the following ways:

| Annotation | Default | Meaning |
|------------|---------|---------|
| `ccm.evroc.com/public-ip-ref` | none | Use a pre-existing public IP instead of allocating one. Requires the full evroc resource ID: `/networking/projects/PROJECT_ID/regions/se-sto/publicIPs/IP_NAME`. When the Service is deleted, the referenced public IP is not deleted. See the [static IP example](examples/03-static-ip.yaml) for a complete manifest. |
| `ccm.evroc.com/proxy-protocol` | `false` | Send the PROXY protocol header so backends can recover the client address. **Only enable this if the backend parses it** — otherwise it is read as application data and every connection breaks |
| `ccm.evroc.com/health-check-port` | each traffic port | Health check the node port of this Service port (by name or number) instead of each traffic port. The port is also exposed as a listener, like every Service port. Not allowed with `externalTrafficPolicy: Local` |
| `ccm.evroc.com/health-check-path` | none | Health check with an HTTP GET of this path instead of a TCP connect, so a backend that accepts connections but is not ready is drained. Not allowed with `externalTrafficPolicy: Local` |
| `ccm.evroc.com/health-check-host` | platform default | Override the HTTP health-check Host header with a lowercase hostname or IPv4 address, without a scheme or port. Requires `health-check-path`. Not allowed with `externalTrafficPolicy: Local` |
| `ccm.evroc.com/lb-type` | `external` | Only `external` is supported. `internal` is rejected, since the evroc API requires a public IP on every load balancer |

When `health-check-host` is absent or empty, the CCM uses the platform default.
For a virtual host, set it to a value such as `health.example.com`. This changes
only the HTTP Host header, not the backend IP or port. Removing it clears the override.

When all backends are unhealthy, the platform can still forward traffic to them;
successful frontend traffic alone does not prove health checks are passing.


### External traffic policy

Setting `externalTrafficPolicy: Local` on your Service is supported. For each
such Service Kubernetes allocates a dedicated `healthCheckNodePort`, and
kube-proxy on every node answers `/healthz` on that port with 200 only while the
node has a ready pod for that Service. Each Service therefore has its own port
and its own per-node verdict: a node can be healthy for one Service and drained
for another. The CCM points that Service's load balancer health check at its
port instead of the traffic port, so nodes without a pod are drained and each
connection is delivered to a pod on the receiving node with no extra hop and no
node-level SNAT.

For `Local`, CCM disables PROXY protocol on these health checks because
kube-proxy expects plain HTTP. The `ccm.evroc.com/proxy-protocol` annotation
still controls application connections. Switching back to `Cluster` restores
health-check inheritance of the application PROXY setting. This requires the
platform's `healthCheck.disableProxyProtocol` API support.

This release uses evroc Go SDK `v0.10.1`.

Keep in mind:

- Load is balanced across nodes, not pods. A node with three pods gets the same
  share as a node with one. Use a topology spread constraint, or stay on
  `Cluster` when replicas are few relative to nodes.
- A node that loses its last pod keeps receiving connections until its health
  check fails. Failover speed is set by the platform's health check interval.
- The health check node port must be served on every node. kube-proxy does
  this; a CNI that replaces kube-proxy must serve it too.

Unlike on pass-through load balancers, `Local` on evroc does **not** preserve
the client source address: the evroc load balancer is a proxy, so backends see
its address. Enable `ccm.evroc.com/proxy-protocol` if the backend needs the
client address. See the [local traffic policy example](examples/05-local-traffic-policy.yaml).

### Unsupported service fields

These are rejected with an explanatory event rather than approximated, so a
Service never appears healthy while behaving differently from what it asked for:

| Field | Why |
|-------|-----|
| non-TCP `ports[].protocol` | evroc load balancer listeners currently support TCP only |
| `allocateLoadBalancerNodePorts: false` | evroc load balancers forward to node ports |
| `sessionAffinity: ClientIP` | evroc backend services have no session affinity setting |
| Any `ipFamilies` containing `IPv6` | evroc load balancer frontends currently support IPv4 only; partially implementing a requested dual-stack load balancer would be misleading |

## Security considerations

- **Credentials.** Use a service account bound to `kubernetesCCMAgent`. The
  config also accepts `auth.refresh_token`, which authenticates as the user who
  issued it with all of that user's permissions; it is meant for development
  only and should never be deployed to a cluster.
- **Who can create load balancers.** Any user allowed to create a
  `type: LoadBalancer` service consumes a public IP and a load balancer in the
  evroc project. Cap this per namespace with a `ResourceQuota` on
  `services.loadbalancers`.
- **The `ccm.evroc.com/public-ip-ref` annotation.** The CCM only checks that
  the referenced IP exists, so anyone who can set it can attach any public IP
  in the project, including one reserved for another service. In a
  multi-tenant cluster, restrict the annotation with an admission policy.
- **Metrics.** The metrics and health endpoint is off by default. The CCM
  runs on the host network, so enabling it with `ccm.metrics.enabled=true`
  exposes the authenticated endpoint on port `ccm.metrics.port` of the node.
- **Node ports.** Open the node-port range only to the VPC or backend subnet
  CIDRs, as described under [enabling load balancer access](#enabling-load-balancer-access).

## License

Apache 2.0 — see [LICENSE](LICENSE).

## Support

This repository is a public release mirror. Development happens internally.

For issues and questions:
- **Issues**: Raise issues through [evroc support channels](https://docs.evroc.com/support.html).
- **Documentation**: See the [evroc documentation](https://docs.evroc.com) for platform guides.

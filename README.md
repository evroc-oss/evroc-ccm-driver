<div align="center">
  <img src="docs/images/evroc-logo.png" alt="evroc" width="300"/>
</div>

# evroc Cloud Controller Manager

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![golangci-lint](https://img.shields.io/github/actions/workflow/status/evroc-oss/evroc-ccm-driver/ci.yml?branch=main&label=golangci-lint&logo=go)](https://github.com/evroc-oss/evroc-ccm-driver/actions/workflows/ci.yml)

Kubernetes [Cloud Controller Manager](https://kubernetes.io/docs/concepts/architecture/cloud-controller/) (CCM) for the [evroc](https://evroc.com) cloud platform.

## Table of Contents

- [Features](#features)
- [Load balancers](#load-balancers)
  - [Annotations](#annotations)
  - [Network prerequisites](#network-prerequisites)
  - [Unsupported service fields](#unsupported-service-fields)
- [Requirements](#requirements)
- [Configuration](#configuration)
  - [Creating credentials](#creating-credentials)
  - [Credential isolation from the CSI driver](#credential-isolation-from-the-csi-driver)
- [Security considerations](#security-considerations)
- [Deployment](#deployment)
  - [Helm (recommended)](#helm-recommended)
  - [Static manifests](#static-manifests)
  - [Important](#important)
- [Development](#development)
  - [Prerequisites](#prerequisites)
- [License](#license)
- [Support](#support)

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

The CCM automatically labels Kubernetes nodes with:
- `topology.kubernetes.io/region` — evroc region (e.g. `se-sto`)
- `topology.kubernetes.io/zone` — evroc availability zone (e.g. `a`)
- `node.kubernetes.io/instance-type` — compute profile (e.g. `c1a.m`)

## Load balancers

A `type: LoadBalancer` service is backed by an evroc load balancer, a public IP,
and a backend pool holding the cluster's VMs. Traffic reaches a node on the
service's node port and is forwarded from there by kube-proxy.

### Annotations

| Annotation | Default | Meaning |
|------------|---------|---------|
| `ccm.evroc.com/public-ip-ref` | none | Use a pre-existing public IP instead of allocating one. An IP given here is not deleted with the service |
| `ccm.evroc.com/lb-type` | `external` | Only `external` is supported. `internal` is rejected, since the evroc API requires a public IP on every load balancer |
| `ccm.evroc.com/proxy-protocol` | `false` | Send the PROXY protocol header so backends can recover the client address. **Only enable this if the backend parses it** — otherwise it is read as application data and every connection breaks |

### Network prerequisites

A `type: LoadBalancer` service is also allocated a node port, and the evroc
load balancer forwards to every backend VM on that port:

```
client -> load balancer :80 -> VM :32061 (node port) -> kube-proxy -> pod :8080
```

Kubernetes picks the node port from 30000-32767 when the service is created, so
it cannot be known in advance and the whole range must be reachable. Without it
the load balancer is created and reports healthy while no traffic arrives.

```console
# Node ports, so the load balancer can reach service backends.
$ evroc networking securitygroup addrule <security-group> \
    --name nodeports --direction Ingress --protocol TCP \
    --port 30000 --end-port 32767 \
    --remote-ip-or-cidr <vpc-cidr>
```

Use the VPC CIDR by default. For a tighter rule, add one rule for each backend
subnet CIDR used by the load balancer. Avoid `0.0.0.0/0`, which exposes the
node-port range more broadly than the CCM requires.

No other security-group rules are required specifically by the CCM. Cluster
networking and node-access rules depend on the Kubernetes distribution and CNI
and should be configured as part of cluster provisioning.

### Unsupported service fields

These are rejected with an explanatory event rather than approximated, so a
service never appears healthy while behaving differently from what it asked for:

| Field | Why |
|-------|-----|
| non-TCP `ports[].protocol` | evroc load balancer listeners currently support TCP only |
| `allocateLoadBalancerNodePorts: false` | evroc load balancers forward to node ports |
| `externalTrafficPolicy: Local` | `Local` requires preserving the client source address, which the evroc load balancer does not support |
| `sessionAffinity: ClientIP` | evroc backend services have no session affinity setting |
| Any `ipFamilies` containing `IPv6` | evroc load balancer frontends currently support IPv4 only; partially implementing a requested dual-stack load balancer would be misleading |

## Requirements

- Go 1.25+
- Kubernetes 1.29+
- evroc credentials with compute, loadbalancer, and networking access
- **Kubernetes node names must equal the evroc VM names.** The CCM looks VMs
  up by node name for node initialization and for load balancer backends.
- **Kubelets must run with `--cloud-provider=external`.** Otherwise the
  distribution's own cloud controller initializes nodes first and the CCM is
  never consulted. On k3s and RKE2, set `disable-cloud-controller: true` and
  `kubelet-arg: ["cloud-provider=external"]`

## Configuration

The CCM reads configuration from a YAML file mounted as a Kubernetes Secret.
The `auth`, `api`, and `context` sections use the same evroc SDK schema as the
[evroc CSI driver](https://github.com/evroc-oss/evroc-csi-driver). This is schema
compatibility only: each file contains one evroc identity. For least privilege,
use separate identities, config files, and Kubernetes Secrets for the CCM and
CSI driver. A shared identity also works when it is intentionally granted both
roles, but both components then have the combined permissions.

```yaml
auth:
  # The bare service account name — the SDK derives the OAuth2 client ID as
  # <service_account_id>_<project>, so do not append the project here.
  service_account_id: "ccm-agent"
  # The credential's private key from `evroc iam serviceaccount credential
  # create`, as a base64-encoded JWK or a path to a file containing it.
  service_account_secret: "changeme"

context:
  organization: "my-org"
  project: "my-project"
  region: "se-sto"
```

See [deploy/evroc-config-example.yaml](deploy/evroc-config-example.yaml) for the full example, including the optional CCM-specific `loadbalancers` section for backend network and IP stack configuration.

### Creating credentials

The CCM needs access to three APIs:

| API | Used for |
|-----|----------|
| compute | node metadata, and the VMs registered as load balancer backends |
| loadbalancer | load balancers, backend pools, backend services, L4 routes |
| networking | the public IP fronting each load balancer, and the VPC and subnets when `loadbalancers.backendNetwork` is set |

`kubernetesCCMAgent` grants the permissions the CCM needs across all three APIs.

```console
$ evroc iam serviceaccount create ccm-agent
$ evroc iam serviceaccount credential create ccm-agent-key --service-account ccm-agent

$ evroc iam rolebinding assign \
    --principal "/iam/projects/<project-id>/serviceAccounts/ccm-agent" \
    --role /iam/roles/kubernetesCCMAgent
```

The credential's private key is printed only once, at creation.

### Credential isolation from the CSI driver

For least privilege, the CCM and CSI driver authenticate as separate evroc
service accounts. Bind the CCM account to `kubernetesCCMAgent` and the CSI
account to `kubernetesCSIAgent`, then give each component its own config file and
Kubernetes Secret. A shared evroc account can instead be bound to both roles and
used by both components, with the tradeoff that each receives the union of
their permissions. Kubernetes ServiceAccounts control Kubernetes RBAC and are
distinct from the evroc identities in the `auth` sections.

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
  CIDRs, as described under [network prerequisites](#network-prerequisites).

## Deployment

### Helm (recommended)

1. Create the CCM config Secret. For least privilege, use a different Secret and
   evroc service account for the CSI driver:

```bash
kubectl create namespace evroc-system

# Copy and edit the example config:
cp deploy/evroc-config-example.yaml my-config.yaml
# Edit my-config.yaml with your credentials...

kubectl create secret generic evroc-ccm-config \
  --namespace evroc-system \
  --from-file=config.yaml=my-config.yaml
```

2. Install the chart from the OCI registry:

```bash
helm install evroc-ccm oci://ghcr.io/evroc-oss/charts/evroc-ccm \
  --version 0.2.0 \
  --namespace evroc-system \
  --set evroc.existingConfigSecret=evroc-ccm-config
```

Or from a local checkout:

```bash
helm install evroc-ccm ./chart \
  --namespace evroc-system \
  --set evroc.existingConfigSecret=evroc-ccm-config
```

You may want to verify the supply-chain security of the artifacts before
deploying. If so, follow the [supply-chain security guide](SUPPLY_CHAIN_SECURITY.md).

### Static manifests

The checked-in manifest is generated from the Helm chart; see
[`deploy/README.md`](deploy/README.md) for the exact command and drift check.

```bash
kubectl create namespace evroc-system

kubectl create secret generic evroc-ccm-config \
  --namespace evroc-system \
  --from-file=config.yaml=<your-config-file>

kubectl apply -f deploy/evroc-ccm.yaml
```

### Important

Nodes must be started with `--cloud-provider=external` for the CCM to initialize them.

## Development

```bash
# Build
make build

# Unit tests
make test

# End-to-end tests against a real cluster — see test/e2e/README.md
KUBECONFIG=/absolute/path/to/kubeconfig make test-e2e

# Lint
make lint

# Run all checks
make verify

# Docker
make docker
```

### Prerequisites

- Go 1.25+
- Docker
- golangci-lint v2.8.0+

## License

Apache 2.0 — see [LICENSE](LICENSE).

## Support

This repository is a public release mirror. Development happens internally.

For issues and questions:
- **Issues**: Raise issues through [evroc support channels](https://docs.evroc.com/support.html).
- **Documentation**: See the [evroc documentation](https://docs.evroc.com) for platform guides.

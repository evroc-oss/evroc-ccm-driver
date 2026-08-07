<div align="center">

# evroc Cloud Controller Manager

Kubernetes Cloud Controller Manager for the evroc cloud platform

[![Tests](https://github.com/evroc-oss/evroc-ccm-driver/actions/workflows/ci.yml/badge.svg)](https://github.com/evroc-oss/evroc-ccm-driver/actions/workflows/ci.yml)
[![Release](https://github.com/evroc-oss/evroc-ccm-driver/actions/workflows/release.yml/badge.svg)](https://github.com/evroc-oss/evroc-ccm-driver/actions/workflows/release.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/evroc-oss/evroc-ccm-driver)](https://goreportcard.com/report/github.com/evroc-oss/evroc-ccm-driver)
[![Go Version](https://img.shields.io/github/go-mod-go-version/evroc-oss/evroc-ccm-driver)](https://github.com/evroc-oss/evroc-ccm-driver/blob/main/go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Security](https://img.shields.io/badge/Security-Signed%20%26%20Attested-green.svg)](docs/VERIFICATION.md)
[![SLSA](https://img.shields.io/badge/SLSA-Provenance-blue.svg)](https://slsa.dev/)
[![GitHub release](https://img.shields.io/github/release/evroc-oss/evroc-ccm-driver.svg)](https://github.com/evroc-oss/evroc-ccm-driver/releases/latest)

</div>

---

Kubernetes [Cloud Controller Manager](https://kubernetes.io/docs/concepts/architecture/cloud-controller/) (CCM) for the [evroc](https://evroc.com) cloud platform.

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
| `evroc.com/public-ip-ref` | none | Use a pre-existing public IP instead of allocating one. An IP given here is not deleted with the service |
| `evroc.com/lb-type` | `external` | Only `external` is supported. `internal` is rejected, since the evroc API requires a public IP on every load balancer |
| `evroc.com/proxy-protocol` | `false` | Send the PROXY protocol header so backends can recover the client address. **Only enable this if the backend parses it** — otherwise it is read as application data and every connection breaks |

### Network prerequisites

A `type: LoadBalancer` service is also allocated a node port, and the evroc
load balancer forwards to every backend VM on that port:

```
client -> load balancer :80 -> VM :32061 (node port) -> kube-proxy -> pod :8080
```

Kubernetes picks the node port from 30000-32767 when the service is created, so
it cannot be known in advance and the whole range must be reachable. Without it
the load balancer is created and reports healthy while no traffic arrives.

A multi-zone cluster also needs the nodes to reach each other: the overlay
network carries pod traffic between zones, and without it cluster DNS and
anything else spanning nodes silently fails.

```console
# Node ports, so the load balancer can reach service backends.
$ evroc networking securitygroup addrule <security-group> \
    --name nodeports --direction Ingress --protocol TCP \
    --port 30000 --end-port 32767 --remote-ip-or-cidr 0.0.0.0/0

# Overlay network between nodes (VXLAN, as used by k3s flannel).
$ evroc networking securitygroup addrule <security-group> \
    --name vxlan --direction Ingress --protocol UDP \
    --port 8472 --remote-ip-or-cidr 10.0.0.0/8

# Kubelet, for logs, exec, and metrics.
$ evroc networking securitygroup addrule <security-group> \
    --name kubelet --direction Ingress --protocol TCP \
    --port 10250 --remote-ip-or-cidr 10.0.0.0/8

# Pod network.
$ evroc networking securitygroup addrule <security-group> \
    --name podcidr --direction Ingress --protocol All \
    --port 0 --remote-ip-or-cidr 10.42.0.0/16
```

Nothing else is required of the platform: kube-proxy programs the node port
itself, and no route or CNI integration is needed from the cloud provider.

### Unsupported service fields

These are rejected with an explanatory event rather than approximated, so a
service never appears healthy while behaving differently from what it asked for:

| Field | Why |
|-------|-----|
| `externalTrafficPolicy: Local` | Backends are whole VMs, not pods, so traffic is always forwarded by kube-proxy and the client source IP is lost. Use `Cluster` with `evroc.com/proxy-protocol` |
| `sessionAffinity: ClientIP` | evroc backend services have no session affinity setting |
| `ipFamilies: [IPv6]` | The load balancer frontend is always IPv4 |

## Requirements

- Go 1.25+
- Kubernetes 1.29+
- evroc credentials with compute, loadbalancer, and networking access
- **Kubelets must run with `--cloud-provider=external`.** Otherwise the
  distribution's own cloud controller initializes nodes first and the CCM is
  never consulted. On k3s and RKE2, set `disable-cloud-controller: true` and
  `kubelet-arg: ["cloud-provider=external"]`

## Configuration

The CCM reads configuration from a YAML file mounted as a Kubernetes secret. The `auth`, `api`, and `context` sections use the evroc SDK's own configuration schema, which is also what the [evroc CSI driver](https://github.com/evroc-oss/evroc-csi-driver) reads, so a config written for one component is understood by the other.

```yaml
auth:
  # The bare service account name — the SDK derives the OAuth2 client ID as
  # <service_account_id>_<project>, so do not append the project here.
  service_account_id: "ccm-agent"
  service_account_secret: "changeme"

context:
  organization: "my-org"
  project: "my-project"
  region: "se-sto"
```

See [deploy/evroc-config-example.yaml](deploy/evroc-config-example.yaml) for the full example, including the optional CCM-specific `network` section for VPC, subnet, and IP stack configuration.

### Creating credentials

The CCM needs access to three APIs:

| API | Used for |
|-----|----------|
| compute | node metadata, and the VMs registered as load balancer backends |
| loadbalancer | load balancers, backend pools, backend services, L4 routes |
| networking | the public IP fronting each load balancer, and the VPC and subnets when `network.vpcRef` is set |

`kubernetesCCMAgent` covers the first two but not networking, so the service
account needs **both** it and `networkingOperator`. Without the second, public
IP allocation fails with a 404 and no load balancer can be created at all,
because the evroc API requires a `publicIPRef` on every load balancer.

```console
$ evroc iam serviceaccount create ccm-agent
$ evroc iam serviceaccount credential create ccm-agent-key --service-account ccm-agent

$ evroc iam rolebinding assign \
    --principal "/iam/projects/<project-id>/serviceAccounts/ccm-agent" \
    --role /iam/roles/kubernetesCCMAgent
$ evroc iam rolebinding assign \
    --principal "/iam/projects/<project-id>/serviceAccounts/ccm-agent" \
    --role /iam/roles/networkingOperator
```

The credential's private key is printed only once, at creation.

### Credential isolation from the CSI driver

The CCM and CSI driver each authenticate as their own evroc service account, bound to `kubernetesCCMAgent` and `kubernetesCSIAgent` respectively, so each holds only the permissions of its own role. They also use separate Kubernetes ServiceAccounts with different RBAC roles, so isolation holds at both the evroc IAM and Kubernetes RBAC levels.

## Deployment

### Helm (recommended)

1. Create the shared config secret:

```bash
kubectl create namespace evroc-system

# Copy and edit the example config:
cp deploy/evroc-config-example.yaml my-config.yaml
# Edit my-config.yaml with your credentials...

kubectl create secret generic evroc-config \
  --namespace evroc-system \
  --from-file=config.yaml=my-config.yaml
```

2. Install the chart from the OCI registry:

```bash
helm install evroc-ccm oci://ghcr.io/evroc-oss/charts/evroc-ccm \
  --namespace evroc-system \
  --set evroc.existingConfigSecret=evroc-config
```

> **The `evroc-ccm` chart and `evroc-ccm-driver` image are currently private on
> ghcr.io.** An anonymous pull fails with
> `failed to authorize: ... 401 Unauthorized`, so an in-cluster install — a
> Helm Job, a Cluster API ClusterResourceSet, an Argo sync — cannot fetch them
> without credentials. The CSI driver's packages are public, so the difference
> shows up as the CCM failing where the CSI driver succeeds.
>
> Until the packages are made public in the repository's package settings,
> authenticate first:
>
> ```bash
> echo "$GITHUB_TOKEN" | helm registry login ghcr.io -u <user> --password-stdin
> ```
>
> and give the workload cluster an image pull secret:
>
> ```bash
> kubectl create secret docker-registry ghcr-creds \
>   --namespace evroc-system \
>   --docker-server=ghcr.io \
>   --docker-username=<user> \
>   --docker-password="$GITHUB_TOKEN"
>
> helm install evroc-ccm oci://ghcr.io/evroc-oss/charts/evroc-ccm \
>   --namespace evroc-system \
>   --set evroc.existingConfigSecret=evroc-config \
>   --set imagePullSecrets[0].name=ghcr-creds
> ```

Or from a local checkout:

```bash
helm install evroc-ccm ./chart \
  --namespace evroc-system \
  --set evroc.existingConfigSecret=evroc-config
```

### Static manifests

```bash
kubectl create namespace evroc-system

kubectl create secret generic evroc-config \
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

## Security

### Image and Chart Signing

All Docker images and Helm charts are signed with [Cosign](https://github.com/sigstore/cosign) using keyless signing via GitHub OIDC. This ensures the authenticity and integrity of released artifacts.

#### Verify Docker Image Signature

Install Cosign and verify the image signature:

```bash
# Install Cosign (if not already installed)
# See: https://docs.sigstore.dev/cosign/installation/

# Verify image signature (keyless)
cosign verify \
  --certificate-identity-regexp="^https://github.com/evroc-oss/evroc-ccm-driver/" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/evroc-oss/evroc-ccm-driver:latest
```

#### Verify Helm Chart Signature

Helm chart `.tgz` packages are signed as blobs. Download the chart and its
Sigstore bundle from the [GitHub release](https://github.com/evroc-oss/evroc-ccm-driver/releases), then verify:

```bash
# Verify Helm chart signature (keyless)
cosign verify-blob evroc-ccm-<VERSION>.tgz \
  --bundle evroc-ccm-<VERSION>.tgz.bundle \
  --certificate-identity-regexp="^https://github.com/evroc-oss/evroc-ccm-driver/" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com"
```

#### Verify SBOM Attestation

```bash
# Verify and view SBOM attestation
cosign verify-attestation \
  --type spdxjson \
  --certificate-identity-regexp="^https://github.com/evroc-oss/evroc-ccm-driver/" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/evroc-oss/evroc-ccm-driver:latest
```

**Extract SBOM for analysis:**

The SBOM is attached as an attestation to the container image. To extract it for vulnerability scanning or license compliance analysis:

```bash
# Extract SBOM to a file
cosign verify-attestation \
  --type spdxjson \
  --certificate-identity-regexp="^https://github.com/evroc-oss/evroc-ccm-driver/" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/evroc-oss/evroc-ccm-driver:latest \
  | jq -r '.payload' | base64 -d | jq -r '.predicate' > sbom.spdx.json
```

You can then analyze the SBOM with vulnerability scanners like [Grype](https://github.com/anchore/grype), [Trivy](https://github.com/aquasecurity/trivy), or [Bomber](https://github.com/devops-kung-fu/bomber):

```bash
# Example: Scan for vulnerabilities using Grype
grype sbom:./sbom.spdx.json
```

#### Verify SLSA Provenance

```bash
# Verify and view SLSA provenance attestation
cosign verify-attestation \
  --type slsaprovenance \
  --certificate-identity-regexp="^https://github.com/evroc-oss/evroc-ccm-driver/" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/evroc-oss/evroc-ccm-driver:latest \
  | jq -r '.payload' | base64 -d | jq
```

This shows the build provenance including the source repository, commit SHA, builder identity, and build timestamps.

### Supply Chain Security

- **Signed Images**: All images and charts are cryptographically signed with Cosign
- **SBOM**: Software Bill of Materials (SPDX format) attached to each release
- **SLSA Provenance**: Build provenance attestations for supply chain transparency
- **Pinned Actions**: GitHub Actions are pinned to commit SHAs to prevent supply chain attacks

## License

Apache 2.0 — see [LICENSE](LICENSE).

## Support

This repository is a public release mirror. Development happens internally.

For issues and questions:
- **Issues**: Raise issues through [evroc support channels](https://docs.evroc.com/support.html).
- **Documentation**: See the [evroc documentation](https://docs.evroc.com) for platform guides.

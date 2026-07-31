# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Load balancer placement in a configured VPC, attaching one subnet per zone the
  backends occupy (`network.vpcRef`, `network.subnetRefs`)
- IPv6 backend routing on `ipv6-only` clusters via `network.stackType`; the load
  balancer frontend is always IPv4
- Opt-in PROXY protocol via the `evroc.com/proxy-protocol` annotation, so
  backends can recover the client address. Off by default, because a backend
  that cannot parse the header reads it as application data
- TCP health checks on backend services
- `evroc.com/lb-type` annotation. Only `external` is supported; `internal` is
  rejected with an explanatory error, since the evroc API requires a public IP
  on every load balancer
- End-to-end tests covering node initialization and load balancer lifecycle
  against a real cluster (`test/e2e`)

### Changed
- **BREAKING:** authentication now uses a service account over jwt-bearer.
  `auth.username`, `auth.password`, and `auth.clientID` are removed
- **BREAKING:** the configuration schema is now the evroc SDK's own
  (`auth`/`api`/`context`), which the CSI driver also reads. `evroc.*` moves to
  `context.*`, `evroc.restURL` to `api.base_url`, and `infrastructure.region` to
  `context.region`
- Services requesting behaviour the evroc load balancer cannot provide are
  rejected rather than silently given different behaviour: `externalTrafficPolicy:
  Local`, `sessionAffinity: ClientIP`, and IPv6-only services
- Load balancer teardown deletes the load balancer before its sub-resources and
  reports failures instead of discarding them, so one wedged resource no longer
  strands the rest as billable orphans
- Updated the evroc SDK to v0.7.3
- Integrate with evroc LoadBalancer API (loadbalancer/v1alpha1)
  - Decomposed resource model: LoadBalancer, BackendPool, BackendService, L4Route
  - Automatic PublicIP allocation with `evroc.com/public-ip-ref` annotation override
  - Full cascading delete of all child resources
  - Backend reconciliation via VM refs (node scaling support)

### Fixed
- Provider IDs now use the immutable VM UUID (`evroc://<vm-uuid>`), matching
  cluster-api-provider-evroc releases instead of using a CCM-specific
  project/name format
- The instance type label is now the compute profile's name rather than its
  fully-qualified ref. Label values may not contain slashes, so the API server
  rejected the entire node update and nodes stayed uninitialized and
  unschedulable
- Public IP requests are built with the SDK builder. The API version was
  hardcoded to `networking/v1beta1` and went stale when the SDK moved to
  `v1beta2`, so every public IP was rejected for a version mismatch
- The chart's control-plane placement no longer uses a `nodeSelector` requiring
  an exact label value. kubeadm sets `node-role.kubernetes.io/control-plane` to
  `""` while k3s and RKE2 set it to `"true"`, so the pod could not schedule on
  those distributions
- The chart grants the RBAC the controller manager needs to start: a RoleBinding
  to `extension-apiserver-authentication-reader`, plus `serviceaccounts` and
  `serviceaccounts/token`
- Controller names in the chart now match `k8s.io/cloud-provider/names`;
  `service-lb` was silently invalid and aborted startup
- IPv6 node addresses are reported, so nodes on `ipv6-only` clusters are no
  longer left with no internal address

### Known issues
- The `kubernetesCCMAgent` role grants compute and loadbalancer access but not
  networking, so on its own it cannot allocate the public IP every load balancer
  requires and load balancer creation fails with a 404. Bind the service account
  to both `kubernetesCCMAgent` and `networkingOperator`

## [0.1.0] - 2026-04-28

Initial release of the evroc Cloud Controller Manager.

### Added
- Kubernetes Cloud Controller Manager for evroc platform
- InstancesV2 implementation:
  - Node topology labeling (region, zone, instance type)
  - Provider ID assignment (`evroc://<project>/<vm-id>`)
  - Node address synchronization (internal/external IP)
  - Instance lifecycle management (existence, shutdown detection)
- LoadBalancer implementation:
  - Automatic LoadBalancer creation for `type: LoadBalancer` Services
  - Multi-port service support
  - Node backend reconciliation on cluster scaling
  - PublicIP lifecycle management
  - Static IP support via `evroc.com/public-ip-ref` annotation
- Helm chart for deployment
- Static Kubernetes manifests
- Cosign-signed container images with SBOM and SLSA provenance
- GitHub Actions CI/CD (lint, test, build, release)
- GitLab CI pipeline (lint, test, build, SAST, secret detection)

### Dependencies
- evroc Go SDK v0.2.9
- Kubernetes v1.29+ (cloud-provider v0.35.2)
- Go 1.25.0

[Unreleased]: https://github.com/evroc-oss/evroc-ccm-driver/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/evroc-oss/evroc-ccm-driver/releases/tag/v0.1.0

# Advanced configuration

The [Quickstart](../README.md#quickstart) covers the minimal setup. This doc
describes optional settings for private cloud instances, multi-cluster
ownership, load balancer networking, and credential isolation.

## API and token endpoints

The defaults target the public evroc cloud. For a private or staging instance,
override both `api.base_url` and `auth.token_url` — the two endpoints belong to
the same environment and must match:

```yaml
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "changeme"
  token_url: "https://authn.<domain>/realms/evroc-customer/protocol/openid-connect/token"

api:
  base_url: "https://api.<domain>"
```

If one endpoint is `https://api.<domain>`, the other is
`https://authn.<domain>/realms/evroc-customer/protocol/openid-connect/token`.

`service_account_id` is the bare name — the SDK derives the OAuth client ID as
`<name>_<project>`, so do not append the project. `service_account_secret` is the
base64 JWK printed by `evroc iam serviceaccount credential create`.

The config also accepts `auth.refresh_token`, which authenticates as the user
who issued it with all of that user's permissions. It is meant for development
only and should never be deployed to a cluster.

## Resource ownership

For clusters sharing an evroc project, set a unique, stable identifier in each
CCM configuration:

```yaml
ccm:
  identifier: cluster-a
```

CCM labels its load balancers, pools, backend services, routes and allocated
IPs with `managed-by: cluster-a`. It filters discovery by this label and
refuses to modify resources with missing or different ownership labels.
Referenced external IPs remain unmodified. This works independently of CAPI.

The identifier defaults to the project name, matching CSI. Set distinct
identifiers for clusters sharing a project, and keep them unchanged while
resources exist. Unlabeled resources are never adopted automatically.

## Load balancer networking

```yaml
loadbalancers:
  backendNetwork:
    vpcID: "vpc-prod"
    subnets:
      - zone: "a"
        subnetID: "subnet-a"
      - zone: "b"
        subnetID: "subnet-b"
  stackType: "ipv4-only"
```

When `backendNetwork` is set, load balancers are deployed only to the listed
zones. Omit it to let the platform apply its default backend network.

`stackType` is one of `ipv4-only` (default), `dual-stack`, or `ipv6-only`.
Load balancer frontends are always IPv4; `ipv6-only` makes the load balancer
reach backends over IPv6.

## PROXY protocol and Local traffic policy

With `ccm.evroc.com/proxy-protocol: "true"` and
`externalTrafficPolicy: Local`, CCM sends application traffic with PROXY
headers but probes kube-proxy's `healthCheckNodePort` at `/healthz` without
those headers. This allows kube-proxy to report whether the node has local
endpoints. No additional annotation is needed.

Switching back to `externalTrafficPolicy: Cluster` restores health-check
inheritance from the backend traffic PROXY setting and clears the Local
probe overrides.

## API permissions

The CCM needs access to three APIs:

| API | Used for |
|-----|----------|
| compute | node metadata, and the VMs registered as load balancer backends |
| loadbalancer | load balancers, backend pools, backend services, L4 routes |
| networking | the public IP fronting each load balancer, and the VPC and subnets when `loadbalancers.backendNetwork` is set |

`kubernetesCCMAgent` grants the permissions the CCM needs across all three
APIs.

## Credential isolation from the CSI driver

For least privilege, the CCM and CSI driver authenticate as separate evroc
service accounts. Bind the CCM account to `kubernetesCCMAgent` and the CSI
account to `kubernetesCSIAgent`, then give each component its own config file
and Kubernetes Secret. A shared evroc account can instead be bound to both
roles and used by both components, with the tradeoff that each receives the
union of their permissions. Kubernetes ServiceAccounts control Kubernetes
RBAC and are distinct from the evroc identities in the `auth` sections.

# Deploying the CCM

The evroc CCM ships as both a Helm chart (recommended) and a static manifest
rendered from that chart.

## Helm (recommended)

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
  --version 0.3.2 \
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
deploying. If so, follow the [supply-chain security guide](../SUPPLY_CHAIN_SECURITY.md).

## Static manifests

The checked-in `deploy/evroc-ccm.yaml` is generated from the Helm chart; see
[`deploy/README.md`](../deploy/README.md) for the exact command and drift check.

```bash
kubectl create namespace evroc-system

kubectl create secret generic evroc-ccm-config \
  --namespace evroc-system \
  --from-file=config.yaml=<your-config-file>

kubectl apply -f deploy/evroc-ccm.yaml
```

## Important

Nodes must be started with `--cloud-provider=external` for the CCM to initialize them.

## Cleanup and cluster teardown

Deleting a LoadBalancer Service cleans up its managed cloud resources, including
partially created stacks. The `service.kubernetes.io/load-balancer-cleanup`
finalizer remains until cleanup completes; externally supplied public IPs are retained.

Before uninstalling CCM or destroying the cluster, delete LoadBalancer Services
and wait for deletion to finish. Otherwise, billable resources may remain.

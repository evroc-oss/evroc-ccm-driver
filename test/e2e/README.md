# CCM end-to-end tests

These tests exercise the two features the CCM provides against a real cluster
and a real evroc project: node initialization (provider ID, topology labels,
addresses) and `type: LoadBalancer` services.

## Requirements

- A Kubernetes cluster whose nodes are evroc VMs.
- **The kubelets must run with `--cloud-provider=external`.** Otherwise the
  distribution's built-in cloud controller initializes nodes first, the CCM is
  never consulted, and the node tests would assert against labels it did not
  write. On k3s, set this in `/etc/rancher/k3s/config.yaml`:

  ```yaml
  disable-cloud-controller: true
  kubelet-arg:
    - "cloud-provider=external"
  ```

  A node that was already initialized keeps its provider ID, which is
  immutable, so it must be deleted once to re-register cleanly.

- The CCM deployed into `evroc-system` with credentials for the project.

## Running

```console
$ export KUBECONFIG=/absolute/path/to/kubeconfig
$ ./test/e2e/run-e2e.sh
```

A single test:

```console
$ ./test/e2e/run-e2e.sh -run TestLoadBalancerService
```

The runner refuses to start unless the CCM is running and every node carries an
`evroc://` provider ID, so a misconfigured cluster fails loudly rather than
producing tests that pass for the wrong reason.

## Credentials

The CCM needs compute, loadbalancer, **and networking** access.
`kubernetesCCMAgent` does not grant networking, so the service account must be
bound to both it and `networkingOperator`. With only the first, the load
balancer tests fail when public IP allocation returns a 404.

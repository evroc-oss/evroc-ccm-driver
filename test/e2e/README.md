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

## Create, run and clean up

Run these commands from the repository root. Install Ansible, Docker, Helm,
kubectl, Go and the evroc CLI locally. Docker must work without sudo, and the
CLI must be authenticated with the project/profile you want to test.

### 1. Create the test cluster

```bash
ansible-playbook test/e2e/ansible/bootstrap.yaml \
  -e ssh_key="$HOME/.ssh/id_ed25519"
```

This creates a VM, installs k3s with the required external cloud-provider settings,
and builds and deploys CCM. It creates a service account with a 48-hour credential
and saves the ready cluster's kubeconfig to `~/.kube/ccm-e2e-config`.

Save the cleanup command printed before provisioning. Its `/tmp/ccm-e2e-TIMESTAMP`
directory contains this run's `resources.yaml`. `FAILED - RETRYING` messages while
waiting for the disk to become Ready are normal: bootstrap polls every five seconds
for up to roughly ten minutes. Wait for bootstrap to finish successfully before
running tests.

By default, bootstrap selects an IPv4 subnet, attaches `default-allow-ssh`,
`default-allow-egress` and `default-allow-intra-vpc`, and creates a test group allowing
TCP `6443` from `0.0.0.0/0` for your local test runner. Optional overrides:
`-e subnet=NAME`, `-e security_group=NAME[,NAME...]` (replaces all groups; must allow
SSH, API access and test traffic), or `-e ccm_config_path=PATH` for existing credentials.

### 2. Run the tests

```bash
export KUBECONFIG="$HOME/.kube/ccm-e2e-config"
./test/e2e/run-e2e.sh
```

To run a single test with the same kubeconfig:

```bash
./test/e2e/run-e2e.sh -run TestLoadBalancerService
```

The runner requires a ready CCM and `evroc://` provider IDs on every node.
Tests clean up their test workloads and Services; the cluster remains available
for debugging and repeat runs.

### 3. Clean up the cluster

Run the cleanup command saved from bootstrap, even if bootstrap or testing failed:

```bash
ansible-playbook test/e2e/ansible/cleanup.yaml \
  -e state_dir=/tmp/ccm-e2e-TIMESTAMP
```

Replace the directory with your run's actual path containing `resources.yaml`;
keep the same evroc CLI profile/project. Cleanup is explicit, not automatic.
It waits for test load balancers to be released, then deletes the VM, disk,
public IP, generated security group and service account. Shared groups are preserved.
It skips missing resources and can be rerun. If Kubernetes cleanup fails, it stops
before deleting the VM; resolve the error and rerun. Local artifacts remain, but
the deleted cluster's kubeconfig is no longer usable.

## Credentials

The CCM needs compute, loadbalancer, **and networking** access. Bind its evroc
service account to `kubernetesCCMAgent`, which grants the permissions required
across all three APIs.

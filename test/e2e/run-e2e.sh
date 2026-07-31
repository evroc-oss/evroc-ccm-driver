#!/bin/bash
set -euo pipefail

# E2E test runner for the evroc CCM.
#
# Usage:
#   KUBECONFIG=/absolute/path/to/kubeconfig ./test/e2e/run-e2e.sh [-run TestName]
#
# The cluster must already exist, and its kubelets must run with
# --cloud-provider=external. Without that the distribution's own cloud
# controller initializes nodes first and the CCM is never consulted, so the
# node tests would pass against labels the CCM did not write.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

log_info()    { echo "[INFO] $*"; }
log_error()   { echo "[ERROR] $*" >&2; }
log_success() { echo "[OK] $*"; }

check_prerequisites() {
    for cmd in kubectl go; do
        if ! command -v "$cmd" &>/dev/null; then
            log_error "$cmd not found"
            exit 1
        fi
    done

    if [[ -z "${KUBECONFIG:-}" ]]; then
        log_error "KUBECONFIG is not set. It must be an absolute path to the test cluster's kubeconfig."
        exit 1
    fi
    if [[ "${KUBECONFIG}" != /* ]]; then
        log_error "KUBECONFIG must be an absolute path, got: ${KUBECONFIG}"
        exit 1
    fi
    if [[ ! -r "${KUBECONFIG}" ]]; then
        log_error "kubeconfig is not readable: ${KUBECONFIG}"
        exit 1
    fi

    if ! kubectl get nodes &>/dev/null; then
        log_error "cannot reach the cluster with KUBECONFIG=${KUBECONFIG}"
        exit 1
    fi
}

check_ccm_running() {
    log_info "checking the CCM is deployed..."
    local ready
    ready=$(kubectl get pods -n evroc-system -l app.kubernetes.io/name=evroc-ccm \
        -o jsonpath='{.items[*].status.containerStatuses[*].ready}' 2>/dev/null || true)
    if [[ "${ready}" != *"true"* ]]; then
        log_error "no ready evroc-ccm pod in namespace evroc-system"
        kubectl get pods -n evroc-system 2>&1 | sed 's/^/    /' || true
        exit 1
    fi
    log_success "CCM is running"
}

check_external_cloud_provider() {
    # A node whose provider ID is not evroc:// was initialized by something
    # else, which would make the node tests meaningless.
    log_info "checking nodes were initialized by the evroc CCM..."
    local ids
    ids=$(kubectl get nodes -o jsonpath='{.items[*].spec.providerID}')
    for id in ${ids}; do
        if [[ "${id}" != evroc://* ]]; then
            log_error "node has providerID '${id}', not evroc://..."
            log_error "the cluster's kubelets must run with --cloud-provider=external"
            exit 1
        fi
    done
    log_success "nodes carry evroc provider IDs"
}

main() {
    check_prerequisites
    check_ccm_running
    check_external_cloud_provider

    log_info "running e2e tests..."
    cd "${PROJECT_ROOT}"
    # Load balancer provisioning is slow, and the tests poll rather than spin.
    go test ./test/e2e/basic/ -v -count=1 -timeout 30m "$@"
    log_success "e2e tests passed"
}

main "$@"

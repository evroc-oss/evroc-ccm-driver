# Artifact Verification Guide

This guide explains how to verify the authenticity and integrity of evroc Cloud Controller Manager releases.

## Overview

All releases are cryptographically signed and attested:

1. **Cosign Keyless Signing** - Signs container images using GitHub OIDC (no keys to manage)
2. **SBOM** - Software Bill of Materials for dependency transparency
3. **SLSA Provenance** - Build integrity attestation

## Prerequisites

### Install Cosign

```bash
# macOS
brew install cosign

# Linux
wget "https://github.com/sigstore/cosign/releases/latest/download/cosign-linux-amd64"
sudo mv cosign-linux-amd64 /usr/local/bin/cosign
sudo chmod +x /usr/local/bin/cosign

# Verify installation
cosign version
```

## Verification Methods

### Method 1: Verify Container Image Signature (Recommended)

Cosign uses **keyless signing** via GitHub OIDC.

```bash
VERSION="v0.1.0"  # Replace with desired version
IMAGE="ghcr.io/evroc-oss/evroc-ccm-driver:${VERSION}"

cosign verify "${IMAGE}" \
  --certificate-identity-regexp="^https://github.com/evroc-oss/evroc-ccm-driver/" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com"
```

**Expected output:**
```
Verification for ghcr.io/evroc-oss/evroc-ccm-driver:v0.1.0 --
The following checks were performed on each of these signatures:
  - The cosign claims were validated
  - The signatures were verified against the specified public key
```

**What this verifies:**
- The image was built by the official GitHub Actions workflow
- The build happened in the evroc-oss/evroc-ccm-driver repository
- The image has not been tampered with since signing

---

### Method 2: Verify SBOM (Software Bill of Materials)

The SBOM provides transparency about all dependencies and components.

**Download and verify SBOM:**

```bash
VERSION="v0.1.0"
BASE_URL="https://github.com/evroc-oss/evroc-ccm-driver/releases/download/${VERSION}"

curl -sLO "${BASE_URL}/sbom.spdx.json"
curl -sLO "${BASE_URL}/sbom.spdx.json.sig"
curl -sLO "${BASE_URL}/sbom.spdx.json.pem"

cosign verify-blob \
  sbom.spdx.json \
  --signature sbom.spdx.json.sig \
  --certificate sbom.spdx.json.pem \
  --certificate-identity-regexp="^https://github.com/evroc-oss/evroc-ccm-driver/" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com"
```

**Inspect SBOM contents:**

```bash
# View as JSON
jq . sbom.spdx.json | less

# List all packages
jq -r '.packages[].name' sbom.spdx.json
```

**Regenerate the SBOM:**

The SBOM is produced by [syft](https://github.com/anchore/syft) from the built
container image, so regenerating it requires building the image first. `make sbom`
does both and writes `sbom.spdx.json`:

```bash
# Install syft (version must match the one used by the release pipeline)
curl -sSfL https://raw.githubusercontent.com/anchore/syft/main/install.sh \
  | sh -s -- -b /usr/local/bin v1.29.0

make sbom
```

To regenerate from a published image instead of a local build:

```bash
VERSION="v0.1.0"
syft "ghcr.io/evroc-oss/evroc-ccm-driver:${VERSION}" -o spdx-json > sbom.spdx.json
```

The syft version is pinned in three places that must be kept in sync: `SYFT_VERSION`
in the `Makefile`, `SYFT_VERSION` in `.gitlab-ci.yml`, and the install step in
`.github/workflows/release.yml`. GitLab CI regenerates the SBOM on every merge
request so failures surface before a GitHub release runs.

Note that a locally regenerated SBOM will not be byte-identical to the released
one: package versions are identical for the same image, but `syft` records the
image reference and build metadata, which differ between a local build and the
published artifact. Compare package sets rather than file hashes:

```bash
jq -r '.packages[] | "\(.name)@\(.versionInfo)"' sbom.spdx.json | sort > /tmp/local.txt
# ...then diff against the same extraction from the released SBOM
```

---

### Method 3: Verify SLSA Provenance

SLSA provenance proves the artifact was built by the expected workflow in a trusted environment.

**Download and verify provenance:**

```bash
VERSION="v0.1.0"
BASE_URL="https://github.com/evroc-oss/evroc-ccm-driver/releases/download/${VERSION}"

curl -sLO "${BASE_URL}/provenance.json"
curl -sLO "${BASE_URL}/provenance.json.sig"
curl -sLO "${BASE_URL}/provenance.json.pem"

cosign verify-blob \
  provenance.json \
  --signature provenance.json.sig \
  --certificate provenance.json.pem \
  --certificate-identity-regexp="^https://github.com/evroc-oss/evroc-ccm-driver/" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com"
```

**Inspect provenance:**

```bash
jq . provenance.json
```

**What provenance tells you:**
- Exact Git commit SHA that was built
- Build workflow that was used
- Build start and finish timestamps
- Build environment metadata

---

### Verify Helm Chart

```bash
VERSION="v0.1.0"
BASE_URL="https://github.com/evroc-oss/evroc-ccm-driver/releases/download/${VERSION}"

curl -sLO "${BASE_URL}/evroc-ccm-${VERSION#v}.tgz"
curl -sLO "${BASE_URL}/evroc-ccm-${VERSION#v}.tgz.prov"

helm verify "evroc-ccm-${VERSION#v}.tgz"
```

---

## Reporting Security Issues

If you discover a security vulnerability, please email: **security@evroc.com**

Do **not** open public GitHub issues for security vulnerabilities.

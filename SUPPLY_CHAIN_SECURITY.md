# Supply Chain Security

Users of the evroc Cloud Controller Manager may want to verify the security of
the delivered artifacts before deploying them. Container images and Helm chart
archives are signed with [Cosign](https://github.com/sigstore/cosign) using
keyless GitHub OIDC signing.

evroc provides:

- **Signed images and charts**: Cryptographic signatures generated with Cosign
- **SBOM**: An SPDX Software Bill of Materials attached to each image
- **SLSA provenance**: Build provenance attestations for supply-chain transparency
- **Pinned actions**: GitHub Actions pinned to commit SHAs

## Prerequisites

Install `cosign` by following the
[Sigstore installation guide](https://docs.sigstore.dev/cosign/installation/).
The SBOM and provenance extraction examples also require `jq`.

## Verify the Helm chart archive

Download the chart archive and its Sigstore bundle from the GitHub release,
verify the signature, and then install the verified archive:

```bash
VERSION=0.2.0
RELEASE_URL="https://github.com/evroc-oss/evroc-ccm-driver/releases/download/v${VERSION}"

curl -sSLO "${RELEASE_URL}/evroc-ccm-${VERSION}.tgz"
curl -sSLO "${RELEASE_URL}/evroc-ccm-${VERSION}.tgz.bundle"

cosign verify-blob "evroc-ccm-${VERSION}.tgz" \
  --bundle "evroc-ccm-${VERSION}.tgz.bundle" \
  --certificate-identity="https://github.com/evroc-oss/evroc-ccm-driver/.github/workflows/release.yml@refs/tags/v${VERSION}" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com"

helm install evroc-ccm "./evroc-ccm-${VERSION}.tgz" \
  --namespace evroc-system \
  --set evroc.existingConfigSecret=evroc-ccm-config
```

## Verify the container image signature

```bash
VERSION=v0.2.0
IMAGE="ghcr.io/evroc-oss/evroc-ccm-driver:${VERSION}"

cosign verify "${IMAGE}" \
  --certificate-identity="https://github.com/evroc-oss/evroc-ccm-driver/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com"
```

This verifies that the image was signed by the official release workflow and
has not changed since signing.

## Verify the SBOM attestation

```bash
VERSION=v0.2.0
IMAGE="ghcr.io/evroc-oss/evroc-ccm-driver:${VERSION}"

cosign verify-attestation "${IMAGE}" \
  --type spdxjson \
  --certificate-identity="https://github.com/evroc-oss/evroc-ccm-driver/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com"
```

To extract the SPDX document for analysis:

```bash
cosign verify-attestation "${IMAGE}" \
  --type spdxjson \
  --certificate-identity="https://github.com/evroc-oss/evroc-ccm-driver/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  | jq -r '.payload' | base64 -d | jq -r '.predicate' > sbom.spdx.json
```

The resulting file can be inspected directly or analyzed with tools such as
[Grype](https://github.com/anchore/grype) or
[Trivy](https://github.com/aquasecurity/trivy).

## Verify the SLSA provenance attestation

```bash
VERSION=v0.2.0
IMAGE="ghcr.io/evroc-oss/evroc-ccm-driver:${VERSION}"

cosign verify-attestation "${IMAGE}" \
  --type slsaprovenance \
  --certificate-identity="https://github.com/evroc-oss/evroc-ccm-driver/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  | jq -r '.payload' | base64 -d | jq
```

The provenance identifies the source repository, commit, workflow, builder,
and build timestamps.

## Reporting security issues

Report security vulnerabilities to **security@evroc.com**. Do not open a public
issue for a suspected vulnerability.

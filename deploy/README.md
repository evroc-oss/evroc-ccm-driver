# Static manifests

`evroc-ccm.yaml` is generated from the Helm chart in `../chart`; do not edit it
directly. It uses the chart defaults with these installation-specific inputs:

- release name: `evroc-ccm`
- namespace: `evroc-system`
- existing config Secret: `evroc-ccm-config`

Regenerate it after changing the chart:

```bash
make generate-manifest
```

`make verify-manifest` checks the committed manifest against a fresh render.
This check also runs in CI so chart changes cannot leave the static manifest
out of date.

`evroc-config-example.yaml` is an example configuration Secret and is maintained
separately.

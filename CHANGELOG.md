# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-09-18

- Always delete the CCM-managed public IP with its service.
- Fix Helm image tag fallback to `v<appVersion>`.
- Fix example resource names and images; add README security considerations.
- Disable the metrics endpoint by default (`ccm.metrics.enabled`).
- Limit token minting to the two controller accounts in `kube-system`.

## [0.1.3] - 2026-09-04

## [0.1.2] - 2026-08-07

Initial version.

[Unreleased]: https://github.com/evroc-oss/evroc-ccm-driver/compare/v0.2.0...HEAD
[0.1.2]: https://github.com/evroc-oss/evroc-ccm-driver/releases/tag/v0.1.2
[0.1.3]: https://github.com/evroc-oss/evroc-ccm-driver/releases/tag/v0.1.3
[0.2.0]: https://github.com/evroc-oss/evroc-ccm-driver/releases/tag/v0.2.0

# Development

## Prerequisites

- Go 1.25+
- Docker
- golangci-lint v2.8.0+

## Build

```bash
make build
```

## Test

```bash
# Unit tests
make test

# End-to-end tests against a real cluster — see test/e2e/README.md
KUBECONFIG=/absolute/path/to/kubeconfig make test-e2e
```

## Lint and verify

```bash
# Lint
make lint

# Run all checks (lint + tests + manifest drift)
make verify
```

## Docker

```bash
make docker
```

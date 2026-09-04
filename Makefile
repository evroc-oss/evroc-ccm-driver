VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
REGISTRY ?= ghcr.io/evroc-oss
IMAGE_NAME ?= evroc-ccm-driver
IMAGE_TAG ?= $(VERSION)
SYFT_VERSION ?= v1.29.0

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.gitCommit=$(GIT_COMMIT) \
	-X main.buildDate=$(BUILD_DATE)

.PHONY: build test test-coverage test-e2e coverage-summary mod-verify verify-tidy lint helm-lint generate-manifest verify-manifest fmt vet verify docker push sbom clean

## Build

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/evroc-ccm ./cmd/evroc-ccm

## Test

test:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...

test-coverage: test
	go tool cover -html=coverage.out -o coverage.html

coverage-summary:
	go tool cover -func=coverage.out

# End-to-end tests. These need a real cluster of evroc VMs whose kubelets run
# with --cloud-provider=external, and a CCM deployed into evroc-system.
# See test/e2e/README.md.
test-e2e:
	./test/e2e/run-e2e.sh

## Verification

mod-verify:
	go mod verify

verify-tidy:
	go mod tidy
	@git diff --exit-code -- go.mod go.sum || { \
		echo "go.mod or go.sum is not tidy. Run 'go mod tidy' and commit."; \
		exit 1; \
	}

lint:
	golangci-lint config verify
	golangci-lint run --timeout 5m

helm-lint:
	helm lint chart/ --set evroc.existingConfigSecret=test

generate-manifest:
	helm template evroc-ccm chart/ \
		--namespace evroc-system \
		--set evroc.existingConfigSecret=evroc-ccm-config \
		> deploy/evroc-ccm.yaml

verify-manifest:
	@rendered=$$(mktemp); \
	trap 'rm -f "$$rendered"' EXIT; \
	helm template evroc-ccm chart/ \
		--namespace evroc-system \
		--set evroc.existingConfigSecret=evroc-ccm-config \
		> "$$rendered"; \
	diff -u deploy/evroc-ccm.yaml "$$rendered" || { \
		echo "deploy/evroc-ccm.yaml is stale; run 'make generate-manifest'"; \
		exit 1; \
	}

fmt:
	gofmt -s -w .

vet:
	go vet ./...

verify: fmt vet lint helm-lint verify-manifest test

## Docker

docker:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG) .

push: docker
	docker push $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

## SBOM

# Regenerates sbom.spdx.json from the local image. Release CI generates and
# attests the same artifact from the published image; see SUPPLY_CHAIN_SECURITY.md.
sbom: docker
	@command -v syft >/dev/null 2>&1 || { \
		echo "syft not found; install with:"; \
		echo "  curl -sSfL https://raw.githubusercontent.com/anchore/syft/main/install.sh | sh -s -- -b /usr/local/bin $(SYFT_VERSION)"; \
		exit 1; \
	}
	syft $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG) -o spdx-json > sbom.spdx.json
	@echo "Wrote sbom.spdx.json"

## Clean

clean:
	rm -rf bin/ coverage.out coverage.html sbom.spdx.json

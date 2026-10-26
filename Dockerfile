# Go 1.27.3 fixes CVE-2026-111943 (crypto/x509 blocks the compromised eMudhra
# intermediate CAs). The fix is compiled into the binary, so the toolchain
# must be kept at a patched version.
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS builder

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

RUN apk add --no-cache git

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o evroc-ccm \
    ./cmd/evroc-ccm

# Alpine 3.21.9+ ships ca-certificates 20261020-r0, which removes the
# compromised eMudhra roots from the trust store.
FROM alpine:3.21@sha256:ee16d92f96db08d9d2d07da48555a27f3a69d8f0759021dbc601195ed0d2b5bd

RUN apk add --no-cache ca-certificates

COPY --from=builder /build/evroc-ccm /usr/local/bin/evroc-ccm

USER 65532:65532

ENTRYPOINT ["evroc-ccm"]

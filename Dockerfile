FROM golang:1.25-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS builder

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

RUN apk add --no-cache git

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Configure Git to use the provided token for private repos
# The github_token secret is provided by the build workflow
RUN --mount=type=secret,id=github_token \
    if [ -f /run/secrets/github_token ]; then \
        export GITHUB_TOKEN=$(cat /run/secrets/github_token) && \
        git config --global url."https://${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"; \
    fi && \
    GOPRIVATE=github.com/evroc-oss GOSUMDB=off go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o evroc-ccm \
    ./cmd/evroc-ccm

FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

RUN apk add --no-cache ca-certificates

COPY --from=builder /build/evroc-ccm /usr/local/bin/evroc-ccm

USER 65532:65532

ENTRYPOINT ["evroc-ccm"]

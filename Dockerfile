# Build stage
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /supergraph-operator ./cmd/main.go

# Rover stage — download pre-built binary from GitHub Releases (supports amd64 + arm64)
FROM debian:bookworm-slim AS rover-installer

ARG ROVER_VERSION=0.38.1
ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && \
    case "${TARGETARCH}" in \
      amd64) ARCH="x86_64-unknown-linux-gnu" ;; \
      arm64) ARCH="aarch64-unknown-linux-gnu" ;; \
      *) echo "Unsupported architecture: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    curl -sSL "https://github.com/apollographql/rover/releases/download/v${ROVER_VERSION}/rover-v${ROVER_VERSION}-${ARCH}.tar.gz" \
      | tar -xz && \
    mv dist/rover /usr/local/bin/rover && \
    rm -rf dist && \
    apt-get purge -y curl && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*

# Runtime stage
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tini && \
    rm -rf /var/lib/apt/lists/*

COPY --from=rover-installer /usr/local/bin/rover /usr/local/bin/rover
COPY --from=builder /supergraph-operator /usr/local/bin/supergraph-operator

USER 65532:65532

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["supergraph-operator"]

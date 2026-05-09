# Build stage
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /supergraph-operator ./cmd/main.go

# Rover stage — download pre-built binary from GitHub Releases (supports amd64 + arm64)
FROM alpine:3.20 AS rover-installer

ARG ROVER_VERSION=0.38.1
ARG TARGETARCH

RUN apk add --no-cache curl tar && \
    case "${TARGETARCH}" in \
      amd64) ARCH="x86_64-unknown-linux-musl" ;; \
      arm64) ARCH="aarch64-unknown-linux-musl" ;; \
      *) echo "Unsupported architecture: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    curl -sSL "https://github.com/apollographql/rover/releases/download/v${ROVER_VERSION}/rover-v${ROVER_VERSION}-${ARCH}.tar.gz" \
      | tar -xz --strip-components=1 -C /usr/local/bin dist/rover

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tini

COPY --from=rover-installer /usr/local/bin/rover /usr/local/bin/rover
COPY --from=builder /supergraph-operator /usr/local/bin/supergraph-operator

USER 65532:65532

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["supergraph-operator"]

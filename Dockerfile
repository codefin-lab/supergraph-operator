# Build stage
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /graph-controller ./cmd/main.go

# Rover stage — download rover CLI
FROM ghcr.io/apollographql/rover:latest AS rover

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tini curl

COPY --from=rover /usr/local/bin/rover /usr/local/bin/rover
COPY --from=builder /graph-controller /usr/local/bin/graph-controller

USER 65532:65532

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["graph-controller"]

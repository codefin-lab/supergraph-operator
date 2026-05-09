# supergraph-operator

[![CI](https://github.com/codefin-lab/supergraph-operator/actions/workflows/ci.yml/badge.svg)](https://github.com/codefin-lab/supergraph-operator/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.23-blue.svg)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache_2.0-green.svg)](./LICENSE)

A Kubernetes operator that automatically composes an [Apollo Federation](https://www.apollographql.com/docs/federation/) supergraph when `SubgraphSchema` custom resources change. No GraphOS account required — runs entirely within your cluster.

## Features

- **Auto-composition** — watches `SubgraphSchema` CRDs and runs `rover supergraph compose` on any change
- **Smart diff detection** — skips composition when schemas haven't changed (SHA-256 checksum)
- **Kubernetes Events** — emits `CompositionSucceeded` / `CompositionFailed` events on each CR
- **Prometheus metrics** — `supergraph_compositions_total`, `supergraph_composition_duration_seconds`, `supergraph_subgraphs_total`
- **Composition timeout** — configurable timeout for rover process (default: 2m)
- **Leader election** — safe to run multiple replicas for HA
- **ConfigMap history** — optionally keep N previous supergraph versions
- **Dry-run mode** — compose and validate without applying changes

## How It Works

1. Each subgraph service declares a `SubgraphSchema` CR containing its `routingUrl` and GraphQL `schema`
2. The controller watches for `SubgraphSchema` create/update/delete events
3. On any change, it computes a combined schemas checksum — if unchanged, skips composition
4. Otherwise, it generates a rover config and runs `rover supergraph compose` (with configurable timeout)
5. The composed supergraph SDL is written to a ConfigMap (default: `graph-supergraph`)
6. The router Deployment is patched with a checksum annotation (`codefin.io/supergraph-checksum`) to trigger a rolling restart
7. Kubernetes Events and Prometheus metrics are emitted for observability

## CRD: SubgraphSchema

**API Group:** `codefin.io/v1alpha1`

### Spec Fields

| Field        | Type   | Required | Description                                      |
|-------------|--------|----------|--------------------------------------------------|
| `routingUrl` | string | Yes      | URL where the subgraph can be reached by the router |
| `schema`     | string | Yes      | Full GraphQL SDL for this subgraph               |

### Status Fields

| Field                | Type   | Description                                     |
|---------------------|--------|-------------------------------------------------|
| `compositionStatus`  | string | `Success`, `Failed`, or `Pending`               |
| `lastComposed`       | date   | Timestamp of the last successful composition     |
| `supergraphChecksum` | string | SHA-256 of the last composed supergraph          |
| `message`            | string | Human-readable details about the composition result |

### Example: Single Subgraph

```yaml
apiVersion: codefin.io/v1alpha1
kind: SubgraphSchema
metadata:
  name: crm-service
  namespace: default
spec:
  routingUrl: "http://crm-service:8080/query"
  schema: |
    type Query {
      health: String!
      customers: [Customer!]!
    }
    type Customer @key(fields: "id") {
      id: ID!
      name: String!
      email: String!
    }
```

### Example: Multiple Subgraphs

```yaml
apiVersion: codefin.io/v1alpha1
kind: SubgraphSchema
metadata:
  name: identity-service
  namespace: default
spec:
  routingUrl: "http://identity-service:8080/query"
  schema: |
    type Query {
      me: User
    }
    type User @key(fields: "id") {
      id: ID!
      username: String!
      role: String!
    }
---
apiVersion: codefin.io/v1alpha1
kind: SubgraphSchema
metadata:
  name: crm-service
  namespace: default
spec:
  routingUrl: "http://crm-service:8080/query"
  schema: |
    type Query {
      customers: [Customer!]!
    }
    type Customer @key(fields: "id") {
      id: ID!
      name: String!
      owner: User!
    }
    type User @key(fields: "id") {
      id: ID!
    }
```

### Checking Status

```bash
kubectl get subgraphschemas -n default
```

```text
NAME               URL                                  STATUS    LAST COMPOSED          AGE
crm-service        http://crm-service:8080/query         Success   2026-03-31T02:00:00Z   5m
identity-service   http://identity-service:8080/query    Success   2026-03-31T02:00:00Z   5m
```

## Controller Configuration

The controller accepts the following CLI flags:

| Flag | Default | Description |
|---|---|---|
| `--namespace` | _(all namespaces)_ | Namespace to watch; empty = watch all namespaces |
| `--federation-version` | `=2.7.0` | Apollo Federation version passed to `rover compose` |
| `--router-deployment` | `graph-router` | Name of the router Deployment to patch on composition |
| `--supergraph-configmap` | `graph-supergraph` | Name of the ConfigMap to store the composed supergraph |
| `--rover-path` | `rover` | Path to the `rover` CLI binary |
| `--composition-timeout` | `2m` | Timeout for rover compose execution |
| `--leader-elect` | `false` | Enable leader election for HA deployments |
| `--dry-run` | `false` | Compose but don't update ConfigMap or Deployment |
| `--history-count` | `0` | Number of previous supergraph versions to keep (0=disabled) |
| `--metrics-bind-address` | `:8080` | Address for the Prometheus metrics endpoint |
| `--health-probe-bind-address` | `:8081` | Address for health/readiness probes |

### Helm Values

Configuration is managed via `values.yaml` and per-environment overrides:

```yaml
# values.yaml (defaults)
controller:
  image:
    repository: ghcr.io/codefin/supergraph-operator
    tag: "latest"
    pullPolicy: IfNotPresent
  replicas: 1
  resources:
    requests:
      memory: "64Mi"
      cpu: "50m"
    limits:
      memory: "256Mi"
      cpu: "250m"

config:
  federationVersion: "=2.7.0"
  routerDeployment: "graph-router"
  supergraphConfigMap: "graph-supergraph"
  compositionTimeout: "2m"
  leaderElect: false
  dryRun: false
  historyCount: 0

namespace: vahalla
```

Per-environment overrides (e.g. `values-local.yaml`):

```yaml
controller:
  image:
    repository: ghcr.io/codefin/supergraph-operator
    tag: "latest"
    pullPolicy: IfNotPresent

config:
  routerDeployment: "graph-router"
  supergraphConfigMap: "graph-supergraph"
  leaderElect: true  # HA mode

namespace: vahalla-local
```

## Quick Start

```bash
# 1. Build and test
make build
make test

# 2. Deploy to Kubernetes (builds image + installs Helm chart with CRD)
make deploy ENV=local

# 3. Apply a SubgraphSchema CR
kubectl apply -f examples/subgraph.yaml

# 4. Check status
kubectl get subgraphschemas
```

## Development

```bash
# Run locally against current kubeconfig
make run

# Run tests
make test

# Generate CRD manifests and deepcopy (requires controller-gen)
make generate

# Build Docker image
make docker-build

# Render Helm templates (dry-run)
make template ENV=local
```

### Prerequisites

- Go 1.23+
- `controller-gen` — install with `go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.1`
- `rover` CLI — included in Docker image, or install locally from [Apollo Rover](https://www.apollographql.com/docs/rover/)
- A running Kubernetes cluster (Docker Desktop, kind, k3d, etc.)

## Project Structure

```text
├── api/v1alpha1/          # CRD type definitions & deepcopy
├── cmd/                   # Entry point (CLI flags, manager setup)
├── internal/
│   ├── controller/        # Reconcile logic (compose, configmap, deploy patch)
│   └── metrics/           # Prometheus metrics definitions
├── charts/                # Helm chart (CRD + RBAC + Deployment)
│   └── supergraph-operator/
│       ├── templates/     # crd.yaml, rbac.yaml, deployment.yaml
│       ├── values.yaml    # Default config
│       ├── values-local.yaml
│       └── values-dev.yaml
├── examples/              # Sample SubgraphSchema CRs
├── .github/workflows/     # CI + Release pipelines
├── config/crd/bases/      # Generated CRD manifests
├── Dockerfile             # Multi-stage build with rover CLI
└── Makefile               # Build, test, deploy, generate targets
```

## Makefile Targets

| Target         | Description                                    |
|---------------|------------------------------------------------|
| `make build`   | Build the controller binary                    |
| `make test`    | Run all tests                                  |
| `make run`     | Build + run locally (requires kubeconfig)       |
| `make generate`| Generate CRD manifests and deepcopy            |
| `make deploy`  | Build image + install/upgrade Helm chart        |
| `make upgrade` | Upgrade Helm release                           |
| `make template`| Dry-run Helm template rendering                |
| `make docker-build` | Build Docker image                        |
| `make k8s-restart`  | Restart controller pod                    |
| `make clean`   | Remove built artifacts                         |

All deploy/template targets accept `ENV=local|dev|demo|prod`.

## Metrics

The operator exposes Prometheus metrics on `:8080/metrics`:

| Metric | Type | Description |
|---|---|---|
| `supergraph_compositions_total` | Counter | Total compositions by status (`success`/`failed`) |
| `supergraph_composition_duration_seconds` | Histogram | Duration of each composition |
| `supergraph_subgraphs_total` | Gauge | Current number of SubgraphSchema resources |
| `supergraph_compositions_skipped_total` | Counter | Compositions skipped (schemas unchanged) |

## Integration

Each subgraph service should include a `SubgraphSchema` resource in its Helm chart:

```yaml
apiVersion: codefin.io/v1alpha1
kind: SubgraphSchema
metadata:
  name: my-service
  namespace: {{ .Values.namespace }}
spec:
  routingUrl: "http://my-service:8080/query"
  schema: |
    {{ .Files.Get "schema.graphqls" | nindent 4 }}
```

See the `examples/` directory for more samples.

Deploy order:

```bash
make deploy ENV=local          # CRD + controller first
# Then deploy subgraph services — each creates a SubgraphSchema CR
# Controller auto-composes and updates the router
```

## CI/CD

- **CI** (`.github/workflows/ci.yml`) — runs on push/PR to `main`: `go test`, `go build`, `helm lint`
- **Release** (`.github/workflows/release.yml`) — runs on tag `v*`: test → build + push Docker image to `ghcr.io/codefin/supergraph-operator` → package + push Helm chart to OCI registry

To release:

```bash
git tag v0.2.0
git push origin v0.2.0
```

## License

Apache License 2.0 — see [LICENSE](./LICENSE)

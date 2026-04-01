# Graph Controller

Kubernetes controller that automatically composes an Apollo Federation supergraph when subgraph schemas change.

## How It Works

1. Each subgraph service declares a `SubgraphSchema` custom resource containing its `routingUrl` and GraphQL `schema`
2. The controller watches for `SubgraphSchema` create/update/delete events
3. On any change, it lists all `SubgraphSchema` resources, generates a rover config, and runs `rover supergraph compose`
4. The composed supergraph is written to a ConfigMap (`graph-supergraph`)
5. The router Deployment is patched with a checksum annotation to trigger a rolling restart

## CRD: SubgraphSchema

```yaml
apiVersion: vahalla.io/v1alpha1
kind: SubgraphSchema
metadata:
  name: crm-service
spec:
  routingUrl: "http://crm-service:8080/query"
  schema: |
    type Query {
      health: String!
    }
```

After composition, the status is updated:

```
kubectl get subgraphschemas
NAME               URL                                  STATUS    LAST COMPOSED          AGE
crm-service        http://crm-service:8080/query         Success   2026-03-31T02:00:00Z   5m
identity-service   http://identity-service:8080/query    Success   2026-03-31T02:00:00Z   5m
```

## Quick Start

```bash
# Build and test
make build
make test

# Deploy to Kubernetes (local)
make deploy ENV=local

# Check status
kubectl get subgraphschemas -n vahalla-local
```

## Development

```bash
# Run locally (requires kubeconfig + CRD installed)
make run

# Run tests
make test

# Build Docker image
make docker-build

# Render Helm templates (dry-run)
make template ENV=local
```

## Project Structure

```
├── api/v1alpha1/          # CRD type definitions
├── cmd/                   # Entry point
├── internal/controller/   # Reconcile logic
├── charts/                # Helm chart (CRD + RBAC + Deployment)
├── config/crd/            # Generated CRD manifests
├── Dockerfile             # Multi-stage build with rover CLI
└── Makefile               # Build, test, deploy targets
```

## Integration with vahalla-mono

Add as a git submodule:

```bash
cd vahalla-mono
git submodule add <repo-url> services/graph-controller
```

Deploy order:

```bash
make svc-deploy s=graph-controller env=local  # CRD + controller first
make svc-deploy s=crm env=local               # creates SubgraphSchema CR
make svc-deploy s=graph env=local              # router reads composed supergraph
```

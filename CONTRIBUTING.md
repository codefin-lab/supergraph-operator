# Contributing to supergraph-operator

Thank you for your interest in contributing!

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone git@github.com:<your-user>/supergraph-operator.git`
3. Create a branch: `git checkout -b feature/my-feature`
4. Make your changes
5. Run tests: `make test`
6. Commit and push
7. Open a Pull Request

## Prerequisites

- Go 1.23+
- `controller-gen` — `go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.1`
- `golangci-lint` — [install instructions](https://golangci-lint.run/welcome/install/)
- Docker (for building images)
- Helm 3 (for chart development)

## Development Workflow

```bash
make build      # Build binary
make test       # Run all tests
make lint       # Run golangci-lint
make generate   # Regenerate CRD manifests and deepcopy
make template   # Dry-run Helm templates
```

## Code Guidelines

- Follow existing code style and patterns
- Add tests for new features
- Keep commits focused and atomic
- Update documentation when adding or changing features

## Pull Request Process

1. Ensure `make test` and `make lint` pass
2. Update README.md if you're adding new features or flags
3. Add a clear description of what your PR does
4. One approval required for merge

## Reporting Issues

Please use GitHub Issues. Include:

- Steps to reproduce
- Expected vs actual behavior
- Go version, Kubernetes version, controller-runtime version

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.

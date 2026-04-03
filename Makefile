.PHONY: help test fmt vet lint tidy install-hooks

help:
	@echo "Available commands:"
	@echo "  make test          - Run tests"
	@echo "  make fmt           - Format code with gofmt"
	@echo "  make vet           - Run go vet"
	@echo "  make lint          - Run golangci-lint"
	@echo "  make tidy          - Tidy go modules"
	@echo "  make install-hooks - Install qlty pre-commit hooks"

test:
	@echo "Running tests..."
	@go test -v ./...

fmt:
	@echo "Formatting code..."
	@gofmt -w -s .
	@echo "✓ Code formatted"

vet:
	@echo "Running go vet..."
	@go vet ./...
	@echo "✓ Vet complete"

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "Running golangci-lint..."; \
		golangci-lint run ./...; \
		echo "✓ Lint complete"; \
	else \
		echo "⚠ golangci-lint not installed. Install: https://golangci-lint.run/usage/install/"; \
	fi

tidy:
	@echo "Tidying go modules..."
	@go mod tidy
	@echo "✓ go mod tidy complete"

install-hooks:
	@qlty githooks install
	@echo "✓ Qlty hooks installed"

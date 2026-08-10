.PHONY: help dev up down go-test web-check generate atlas-diff atlas-hash atlas-apply docs-check security

help:
	@awk 'BEGIN {FS = ":.*##"; print "Usage: make <target>\n"} /^[a-zA-Z_-]+:.*?##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

dev: ## Start the core development stack
	docker compose up --build postgres valkey api bot worker

up: ## Start all services, including web applications
	docker compose --profile web up --build

down: ## Stop the local stack
	docker compose --profile web down

go-test: ## Run Go tests
	go test -race -cover ./...

web-check: ## Type-check and lint frontend workspaces
	bun run check

generate: ## Generate Go, SQL, and TypeScript API bindings
	go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
	go tool sqlc generate
	bun run generate:api

atlas-diff: ## Create a reviewed migration from the desired schema
	atlas migrate diff --env local

atlas-hash: ## Recompute migration checksums after a hand-authored migration
	atlas migrate hash --dir file://database/migrations

atlas-apply: ## Apply committed migrations
	atlas migrate apply --env local

docs-check: ## Validate Mintlify documentation
	cd docs && PUPPETEER_SKIP_DOWNLOAD=true bunx mint@latest validate
	cd docs && PUPPETEER_SKIP_DOWNLOAD=true bunx mint@latest broken-links --check-anchors

security: ## Run local security checks
	go tool govulncheck ./...
	gitleaks detect --source .

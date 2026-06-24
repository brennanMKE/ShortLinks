.PHONY: dev dev-built build test

# Start the local dev server (hot-reload: Vite :5173 + Go API :8080).
# No Postgres, no systemd, no migrations needed.
# Open http://localhost:5173 — logs in as the mock admin automatically.
dev:
	./scripts/dev.sh

# Start the local dev server with the pre-built embedded SPA (Go :8080 only).
dev-built:
	./scripts/dev.sh --built

# Full production build (SPA + Go binary with embedded web/dist/).
build:
	cd web && npm run build && cd ..
	go build ./cmd/shortlinks

# Run all Go tests.
test:
	go test ./...

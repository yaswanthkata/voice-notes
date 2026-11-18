.PHONY: run test check build

run: ## Start the server (reads .env)
	go run ./cmd/server

test: ## Unit tests with the race detector; no network or API keys needed
	go test -race ./...

check: ## Everything CI should run
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "needs gofmt: $$unformatted"; exit 1; fi
	go vet ./...
	go test -race ./...

build: ## Static binary in ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/voice-notes ./cmd/server


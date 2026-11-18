.PHONY: run test check build docker deploy-gcp destroy-gcp

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

docker: ## Container image (secrets are passed at runtime, never baked in)
	docker build -t voice-notes .

deploy-gcp: ## Deploy to Google Cloud Run; needs GEMINI_API_KEY and API_KEY in the environment
	deploy/cloudrun.sh

destroy-gcp: ## Delete the Cloud Run service
	deploy/cloudrun.sh destroy

.PHONY: build test test-short test-e2e test-coverage specs verify clean

build:
	go build -o bin/ai-team ./cmd/ai-team

test:
	go test ./...

test-short:
	go test -short ./...

test-e2e:
	go test -run TestE2E ./e2etest/...

test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@COVERAGE=$$(go tool cover -func=coverage.out | awk '/^total:/ {gsub("%", "", $$3); print $$3}'); \
	awk -v coverage="$$COVERAGE" 'BEGIN { if (coverage < 60.0) { printf "coverage %.1f%% is below 60.0%%\n", coverage; exit 1 } }'

specs:
	npx --yes @fission-ai/openspec@1.4.1 validate --all --strict --no-interactive

verify: specs
	go mod verify
	go vet ./...
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
	go test -race ./...
	cd web && npm ci && npm audit --audit-level=high && npm run lint && npm test && npm run build

clean:
	rm -rf bin/
	rm -rf tmp/

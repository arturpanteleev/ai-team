TAG ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

PLATFORMS := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64

.PHONY: build test test-short test-e2e test-coverage specs verify clean release-binaries

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
	bash scripts/coverage-gate.sh coverage.out

specs:
	npx --yes @fission-ai/openspec@1.4.1 validate --all --strict --no-interactive

verify: specs
	@UNFORMATTED=$$(gofmt -l .); \
	if [ -n "$$UNFORMATTED" ]; then \
		echo "Файлы не отформатированы gofmt:" >&2; \
		echo "$$UNFORMATTED" >&2; \
		exit 1; \
	fi
	go mod verify
	go vet ./...
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
	go test -race ./...
	$(MAKE) test-coverage
	$(MAKE) test-e2e
	@FRONTEND_DIST_SNAPSHOT=$$(mktemp -d); \
	cp -R web/dist "$$FRONTEND_DIST_SNAPSHOT/dist"; \
	trap 'rm -rf -- "$$FRONTEND_DIST_SNAPSHOT"' EXIT; \
	cd web && npm ci && npm run build && diff -qr "$$FRONTEND_DIST_SNAPSHOT/dist" dist && npm run lint && npm test && npm audit --audit-level=high

clean:
	rm -rf bin/
	rm -rf tmp/

release-binaries:
	@mkdir -p bin
	@for platform in $(PLATFORMS); do \
		os="$${platform%%-*}"; arch="$${platform##*-}"; \
		echo "GOOS=$$os GOARCH=$$arch go build -ldflags \"-X main.version=$(TAG)\" -o bin/ai-team-$${platform} ./cmd/ai-team"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -ldflags "-X main.version=$(TAG)" -o bin/ai-team-$${platform} ./cmd/ai-team || exit 1; \
	done

.PHONY: build test clean

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

clean:
	rm -rf bin/
	rm -rf tmp/

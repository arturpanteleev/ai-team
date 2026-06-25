.PHONY: build test clean

build:
	go build -o bin/ai-team ./cmd/ai-team

test:
	go test ./pkg/...

clean:
	rm -rf bin/
	rm -rf tmp/

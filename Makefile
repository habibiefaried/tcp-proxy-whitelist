.PHONY: all build test clean docker

# Build the binary locally.
all: build

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o wlistproxy .

# Run tests (if any).
test:
	go test -v -race ./...

# Run the proxy locally (requires env vars).
run: build
	BIND_PORT=9090 REMOTE_ADDR_PAIR=localhost:8080 WHITELISTED_SUBNET=127.0.0.1/8 ./wlistproxy

# Build the Docker image.
docker:
	docker build -t tcp-proxy-whitelist:latest .

# Remove build artifacts.
clean:
	rm -f wlistproxy

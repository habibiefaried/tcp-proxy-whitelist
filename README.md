# tcp-proxy-whitelist

A minimal, zero-dependency TCP proxy with IP whitelist filtering — written in Go.

It listens on a configurable port, checks every incoming connection's remote IP against a list of allowed CIDR subnets, and forwards whitelisted connections to a designated upstream server. Connections from IPs not in the whitelist are rejected and logged.

## Features

- **Zero external dependencies** — uses only the Go standard library.
- **CIDR-based whitelisting** — comma-separated list of subnets (e.g. `10.0.0.0/8,172.16.0.0/12`).
- **Graceful shutdown** — handles SIGINT/SIGTERM, waits for active connections to drain.
- **Static binary** — builds into a single, portable executable.
- **Tiny Docker image** — multi-stage build based on Alpine Linux (~15 MB).
- **Production-tested** — used as a secure proxy between [Nomad](https://www.nomadproject.io/) cluster agents.

## Architecture

```
  Client (1.2.3.4)
       │
       ▼
┌─────────────────────┐
│  wlistproxy          │
│  listen: 0.0.0.0:PORT│
│                      │
│  ┌──────────────┐    │
│  │ IP whitelist? │    │
│  │ 10.0.0.0/8   │    │
│  └──────┬───────┘    │
│    NO   │   YES      │
│    ✗    │    ✓       │
└─────────┼────────────┘
          │
          ▼
   Upstream Server
   (REMOTE_ADDR_PAIR)
```

## Configuration

All configuration is done via environment variables:

| Variable | Required | Description |
|---|---|---|
| `BIND_PORT` | **Yes** | Port to listen on. Binds to all interfaces (`0.0.0.0`). |
| `REMOTE_ADDR_PAIR` | **Yes** | Upstream server in `ip:port` format (e.g. `10.0.0.5:8080`). |
| `WHITELISTED_SUBNET` | No | Comma-separated CIDR subnets. If empty or unset, **all connections are blocked**. Set to `0.0.0.0/0` to allow everything (not recommended). |

## Quick Start

### Run with Go

```sh
BIND_PORT=9090 \
REMOTE_ADDR_PAIR=localhost:8080 \
WHITELISTED_SUBNET=127.0.0.1/8,10.0.0.0/8 \
go run main.go
```

### Run the binary

Download the latest release from [GitHub Releases](https://github.com/habibiefaried/tcp-proxy-whitelist/releases), then:

```sh
chmod +x wlistproxy_linux_amd64
BIND_PORT=9090 REMOTE_ADDR_PAIR=10.0.0.5:8080 WHITELISTED_SUBNET=10.0.0.0/8 ./wlistproxy_linux_amd64
```

### Run with Docker

```sh
docker run --rm \
  -e BIND_PORT=9090 \
  -e REMOTE_ADDR_PAIR=10.0.0.5:8080 \
  -e WHITELISTED_SUBNET=10.0.0.0/8 \
  -p 9090:9090 \
  ghcr.io/habibiefaried/tcp-proxy-whitelist:latest
```

Build the image locally:

```sh
make docker
# or:
docker build -t tcp-proxy-whitelist .
```

## Install as a systemd Service

Download the binary:

```sh
wget "$(curl -s https://api.github.com/repos/habibiefaried/tcp-proxy-whitelist/releases/latest \
  | jq -r '.assets[] | select(.name | test("linux_amd64$")) | .browser_download_url')" \
  -O /usr/local/bin/wlistproxy
chmod +x /usr/local/bin/wlistproxy
```

Create the service unit:

```sh
cat > /etc/systemd/system/wlistproxy.service <<'EOF'
[Unit]
Description=TCP Proxy with IP Whitelist
Requires=network-online.target
After=network-online.target

[Service]
Environment="BIND_PORT=9090"
Environment="REMOTE_ADDR_PAIR=10.0.0.5:8080"
Environment="WHITELISTED_SUBNET=10.0.0.0/8,172.16.0.0/12"
Restart=on-failure
StandardOutput=append:/var/log/wlistproxy.log
StandardError=append:/var/log/wlistproxy.err
ExecStart=/usr/local/bin/wlistproxy
ExecReload=/bin/kill -HUP $MAINPID
KillSignal=SIGINT
LimitNOFILE=infinity
LimitNPROC=infinity

[Install]
WantedBy=multi-user.target
EOF
```

Enable and start:

```sh
systemctl daemon-reload
systemctl enable wlistproxy
systemctl start wlistproxy
```

For multiple ports (e.g. proxying different upstreams), create additional units like `wlistproxy2.service` with different `BIND_PORT` and environment values.

## Build from Source

```sh
git clone https://github.com/habibiefaried/tcp-proxy-whitelist.git
cd tcp-proxy-whitelist
make build
```

Requires Go 1.26 or later.

## License

[Apache License 2.0](LICENSE)

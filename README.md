# tcp-proxy-whitelist
Simple golang tcp proxy for security purposes (with whitelisting IP)
# How to use

```
BIND_PORT=9090 REMOTE_ADDR_PAIR=localhost:8080 WHITELISTED_SUBNET=127.0.21.2/32,172.17.0.1/32 go run main.go
```
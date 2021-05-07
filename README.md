# tcp-proxy-whitelist
Simple golang tcp proxy for security purposes (with whitelisting IP)
# How to use

Run this binary with environment variable
* BIND_PORT: Which port to bind, this will be bound to all interface (0.0.0.0)
* REMOTE_ADDR_PAIR: Upstream server with format `ip:port`. See example below
* WHITELISTED_SUBNET: All subnets that allowed to access this port. Not specifying this env var resulting reject from all IP (localhost included). Set this port 0.0.0.0/0 to let all IP connect to this machine (not recommended though, not this app purpose).

# Example 
```
BIND_PORT=9090 REMOTE_ADDR_PAIR=localhost:8080 WHITELISTED_SUBNET=127.0.0.1/8,172.17.0.1/16 go run main.go
```
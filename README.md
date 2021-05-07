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

# Install

Run as root

```
wget `curl -s https://api.github.com/repos/habibiefaried/tcp-proxy-whitelist/releases/latest | jq -r .assets[2].browser_download_url` -O /usr/bin/wlistproxy
chmod +x /usr/bin/wlistproxy
```

Then

```
cat > /etc/systemd/system/wlistproxy.service <<EOF
[Unit]
Description=wlistproxy
Requires=network-online.target
After=network-online.target
[Service]
Environment="BIND_PORT=<as you wish>"
Environment="REMOTE_ADDR_PAIR=<as you wish>"
Environment="WHITELISTED_SUBNET=<as you wish>"
Restart=on-failure
StandardOutput=append:/var/log/wlistproxy.log
StandardError=append:/var/log/wlistproxy.err
ExecStart=/usr/bin/wlistproxy
ExecReload=/bin/kill -HUP \$MAINPID
KillSignal=SIGINT
LimitNOFILE=infinity
LimitNPROC=infinity
[Install]
WantedBy=multi-user.target
EOF
```

And then enable and start it

```
systemctl enable wlistproxy
systemctl start wlistproxy
```

You may want to add service like `wlistproxy2` etc if you have multiple ports.

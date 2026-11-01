<div align="center">
  <img src=".github/assets/logo.svg" alt="kamui Logo" width="200">
  <h1>Kamui</h1>

  <a href="https://github.com/Tanq16/kamui/actions/workflows/release.yaml"><img alt="Build Workflow" src="https://github.com/Tanq16/kamui/actions/workflows/release.yaml/badge.svg"></a>&nbsp;<a href="https://github.com/Tanq16/kamui/releases"><img alt="GitHub Release" src="https://img.shields.io/github/v/release/Tanq16/kamui"></a>&nbsp;<a href="https://hub.docker.com/r/tanq16/kamui"><img alt="Docker Pulls" src="https://img.shields.io/docker/pulls/tanq16/kamui"></a><br><br>
  <a href="#features">Features</a> &bull; <a href="#install">Install</a> &bull; <a href="#usage">Usage</a> &bull; <a href="#notes">Notes</a>
</div>

---

kamui is a DNS blocker for a home LAN, shipped as one Go binary with an embedded web UI. Devices query it over plain DNS on port 53, and it forwards to upstreams over DNS-over-HTTPS or DNS-over-TLS.

It is not a public resolver. It serves no DoH, DoT, or DHCP, and the UI has no login, so both ports stay on the LAN.

## Features

- Block lists by URL in hosts, domain-per-line, adblock (`||domain^`, `@@||domain^`), and `*.domain` formats, each with its own on/off switch and scheduled refresh.
- Allowlist and custom blocks, with an `include subdomains` flag per entry and one-click block or unblock from the query log.
- DNS rewrites for split-horizon names, as exact or wildcard patterns answering with IPs or a CNAME.
- Upstreams over DoH, DoT, or plain DNS, tried in order with hedged fallback and a bootstrap resolver for their hostnames.
- Conditional forwarding of local domains and private reverse lookups to the router.
- CNAME-target blocking, DNS rebinding protection, and the Firefox DoH and iCloud Private Relay canaries.
- A TTL-honoring response cache, a SQLite query log, and daily statistics.
- A global switch that turns blocking off or pauses it for 5 minutes, 1 hour, or a custom time.
- Web UI with a dashboard, query log, DNS settings, and app settings, including config export and import.

## Install

### Docker

The image runs as UID and GID `10001`, so the data directory needs that owner.

```bash
mkdir -p $HOME/.kamui && sudo chown 10001:10001 $HOME/.kamui
```
```bash
docker run -d --name kamui \
  --network host \
  -v $HOME/.kamui:/data \
  --restart unless-stopped \
  tanq16/kamui:latest
```

The UI is at `http://<host-ip>:8053`. Host networking lets kamui bind port 53 directly and lets the query log see real client IPs. The same setup as a compose file:

```yaml
services:
  kamui:
    image: tanq16/kamui:latest
    container_name: kamui
    restart: unless-stopped
    network_mode: host
    volumes:
      - ./data:/data # change as needed
```

### Release binary

Download `kamui-linux-amd64`, `kamui-linux-arm64`, or `kamui-darwin-arm64` from the [releases](https://github.com/Tanq16/kamui/releases) page.

```bash
sudo install -m 0755 kamui-linux-amd64 /usr/local/bin/kamui
```

### systemd

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin kamui
```

`/etc/systemd/system/kamui.service`:

```ini
[Unit]
Description=kamui DNS blocker
Wants=network-online.target
After=network-online.target

[Service]
User=kamui
Group=kamui
ExecStart=/usr/local/bin/kamui serve --data-dir /var/lib/kamui
StateDirectory=kamui
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now kamui
```

`AmbientCapabilities=CAP_NET_BIND_SERVICE` is what lets the unprivileged user bind port 53. Outside systemd, `sudo setcap cap_net_bind_service=+ep /usr/local/bin/kamui` does the same for the binary.

### Free port 53 from systemd-resolved

Most Linux hosts run the `systemd-resolved` stub listener on port 53. Turn it off and point `/etc/resolv.conf` at the real resolver list, because the default link targets the stub that no longer answers.

```bash
sudo mkdir -p /etc/systemd/resolved.conf.d
printf '[Resolve]\nDNSStubListener=no\n' | sudo tee /etc/systemd/resolved.conf.d/kamui.conf
sudo ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
sudo systemctl restart systemd-resolved
```

### Build from source

Needs Go 1.27 or newer, `make`, and `curl`.

```bash
make build
```

## Usage

Point the router's DHCP DNS server at the kamui host, then manage everything else from the UI. Every setting applies live without a restart.

| Flag | Env | Default | Description |
|---|---|---|---|
| `--dns-addr` | `KAMUI_DNS_ADDR` | `0.0.0.0:53` | DNS listen address, UDP and TCP |
| `--http-addr` | `KAMUI_HTTP_ADDR` | `0.0.0.0:8053` | Web UI and API listen address |
| `--data-dir` | `KAMUI_DATA_DIR` | `./data` | Holds `config.json`, `kamui.db`, and `lists/` |
| `--debug` | | `false` | Debug logging |

### Testing on another port

Any `host:port` works for both listeners, so kamui can run beside an existing resolver without root.

```bash
./kamui serve --dns-addr 127.0.0.1:15353 --http-addr 127.0.0.1:18053 --data-dir ./test-data
dig @127.0.0.1 -p 15353 example.com
```

### Router side

With conditional forwarding on, kamui sends local names such as `printer.lan` and private reverse lookups to the router. The router has to answer those itself rather than send unknown ones back to kamui. On dnsmasq and OpenWrt, mark the domain local (`local=/lan/`, or "Local server" `/lan/` in LuCI) and enable `bogus-priv` so private reverse lookups stay on the router. kamui also answers NXDOMAIN to local-name queries arriving from the router's own address, which breaks the loop when the router is misconfigured.

## Notes

- **Blocking switch:** off and paused disable custom blocks, lists, and the CNAME check. Canaries, rewrites, local forwarding, special-use names, and rebinding protection stay active.
- **Cache flushes:** every change of blocking state flushes the response cache. List toggles and rule edits apply to the next query without a flush.
- **iCloud Private Relay:** the canary turns Private Relay off for LAN devices, which would otherwise resolve through Apple and skip both blocking and rewrites. It can be turned off in DNS settings.
- **Rebinding protection:** upstream answers pointing at private, loopback, or link-local addresses are dropped. Domains such as `plex.direct` are exempt by default.
- **Rewrites:** a matched name answers HTTPS and SVCB queries with an empty response, and forwards other types such as TXT and SOA so ACME DNS-01 renewals keep working.
- **Data directory:** `config.json` (mode `0600`) holds settings, lists, rules, and rewrites, and is what the UI exports and imports. `kamui.db` holds the query log and statistics, and `lists/` holds the last good download of each list.

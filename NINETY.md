# ninety-core

sing-box core for [Ninety](https://github.com/pathetixx/190x4-Ninety), forked from
[SagerNet/sing-box](https://github.com/SagerNet/sing-box). GPL-3.0, same as upstream.

Branch `ninety` carries the patches Ninety needs on top of an upstream tag.
`upstream` remote points at SagerNet; updating means merging a new upstream tag
into `ninety`.

Base: **v1.13.16**

## Delta to upstream

### Unified delay

`experimental.unified_delay.enabled` makes every URLTest report the RTT of a
second request over the connection it already opened, instead of the first
request that also paid for the TCP/TLS handshake. Without it VLESS+Reality nodes
read 2-3x slower than they are.

- `common/urltest/context.go` — the context flag.
- `common/urltest/urltest.go` — the second measurement.
- `option/experimental.go` — the option.
- `box.go` — flag goes on the root context before groups and the Clash API
  server capture it.
- `experimental/clashapi/proxies.go` — the single `GET /proxies/{name}/delay`
  probe builds its context from `server.ctx`, so it sees the flag too.

## Build

Pure Go, no cgo. Tags Ninety ships with:

```
with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale,with_ccm,with_ocm,badlinkname,tfogo_checklinkname0,with_grpc
```

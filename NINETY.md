# ninety-core

sing-box core for [Ninety](https://github.com/pathetixx/190x4-Ninety), forked from
[SagerNet/sing-box](https://github.com/SagerNet/sing-box). GPL-3.0, same as upstream.

Branch `ninety` carries the patches Ninety needs on top of an upstream tag.
`upstream` remote points at SagerNet; updating means merging a new upstream tag
into `ninety`.

Base: **v1.13.19**

`upstream-watch.yml` reads that line every Monday, compares it with the latest
upstream release and opens a PR (clean merge) or an issue (conflicts). Keep the
format as-is when moving to a new base.

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

### WireGuard junk traffic and AmneziaWG shaping

`noise` on a WireGuard endpoint covers two things. `noise.fake_packet` sends a
burst of random UDP packets before the handshake initiation, so a DPI box does
not see a WireGuard signature as the first thing on the flow; Ninety uses it for
WARP. `noise.amnezia` carries an AmneziaWG `.conf` verbatim — `jc`/`jmin`/`jmax`
junk packets, `s1`/`s2` handshake padding, `h1`..`h4` message types and the
`i1`..`i5` specified packets — so a profile imported from a file speaks the same
protocol its peer does.

The implementation lives in
[ninety-wireguard-go](https://github.com/pathetixx/ninety-wireguard-go), pulled
in with a `replace` directive. On this side:

- `option/wireguard.go` — the `noise` config field.
- `transport/wireguard/endpoint_options.go`, `protocol/wireguard/endpoint.go`,
  `transport/wireguard/endpoint.go` — carry it to the device. Shaping is
  validated when the endpoint is built and installed with `Device.SetNoise`,
  because both kinds of rejection would otherwise appear as a tunnel that never
  completes a handshake and says nothing about why.
- `transport/wireguard/client_bind.go` — `SendWithoutModify`, the junk-traffic
  send path that leaves the payload alone instead of stamping the per-endpoint
  reserved routing key into it. A shaped device sends everything this way, so
  reserved bytes and `noise.amnezia` on one peer are rejected as a conflict:
  those bytes land inside a magic header or inside handshake padding.

### Balancer outbound

`type: "balancer"` routes each new connection through the lowest-delay outbound
of its group and interrupts existing connections when the leader changes. Ninety
exposes it as "Auto".

It measures nothing itself: delays come from the shared URLTest history, which a
`urltest` group over the same outbounds keeps up to date, so one health check
feeds both the UI and this group. A failed dial drops that outbound's
measurement, so it sorts last until the next check produces a fresh one.

Only `lowest-delay` is implemented. The upstream this was modelled on also had
round-robin, consistent-hashing and sticky-sessions strategies; Ninety never used
them, and an unused strategy is an untested one.

- `option/balancer.go` — the options.
- `protocol/group/balancer.go` — the group.
- `constant/proxy.go`, `include/registry.go` — type and registration.

### TLS tricks

`tls.tls_tricks` on an outbound, aimed at DPI that matches on the ClientHello:

- `mixedcase_sni` randomises the letter case of the SNI, drawn again per
  connection. Hostnames are case-insensitive, so neither the handshake nor the
  certificate check changes; a filter comparing the SNI byte-for-byte does.
- `padding_size` is a `"from-to"` byte range for the ClientHello padding
  extension, drawn again per handshake, so the message stops landing on the one
  length its fingerprint always produces.

Both need the uTLS client, and both are refused - loudly, not ignored - for the
plain client and for REALITY. REALITY seals its authentication over the whole
ClientHello and already sends a byte-exact browser fingerprint with a genuine
SNI, so rewriting that message is both risky and pointless.

- `option/tls_tricks.go`, `option/tls.go` — the options.
- `common/tls/tls_tricks.go` — the SNI and padding helpers.
- `common/tls/utls_client.go` — applied per connection.
- `common/tls/std_client.go`, `common/tls/reality_client.go` — the refusals.

### Dependency versions

The security-relevant bumps live in `go.mod` here rather than being applied at
build time by whoever compiles this. `security.yml` in Ninety audits the built
binary with govulncheck, so a regression shows up there.

## Build

Pure Go, no cgo. Tags Ninety ships with:

```
with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale,with_ccm,with_ocm,badlinkname,tfogo_checklinkname0,with_grpc
```

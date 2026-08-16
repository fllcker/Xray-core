# Xray-core, fllcker fork

A fork of [XTLS/Xray-core](https://github.com/XTLS/Xray-core) that adds one
thing: a gRPC API for dropping a user's live sessions at named addresses and
refusing them there for a short while.

It exists to make sharing one subscription across devices inconvenient rather
than to punish it. A legitimate user never notices; someone handing their link
around gets a connection that keeps breaking.

Upstream base: see [UPSTREAM_TAG](UPSTREAM_TAG).
Design notes: [PLAN.md](PLAN.md). Patch inventory: [PATCHES.md](PATCHES.md).

---

## Why this is not just "ban the account"

Counting addresses per user is noisy. A phone switching from Wi-Fi to mobile
data holds two addresses at once for as long as the idle timeout allows; a
dual-stack client can hold an IPv4 and an IPv6 address simultaneously and
legitimately. Acting on that with a ban produces exactly the support tickets
nobody wants.

So the core offers a primitive, not a policy: *close this user's connections at
these addresses and turn them away for N seconds*. How many addresses are
allowed, which one counts as excess and how long to hold it is a decision for
the backend, where it can change without rebuilding Xray.

## What changed from upstream

Everything new lives in new files. The footprint inside upstream files is 35
lines across five of them, each marked with a `[fllcker:FLK-NNN]` comment and
documented in [PATCHES.md](PATCHES.md):

| File | Patch | What it does |
|---|---|---|
| `app/dispatcher/default.go` | FLK-001, FLK-002 | registers live sessions, one hook per dispatch path |
| `proxy/vless/inbound/inbound.go` | FLK-003 | turns away a blocked address (VLESS, both TCP and XHTTP) |
| `proxy/hysteria/server.go` | FLK-004 | same for Hysteria2, which authenticates at the QUIC layer |
| `infra/conf/api.go` | FLK-005 | makes the service name resolvable in config |
| `main/distro/all/all.go` | FLK-006 | links the package into the binary |

New packages: `app/fllcker` (session and ban registries) and
`app/fllcker/command` (the gRPC service).

`bash scripts/check-patches.sh` verifies every patch is still present. Run it
after any rebase onto a new upstream release: a lost line breaks nothing at
build time, which is precisely what makes it dangerous.

## Install

```bash
bash -c "$(curl -L https://github.com/fllcker/xray-core/raw/main/scripts/install-release.sh)" @ install
```

Same as the official installer, pointed at this fork. Systemd unit, service
user, geodata and log directories all end up where they normally do, so
everything written for stock Xray still applies.

Note that the official one-liner would install **upstream** instead: its script
hardcodes `XTLS/Xray-core` as the download source and has no flag to change it.
That is the only reason [scripts/install-release.sh](scripts/install-release.sh)
exists here — it is that script with the repository turned into a variable.

To install from somewhere else without editing the file:

```bash
GITHUB_REPO=you/your-fork bash -c "$(curl -L .../scripts/install-release.sh)" @ install
```

Building from source instead:

```bash
go build -o xray ./main
```

## Enabling the API

```json
{
  "api": {
    "tag": "api",
    "listen": "127.0.0.1:10085",
    "services": ["FllckerService", "StatsService", "ReflectionService"]
  }
}
```

The commander binds this address itself, so no extra inbound or routing rule is
needed.

> **Bind to loopback only.** None of these methods authenticate anything. Reach
> them over SSH port forwarding or a private network, never from the outside.

`ReflectionService` is what lets `grpcurl` work without a copy of the `.proto`.
Drop it once your backend talks to the service directly.

### Recommended policy timeouts

```json
{
  "policy": {
    "levels": {
      "0": {
        "connIdle": 90,
        "statsUserOnline": true
      }
    }
  }
}
```

`connIdle` defaults to 300 seconds, and it is the reason online counts lag in
stock Xray: an address disappears only once its last stream ends, and a stream
that stops carrying traffic hangs around for the whole idle timeout. Lowering it
to 60–120 makes the picture far more accurate. The cost is that genuinely idle
long-lived connections get cut and have to be reopened.

## Command line

The same operations are available from the binary, which is usually quicker than
reaching for `grpcurl` when something looks wrong:

```bash
xray api fllcker sessions -s 127.0.0.1:10085
xray api fllcker sessions -s 127.0.0.1:10085 -email user@example.com
xray api fllcker kick    -s 127.0.0.1:10085 -email user@example.com -ip 1.1.1.1,2.2.2.2 -seconds 10
xray api fllcker bans    -s 127.0.0.1:10085
xray api fllcker unban   -s 127.0.0.1:10085 -email user@example.com -ip 1.1.1.1
```

`-ip` repeats and also accepts a comma separated list. Output is JSON, so it
pipes into `jq`. Run `xray help api fllcker <command>` for the full description
of any of them.

## API reference

All timestamps are unix seconds. Every call that changes state requires
`email` — an operation scoped to an address alone would hit every user behind
the same carrier-grade NAT.

### Kick

Closes connections and blocks reconnection.

```bash
grpcurl -plaintext -d '{"email":"user1","ips":["1.1.1.1","2.2.2.2"],"ban_seconds":10}' \
  127.0.0.1:10085 xray.app.fllcker.command.FllckerService/Kick
```

| Field | Meaning |
|---|---|
| `email` | required |
| `ips` | addresses to act on; empty means all of the user's |
| `ban_seconds` | 0 kicks without blocking, which on its own achieves little |

Returns `closed` (distinct connections closed) and `banned_until`.

Pass every address in one call rather than looping. Kicking one at a time leaves
a window in which the client dropped by an earlier call is already back on an
address you have not named yet.

For Hysteria2, `closed` counts QUIC streams rather than clients: a kick ends the
stream while the client stays authenticated at the transport layer, and it is
the block that turns away whatever it opens next.

### ListSessions

```bash
grpcurl -plaintext -d '{"email":"user1"}' \
  127.0.0.1:10085 xray.app.fllcker.command.FllckerService/ListSessions
```

Empty `email` returns every user. Each address carries:

| Field | Meaning |
|---|---|
| `conns` | live connections from this address |
| `since` | when the address first appeared — who arrived when |
| `last_seen` | when it last opened a stream — whether it is still doing anything |
| `banned_until` | 0 when not blocked |

### ListBans, Unban

```bash
grpcurl -plaintext -d '{}' \
  127.0.0.1:10085 xray.app.fllcker.command.FllckerService/ListBans

grpcurl -plaintext -d '{"email":"user1","ips":["1.1.1.1"]}' \
  127.0.0.1:10085 xray.app.fllcker.command.FllckerService/Unban
```

Expired blocks are never listed. `Unban` with an empty `ips` lifts all of the
user's.

## Writing the policy

The interesting decision is not whom to kick but whether to kick at all.

`since` tells you who arrived when. `last_seen` tells you who is still alive,
because it advances on every new stream — and an address left behind by a
network switch stops opening streams, while a device in use opens them
continuously.

That distinction matters because one device can hold two addresses at once, for
reasons that have nothing to do with sharing:

- dual-stack IPv4/IPv6, routine on mobile carriers with IPv6;
- iOS Wi-Fi Assist during a handoff, where both interfaces are genuinely in use;
- carrier NAT rotating the public address mid-session.

In all three, "keep the newest, drop the rest" fires on an honest user, and
during a handoff the newest address flips back and forth so the client kicks
itself in a loop. Two cheap guards:

1. Only act when both addresses have a fresh `last_seen`. A silent one is a
   leftover that expires on its own.
2. Require the condition to hold across two polls, roughly ten seconds, before
   kicking. A transient flap disappears; someone actually sharing does not.

## Development

```bash
go build ./...
go test ./app/fllcker/...
bash scripts/check-patches.sh
```

To regenerate the protobuf after editing `app/fllcker/command/command.proto`:

```bash
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       app/fllcker/command/command.proto
```

Do not run `infra/vprotogen` — it walks the whole tree and regenerates every
`.pb.go`, producing a diff across hundreds of files and a guaranteed conflict on
the next rebase.

`go test ./...` and `go vet ./...` are not green on upstream either: some tests
need network access or geodata files that are not in the repository, and vet
reports pre-existing findings in `proxy/vless`. CI checks what this fork is
responsible for; see [PLAN.md](PLAN.md) §13.4.

## License

[Mozilla Public License 2.0](LICENSE), inherited from upstream.

One exception: [scripts/install-release.sh](scripts/install-release.sh) is
derived from [XTLS/Xray-install](https://github.com/XTLS/Xray-install) and stays
under **GPL-3.0**, as its origin requires. It is a standalone shell script and
is not part of the binary, so the two licenses do not interact.

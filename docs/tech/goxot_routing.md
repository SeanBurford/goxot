# GoXOT Call Routing

## Overview

Every inbound CALL_REQ is routed by examining the called X.121 address against the configured `servers` list.  `GetServer(addr, defaultLocal)` returns the longest-prefix-matching server entry and a **local** flag:

- If the server's IP (or any IP resolved from its DNS name) is assigned to a local network interface → `local = true`.
- If DNS resolution fails or no server matches → `local = defaultLocal`.

Each process passes a different `defaultLocal` value and acts on the result accordingly.

## Per-process routing policy

| Process | `defaultLocal` | `local=true` | `local=false, srv≠nil` | `local=false, srv=nil` |
|---|---|---|---|---|
| **xot-server** | `true` | Route to tun-gateway | Route to xot-gateway | Reject (CLR, cause busy) |
| **xot-gateway** | `false` | Reject (CLR, cause busy) | Dial remote XOT server | Reject (CLR, cause busy) |
| **tun-gateway** (outbound CALL_REQ) | `true` | Pass through TUN | Intercept → xot-gateway | Pass through TUN |
| **tun-gateway** (SyncRoutes) | `true` | Skip route | Install X.25 kernel route | Skip route |

## Routing decision table

### Common cases

| Scenario | Caller | Destination config | Resolved IP | `local` | Routing outcome |
|---|---|---|---|---|---|
| Remote client → local X.25 service | xot-server | Prefix matches, `ip` is a local interface address | Local | `true` | xot-server → tun-gateway → TUN → kernel |
| Remote client → remote XOT server | xot-server | Prefix matches, `ip` is a remote address | Remote | `false` | xot-server → xot-gateway → TCP → remote |
| Local X.25 socket → remote XOT server | tun-gateway | Prefix matches, `ip` is a remote address | Remote | `false` | tun-gateway intercepts → xot-gateway → TCP → remote |
| Local X.25 socket → local X.25 service | tun-gateway | Prefix matches, `ip` is a local interface address | Local | `true` | tun-gateway does not intercept; kernel routes via TUN subscription |
| Remote client → unconfigured address | xot-server | No prefix match | — | `true` (default) | xot-server → tun-gateway → TUN (kernel delivers or clears) |

### Failure cases

| Scenario | Effect |
|---|---|
| DNS resolution fails in `GetServer` | `local = defaultLocal`; routing proceeds as if locality is unknown |
| DNS resolution fails in xot-gateway (after routing decision) | `ResolveXotDestination` returns error → CLR sent back to local caller |
| tun-gateway socket (`/tmp/xot_tun.sock`) unreachable | xot-server `Dial` fails → CLR (cause out-of-order) sent to remote client |
| xot-gateway socket (`/tmp/xot_gwy.sock`) unreachable | xot-server or tun-gateway `Dial` fails → CLR (cause network congestion / out-of-order) sent to caller |
| All remote TCP addresses unreachable | xot-gateway exhausts IP list → CLR (cause out-of-order) sent to local caller |
| Local interface enumeration fails in `isLocalIP` | Returns `false`; destination treated as remote |

### Edge cases

| Scenario | Effect |
|---|---|
| Server `ip` is `127.0.0.1` | Detected as local on any host; traffic always routed through tun-gateway, never dialled outbound by xot-gateway |
| DNS name resolves to a mix of local and remote IPs | Any local IP causes `local = true`; the whole entry is treated as local |
| Server IP changes from remote to local (config hot-reload) | SyncRoutes removes the X.25 kernel route on next reload; subsequent calls route via tun-gateway |
| Server IP changes from local to remote (config hot-reload) | SyncRoutes installs the X.25 kernel route; subsequent outbound calls are intercepted and relayed |
| DNS-based server, prefix too short to satisfy the DNS pattern | `ResolveXotDestination` returns a match error → `local = defaultLocal`; in SyncRoutes (`defaultLocal=true`) the route is skipped |
| No servers configured | xot-server routes everything to tun-gateway (default local); xot-gateway rejects everything; tun-gateway installs no routes |
| xot-gateway receives a CALL_REQ for a local address | Rejected with CLR (cause busy); prevents routing loops |
| Multiple calls to same remote prefix in quick succession | Each call independently resolves DNS (cached 60 s); routing decisions are consistent within the cache window |

## Route installation (SyncRoutes)

`tun-gateway` maintains X.25 kernel routes only for **remote** destinations so that the kernel passes outbound CALL_REQs to the TUN interface, where tun-gateway intercepts them.

- Routes for local destinations are omitted: calls to local addresses arrive via xot-server → tun-gateway's Unix socket and do not need a kernel route.
- `defaultLocal=true` in SyncRoutes means that if locality cannot be determined (DNS error, short prefix), the route is conservatively skipped rather than installed for a potentially unreachable destination.

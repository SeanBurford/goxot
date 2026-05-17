# tun-loopback(8)

## NAME

tun-loopback — multi-TUN userland local-to-local X.25 loopback relay

## SYNOPSIS

```
sudo tun-loopback [OPTIONS]
```

## DESCRIPTION

**tun-loopback** implements a userland relay that makes local-to-local X.25
connections possible on Linux without kernel changes.

The Linux kernel's X.25 packet layer cannot loop a `connect()` back to a local
`listen()` socket on the same TUN interface: the outgoing `CALL_REQUEST` exits
via `dev_queue_xmit()` and the receive path is only entered from a device, never
from a local transmit.  Using a single TUN as a loopback also fails due to LCI
collisions (the caller's LCI is found in the kernel socket list before it can be
dispatched to the listener).

**tun-loopback** resolves this by creating one `ARPHRD_X25` TUN interface per
address configured in the `tun-loopback.routes` list.  Each TUN gets an X.25
route for its address.  A relay goroutine per TUN reads frames and copies them
to the appropriate peer TUN, remapping LCIs where necessary to prevent conflicts.

```
X.25 application A          X.25 application B
  connect("addrB")            listen("addrB")
       │                             │
  AF_X25 socket                AF_X25 socket
       │                             │
  tunlb0 (route: addrA)       tunlb1 (route: addrB)
       │                             │
       └──── tun-loopback relay ─────┘
              (reads tunlb0, writes tunlb1 and vice versa)
```

Call flow (two routes `addrA` and `addrB`):

1. Application A calls `connect({AF_X25, "addrB"})`.  Kernel allocates LCI=N on
   `tunlb0`'s neighbour and writes `CALL_REQUEST` to `tunlb0`.
2. Relay reads `CALL_REQUEST` from `tunlb0`, resolves `addrB` → `tunlb1`,
   allocates a relay LCI M on `tunlb1` (remapping if M would conflict), and
   writes the frame to `tunlb1` with LCI=M.
3. Kernel on `tunlb1` sees an unknown LCI=M inbound, finds the listener bound to
   `addrB`, creates an accepted socket `(LCI=M, nb_tunlb1)`, and writes
   `CALL_ACCEPTED` back to `tunlb1`.
4. Relay reads `CALL_ACCEPTED` from `tunlb1` (LCI=M), remaps to LCI=N, writes to
   `tunlb0`.  Application A's `connect()` returns.
5. Data and control frames are relayed symmetrically in both directions.

**LCI conflict avoidance**: the relay allocates destination-side LCIs from the
configured `lci_start`–`lci_end` range, tracking which LCIs are in use per TUN.
If an incoming LCI from the source already conflicts with an existing session on
the destination TUN, a fresh LCI is allocated from the relay range.

## OPTIONS

| Flag | Default | Description |
|---|---|---|
| `-config` | `config.json` | Path to configuration file |
| `-tun-prefix` | `tunlb` | Prefix for generated TUN interface names (`tunlb0`, `tunlb1`, …) |
| `-trace` | false | Log every forwarded packet (hex dump) |
| `-stats-port` | 0 (disabled) | Port for `/varz` HTTP statistics endpoint |

## CONFIGURATION

**tun-loopback** reads the `tun-loopback` section of `config.json`:

```json
{
  "tun-loopback": {
    "lci_start": 1024,
    "lci_end":   4095,
    "stats-port": 8004,
    "routes": ["127100", "127200"]
  }
}
```

| Key | Default | Description |
|---|---|---|
| `lci_start` | 1024 | First relay LCI (must be > kernel's allocation range, typically > 255) |
| `lci_end` | 2048 | Last relay LCI (inclusive) |
| `stats-port` | 0 | Port for `/varz` endpoint; 0 disables |
| `routes` | (required) | X.121 addresses; each entry creates one TUN interface |

Each address in `routes` becomes an exact-match X.25 route (prefix length equals
the address length).  The route points to the corresponding TUN interface.  The
kernel routes outgoing `connect()` calls via these routes.

`lci_start` and `lci_end` define the relay's LCI namespace on destination TUNs.
The kernel allocates LCIs starting from 1 for its own sockets; keeping
`lci_start` above 255 (or the kernel's maximum) prevents collisions.

## SIGNALS

`SIGINT` and `SIGTERM` cause **tun-loopback** to delete all X.25 routes, send
`TunHeaderDisconnect` to each TUN, close all TUN file descriptors, and exit.

## STATISTICS

When `stats-port` is non-zero, the process serves `expvar` metrics at
`http://0.0.0.0:<port>/varz`.  Per-interface counters (`<tunlbN>_packets_sent`,
etc.) are updated in the relay hot path.

## EXAMPLES

Two-address loopback (local listener at `127200`, caller at any address routed via
`127100`):

```json
{
  "tun-loopback": {
    "lci_start": 1024,
    "lci_end":   2048,
    "routes": ["127100", "127200"]
  }
}
```

```bash
sudo tun-loopback -config config.json

# In another terminal, start a listener:
./tun-listener -address 127200

# Connect from an X.25 application:
xotpad -g 127.0.0.1 -a 127111 127200
```

## PRIVILEGES

**tun-loopback** requires `CAP_NET_ADMIN` to:

- Create and configure `ARPHRD_X25` TUN devices
- Add and delete X.25 routes (`SIOCADDRT` / `SIOCDELRT`)
- Set the X.25 subscription (`SIOCX25SSUBSCRIP`)

Run as root or with the appropriate capability set.

## SEE ALSO

tun-gateway(8), tun-listener(8), xot-server(8), xot-gateway(8)

docs/tech/linux_x25_routing.md — design rationale for the two-TUN loopback approach

## CAVEATS

- The relay does not implement X.25 restart timer recovery; if a TUN link goes
  down mid-call, the peer TUN receives a `CLEAR_REQUEST` but no timer-driven
  retry is attempted.
- Each route in `routes` creates one TUN interface.  The kernel imposes a limit on
  the total number of TUN devices (typically 255 per uid namespace).
- Changing `routes` at runtime requires a process restart; the relay does not
  watch the config file for changes to the route list.

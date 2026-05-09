# x25_ping(1)

## NAME

x25_ping - send X.25 probe packets and measure round-trip time

## SYNOPSIS

```
x25_ping [options]
```

## DESCRIPTION

**x25_ping** establishes an X.25 virtual circuit over an XOT (X.25 over TCP,
RFC 1613) gateway and sends periodic probe packets, reporting the round-trip
time for each probe.

A CALL_REQUEST is sent at startup and the circuit is held open for the duration
of the run.  Each probe is sent once per second.  On exit the circuit is
terminated cleanly with a CLEAR_REQUEST / CLEAR_CONFIRM exchange.

The `restart` method is an exception: a RESTART_REQUEST clears all circuits on
the link, so the virtual circuit is re-established between each probe.

## OPTIONS

**-a** *address*
: Local (calling) X.25 address.  Default: `127001`.

**-d** *address*
: Destination (called) X.25 address.  Default: `127100`.

**-g** *host*[**:***port*]
: XOT gateway address.  If the port is omitted, `1998` is used.
  Default: `localhost:1998`.

**-L** *lci*
: Logical Channel Identifier used in the CALL_REQUEST and all subsequent
  packet headers.  Valid range: 1–4095.  Default: `1`.

**-m** *method*
: Probe method.  See **METHODS** below.  Default: `data`.

**-l** *bytes*
: Payload size for the `data` and `moredata` methods.  Default: `64`.

**-T** *seconds*
: Stop after this many seconds.  `0` runs indefinitely until interrupted.
  Default: `0`.

**-C** *cud*
: Call user data to include in the CALL_REQUEST.  Accepts a named alias or
  an even-length hexadecimal string.  See **CALL USER DATA** below.
  Default: none.

## METHODS

**data**
: Send an X.25 DATA packet containing **-l** bytes of `A` characters.  The
  modulo-8 send sequence number P(S) is incremented each probe.  Times the
  RR (Receive Ready) supervisory frame returned by the remote DTE.

**moredata**
: Identical to `data` but sets the M (More Data) bit in the DATA packet
  header, indicating that more data follows in the same logical record.

**irq**
: Send an X.25 INTERRUPT packet with data octet `0x01`.  Times the
  INTERRUPT_CONFIRM response.

**reset**
: Send a RESET_REQUEST (DTE-originated, diagnostic 0).  Times the
  RESET_CONFIRM response.

**restart**
: Send a RESTART_REQUEST (LCI 0, DTE-originated, diagnostic 0).  Times the
  RESTART_CONFIRM response.  Because a restart clears all virtual circuits on
  the link, the call is re-established before each probe.

## CALL USER DATA

Call user data (CUD) is appended to the CALL_REQUEST payload after the
facilities field.  The **-C** option accepts either a named alias or a
hexadecimal byte string (even number of hex digits).

| Alias   | Hex bytes  | Protocol              |
|---------|------------|-----------------------|
| `multi` | `00000000` | Multiprotocol         |
| `pad`   | `01000000` | X.3 PAD               |
| `mail`  | `c0f70000` | Electronic mail       |
| `ip`    | `cc`       | IP (RFC 1356)         |

Any value not matching a named alias is decoded as raw hexadecimal, e.g.
`-C c0f70000` is equivalent to `-C mail`.

## CALL SETUP

The CALL_REQUEST negotiates the following facilities:

- Packet size: 128 bytes (send and receive)
- Window size: 1 (send and receive)

The modulo (sequence number space) is determined by the GFI field returned in
the CALL_CONNECTED packet.  GFI=1 selects mod-8 (3-byte data packet headers,
P(S) range 0–7); GFI=2 selects mod-128 (4-byte data packet headers, P(S)
range 0–127).  Linux kernel X.25 typically returns GFI=2 (mod-128).  The
negotiated GFI is logged at call setup time.

## OUTPUT

Each successful probe prints a line to standard output:

```
probe N: LABEL rtt=RTTµs
```

Where *N* is the probe sequence number, *LABEL* describes the packet type
(e.g. `DATA(P(S)=3)`, `DATA(M,P(S)=3)`, `INTERRUPT`, `RESET_REQ`,
`RESTART_REQ`), and *RTT* is the round-trip time in microseconds measured
from just before the probe is sent to when the confirming response is received.

Diagnostic messages (call setup, clear, errors) are written to standard error
via the standard Go log package (timestamp prefix).

## EXIT STATUS

**0**
: Run completed normally (probe count reached **-T** limit or interrupted by
  signal).

**1**
: Fatal error: gateway unreachable, call setup failed, or invalid options.

## SIGNALS

**SIGINT**, **SIGTERM**
: Stop probing, print the probe count, send a CLEAR_REQUEST to tear down the
  active circuit, and exit.

## EXAMPLES

Probe with default settings (data method, localhost gateway):

```
x25_ping -d 310280
```

Send 30 seconds of IRQ probes through a remote gateway:

```
x25_ping -m irq -g xot.example.com -a 310100 -d 310280 -T 30
```

Test IP encapsulation negotiation using CUD:

```
x25_ping -d 310280 -C ip
```

Test PAD negotiation with a larger payload and specific LCI:

```
x25_ping -d 310280 -C pad -l 128 -L 42
```

Send More-Data flagged packets to exercise reassembly:

```
x25_ping -m moredata -d 310280 -l 128
```

## SEE ALSO

**xot-gateway**(1), **xot-server**(1), RFC 1613 (XOT), ITU-T X.25

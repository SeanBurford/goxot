# Data Path Technical Analysis - GoXOT

This analysis examines the data path through `xot-server` and `xot-gateway`, identifying potential bottlenecks and architectural issues that contribute to the performance degradation observed after large data transfers.

## Data Path Overview

The data path for an X.25 call through the suite involves multiple components and IPC hops:

1.  **Entry**: `xot-server` accepts an external TCP connection.
2.  **Dispatch**: `xot-server` reads the first packet (expecting a `Call Request`).
3.  **Routing**: 
    - If routed to a remote XOT server, it connects to `xot-gateway` via a `unixpacket` socket (`/tmp/xot_gwy.sock`).
    - If routed to the local TUN interface, it connects to `tun-gateway` via `/tmp/xot_tun.sock`.
4.  **Relay**: Two bidirectional relay goroutines are spawned to move packets between the source TCP connection and the gateway unix socket.
5.  **Exit**: `xot-gateway` (or `tun-gateway`) performs a similar relay between its unix socket and the final destination (TCP or TUN).

### Visual Path
`Remote DTE --(TCP XOT)--> xot-server --(Unix Socket)--> xot-gateway --(TCP XOT)--> Destination DTE`

## Identified Performance Bottlenecks

### 1. Excessive Memory Allocations and GC Pressure
The current implementation performs several allocations per packet transit:
- **`ReadXot` (TCP Path)**: Allocates a new byte slice of exactly the packet length for every read (`make([]byte, length)`).
- **`ParseX25`**: Allocates a new `X25Packet` struct for every packet to extract the LCI and Packet Type. 
- **`Serialize`**: Allocates another byte slice whenever a packet needs to be serialized for logging or forwarding to a different interface type.
- **`ReadTun` / `WriteTun`**: Perform allocations for every packet entering or leaving the TUN interface.

**Impact**: During high-throughput "large transfers", the rate of allocation triggers frequent Garbage Collection (GC) cycles. As the heap grows and fragments, the duration of these pauses increases, perceived as a slowdown in data flow.

### 2. Synchronous and Verbose Trace Logging
The `-trace` feature logs the entirety of every packet in hexdump format (`% X`).
- **Synchronicity**: Logging via the standard `log` package is synchronous and protected by a global mutex.
- **Overhead**: For a 4096-byte packet, generating and writing a hex string is computationally expensive.
- **Redundant Processing**: Every relay loop calls `ParseX25` twice if tracing is enabled (once for the trace, once for the logic).

**Impact**: Throughput is effectively capped by the speed of the logging subsystem.

### 3. Multiplexing and LCI Handling Issues
RFC 1613 allows multiple X.25 LCIs to be multiplexed over a single TCP connection. The current architecture fails to support this correctly:
- **Relay Monopolization**: Once a `Call Request` is established, the relay goroutines in `xot-server` take over the TCP connection. 
- **Packet Loss**: If a client sends a second `Call Request` or data legacy on a different LCI over the same TCP connection, the relay goroutines log a "Mismatched LCI" error and **drop the packet**.
- **Retry Storms**: Dropped packets lead to protocol-level retries and timeouts, significantly reducing effective throughput.

### 4. Logic/Session Leaks in `tun-gateway`
`tun-gateway` allocates a new `Session` and local LCI whenever it receives a packet for an LCI it hasn't seen before on a specific connection, even if it's not a `Call Request`.
- **Orphan Sessions**: If unexpected or malformed traffic reaches `tun-gateway`, it leaks sessions until the unix connection is closed.
- **Lookup Degradation**: As the `SessionManager`'s maps grow due to leaks, the time required for LCI lookups increases.

## Recommendations for Improvement

1.  **Buffer Pooling**: Extend the use of `sync.Pool` to cover the payload slices returned by `ReadXot`. Avoid `make([]byte, length)` in high-frequency loops.
2.  **In-place Parsing**: Modify `ParseX25` to work with offsets on a shared buffer rather than many small allocations.
3.  **Asynchronous Logging**: Use a buffered, non-blocking logger for trace data, and avoid hexdumping large packets unless strictly necessary.
4.  **Multiplexing Support**: Rewrite `xot-server` to handle multiple LCI relays per TCP connection using a central dispatcher rather than blocking on a single `relayWg.Wait()`.
5.  **Strict State Management**: Only create `Session` objects in response to valid `Call Request` packets or kernel-side arrivals, and implement aggressive aging for idle sessions.

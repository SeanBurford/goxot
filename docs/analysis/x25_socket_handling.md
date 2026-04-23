# X.25 Socket Handling Analysis

Analysis of goxot's socket-level interactions with the Linux X.25 kernel module against `docs/tech/linux_x25_and_tun.md`. Issues are identified with `SOCKxxx` identifiers.

Source references are to the `x25-6.12.74+deb13+1-amd64` kernel module tree and the goxot `src/` directory.

---

## SOCK001 — `x25_route_struct.Device` field undersized

**Severity**: Low (works in practice; unsafe in principle)

**Location**: `src/cmd/tun-gateway/main.go:59–63`

**Description**: The Go `x25_route_struct` defines `Device [16]byte`, but the kernel's `struct x25_route_struct` (from `include/uapi/linux/x25.h`) defines `device[200-sizeof(unsigned long)]`, which is 192 bytes on x86\_64. When `SIOCADDRT` or `SIOCDELRT` is issued, `x25_route_ioctl()` calls `copy_from_user(&rt, arg, sizeof(rt))` and reads 212 bytes from the supplied pointer (`x25_route.c:172`). The Go struct is only 36 bytes; the kernel reads 176 bytes beyond the struct from adjacent stack or heap memory.

This works in practice because:
- Network interface names are at most 15 characters (IFNAMSIZ=16 including NUL).
- The kernel only uses `rt.device` as a NUL-terminated string up to IFNAMSIZ bytes.
- Linux user-space memory pages are mapped, so the read does not fault.

The extra bytes read by the kernel are ignored, but the behaviour is technically undefined and fragile on future kernel versions or with different struct alignment.

**Reference**: `src/cmd/tun-gateway/main.go:59`, `x25_route.c:172–173`

**Suggested fix**: Define `Device [192]byte` (or use `[200-8]byte` on 64-bit) to match the kernel struct exactly, and zero-fill when setting the interface name.

---

## SOCK002 — `SIOCX25GCAUSEDIAG` constant value incorrect

**Severity**: Medium (silent misbehaviour if the IOCTL is ever called)

**Location**: `src/cmd/tun-gateway/main.go:40`

**Description**: The constant is defined as:
```go
SIOCX25GCAUSEDIAG = 0x89E4
```
The correct value is `0x89E8` (SIOCPROTOPRIVATE + 8). The value `0x89E4` is `SIOCX25GDTEFACILITIES` (SIOCPROTOPRIVATE + 4), which returns DTE network-address extension facilities, not cause/diagnostic codes.

The constant is defined but never passed to `ioctl()` in the current tun-gateway code, so there is no runtime impact. However, the incorrect value would silently return wrong data if the IOCTL were ever used.

**Reference**: `src/cmd/tun-gateway/main.go:40`, `af_x25.c:1449–1498`, `include/uapi/linux/x25.h`

**Suggested fix**: Change to `SIOCX25GCAUSEDIAG = 0x89E8`.

---

## SOCK003 — tun-listener IOCTL constants incorrect

**Severity**: High (tun-listener calls these IOCTLs and gets wrong results)

**Location**: `src/cmd/tun-listener/main.go:17–18, 101`

**Description**: Two incorrect constant values and one use of an unrelated IOCTL:

1. `SIOCX25GCALLUSERDATA = 0x89E3` — should be `0x89E6`. The value `0x89E3` is `SIOCX25SFACILITIES` (set facilities), not a read IOCTL. Calling this with a `x25_calluserdata` buffer attempts to set facilities from uninitialised memory rather than reading call user data.

2. Line 101 uses the magic value `0x89E5` (`SIOCX25SDTEFACILITIES`) to retrieve the LCI of the connected socket. There is no standard Linux AF_X25 IOCTL for reading a socket's LCI. The correct LCI can be obtained indirectly (e.g., from `/proc/net/x25` or by reading it when the CALL_REQUEST is parsed). This IOCTL silently fails or returns unrelated data.

**Reference**: `src/cmd/tun-listener/main.go:17–18, 101`, `af_x25.c:1449–1631`, `include/uapi/linux/x25.h`

**Suggested fix**:
- Change `SIOCX25GCALLUSERDATA` to `0x89E6`.
- Remove or comment out the `0x89E5` LCI query; there is no supported IOCTL for this. Use `/proc/net/x25` or pass the LCI through the application if needed.

---

## SOCK004 — `TunHeaderDisconnect` with empty payload silently ignored

**Severity**: High (link teardown events are missed)

**Location**: `src/cmd/tun-gateway/main.go:488–622`

**Description**: In `handleTunRead()`, the empty-payload guard short-circuits on `continue` before the disconnect handler is reached:

```go
if len(payload) == 0 {
    if hdr == TunHeaderConnect {
        WriteTun(tg.ifce, TunHeaderConnect, nil)
    }
    continue  // <-- TunHeaderDisconnect never reaches the handler below
}
// ... packet processing ...
if hdr == TunHeaderDisconnect {   // only reached when len(payload) > 0
    tg.closeAllSessions()
}
```

The kernel's `x25_terminate_link()` sends a single-byte frame (`X25_IFACE_DISCONNECT`) that, after the 4-byte PI header, results in exactly `[PI][0x02]` with no payload bytes. Consequently, `closeAllSessions()` is never called in response to a kernel-initiated link teardown. All sessions remain in the session manager and associated remote TCP connections are not cleared.

**Reference**: `src/cmd/tun-gateway/main.go:488–622`, `x25_dev.c:173–193` (`x25_terminate_link`)

**Suggested fix**: Add a `TunHeaderDisconnect` check inside the empty-payload branch:
```go
if len(payload) == 0 {
    if hdr == TunHeaderConnect {
        WriteTun(tg.ifce, TunHeaderConnect, nil)
    } else if hdr == TunHeaderDisconnect {
        log.Printf("TUN: Received Disconnect from kernel - cleaning up all sessions")
        tg.closeAllSessions()
    }
    continue
}
```

---

## SOCK005 — "Used by GoXOT" IOCTL table inaccurate in documentation

**Severity**: Low (documentation only)

**Location**: `docs/tech/linux_x25_and_tun.md` (prior to the fix in this commit)

**Description**: The previous documentation listed `SIOCX25GCAUSEDIAG` and `SIOCX25SFACILITIES` under "Used by GoXOT". Neither is actually called via `ioctl()` in any goxot component:

- `SIOCX25GCAUSEDIAG` is defined as a constant in tun-gateway but never passed to `ioctl()`.
- `SIOCX25SFACILITIES` is not defined anywhere in the goxot source tree.

The only IOCTLs actually invoked are `SIOCADDRT`, `SIOCDELRT` (in tun-gateway), and `SIOCX25GFACILITIES` (in tun-listener).

**Reference**: `src/cmd/tun-gateway/main.go`, `src/cmd/tun-listener/main.go`

**Status**: Fixed in `docs/tech/linux_x25_and_tun.md` (the updated version of that file corrects the table).

---

## SOCK006 — No `TunHeaderDisconnect` sent to kernel on gateway shutdown

**Severity**: Medium (kernel sockets left in undefined state after gateway exits)

**Location**: `src/cmd/tun-gateway/main.go:366–373`

**Description**: When tun-gateway receives `SIGINT` or `SIGTERM`, the signal handler calls `os.Remove(sockPath)` and `os.Exit(0)`. The TUN file descriptor is closed when the process exits, which causes the kernel to fire `NETDEV_DOWN`. However, this does not guarantee that all AF_X25 sockets are cleanly disconnected before user-space exits.

Sending `TunHeaderDisconnect (0x02)` before closing the fd ensures the kernel calls `x25_link_terminated()` → `x25_kill_by_neigh()`, which synchronously disconnects all sockets and returns `ENETUNREACH` to any waiting applications. Without this, applications blocking on `recv()` may hang until they detect the device closure through an unrelated path.

**Reference**: `src/cmd/tun-gateway/main.go:366–373`, `x25_dev.c:173–193`, `x25_link.c:250–258`

**Suggested fix**: Before `os.Exit(0)`, write `[PI][0x02]` to the TUN interface:
```go
WriteTun(ifce, TunHeaderDisconnect, nil)
```

---

## SOCK007 — `struct x25_subscrip_struct` documented with wrong field types

**Severity**: Low (documentation only; not used in goxot code)

**Location**: `docs/tech/linux_x25_and_tun.md` (prior to the fix in this commit)

**Description**: The previous documentation showed:
```c
struct x25_subscrip_struct {
    char device[200];
    unsigned int global_facil_mask;
    unsigned int extended;
};
```
The correct definition from `include/uapi/linux/x25.h` is:
```c
struct x25_subscrip_struct {
    char          device[200-sizeof(unsigned long)]; /* 192 bytes on x86_64 */
    unsigned long global_facil_mask;
    unsigned int  extended;
};
```
Both the `device` field size and the type of `global_facil_mask` were wrong.

**Status**: Fixed in `docs/tech/linux_x25_and_tun.md`.

---

## SOCK008 — Missing RESTART handshake step in connect documentation

**Severity**: Medium (incomplete implementation guide)

**Location**: `docs/tech/linux_x25_and_tun.md` (prior to the fix in this commit)

**Description**: The previous documentation documented the `TunHeaderConnect` echo but did not document the subsequent `RESTART_REQUEST` / `RESTART_CONFIRMATION` exchange that is required before the kernel enters `X25_LINK_STATE_3`. Without receiving a `RESTART_CONFIRMATION`, the kernel remains in `X25_LINK_STATE_2` and all outbound frames (including CALL_REQUESTs from connected sockets) are queued but never transmitted. The T20 timer (default 180 s) eventually retransmits the RESTART_REQUEST.

The goxot tun-gateway handles this correctly (`handleTunRead` responds to `PktTypeRestartRequest`), but the documentation did not describe it.

**Status**: Fixed in `docs/tech/linux_x25_and_tun.md` (Connect Handshake section updated, Low Level Operations added).

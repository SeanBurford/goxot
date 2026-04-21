# Interfacing with Linux X.25 and TUN Interfaces

This document describes how GoXOT interfaces with the Linux kernel's X.25 implementation via the AF_X25 socket family and TUN network devices.

## The Linux X.25 Stack

The Linux kernel provides an AF_X25 socket family that implements the X.25 Packet Layer Protocol (PLP). It can run over various link layers, including LAPB (standard serial) and TUN (virtual encapsulation).

### Socket API
Standard POSIX socket calls are used:
*   **Socket Creation**: `socket(AF_X25, SOCK_SEQPACKET, 0)`.
*   **Addressing**: Uses `struct sockaddr_x25`.

## X.25 over TUN (ARPHRD_X25)

The `tun-gateway` interfaces with the kernel by creating a TUN device and setting its link type to `ARPHRD_X25` (value 271). This tells the kernel to treat the interface as a native X.25 packet device.

### Encapsulation and Handshake
Packets exchanged with the TUN device include a 4-byte PI header (`[0x00, 0x00, 0x08, 0x05]`) followed by a 1-byte control header.

#### Control Headers
The following headers are defined (source: `net/x25/x25_dev.c` and `drivers/net/wan/x25_asy.c` logic):

| Value | Name | Purpose |
| :--- | :--- | :--- |
| `0x00` | `TunHeaderData` | Standard X.25 PLP packet data follows. |
| `0x01` | `TunHeaderConnect` | Link Layer (L2) connection request/ack. |
| `0x02` | `TunHeaderDisconnect` | Link Layer (L2) disconnection. |
| `0x03` | `TunHeaderParam` | Exchange of link parameters. |

#### The Connect Handshake
When the kernel's X.25 stack initializes an association on an interface (e.g., when a socket is bound or a route is activated), it sends a `TunHeaderConnect (0x01)`. The gateway **must** respond with `0x01` to acknowledge the link is "UP".

#### Observed Behaviours

A critical observed behaviour in the Linux X.25 implementation over TUN is the requirement for the DTE (Gateway) to explicitly respond to the `TunHeaderConnect (0x01)` handshake. 

When the kernel initializes an X.25 association or prepares to transmit data (often triggered by routing a call or binding a socket to the interface), it may issue a `TunHeaderConnect` with an empty payload. The gateway **must** respond with an identical `TunHeaderConnect` packet to the kernel. Failure to provide this acknowledgement prevents the kernel from transitioning the interface to a synchronized state, blocking subsequent data transfer.

Additionally, the gateway must manage session state based on X.25 Control Packets:
*   **Session Cleanup**: Upon receiving a `PktTypeClearRequest` from the TUN interface, the gateway must remove the associated LCI mapping to free resources and prevent state desynchronization.
*   **Interface Shutdown**: Receipt of a `TunHeaderDisconnect (0x02)` signals a link-layer teardown, and the gateway should immediately close all active sessions associated with that interface.

This requirement is handled in `tun-gateway` within the `handleTunRead` loop:
```go
if hdr == TunHeaderConnect {
    WriteTun(tg.ifce, TunHeaderConnect, nil)
} else if hdr == TunHeaderDisconnect {
    tg.closeAllSessions()
}
```

#### The Disconnect Handshake
The kernel sends `TunHeaderDisconnect (0x02)` when the interface is brought down or the link is to be terminated. 
*   **Fact**: This signals a Link Layer (L2) disconnect.
*   **Superseding L3 Disconnect**: While standard X.25 `CLR_REQ` (L3) packets are used for individual sessions, a `TunHeaderDisconnect` (L2) immediately terminates the entire logical link. If GoXOT receives `0x02`, it should assume all sessions on that interface are implicitly cleared by the kernel. However, for graceful cleanup, GoXOT should still relay any pending L3 `CLR_REQ` packets if the link was still viable.

## Linux X.25 IOCTLs

The module supports several IOCTLs for management.

### Used by GoXOT
| IOCTL | Structure | Description |
| :--- | :--- | :--- |
| `SIOCADDRT` | `x25_route_struct` | Add a prefix-based route to an interface. |
| `SIOCDELRT` | `x25_route_struct` | Remove a route. |
| `SIOCX25GCAUSEDIAG` | `x25_causediag` | Get the last received Cause/Diag codes. |
| `SIOCX25SFACILITIES` | `x25_facilities` | Set requested facilities for a socket. |

### Available but Unused
| IOCTL | Structure | Description |
| :--- | :--- | :--- |
| `SIOCX25GSUBSCRIP` | `x25_subscrip_struct` | Get interface-wide subscription limits (LCI ranges). |
| `SIOCX25SSUBSCRIP` | `x25_subscrip_struct` | Set LCI ranges and global facility masks. |
| `SIOCX25GCALLUSERDATA`| `unsigned char[128]` | Get the Call User Data from an incoming call. |
| `SIOCX25SCALLUSERDATA`| `unsigned char[128]` | Set Call User Data for an outgoing call request. |
| `SIOCX25SCAUSEDIAG`| `x25_causediag` | Set the Cause/Diag for an outgoing Clear packet. |
| `SIOCX25GTCPBIND` | - | Older patch-set IOCTL for bridging to TCP sockets. |

## Data Structures

### `struct x25_address`
```c
struct x25_address {
    char x25_addr[16]; // NUL-terminated ASCII string of digits
};
```

### `struct x25_facilities`
```c
struct x25_facilities {
    unsigned int winsize_in, winsize_out;
    unsigned int pacsize_in, pacsize_out;
    unsigned int throughput;
    unsigned int reverse;
};
```
Note: In Linux, packet sizes in facilities are often handled as powers of 2 (e.g., 9 for 512).

### `struct x25_subscrip_struct`
```c
struct x25_subscrip_struct {
    char device[200];
    unsigned int global_facil_mask;
    unsigned int extended;
};
```

## References
* `man 7 x25`: Linux X.25 protocol implementation.
* Linux Kernel: `net/x25/af_x25.c`
* Linux Kernel: `include/uapi/linux/x25.h`
* Linux Kernel: `drivers/net/wan/x25_asy.c` (header definitions 0x01/0x02)

# X.25 Cause and Diagnostic Codes

When an X.25 logical channel is cleared, reset, or restarted, the packet includes a Cause code and an optional Diagnostic code to specify the reason for the action.

## Cause Codes

Cause codes indicate why a particular procedure (Clear, Reset, Restart) was initiated.

### Clear Request Causes (Annex E)
These codes appear in the `CLR_REQ` packet during call clearing.

| Code (Hex) | Meaning | When to Send / Expect |
| :--- | :--- | :--- |
| `0x00` | **DTE Originated** | Sent by a DTE to initiate normal call clearing. (State p4 -> p5) |
| `0x01` | **Number Busy** | Sent by the network/gateway when the destination address is busy. |
| `0x03` | **Invalid Facility Request** | Sent when an unsupported facility is requested in a Call Request. |
| `0x05` | **Network Congestion** | Sent when resources are exhausted. (State p2) |
| `0x09` | **Out of Order** | Sent when the destination DTE is not responding. |
| `0x0B` | **Access Barred** | Sent when a security or policy check fails. |
| `0x42` | **Local Procedure Error** | Sent when a protocol violation occurs (e.g., packet too long). |

### Reset Request Causes (Annex F)
These codes appear in `RESET_REQ` packets.

| Code (Hex) | Meaning |
| :--- | :--- |
| `0x00` | DTE Originated (Normal reset) |
| `0x01` | Out of Order (PVC only) |
| `0x03` | Remote Procedure Error |

### Restart Request Causes (Annex H)
These codes appear in `RESTART_REQ` packets.

| Code (Hex) | Meaning |
| :--- | :--- |
| `0x00` | Local Procedure Error |
| `0x01` | Network Congestion |
| `0x03` | Network Operational |

## Diagnostic Codes (Annex G)

Diagnostic codes provide additional detail for the cause. They are common across Clear, Reset, and Restart packets.

| Code (Decimal) | Meaning | Context in GoXOT |
| :--- | :--- | :--- |
| **0** | No additional information | Default fallback. |
| **33** (0x21) | Invalid P(S) | Sequence error. |
| **34** (0x22) | Invalid P(R) | Sequence error. |
| **39** (0x27) | **Packet Too Long** | Calculated by the gateway if the payload exceeds negotiated maximums. |
| **65** (0x41) | Prefix not allowed | Addressing error. |
| **66** (0x42) | Facility not allowed | Security policy. |

## Observed Cause/Diagnostic Usage Comparison

This table tracks how different implementations use specific Cause and Diagnostic pairs in practice.

| Cause / Diag (Hex) | GoXOT Usage | Linux Kernel Usage | Cisco Usage |
| :--- | :--- | :--- | :--- |
| **0x00 / 0x00** | Normal call clearing relay. | Socket `close()` called by user. | - |
| **0x42 / 0x27** | Enforcing Max X.25 packet size limits. | Internal protocol violation or hardware MTU mismatch. | - |
| **0x05 / 0x00** | LCI mapping table exhaustion. | No free logical channels available in kernel. | - |
| **0x09 / 0x00** | Session cleanup notification during gateway shutdown. | Link Layer failure or Interface `IFF_DOWN`. | - |
| **0x01 / 0x00** | Routing failure: Called X.121 prefix not found in config or destination unreachable. | Target address prefix not found in routing table. | - |
| **0x03 / 0x00** | Malformed facility request in CALL_REQ or peer rejected negotiated facilities. | Facility negotiation failure during `connect()`. | - |

## Implementation in GoXOT

GoXOT generates Cause and Diagnostic codes primarily when enforcing protocol constraints or when a destination becomes unreachable.

Example generating a Local Procedure Error due to size:
```go
clr := xot.CreateClearRequest(pkt.LCI, xot.CauseLocalProcedureError, xot.DiagPacketTooLong)
```

### Observed Cause/Diag Retrieval
On Linux, the `SIOCX25GCAUSEDIAG` IOCTL can be used to retrieve these codes from the kernel after a socket error. GoXOT uses this in the `tun-gateway` to log the reason for kernel-initiated clears.

## References
* ITU-T Recommendation X.25 (10/96) - Annex E: "Clear cause codes".
* ITU-T Recommendation X.25 (10/96) - Annex F: "Reset cause codes".
* ITU-T Recommendation X.25 (10/96) - Annex G: "Diagnostic codes".
* ITU-T Recommendation X.25 (10/96) - Annex H: "Restart cause codes".

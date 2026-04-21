# X.25 Facilities

X.25 Facilities allow for the negotiation of connection parameters such as packet size, window size, and throughput class. This document details the facilities supported by GoXOT.

For Linux-specific details on how facilities are handled via kernel socket IOCTLs, see [linux_x25_and_tun.md](linux_x25_and_tun.md).

## Supported Facilities

| Facility | Code (Hex) | Class | Supported | Limitations | Reference (X.25) |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Packet Size** | `0x42` | B | Yes | Supported as powers of 2. | Section 7.2.2.2 |
| **Window Size** | `0x43` | B | Yes | Modulo 8 only; Extended window sizes (Class C) NOT supported. | Section 7.2.2.3 |
| **Throughput Class** | `0x02` | A | No | Ignored by gateway relay. | Section 7.2.2.1 |
| **Closed User Group (CUG)** | `0x03` | A | No | | Section 6.1 |
| **Fast Select** | `0x12` | A | No | Handled as standard data if present in Call Request. | Section 6.16 |

## Facility Classes

ITU X.25 Section 7.1.5 defines four classes of facilities based on the length of their parameter field:
*   **Class A**: Length = 1 byte.
*   **Class B**: Length = 2 bytes.
*   **Class C**: Length = 3 bytes.
*   **Class D**: Variable length.

GoXOT's `FormatFacilities` and `ParseCallRequest` functions support identifying facilities by class to correctly skip or parse them.

```go
class := code >> 6
switch class {
    case 0: valLen = 1 // Class A
    case 1: valLen = 2 // Class B
    case 2: valLen = 3 // Class C
    case 3: valLen = int(fac[i+1]); i++ // Class D
}
```

## Implementation Details

### Packet Size (`0x42`)
The parameter field consists of two bytes:
1. Requested packet size from the calling DTE (expressed as log2).
2. Requested packet size from the called DTE (expressed as log2).

GoXOT translates these during logging: `pkt:512/512` represents the 9th power of 2.

### Window Size (`0x43`)
The parameter field consists of two bytes:
1. Requested window size for the direction from the calling DTE.
2. Requested window size for the direction from the called DTE.

Standard window sizes range from 1 to 7 for Modulo 8.

## References
* ITU-T Recommendation X.25 (10/96) - Section 7: "Packet layer optional user facilities".
* ITU-T Recommendation X.25 (10/96) - Section 6: "Description of optional user facilities".

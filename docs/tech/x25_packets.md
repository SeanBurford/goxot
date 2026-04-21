# X.25 Packet Formats

This document describes the X.25 Packet Layer Protocol (PLP) packet formats as implemented in GoXOT.

## Supported Packet Types

| Type | ID (Hex) | Supported | Limitations | Reference (X.25) |
| :--- | :--- | :--- | :--- | :--- |
| **CALL_REQ** | `0x0B` | Yes | Extended addressing (BCD > 15 digits) NOT supported; Basic facilities only. | Section 5.2.1 |
| **CALL_CONN** | `0x0F` | Yes | Limited facilities support. | Section 5.2.2 |
| **CLR_REQ** | `0x13` | Yes | Basic cause/diag support. | Section 5.2.3 |
| **CLR_CONF** | `0x17` | Yes | | Section 5.2.4 |
| **DATA** | `Bit 0=0` | Yes | Standard GFI 0x1 (Modulo 8) supported. | Section 5.3 |
| **RR** | `0x01` | Yes | Flow control. | Section 5.4.1 |
| **RNR** | `0x05` | Yes | Flow control. | Section 5.4.2 |
| **REJ** | `0x09` | Yes | Flow control. | Section 5.4.3 |
| **RESET_REQ** | `0x1B` | Partial | Parsed, but often triggers session termination. | Section 5.5.1 |
| **RESET_CONF** | `0x1F` | Partial | | Section 5.5.2 |
| **RESTART_REQ** | `0xFB` | Yes | Handled primarily at the TUN interface level. | Section 5.5.3 |
| **RESTART_CONF** | `0xFF` | Yes | | Section 5.5.4 |
| **DIAG** | `0xF1` | Yes | Logged but no automatic action taken. | Section 5.6.1 |
| **REG_REQ** | `0xF3` | No | Registration procedures NOT implemented. | Section 5.7.1 |
| **REG_CONF** | `0xF7` | No | | Section 5.7.2 |

## General Packet Structure

All X.25 PLP packets share a 3-byte common header followed by a variable payload.

### Common Header
1. **Byte 0**: 
   * Bits 7-4: **GFI** (General Format Identifier). GoXOT defaults to `0x01` (Modulo 8, no D-bit).
   * Bits 3-0: **LCI High** (Logical Channel Identifier bits 11-8).
2. **Byte 1**: **LCI Low** (Logical Channel Identifier bits 7-0).
3. **Byte 2**: **Packet Type Identifier**.

### Payload
The payload structure depends on the packet type:
* **Call Request/Connected**: Address Lengths (1 byte), Addresses (BCD), Facility Length (1 byte), Facilities, User Data.
* **Clear Request**: Cause (1 byte), Diagnostic (1 byte).
* **Data**: User data payload starts immediately after the 3-byte header.

## XOT Encapsulation (RFC 1613 Section 4.1)

When transmitted over TCP (XOT), each packet is preceded by a 4-octet header in Network Byte Order.

| Field | Size | Description |
| :--- | :--- | :--- |
| **Version** | 2 bytes | Currently set to `0x0000`. |
| **XOT Length** | 2 bytes | Length of the following X.25 packet (header + payload). |
| **X.25 Packet** | Variable | Standard X.25 PLP packet data. |

## Addressing Limitations

The current BCD (Binary Coded Decimal) decoder in `x25.go` assumes standard addressing. Extended addressing formats (ITU-T X.25 Section 5.2.1.1.1) are not currently supported. Address lengths are limited to 15 digits as per the 4-bit length fields.

## References
* ITU-T Recommendation X.25 (10/96) - Section 5: "Packet formats".
* ITU-T Recommendation X.25 Corrigendum 1 (09/98).

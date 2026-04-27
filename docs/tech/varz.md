# Varz

xot-server, tun-gateway and xot-gateway each support export of counters over varz.

If the servers are started with `--stats-port=12345` or have a `stats-port` specified in their configuration, the varz service will be started on that port.

## Variables

| Name               | Type  | Unit          | Description                       |
| ----               | ----  | ----          | -----------                       |
| `uptime`           | int64 | Second        | Time since startup                |
| `threads_active`   | map   | Thread count  | Active thread names               |
| `dns_requests`     | int64 | Request count | Number of DNS requests made       |
| `packets_handled`  | map   | Packet count  | Number of packets handled by type |
| `packets_handled`  | map   | Packet count  | Number of packets handled by type |
| `causes_received`  | map   | Packet count  | Number of errors received by type |
| `causes_generated` | map   | Packet count  | Number of errors sent by type     |

## Interface specific variables

Each of these variables is a map of interface name to int64 count:

| Name                         | Description                                    |
| ----                         | -----------                                    |
| `interface_sessions_opened`  | Sessions initiated on the link layer           |
| `interface_sessions_closed`  | Sessions terminated on the link layer          |
| `interface_call_request`     | Call request packets seen (either direction)   |
| `interface_call_connected`   | Call connected packets seen (either direction) |
| `interface_clear_request`    | Clear request packets seen (either direction)  |
| `interface_clear_confirm`    | Clear confirm packets seen (either direction)  |
| `interface_packets_sent`     | Packets sent                                   |
| `interface_packets_received` | Packets received                               |
| `interface_bytes_sent`       | Bytes sent                                     |
| `interface_bytes_received`   | Bytes received                                 |

### Interface names

Interface names are server dependent:

xot-server:

*  `xot`: Inbound sessions from the network.
*  `tun`: Outbound sessions to the tun-gateway.
*  `xot_fwd`: Outbound sessions to the xot-gateway.

tun-gateway:

*  `tun`: Inbound/outbound sessions to the tun device.
*  `xot`: Outbound sessions to the xot-gateway.
*  `unix`: Inbound sessions from the xot-server.

xot-gateway:

*  `unix`: Inbound sessions from the xot-server or the tun-gateway.
*  `xot`: Outbound sessions to the network.

# XOT-GATEWAY(1)

## NAME
xot-gateway - X.25 over TCP (XOT) Outbound Gateway

## SYNOPSIS
**xot-gateway** [**-config** *path*] [**-trace**] [**-graceperiod** *seconds*]

## DESCRIPTION
**xot-gateway** handles outbound XOT connections. It listens on a Unix domain socket for X.25 packets from internal components (like **xot-server** or **tun-gateway**) and establishes outbound TCP connections to remote XOT servers.

It performs routing based on the called X.25 address using the provided configuration file. It supports both static IP destinations and dynamic DNS resolution based on X.121 address patterns.

## DNS RESOLUTION
When a server entry in `config.json` uses `dns_name` and `dns_pattern`, **xot-gateway** will:
1. Apply the `dns_pattern` regex to the called X.121 address.
2. Substitute matching groups into the `dns_name` template (e.g., `\1`, `\2`).
3. Resolve the resulting hostname via DNS A/AAAA records.
4. Attempt to connect to each resolved IP address in sequence.

DNS results are cached for 60 seconds.

## OPTIONS
**-config** *path*
    Path to the JSON configuration file containing remote server definitions. Default is "config.json".

**-trace**
    Enable detailed trace logging of all X.25 packets passing through the gateway.

**-graceperiod** *seconds*
    The number of seconds to wait for active connections to finish during a graceful shutdown. Default is 5.

## FILES
*/tmp/xot_gwy.sock*
    Unix domain socket where **xot-gateway** listens for internal relay requests.

## SEE ALSO
**xot-server**(1), **tun-gateway**(1)

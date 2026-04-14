# XOT-SERVER(1)

## NAME
xot-server - RFC 1613 X.25 over TCP (XOT) Relay Server

## SYNOPSIS
**xot-server** [**-listen** *address*] [**-config** *path*] [**-trace**] [**-graceperiod** *seconds*]

## DESCRIPTION
**xot-server** is the primary entry point for incoming XOT connections. It listens for TCP connections on port 1998 (by default) and relays X.25 packets to either **tun-gateway** or **xot-gateway** based on the called X.25 address.

It implements bidirectional relaying between the external XOT client and the internal Unix domain sockets used by the gateways.

## OPTIONS
**-listen** *address*
    The IP address and port to listen for incoming XOT connections. Default is "0.0.0.0:1998".

**-config** *path*
    Path to the JSON configuration file containing routing information. Default is "config.json".

**-trace**
    Enable detailed trace logging of all X.25 packets passing through the server.

**-graceperiod** *seconds*
    The number of seconds to wait for active connections to finish during a graceful shutdown (SIGHUP). Default is 5.

## SIGNALS
**SIGHUP**
    Triggers a graceful shutdown. The server stops accepting new connections and waits for active ones to close or for the grace period to expire.

**SIGINT, SIGTERM**
    Triggers an immediate shutdown, closing all active connections.

## FILES
*/tmp/xot_tun.sock*
    Unix domain socket for communicating with **tun-gateway**.

*/tmp/xot_gwy.sock*
    Unix domain socket for communicating with **xot-gateway**.

## SEE ALSO
**tun-gateway**(1), **xot-gateway**(1)

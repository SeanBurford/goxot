package xot

import (
	"expvar"
	"fmt"
	"net/http"
	"time"
)

var (
	StartTime = time.Now()

	ThreadsActive = expvar.NewMap("threads_active")

	DnsRequests = expvar.NewInt("dns_requests")

	PacketsHandled  = expvar.NewMap("packets_handled")
	CausesReceived  = expvar.NewMap("causes_received")
	CausesGenerated = expvar.NewMap("causes_generated")

	// Interface-specific stats
	InterfaceSessionsOpened  = expvar.NewMap("interface_sessions_opened")
	InterfaceSessionsClosed  = expvar.NewMap("interface_sessions_closed")
	InterfaceCallRequest     = expvar.NewMap("interface_call_request")
	InterfaceCallConnected   = expvar.NewMap("interface_call_connected")
	InterfaceClearRequest    = expvar.NewMap("interface_clear_request")
	InterfaceClearConfirm    = expvar.NewMap("interface_clear_confirm")
	InterfacePacketsSent     = expvar.NewMap("interface_packets_sent")
	InterfacePacketsReceived = expvar.NewMap("interface_packets_received")
	InterfaceBytesSent       = expvar.NewMap("interface_bytes_sent")
	InterfaceBytesReceived   = expvar.NewMap("interface_bytes_received")
)

func StartStatsServer(port int) {
	if port == 0 {
		return
	}
	// Also expose on /varz as requested
	http.Handle("/varz", expvar.Handler())
	go func() {
		addr := fmt.Sprintf(":%d", port)
		fmt.Printf("Stats server listening on %s\n", addr)
		// We use http.ListenAndServe which uses DefaultServeMux
		// expvar already registers /debug/vars on DefaultServeMux
		if err := http.ListenAndServe(addr, nil); err != nil {
			fmt.Printf("Stats server failed: %v\n", err)
		}
	}()
}

func init() {
	expvar.Publish("uptime", expvar.Func(func() interface{} {
		return int64(time.Since(StartTime).Seconds())
	}))
}

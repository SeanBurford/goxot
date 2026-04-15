package xot

import (
	"expvar"
	"fmt"
	"net/http"
)

var (
	SessionsOpened = expvar.NewInt("sessions_opened")
	SessionsClosed = expvar.NewInt("sessions_closed")

	PacketsHandled = expvar.NewMap("packets_handled")
	ThreadStarts   = expvar.NewMap("thread_starts")

	CallsSent     = expvar.NewMap("calls_sent")
	CallsReceived = expvar.NewMap("calls_received")

	BytesSent     = expvar.NewMap("bytes_sent")
	BytesReceived = expvar.NewMap("bytes_received")

	DnsRequests = expvar.NewInt("dns_requests")

	CausesReceived  = expvar.NewMap("causes_received")
	CausesGenerated = expvar.NewMap("causes_generated")
)

func StartStatsServer(port int) {
	if port == 0 {
		return
	}
	// Also expose on /varz as requested
	http.Handle("/varz", expvar.Handler())
	go func() {
		addr := fmt.Sprintf(":%d", port)
		// We use http.ListenAndServe which uses DefaultServeMux
		// expvar already registers /debug/vars on DefaultServeMux
		if err := http.ListenAndServe(addr, nil); err != nil {
			fmt.Printf("Stats server failed: %v\n", err)
		}
	}()
}

package xot

import (
	"expvar"
	"fmt"
	"net/http"
	"time"
)

var (
	// StartTime records when the process started.
	StartTime = time.Now()

	// ThreadsActive tracks the number of active goroutines by name.
	ThreadsActive = expvar.NewMap("threads_active")

	// DNSRequests counts the total number of DNS lookups performed.
	DNSRequests = expvar.NewInt("dns_requests")

	// PacketsHandled counts packets processed, keyed by packet type name.
	PacketsHandled = expvar.NewMap("packets_handled")
	// CausesReceived counts X.25 clear causes received, keyed by cause code.
	CausesReceived = expvar.NewMap("causes_received")
	// CausesGenerated counts X.25 clear causes generated locally, keyed by reason.
	CausesGenerated = expvar.NewMap("causes_generated")
)

// InterfaceSessionsOpened and related vars track per-interface session and packet statistics.
var (
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

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// StartStatsServer starts an HTTP server exposing expvar metrics at /debug/vars and /varz.
func StartStatsServer(addr string) {
	if addr == "" {
		return
	}
	// Also expose on /varz as requested
	http.Handle("/varz", corsMiddleware(expvar.Handler()))
	go func() {
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

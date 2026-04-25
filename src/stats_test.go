package xot

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestStartStatsServerZeroPort(t *testing.T) {
	// port == 0 is a no-op; verify no panic
	StartStatsServer(0)
}

func TestStartStatsServerEnabled(t *testing.T) {
	// Find a free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	StartStatsServer(port)

	// Give the goroutine time to start
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/varz")
	if err != nil {
		t.Fatalf("GET /varz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	bodyStr := string(body)
	if len(bodyStr) == 0 {
		t.Errorf("Expected non-empty /varz response")
	}
}

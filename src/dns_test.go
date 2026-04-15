package xot

import (
	"testing"
)

func TestResolveXotDestination(t *testing.T) {
	// This test will likely fail in this environment because of actual DNS lookups,
	// but we can at least test the pattern substitution logic if we had a way to mock net.LookupHost.
	// For now, let's just test that it correctly identifies IP vs DNS.

	ips, err := ResolveXotDestination("123456", &XotServerConfig{IP: "1.2.3.4"})
	if err != nil {
		t.Errorf("ResolveXotDestination failed for IP: %v", err)
	}
	if len(ips) != 1 || ips[0] != "1.2.3.4" {
		t.Errorf("Expected [1.2.3.4], got %v", ips)
	}

	// We won't test the actual DNS lookup here as it depends on external state.
}

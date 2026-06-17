package xot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigManager(t *testing.T) {
	filename := "test_config.json"
	content := `{
		"tun-gateway": {"lci_start": 10, "lci_end": 20},
		"xot-gateway": {},
		"xot-server": {"stats-port": 12345},
		"servers": [{"prefix": "123/3", "ip": "1.1.1.1"}]
	}`
	err := os.WriteFile(filename, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(filename)

	cm, err := NewConfigManager(filename)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	tun := cm.GetTunGatewayConfig()
	if tun.LciStart != 10 || tun.LciEnd != 20 {
		t.Errorf("Expected TUN LCI 10-20, got %d-%d", tun.LciStart, tun.LciEnd)
	}

	xgw := cm.GetXotGatewayConfig()
	if xgw.StatsPort != "" {
		t.Errorf("Expected XOT Gateway stats-port empty, got %q", xgw.StatsPort)
	}

	xsr := cm.GetXotServerConfig()
	if xsr.StatsPort != ":12345" {
		t.Errorf("Expected XOT Server stats-port ':12345', got %q", xsr.StatsPort)
	}

	srv, _ := cm.GetServer("12345", false)
	if srv == nil {
		t.Errorf("GetServer failed to find matching server")
	} else if srv.IP != "1.1.1.1" {
		t.Errorf("Expected IP 1.1.1.1, got %s", srv.IP)
	}

	// Test DNS validation
	dnsContent := `{
		"servers": [
			{"prefix": "456/3", "dns_name": "example.org"},
			{"prefix": "789/3", "dns_pattern": "^(...)", "dns_name": "\\1.example.org"}
		]
	}`
	os.WriteFile(filename, []byte(dnsContent), 0644)

	// Force mod time change for reload
	now := time.Now().Add(time.Second)
	os.Chtimes(filename, now, now)

	cm.Reload()

	srv, _ = cm.GetServer("45678", false)
	if srv == nil {
		t.Errorf("Failed to find DNS server 456")
	} else {
		if srv.DNSPattern != "^(...)(...)" {
			t.Errorf("Expected default DNS pattern, got %s", srv.DNSPattern)
		}
	}

	srv, _ = cm.GetServer("78901", false)
	if srv == nil {
		t.Errorf("Failed to find DNS server 789")
	} else {
		if srv.DNSPattern != "^(...)" {
			t.Errorf("Expected custom DNS pattern, got %s", srv.DNSPattern)
		}
	}
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	return path
}

func TestGetServers(t *testing.T) {
	path := writeConfigFile(t, `{
		"servers": [
			{"prefix": "111/3", "ip": "1.1.1.1", "port": 1998},
			{"prefix": "222/3", "ip": "2.2.2.2", "port": 1998}
		]
	}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	servers := cm.GetServers()
	if len(servers) != 2 {
		t.Fatalf("Expected 2 servers, got %d", len(servers))
	}
	// Verify these are copies (not pointers into internal state)
	servers[0].IP = "modified"
	fresh := cm.GetServers()
	if fresh[0].IP == "modified" {
		t.Error("GetServers returned a reference, not a copy")
	}
}

func TestConfigServerBothIPAndDNS(t *testing.T) {
	path := writeConfigFile(t, `{
		"servers": [
			{"prefix": "123/3", "ip": "1.1.1.1", "dns_name": "example.org"}
		]
	}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	servers := cm.GetServers()
	if len(servers) != 0 {
		t.Errorf("Expected 0 servers (both IP and DNS), got %d", len(servers))
	}
}

func TestConfigServerNeitherIPNorDNS(t *testing.T) {
	path := writeConfigFile(t, `{
		"servers": [
			{"prefix": "123/3", "port": 1998}
		]
	}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	servers := cm.GetServers()
	if len(servers) != 0 {
		t.Errorf("Expected 0 servers (neither IP nor DNS), got %d", len(servers))
	}
}

func TestConfigDNSMissingName(t *testing.T) {
	path := writeConfigFile(t, `{
		"servers": [
			{"prefix": "123/3", "dns_pattern": "^(...)(.*)"}
		]
	}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	servers := cm.GetServers()
	if len(servers) != 0 {
		t.Errorf("Expected 0 servers (dns_pattern but no dns_name), got %d", len(servers))
	}
}

func TestConfigDNSDefaultPattern(t *testing.T) {
	path := writeConfigFile(t, `{
		"servers": [
			{"prefix": "12/2", "dns_name": "\\1.example.com"}
		]
	}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	srv, _ := cm.GetServer("12345", false)
	if srv == nil {
		t.Fatal("Expected server, got nil")
	}
	if srv.DNSPattern != "^(...)(...)" {
		t.Errorf("Expected default DNS pattern '^(...)(...)', got %q", srv.DNSPattern)
	}
}

func TestGetServerLongestPrefix(t *testing.T) {
	path := writeConfigFile(t, `{
		"servers": [
			{"prefix": "12/2",   "ip": "1.0.0.1"},
			{"prefix": "1234/4", "ip": "1.0.0.4"},
			{"prefix": "123/3",  "ip": "1.0.0.3"}
		]
	}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	cases := []struct {
		addr string
		ip   string
	}{
		{"12000", "1.0.0.1"},
		{"12300", "1.0.0.3"},
		{"12345", "1.0.0.4"},
	}
	for _, c := range cases {
		srv, _ := cm.GetServer(c.addr, false)
		if srv == nil {
			t.Errorf("GetServer(%q) returned nil", c.addr)
		} else if srv.IP != c.ip {
			t.Errorf("GetServer(%q): got %s, want %s", c.addr, srv.IP, c.ip)
		}
	}
}

func TestGetServerLocality(t *testing.T) {
	path := writeConfigFile(t, `{
		"servers": [
			{"prefix": "100/3", "ip": "127.0.0.1"},
			{"prefix": "200/3", "ip": "192.0.2.1"}
		]
	}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	srv, local := cm.GetServer("10000", false)
	if srv == nil {
		t.Fatal("expected server for 100xx")
	}
	if !local {
		t.Error("127.0.0.1 should be detected as local")
	}

	srv, local = cm.GetServer("20000", false)
	if srv == nil {
		t.Fatal("expected server for 200xx")
	}
	if local {
		t.Error("192.0.2.1 (TEST-NET) should not be detected as local")
	}

	// No server: local flag should reflect defaultLocal
	srv, local = cm.GetServer("99999", true)
	if srv != nil {
		t.Error("expected nil server for unmatched address")
	}
	if !local {
		t.Error("defaultLocal=true should be returned when no server matches")
	}

	srv, local = cm.GetServer("99999", false)
	if srv != nil {
		t.Error("expected nil server for unmatched address")
	}
	if local {
		t.Error("defaultLocal=false should be returned when no server matches")
	}
}

func TestConfigInvalidJSON(t *testing.T) {
	path := writeConfigFile(t, `{invalid json`)

	_, err := NewConfigManager(path)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestConfigManagerRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	cm, err := NewConfigManager(path)
	if err == nil {
		t.Error("Expected error for missing config file")
	}
	if cm == nil {
		t.Fatal("NewConfigManager must return non-nil cm even when config is absent")
	}
	if cm.GetServers() != nil {
		t.Error("Expected nil servers before config exists")
	}

	if err := os.WriteFile(path, []byte(`{"servers": [{"prefix": "1/1", "ip": "1.2.3.4"}]}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := cm.Reload(); err != nil {
		t.Fatalf("Reload after config created: %v", err)
	}
	if servers := cm.GetServers(); len(servers) != 1 {
		t.Errorf("Expected 1 server after reload, got %d", len(servers))
	}
}

func TestConfigLCIDefaults(t *testing.T) {
	path := writeConfigFile(t, `{"servers": []}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	tun := cm.GetTunGatewayConfig()
	if tun.LciStart != 1024 {
		t.Errorf("Expected LciStart=1024, got %d", tun.LciStart)
	}
	if tun.LciEnd != 2048 {
		t.Errorf("Expected LciEnd=2048, got %d", tun.LciEnd)
	}
}

func TestConfigModuloDefault(t *testing.T) {
	path := writeConfigFile(t, `{"servers": []}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	gw := cm.GetTunGatewayConfig()
	if gw.Modulo != 8 {
		t.Errorf("tun-gateway: expected default modulo 8, got %d", gw.Modulo)
	}
	lb := cm.GetTunLoopbackConfig()
	if lb.Modulo != 8 {
		t.Errorf("tun-loopback: expected default modulo 8, got %d", lb.Modulo)
	}
}

func TestConfigModuloValid(t *testing.T) {
	path := writeConfigFile(t, `{
		"tun-gateway":  {"modulo": 128},
		"tun-loopback": {"modulo": 8}
	}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	gw := cm.GetTunGatewayConfig()
	if gw.Modulo != 128 {
		t.Errorf("tun-gateway: expected modulo 128, got %d", gw.Modulo)
	}
	lb := cm.GetTunLoopbackConfig()
	if lb.Modulo != 8 {
		t.Errorf("tun-loopback: expected modulo 8, got %d", lb.Modulo)
	}
}

func TestConfigModuloInvalid(t *testing.T) {
	path := writeConfigFile(t, `{
		"tun-gateway":  {"modulo": 64},
		"tun-loopback": {"modulo": 7}
	}`)

	cm, err := NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager failed: %v", err)
	}

	gw := cm.GetTunGatewayConfig()
	if gw.Modulo != 8 {
		t.Errorf("tun-gateway: invalid modulo should default to 8, got %d", gw.Modulo)
	}
	lb := cm.GetTunLoopbackConfig()
	if lb.Modulo != 8 {
		t.Errorf("tun-loopback: invalid modulo should default to 8, got %d", lb.Modulo)
	}
}

package xot

import (
	"os"
	"testing"
	"time"
	"path/filepath"
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
	if xgw.StatsPort != 0 {
		t.Errorf("Expected XOT Gateway stats-port 0, got %d", xgw.StatsPort)
	}

	xsr := cm.GetXotServerConfig()
	if xsr.StatsPort != 12345 {
		t.Errorf("Expected XOT Server stats-port 12345, got %d", xsr.StatsPort)
	}

	srv := cm.GetServer("12345")
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

	srv = cm.GetServer("45678")
	if srv == nil {
		t.Errorf("Failed to find DNS server 456")
	} else {
		if srv.DNSPattern != "^(...)(...)" {
			t.Errorf("Expected default DNS pattern, got %s", srv.DNSPattern)
		}
	}

	srv = cm.GetServer("78901")
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

	srv := cm.GetServer("12345")
	if srv == nil {
		t.Fatal("Expected server, got nil")
	}
	if srv.DNSPattern != "^(...)(...)" {
		t.Errorf("Expected default DNS pattern '^(...)(...)', got %q", srv.DNSPattern)
	}
}

func TestConfigInvalidJSON(t *testing.T) {
	path := writeConfigFile(t, `{invalid json`)

	_, err := NewConfigManager(path)
	if err == nil {
		t.Error("Expected error for invalid JSON")
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

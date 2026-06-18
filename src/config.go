package xot

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LciStartDefault is the default starting LCI for the TUN interface LCI range.
const (
	LciStartDefault     = 1024
	LciEndDefault       = 2048
	PortDefault         = 1998
	TCPKeepaliveDefault = 30 // seconds; applied when "tcp-keepalive-interval" is absent from config
)

// ServerConfig configures an XOT server destination with prefix, addressing, and keepalive settings.
type ServerConfig struct {
	Prefix               string   `json:"prefix"`                 // X.121 prefix (e.g., "123/3")
	IP                   string   `json:"ip"`                     // XOT server IP
	Port                 AddrSpec `json:"port"`                   // Port or host:port (default PortDefault)
	DNSPattern           string   `json:"dns_pattern"`            // Regex for DNS lookup
	DNSName              string   `json:"dns_name"`               // DNS name template (e.g., "\2.\1.example.org")
	TCPKeepaliveInterval *int     `json:"tcp-keepalive-interval"` // TCP keepalive idle seconds; nil→30, 0→disabled
	X25KeepaliveInterval int      `json:"x25-keepalive-interval"` // X.25 INTERRUPT keepalive seconds; 0→disabled (default)
}

// TunConfig holds LCI range and modulus settings for a TUN interface.
type TunConfig struct {
	LciStart int `json:"lci_start"` // Start of TUN LCI range
	LciEnd   int `json:"lci_end"`   // End of TUN LCI range
	Modulo   int `json:"modulo"`    // Window modulus: 8 (default) or 128
}

// ServiceConfig holds common service settings such as the stats port.
type ServiceConfig struct {
	StatsPort AddrSpec `json:"stats-port"`
}

// TunGatewayConfig holds configuration for the tun-gateway process.
type TunGatewayConfig struct {
	TunConfig
	ServiceConfig
}

// TunLoopbackConfig holds configuration for the tun-loopback process.
type TunLoopbackConfig struct {
	TunConfig
	ServiceConfig
	Routes []string `json:"routes"` // X.121 addresses; each gets its own TUN interface
}

// DestinationConfig holds per-destination X.25 facility overrides.
type DestinationConfig struct {
	Facilities map[string]string `json:"facilities"`
}

// Config is the top-level configuration structure loaded from the JSON config file.
type Config struct {
	TunGateway   TunGatewayConfig             `json:"tun-gateway"`
	TunLoopback  TunLoopbackConfig            `json:"tun-loopback"`
	XotGateway   ServiceConfig                `json:"xot-gateway"`
	XotServer    ServiceConfig                `json:"xot-server"`
	Servers      []ServerConfig               `json:"servers"`
	Destinations map[string]DestinationConfig `json:"destinations"`
}

// ConfigManager loads and hot-reloads the JSON configuration file.
type ConfigManager struct {
	mu       sync.RWMutex
	filename string
	config   *Config
	lastMod  time.Time
}

// NewConfigManager creates a ConfigManager and performs an initial load of filename.
func NewConfigManager(filename string) (*ConfigManager, error) {
	cm := &ConfigManager{filename: filename}
	_, err := cm.Reload()
	return cm, err
}

// Reload re-reads the config file if it has changed. Returns true if reloaded.
func (cm *ConfigManager) Reload() (bool, error) {
	if cm == nil {
		return false, nil
	}
	info, err := os.Stat(cm.filename)
	if err != nil {
		return false, err
	}

	if info.ModTime().Equal(cm.lastMod) {
		return false, nil
	}

	data, err := os.ReadFile(cm.filename)
	if err != nil {
		return false, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, err
	}

	// Set defaults for TUN
	if cfg.TunGateway.LciStart == 0 {
		cfg.TunGateway.LciStart = LciStartDefault
	}
	if cfg.TunGateway.LciEnd == 0 {
		cfg.TunGateway.LciEnd = LciEndDefault
	}

	// Clamp to the valid X.25 LCI range.  The LCI field is 12 bits (0–4095).
	// LCI 0 is reserved for link-level frames; the usable range is 1–4095.
	// Values outside this range are silently truncated by the 12-bit encoding,
	// producing LCI 0 on the wire and confusing the kernel ("unknown 0B LCI 000").
	if cfg.TunGateway.LciStart < LCIMin {
		log.Printf("Warning: lci_start %d < minimum %d, clamping", cfg.TunGateway.LciStart, LCIMin)
		cfg.TunGateway.LciStart = LCIMin
	}
	if cfg.TunGateway.LciEnd > LCIMax {
		log.Printf("Warning: lci_end %d > maximum %d, clamping", cfg.TunGateway.LciEnd, LCIMax)
		cfg.TunGateway.LciEnd = LCIMax
	}
	if cfg.TunGateway.LciStart >= cfg.TunGateway.LciEnd {
		log.Printf("Warning: lci_start %d >= lci_end %d, resetting to defaults %d-%d",
			cfg.TunGateway.LciStart, cfg.TunGateway.LciEnd, LciStartDefault, LciEndDefault)
		cfg.TunGateway.LciStart = LciStartDefault
		cfg.TunGateway.LciEnd = LciEndDefault
	}
	if cfg.TunGateway.Modulo == 0 {
		cfg.TunGateway.Modulo = 8
	} else if cfg.TunGateway.Modulo != 8 && cfg.TunGateway.Modulo != 128 {
		log.Printf("Warning: tun-gateway modulo %d is invalid (must be 8 or 128), defaulting to 8", cfg.TunGateway.Modulo)
		cfg.TunGateway.Modulo = 8
	}

	// Set defaults and clamp for TunLoopback.
	if cfg.TunLoopback.LciStart == 0 {
		cfg.TunLoopback.LciStart = LciStartDefault
	}
	if cfg.TunLoopback.LciEnd == 0 {
		cfg.TunLoopback.LciEnd = LciEndDefault
	}
	if cfg.TunLoopback.LciStart < LCIMin {
		log.Printf("Warning: tun-loopback lci_start %d < minimum %d, clamping", cfg.TunLoopback.LciStart, LCIMin)
		cfg.TunLoopback.LciStart = LCIMin
	}
	if cfg.TunLoopback.LciEnd > LCIMax {
		log.Printf("Warning: tun-loopback lci_end %d > maximum %d, clamping", cfg.TunLoopback.LciEnd, LCIMax)
		cfg.TunLoopback.LciEnd = LCIMax
	}
	if cfg.TunLoopback.LciStart >= cfg.TunLoopback.LciEnd {
		log.Printf("Warning: tun-loopback lci_start %d >= lci_end %d, resetting to defaults %d-%d",
			cfg.TunLoopback.LciStart, cfg.TunLoopback.LciEnd, LciStartDefault, LciEndDefault)
		cfg.TunLoopback.LciStart = LciStartDefault
		cfg.TunLoopback.LciEnd = LciEndDefault
	}
	if cfg.TunLoopback.Modulo == 0 {
		cfg.TunLoopback.Modulo = 8
	} else if cfg.TunLoopback.Modulo != 8 && cfg.TunLoopback.Modulo != 128 {
		log.Printf("Warning: tun-loopback modulo %d is invalid (must be 8 or 128), defaulting to 8", cfg.TunLoopback.Modulo)
		cfg.TunLoopback.Modulo = 8
	}

	// Set defaults and validate servers
	validServers := make([]ServerConfig, 0, len(cfg.Servers))
	for i := range cfg.Servers {
		srv := cfg.Servers[i]
		if srv.Port == "" {
			srv.Port = AddrSpec(fmt.Sprintf(":%d", PortDefault))
		}
		if srv.TCPKeepaliveInterval == nil {
			v := TCPKeepaliveDefault
			srv.TCPKeepaliveInterval = &v
		}

		hasIP := srv.IP != ""
		hasDNS := srv.DNSName != "" || srv.DNSPattern != ""

		if hasIP && hasDNS {
			log.Printf("Error in config: server %s has both IP and DNS attributes - ignoring", srv.Prefix)
			continue
		}

		if !hasIP && !hasDNS {
			log.Printf("Error in config: server %s has neither IP nor DNS attributes - ignoring", srv.Prefix)
			continue
		}

		if hasDNS {
			if srv.DNSName == "" {
				log.Printf("Error in config: server %s has dns_pattern but no dns_name - ignoring", srv.Prefix)
				continue
			}
			if srv.DNSPattern == "" {
				srv.DNSPattern = "^(...)(...)"
			}
		}

		validServers = append(validServers, srv)
	}
	cfg.Servers = validServers

	// Validate destinations
	cfg.Destinations = validateDestinations(cfg.Destinations)

	cm.mu.Lock()
	cm.config = &cfg
	cm.lastMod = info.ModTime()
	cm.mu.Unlock()

	log.Printf("Configuration reloaded from %s", cm.filename)
	return true, nil
}

// GetTunGatewayConfig returns the current TunGatewayConfig.
func (cm *ConfigManager) GetTunGatewayConfig() TunGatewayConfig {
	if cm == nil {
		return TunGatewayConfig{TunConfig: TunConfig{LciStart: LciStartDefault, LciEnd: LciEndDefault, Modulo: 8}}
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return TunGatewayConfig{TunConfig: TunConfig{LciStart: LciStartDefault, LciEnd: LciEndDefault, Modulo: 8}}
	}
	return cm.config.TunGateway
}

// GetTunLoopbackConfig returns the current TunLoopbackConfig.
func (cm *ConfigManager) GetTunLoopbackConfig() TunLoopbackConfig {
	if cm == nil {
		return TunLoopbackConfig{TunConfig: TunConfig{LciStart: LciStartDefault, LciEnd: LciEndDefault}}
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return TunLoopbackConfig{TunConfig: TunConfig{LciStart: LciStartDefault, LciEnd: LciEndDefault}}
	}
	return cm.config.TunLoopback
}

// GetXotGatewayConfig returns the ServiceConfig for the xot-gateway.
func (cm *ConfigManager) GetXotGatewayConfig() ServiceConfig {
	if cm == nil {
		return ServiceConfig{}
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return ServiceConfig{}
	}
	return cm.config.XotGateway
}

// GetXotServerConfig returns the ServiceConfig for the xot-server.
func (cm *ConfigManager) GetXotServerConfig() ServiceConfig {
	if cm == nil {
		return ServiceConfig{}
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return ServiceConfig{}
	}
	return cm.config.XotServer
}

// GetServers returns a copy of the current server list.
func (cm *ConfigManager) GetServers() []ServerConfig {
	if cm == nil {
		return nil
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return nil
	}
	servers := make([]ServerConfig, len(cm.config.Servers))
	copy(servers, cm.config.Servers)
	return servers
}

// GetDestinations returns a copy of the current destination map.
func (cm *ConfigManager) GetDestinations() map[string]DestinationConfig {
	if cm == nil {
		return nil
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return nil
	}
	destinations := make(map[string]DestinationConfig)
	for k, v := range cm.config.Destinations {
		destinations[k] = v
	}
	return destinations
}

// GetDestination returns the DestinationConfig for addr, or nil if not configured.
func (cm *ConfigManager) GetDestination(addr string) *DestinationConfig {
	if cm == nil {
		return nil
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return nil
	}
	if dest, ok := cm.config.Destinations[addr]; ok {
		return &dest
	}
	return nil
}

// isLocalIP returns true if ip matches any address on a local network interface.
func isLocalIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ifIP net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ifIP = v.IP
			case *net.IPAddr:
				ifIP = v.IP
			}
			if ifIP != nil && ifIP.Equal(parsed) {
				return true
			}
		}
	}
	return false
}

// GetServer returns the longest-prefix-matching server for x121Addr, plus a
// local flag.  If the server's IP (or any IP resolved from its DNS name) is
// assigned to a local interface the flag is true; if resolution fails or no
// server is found the flag defaults to defaultLocal.
func (cm *ConfigManager) GetServer(x121Addr string, defaultLocal bool) (*ServerConfig, bool) {
	if cm == nil {
		return nil, defaultLocal
	}
	// Reload config if it changed on disk
	if _, err := cm.Reload(); err != nil {
		log.Printf("Warning: failed to reload config: %v", err)
	}

	cm.mu.RLock()
	if cm.config == nil {
		cm.mu.RUnlock()
		return nil, defaultLocal
	}
	var best *ServerConfig
	bestLen := -1
	for _, srv := range cm.config.Servers {
		parts := strings.Split(srv.Prefix, "/")
		if len(parts) != 2 {
			log.Printf("Warning: Prefix %s ignored: incorrect format", srv.Prefix)
			continue
		}
		prefix := parts[0]
		plen, err := strconv.Atoi(parts[1])
		if err != nil {
			log.Printf("Warning: Prefix %s/%s ignored: %v", parts[0], parts[1], err)
			continue
		}
		if len(parts[0]) != plen {
			log.Printf("Warning: Prefix %s/%s ignored: len %d != %d", parts[0], parts[1], len(parts[0]), plen)
			continue
		}
		if strings.HasPrefix(x121Addr, prefix) && plen > bestLen {
			srvCopy := srv
			best = &srvCopy
			bestLen = plen
		}
	}
	cm.mu.RUnlock()

	if best == nil {
		return nil, defaultLocal
	}

	ips, err := ResolveXotDestination(x121Addr, best)
	if err != nil {
		return best, defaultLocal
	}
	for _, ip := range ips {
		if isLocalIP(ip) {
			return best, true
		}
	}
	return best, false
}

func validateDestinations(destinations map[string]DestinationConfig) map[string]DestinationConfig {
	validDests := make(map[string]DestinationConfig)
	for addr, dest := range destinations {
		validFacs := make(map[string]string)
		for codeHex, valHex := range dest.Facilities {
			codeBytes, err := hex.DecodeString(codeHex)
			if err != nil || len(codeBytes) != 1 {
				log.Printf("Error in config: invalid facility code %s for destination %s - ignoring", codeHex, addr)
				continue
			}
			valBytes, err := hex.DecodeString(valHex)
			if err != nil {
				log.Printf("Error in config: invalid facility value %s for destination %s - ignoring", valHex, addr)
				continue
			}
			code := codeBytes[0]
			class := code >> 6
			expectedLen := 0
			switch class {
			case 0:
				expectedLen = 1
			case 1:
				expectedLen = 2
			case 2:
				expectedLen = 3
			case 3:
				// variable
			}

			if class != 3 && len(valBytes) != expectedLen {
				log.Printf("Error in config: facility %02x (class %d) expects %d bytes, got %d - ignoring", code, class, expectedLen, len(valBytes))
				continue
			}
			validFacs[codeHex] = valHex
		}
		if len(validFacs) > 0 {
			dest.Facilities = validFacs
			validDests[addr] = dest
		}
	}
	return validDests
}

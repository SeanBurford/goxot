package xot

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	LciStartDefault = 1024
	LciEndDefault = 2048
	PortDefault = 1998
)

type XotServerConfig struct {
	Prefix     string `json:"prefix"`      // X.121 prefix (e.g., "123/3")
	IP         string `json:"ip"`          // XOT server IP
	Port       int    `json:"port"`        // Port (default PortDefault)
	DNSPattern string `json:"dns_pattern"` // Regex for DNS lookup
	DNSName    string `json:"dns_name"`    // DNS name template (e.g., "\2.\1.example.org")
}

type TunConfig struct {
	LciStart int `json:"lci_start"` // Start of TUN LCI range
	LciEnd   int `json:"lci_end"`   // End of TUN LCI range
}

type ServiceConfig struct {
	StatsPort int `json:"stats-port"`
}

type TunGatewayConfig struct {
	TunConfig
	ServiceConfig
}

type Config struct {
 	TunGateway TunGatewayConfig  `json:"tun-gateway"`
 	XotGateway ServiceConfig     `json:"xot-gateway"`
 	XotServer  ServiceConfig     `json:"xot-server"`
 	Servers    []XotServerConfig `json:"servers"`
}

type ConfigManager struct {
	mu       sync.RWMutex
	filename string
	config   *Config
	lastMod  time.Time
}

func NewConfigManager(filename string) (*ConfigManager, error) {
	cm := &ConfigManager{filename: filename}
	if _, err := cm.Reload(); err != nil {
		return nil, err
	}
	return cm, nil
}

func (cm *ConfigManager) Reload() (bool, error) {
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

	// Set defaults and validate servers
	validServers := make([]XotServerConfig, 0, len(cfg.Servers))
	for i := range cfg.Servers {
		srv := cfg.Servers[i]
		if srv.Port == 0 {
			srv.Port = PortDefault
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

	cm.mu.Lock()
	cm.config = &cfg
	cm.lastMod = info.ModTime()
	cm.mu.Unlock()

	log.Printf("Configuration reloaded from %s", cm.filename)
	return true, nil
}

func (cm *ConfigManager) GetTunGatewayConfig() TunGatewayConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
 	if cm.config == nil {
 		return TunGatewayConfig{TunConfig: TunConfig{LciStart: LciStartDefault, LciEnd: LciEndDefault}}
 	}
 	return cm.config.TunGateway
}
 
func (cm *ConfigManager) GetXotGatewayConfig() ServiceConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return ServiceConfig{}
	}
	return cm.config.XotGateway
}

func (cm *ConfigManager) GetXotServerConfig() ServiceConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return ServiceConfig{}
	}
	return cm.config.XotServer
}

func (cm *ConfigManager) GetServers() []XotServerConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return nil
	}
	servers := make([]XotServerConfig, len(cm.config.Servers))
	copy(servers, cm.config.Servers)
	return servers
}

func (cm *ConfigManager) GetServer(x121Addr string) *XotServerConfig {
	// Reload config if it changed on disk
	if _, err := cm.Reload(); err != nil {
		log.Printf("Warning: failed to reload config: %v", err)
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for _, srv := range cm.config.Servers {
		parts := strings.Split(srv.Prefix, "/")
		if len(parts) != 2 {
			continue
		}
		prefix := parts[0]
		if strings.HasPrefix(x121Addr, prefix) {
			srvCopy := srv
			return &srvCopy
		}
	}
	return nil
}

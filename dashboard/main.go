package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// VarzCache stores the cached response and its expiration time
type VarzCache struct {
	Data       interface{}
	Expiration time.Time
}

type ServiceConfig struct {
	StatsPort int `json:"stats-port"`
}

type Config struct {
	XotServer  ServiceConfig `json:"xot-server"`
	XotGateway ServiceConfig `json:"xot-gateway"`
	TunGateway ServiceConfig `json:"tun-gateway"`
}

var (
	cache      = make(map[string]VarzCache)
	cacheMutex sync.RWMutex
	serverIP   string
	configFile string
	config     Config
)

const CacheDuration = 990 * time.Millisecond

func main() {
	flag.StringVar(&serverIP, "server", "127.0.0.1", "IP address of the varz server")
	flag.StringVar(&configFile, "config", "config.json", "Name of the config file")
	flag.Parse()

	loadConfig()

	// API endpoint for proxying varz requests
	http.HandleFunc("/api/varz", handleVarzProxy)

	// Serve static files from the 'dist' directory (frontend build)
	fs := http.FileServer(http.Dir("./dist"))
	http.Handle("/", fs)

	port := 9090
	fmt.Printf("Goxot Dashboard Server starting on http://localhost:%d\n", port)
	fmt.Printf("Monitoring Services on %s\n", serverIP)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

func loadConfig() {
	// Default ports
	config = Config{
		XotServer:  ServiceConfig{StatsPort: 8001},
		XotGateway: ServiceConfig{StatsPort: 8002},
		TunGateway: ServiceConfig{StatsPort: 8003},
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("Config file %s not found, using default ports\n", configFile)
			return
		}
		log.Printf("Error reading config file %s: %v\n", configFile, err)
		return
	}

	if err := json.Unmarshal(data, &config); err != nil {
		log.Printf("Error parsing config file %s: %v\n", configFile, err)
	} else {
		log.Printf("Loaded configuration from %s\n", configFile)
	}
}

func handleVarzProxy(w http.ResponseWriter, r *http.Request) {
	// Enable CORS for development
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	service := r.URL.Query().Get("service")
	if service == "" {
		http.Error(w, "Query parameter 'service' is required", http.StatusBadRequest)
		return
	}

	var statsPort int
	switch service {
	case "xot-server":
		statsPort = config.XotServer.StatsPort
	case "xot-gateway":
		statsPort = config.XotGateway.StatsPort
	case "tun-gateway":
		statsPort = config.TunGateway.StatsPort
	default:
		http.Error(w, "Invalid service name", http.StatusBadRequest)
		return
	}

	targetURL := fmt.Sprintf("http://%s:%d/varz", serverIP, statsPort)

	// Check cache
	cacheMutex.RLock()
	cached, found := cache[targetURL]
	cacheMutex.RUnlock()

	if found && time.Now().Before(cached.Expiration) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		json.NewEncoder(w).Encode(cached.Data)
		return
	}

	// Fetch from target
	client := http.Client{
		Timeout: 2 * time.Second,
	}
	resp, err := client.Get(targetURL)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error fetching from %s: %v", targetURL, err), http.StatusGatewayTimeout)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Target returned status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "Error reading response", http.StatusInternalServerError)
		return
	}

	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		http.Error(w, "Error parsing JSON from target", http.StatusInternalServerError)
		return
	}

	// Update cache
	cacheMutex.Lock()
	cache[targetURL] = VarzCache{
		Data:       data,
		Expiration: time.Now().Add(CacheDuration),
	}
	cacheMutex.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	w.Write(body)
}

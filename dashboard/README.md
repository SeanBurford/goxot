# Goxot VT-100 Dashboard

A retro-styled, green-screen dashboard for monitoring `xot-server`, `xot-gateway`, and `tun-gateway` instances from the [goxot](https://github.com/SeanBurford/goxot) project.

## Features

- **VT-100 Aesthetic**: Classic green CRT styling with scanlines and glow effects.
- **Real-time Monitoring**: Tracks uptime, packet flows, and interface counters.
- **Configurable**: Adjustable refresh rates (1s to 15m) and server/port settings.
- **System Overview**: Live status check on all three protocol components.
- **Collapsible Metrics**: Expand or collapse detailed interface metrics to focus on specific components.

## Prerequisites

- **Node.js**: Version 18 or higher is recommended.
- **npm**: Standard Node package manager.
- **Goxot Services**: Instances of `xot-server`, `xot-gateway`, or `tun-gateway` running with the `--stats-port` enabled.

## Installation

1. **Clone the repository** (if applicable) or enter the project directory.
2. **Install frontend dependencies**:
   ```bash
   npm install
   ```

## Running the Dashboard

### 1. Start the Backend Proxy (Required for CORS)
The dashboard includes a backend server written in Go that acts as a proxy to bypass CORS restrictions when fetching data from the goxot services. It also caches responses for 0.99 seconds to reduce load.

You must have **Go** installed on your system.
```bash
go run main.go --server=127.0.0.1 --config=config.json
```
The backend will start on `http://localhost:9090`.

**Flags:**
* `--server`: IP address of the varz server (default: `127.0.0.1`).
* `--config`: Name of the config file (default: `config.json`). If no config file is found, it uses default ports (8001, 8002, 8003).

### 2. Start the Frontend
In a separate terminal:

#### Development Mode
```bash
npm run dev
```

#### Production Build
```bash
npm run build
```
The static files will be generated in the `dist/` directory. The Go server (`main.go`) is configured to serve these files at `http://localhost:9090` if they exist.

## Why a Proxy?
Directly fetching from the goxot services at `http://localhost:8001/varz` from a browser often triggers **CORS (Cross-Origin Resource Sharing)** blocks. The Go backend (`main.go`) provides a proxy endpoint at `/api/varz` that fetches the data on behalf of the browser and returns it with the necessary CORS headers.

## Usage

1. Open the dashboard in your browser.
2. Enter the **Server IP** of the machine where your goxot services are running.
3. Configure the **Ports** for each service (defaults are 8001, 8002, 8003).
4. Select your preferred **Refresh Rate**.
5. The dashboard will automatically begin scraping the `/varz` endpoints and visualizing the data.

## License
Apache-2.0

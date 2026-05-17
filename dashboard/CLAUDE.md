# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What it does

A VT-100/green-screen styled monitoring dashboard for the three goxot services (`xot-server`, `xot-gateway`, `tun-gateway`). Shows real-time uptime, packet flows, session counts, and interface counters. Supports live time-series graphing of any selected metric, configurable refresh rates (1s–15m), collapsible service panels, and a Reset button to track deltas from a baseline.

## Stack

- **Frontend**: React 19 + TypeScript, Vite 6, Tailwind CSS 4, Recharts, Framer Motion
- **Backend proxy**: Go (`main.go`) — serves the built frontend and proxies `/api/varz` requests to avoid CORS issues

## Commands

```bash
# Backend (Go proxy)
go run main.go --server=127.0.0.1 --config=config.json   # port 9090

# Frontend dev server
npm install
npm run dev        # http://localhost:3000 with HMR

# Production
npm run build      # outputs to dist/
npm run lint       # TypeScript type check only (tsc --noEmit)
npm run preview    # serve dist/ locally
```

Set `DISABLE_HMR=true` to turn off hot module replacement (useful in some AI/remote environments).

## Architecture

**Data flow**: Frontend polls Go proxy at `/api/varz?service=<name>` → proxy fetches from configured stats port on target host → 990ms in-memory cache prevents overloading services → frontend computes per-fetch deltas and appends selected metric to a 100-point rolling history for graphing.

**CORS**: Direct browser requests to goxot `/varz` endpoints lack CORS headers. The Go proxy adds them and also serves the built static files from `./dist/`.

**Direct fallback**: `App.tsx` tries the Go proxy first, then falls back to a direct fetch with a shorter timeout. This allows development without the proxy.

**State**: All state (offsets/baseline, history, config) is in-memory; nothing persists across page reloads.

## Key files

| File | Role |
|---|---|
| `main.go` | Go proxy: `/api/varz` with caching, serves `./dist/` |
| `src/App.tsx` | All app logic: fetch loop, state, layout, `ServiceSection` component |
| `src/types.ts` | `VarzResponse`, `ServiceState`, `DashboardConfig` interfaces |
| `src/index.css` | Tailwind + custom terminal aesthetics (`.terminal-box`, `.glow-text`, scanline animations) |
| `config.json` | Stats ports per service (defaults: 8001/8002/8003) |

## Config

`config.json` sets stats ports for each service. The `--server` flag sets the target host IP. Port 9090 is the proxy's own port.

The Tailwind theme and custom CSS classes are defined in `vite.config.ts` and `src/index.css` respectively — the VT-100 green (`#33ff33`) color scheme is set via CSS custom properties.

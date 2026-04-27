/**
 * @license
 * SPDX-License-Identifier: Apache-2.0
 */

export interface VarzResponse {
  uptime: number;
  threads_active?: Record<string, number>;
  dns_requests?: number;
  packets_handled?: Record<string, number>;
  causes_received?: Record<string, number>;
  causes_generated?: Record<string, number>;
  interface_sessions_opened?: Record<string, number>;
  interface_sessions_closed?: Record<string, number>;
  interface_call_request?: Record<string, number>;
  interface_call_connected?: Record<string, number>;
  interface_clear_request?: Record<string, number>;
  interface_clear_confirm?: Record<string, number>;
  interface_packets_sent?: Record<string, number>;
  interface_packets_received?: Record<string, number>;
  interface_bytes_sent?: Record<string, number>;
  interface_bytes_received?: Record<string, number>;
}

export interface ServiceState {
  name: string;
  connected: boolean;
  data: VarzResponse | null;
  prevData: VarzResponse | null;
  lastUpdate: number;
}

export type RefreshRate = 1000 | 10000 | 60000 | 900000;

export interface DashboardConfig {
  serverIp: string;
  xotServerPort: number;
  xotGatewayPort: number;
  tunGatewayPort: number;
  refreshRate: RefreshRate;
}

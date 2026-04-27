/**
 * @license
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { useState, useEffect, useCallback, useRef } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { Settings, Activity, Server, Network, Terminal, ShieldAlert, Cpu, LineChart as ChartIcon } from 'lucide-react';
import { DashboardConfig, RefreshRate, ServiceState, VarzResponse } from './types';
import { ResponsiveContainer, LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip as ChartTooltip } from 'recharts';

const REFRESH_OPTIONS: { label: string; value: RefreshRate }[] = [
  { label: '1S', value: 1000 },
  { label: '10S', value: 10000 },
  { label: '1M', value: 60000 },
  { label: '15M', value: 900000 },
];

const INTERFACE_VARS = [
  { key: 'interface_sessions_opened', label: 'Sessions Opened', desc: 'Sessions initiated on the link layer' },
  { key: 'interface_sessions_closed', label: 'Sessions Closed', desc: 'Sessions terminated on the link layer' },
  { key: 'interface_call_request', label: 'Call Requests', desc: 'Call request packets seen (either direction)' },
  { key: 'interface_call_connected', label: 'Call Connected', desc: 'Call connected packets seen (either direction)' },
  { key: 'interface_clear_request', label: 'Clear Requests', desc: 'Clear request packets seen (either direction)' },
  { key: 'interface_clear_confirm', label: 'Clear Confirms', desc: 'Clear confirm packets seen (either direction)' },
  { key: 'interface_packets_sent', label: 'Packets Sent', desc: 'Packets sent' },
  { key: 'interface_packets_received', label: 'Packets Received', desc: 'Packets received' },
  { key: 'interface_bytes_sent', label: 'Bytes Sent', desc: 'Bytes sent' },
  { key: 'interface_bytes_received', label: 'Bytes Received', desc: 'Bytes received' },
];

const SERVICE_INTERFACES: Record<string, string[]> = {
  'XOT Server': ['xot', 'tun', 'xot_fwd'],
  'TUN Gateway': ['tun', 'xot', 'unix'],
  'XOT Gateway': ['unix', 'xot'],
};

function formatUptime(seconds: number): string {
  const d = Math.floor(seconds / (3600 * 24));
  const h = Math.floor((seconds % (3600 * 24)) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;

  const dStr = d > 0 ? `${d}D, ` : "";
  const hStr = h.toString().padStart(2, '0');
  const mStr = m.toString().padStart(2, '0');
  const sStr = s.toString().padStart(2, '0');

  return `${dStr}${hStr}:${mStr}:${sStr}`;
}

const ServiceSection = ({ 
  state, 
  interfaces, 
  onSelectMetric,
  selectedMetric,
  offset
}: { 
  state: ServiceState; 
  interfaces: string[];
  onSelectMetric: (serviceName: string, key: string, iface: string, label: string) => void;
  selectedMetric: { serviceName: string, key: string, iface: string } | null;
  offset: VarzResponse | null;
}) => {
  const isDown = !state.connected;
  const [isExpanded, setIsExpanded] = useState(true);
  const [tooltip, setTooltip] = useState<{ x: number, y: number, text: string } | null>(null);

  const handleMouseMove = (e: React.MouseEvent, text: string) => {
    setTooltip({ x: e.clientX + 15, y: e.clientY + 15, text });
  };

  return (
    <section className={`mb-8 ${isDown ? 'offline' : ''} w-full relative`}>
      {tooltip && (
        <div 
          className="fixed z-[100] pointer-events-none bg-black border border-green-screen p-2 text-green-screen text-xs max-w-[200px] shadow-[0_0_10px_#33ff33] rounded"
          style={{ top: tooltip.y, left: tooltip.x }}
        >
          {tooltip.text}
        </div>
      )}
      <div className="terminal-box w-full">
        <div 
          className="terminal-header flex justify-between items-center cursor-pointer hover:brightness-110 transition-all font-mono"
          onClick={() => setIsExpanded(!isExpanded)}
        >
          <div className="flex items-center gap-2">
            {isExpanded ? <Activity size={20} className="text-green-screen animate-pulse" /> : <Terminal size={20} />}
            <span>{state.name} {isDown ? '- LINK DOWN' : ''}</span>
          </div>
          <span className="text-sm font-mono opacity-60">
            {isExpanded ? '[ SHRINK ]' : '[ EXPAND ]'}
          </span>
        </div>
        
        <AnimatePresence>
          {isExpanded && (
            <motion.div
              initial={{ height: 0, opacity: 0 }}
              animate={{ height: 'auto', opacity: 1 }}
              exit={{ height: 0, opacity: 0 }}
              transition={{ duration: 0.3, ease: "easeInOut" }}
              className="overflow-hidden"
            >
              <table className="data-table w-full">
                <thead>
                  <tr>
                    <th>Variable</th>
                    <th>I/F</th>
                    <th className="text-right">Current</th>
                    <th className="text-right">Change</th>
                  </tr>
                </thead>
                <tbody>
                  {(INTERFACE_VARS).map((v) => {
                    const nonZeroInterfaces = interfaces.filter(iface => {
                      const rawVal = (state.data as any)?.[v.key]?.[iface] || 0;
                      const offVal = (offset as any)?.[v.key]?.[iface] || 0;
                      return (rawVal - offVal) > 0;
                    });
                    
                    return nonZeroInterfaces.map((iface, idx) => {
                      const rawVal = (state.data as any)?.[v.key]?.[iface] || 0;
                      const offVal = (offset as any)?.[v.key]?.[iface] || 0;
                      const currentVal = rawVal - offVal;

                      const prevRawVal = (state.prevData as any)?.[v.key]?.[iface] || 0;
                      const prevVal = Math.max(0, prevRawVal - offVal);
                      
                      const change = currentVal - prevVal;
                      const isSelected = selectedMetric?.serviceName === state.name && selectedMetric?.key === v.key && selectedMetric?.iface === iface;
                      
                      return (
                        <tr 
                          key={`${v.key}-${iface}`} 
                          className={`hover:bg-green-screen/10 transition-colors group ${isSelected ? 'bg-green-screen/20' : ''}`}
                        >
                          <td 
                            className="font-bold opacity-80 cursor-help relative"
                            onMouseMove={(e) => handleMouseMove(e, v.desc)}
                            onMouseLeave={() => setTooltip(null)}
                            onClick={() => onSelectMetric(state.name, v.key, iface, v.label)}
                          >
                            <span className={`hover:text-white transition-colors flex items-center gap-2 ${isSelected ? 'text-white underline decoration-green-screen' : ''}`}>
                              {idx === 0 ? v.label : ""}
                              {idx === 0 && <ChartIcon size={14} className="opacity-0 group-hover:opacity-100" />}
                            </span>
                          </td>
                          <td className="text-green-screen font-mono">{iface}</td>
                          <td className="text-right font-mono">{currentVal.toLocaleString()}</td>
                          <td className={`text-right font-mono ${change > 0 ? 'text-white' : change < 0 ? 'text-red-500' : 'opacity-30'}`}>
                            {change === 0 ? "0" : change > 0 ? `+${change}` : change}
                          </td>
                        </tr>
                      );
                    });
                  })}
                </tbody>
              </table>
            </motion.div>
          )}
        </AnimatePresence>

        {isDown && (
          <div className="absolute inset-0 bg-black/40 flex items-center justify-center pointer-events-none z-10">
            <span className="text-[40px] font-bold opacity-20 rotate-12 uppercase border-4 border-red-500 p-8 text-red-500">No Connection</span>
          </div>
        )}
      </div>
    </section>
  );
};

export default function App() {
  const [config, setConfig] = useState<DashboardConfig>({
    serverIp: '127.0.0.1',
    xotServerPort: 8001,
    xotGatewayPort: 8002,
    tunGatewayPort: 8003,
    refreshRate: 10000,
  });

  const [services, setServices] = useState<Record<string, ServiceState>>({
    'XOT Server': { name: 'XOT Server', connected: false, data: null, prevData: null, lastUpdate: 0 },
    'TUN Gateway': { name: 'TUN Gateway', connected: false, data: null, prevData: null, lastUpdate: 0 },
    'XOT Gateway': { name: 'XOT Gateway', connected: false, data: null, prevData: null, lastUpdate: 0 },
  });

  const [selectedMetric, setSelectedMetric] = useState<{ serviceName: string, key: string, iface: string, label: string } | null>(null);
  const [history, setHistory] = useState<{ time: string, value: number }[]>([]);
  const [offsets, setOffsets] = useState<Record<string, VarzResponse | null>>({
    'XOT Server': null,
    'TUN Gateway': null,
    'XOT Gateway': null,
  });

  const handleReset = () => {
    const newOffsets: Record<string, VarzResponse | null> = {};
    Object.entries(services).forEach(([name, state]) => {
      newOffsets[name] = state.data ? JSON.parse(JSON.stringify(state.data)) : null;
    });
    setOffsets(newOffsets);
  };

  const handleSelectMetric = (serviceName: string, key: string, iface: string, label: string) => {
    setSelectedMetric({ serviceName, key, iface, label });
    setHistory([]); // Clear history on new selection
  };

  const servicesRef = useRef(services);
  useEffect(() => {
    servicesRef.current = services;
    
    // Update history if a metric is selected
    if (selectedMetric) {
      const service = services[selectedMetric.serviceName];
      if (service && service.connected && service.data) {
        const currentVal = (service.data as any)?.[selectedMetric.key]?.[selectedMetric.iface] || 0;
        const prevVal = (service.prevData as any)?.[selectedMetric.key]?.[selectedMetric.iface] || 0;
        const delta = currentVal - prevVal;
        
        const timeLabel = new Date().toLocaleTimeString([], { hour12: false, minute: '2-digit', second: '2-digit' });
        
        setHistory(prev => {
          const newHistory = [...prev, { time: timeLabel, value: delta }];
          // Keep last 100 points
          return newHistory.slice(-100);
        });
      }
    }
  }, [services, selectedMetric]);

  const fetchData = useCallback(async () => {
    const targets = [
      { name: 'XOT Server', port: config.xotServerPort },
      { name: 'TUN Gateway', port: config.tunGatewayPort },
      { name: 'XOT Gateway', port: config.xotGatewayPort },
    ];

    const newResults: Record<string, Partial<ServiceState>> = {};

    for (const target of targets) {
      const serviceId = target.name.toLowerCase().replace(' ', '-');
      // Use the local Go proxy server on port 9090 with the service name
      const proxyUrl = `http://${config.serverIp}:9090/api/varz?service=${serviceId}`;
      
      try {
        const response = await fetch(proxyUrl, { signal: AbortSignal.timeout(2000) });
        
        if (response.ok) {
          const data: VarzResponse = await response.json();
          newResults[target.name] = {
            connected: true,
            data: data,
            prevData: servicesRef.current[target.name].data,
            lastUpdate: Date.now(),
          };
        } else {
          throw new Error('Proxy error');
        }
      } catch (err) {
        // Fallback: try direct fetch if proxy fails (maybe it's not running)
        const targetUrl = `http://${config.serverIp}:${target.port}/varz`;
        try {
          const directResponse = await fetch(targetUrl, { signal: AbortSignal.timeout(1000) });
          if (directResponse.ok) {
            const data: VarzResponse = await directResponse.json();
            newResults[target.name] = {
              connected: true,
              data: data,
              prevData: servicesRef.current[target.name].data,
              lastUpdate: Date.now(),
            };
          } else {
            throw new Error('Direct fetch failed');
          }
        } catch (directErr) {
          newResults[target.name] = {
            connected: false,
            data: null,
            prevData: null,
            lastUpdate: Date.now(),
          };
        }
      }
    }

    setServices(prev => {
      const updated = { ...prev };
      for (const [name, result] of Object.entries(newResults)) {
        updated[name] = { ...updated[name], ...result };
      }
      return updated;
    });
  }, [config.serverIp, config.xotServerPort, config.xotGatewayPort, config.tunGatewayPort]);

  useEffect(() => {
    const interval = setInterval(fetchData, config.refreshRate);
    fetchData(); // Initial fetch
    return () => clearInterval(interval);
  }, [config.refreshRate, fetchData]);

  return (
    <div className="min-h-screen p-4 pt-12 relative overflow-hidden bg-green-dark">
      <div className="scanline-overlay" />
      <div className="scanline-bar" />

      <header className="terminal-box mb-6 flex flex-wrap gap-6 items-center justify-between shadow-[0_0_15px_rgba(51,255,51,0.2)]">
        <div className="flex items-center gap-6">
          <div className="flex items-center gap-4">
            <Terminal size={48} className="text-green-screen" />
            <h1 className="text-4xl font-bold glow-text uppercase tracking-tighter">GOXOT-MONITOR <span className="opacity-50 text-2xl">v1.0.4</span></h1>
          </div>
          
          <div className="flex gap-8 items-center">
            <div className="flex items-center gap-3">
              <label className="text-[20px] uppercase opacity-70">Server IP:</label>
              <input 
                type="text" 
                value={config.serverIp}
                onChange={(e) => setConfig({ ...config, serverIp: e.target.value })}
                className="w-48"
              />
            </div>
            <div className="flex items-center gap-3">
              <label className="text-[20px] uppercase opacity-70">Refresh:</label>
              <select 
                value={config.refreshRate} 
                onChange={(e) => setConfig({ ...config, refreshRate: parseInt(e.target.value) as RefreshRate })}
                className="bg-green-dark"
              >
                {REFRESH_OPTIONS.map(opt => <option key={opt.value} value={opt.value}>{opt.label}</option>)}
              </select>
            </div>
          </div>
        </div>

        <div className="flex gap-8 text-[22px] font-mono">
          <div>XOT: <span className="text-white">{config.xotServerPort}</span></div>
          <div>TUN: <span className="text-white">{config.tunGatewayPort}</span></div>
          <div>GW: <span className="text-white">{config.xotGatewayPort}</span></div>
        </div>

        <div className="flex items-center gap-4">
          <button 
            className="flex items-center gap-2 p-2 border border-green-screen hover:bg-green-screen hover:text-black transition-all cursor-pointer font-bold uppercase tracking-tighter"
            onClick={handleReset}
            title="Zero out current metrics"
          >
            Reset
          </button>
          <button 
            className="p-2 border border-green-screen hover:bg-green-screen hover:text-black transition-all"
            onClick={() => setIsSettingsOpen(true)}
          >
            <Settings size={24} />
          </button>
        </div>
      </header>

      <main className="w-full px-4 grid grid-cols-12 gap-6">
        {/* LEFT COLUMN */}
        <div className="col-span-12 lg:col-span-6 flex flex-col gap-6">
          {/* OVERVIEW */}
          <section className="terminal-box w-full">
            <div className="terminal-header">System Overview</div>
            <table className="data-table w-full">
              <thead>
                <tr>
                  <th>Server</th>
                  <th>Status</th>
                  <th className="text-right">Uptime</th>
                </tr>
              </thead>
              <tbody>
                {(Object.entries(services) as [string, ServiceState][]).map(([name, state]) => (
                  <tr key={name}>
                    <td>{name.toLowerCase().replace(' ', '-')}</td>
                    <td>
                      <span className={state.connected ? "text-white animate-pulse" : "text-red-500"}>
                        {state.connected ? "ONLINE" : "OFFLINE"}
                      </span>
                    </td>
                    <td className="text-right font-mono">{(state.data as VarzResponse)?.uptime ? formatUptime((state.data as VarzResponse).uptime) : "--D --:--:--"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>

          {/* FLOWS (Packet analysis) */}
          <section className="terminal-box w-full">
            <div className="terminal-header">Active Flows</div>
            <div className="grid grid-cols-1 gap-4">
              {(Object.entries(services) as [string, ServiceState][]).map(([name, state]) => (
                <div key={name} className="border border-green-screen/20 p-4 w-full bg-black/20">
                  <h3 className="text-[20px] font-bold mb-2 uppercase opacity-50 tracking-widest">{name} Packets</h3>
                  {!state.connected ? (
                    <div className="text-[18px] text-red-500/50 italic py-2">{'>>'} [LINK LOST]</div>
                  ) : (
                    <div className="space-y-1 font-mono text-[20px]">
                      {Object.entries((state.data as VarzResponse)?.packets_handled || {}).length > 0 ? (
                        Object.entries((state.data as VarzResponse)?.packets_handled || {}).map(([type, rawCount]) => {
                          const offCount = (offsets[name]?.packets_handled as any)?.[type] || 0;
                          const count = (rawCount as number) - offCount;
                          
                          if (count <= 0 && offCount > 0) return null;

                          return (
                            <div key={type} className="flex justify-between hover:bg-green-screen/5 transition-colors">
                              <span className="opacity-70">{'>'} {type}</span>
                              <span className="text-white font-bold">{count.toLocaleString()}</span>
                            </div>
                          );
                        })
                      ) : (
                        <div className="text-green-screen/30 italic">{'>'} NO ACTIVITY</div>
                      )}
                    </div>
                  )}
                </div>
              ))}
            </div>
          </section>

          {/* GRAPH SECTION */}
          <section className="terminal-box min-h-[350px] w-full flex flex-col">
            <div className="terminal-header flex justify-between items-center">
              <div className="flex items-center gap-2">
                <Activity size={20} className="text-green-screen" />
                <span>Metric Change Rate</span>
              </div>
              {selectedMetric && (
                <div className="text-sm font-mono text-white tracking-widest">
                  [ {selectedMetric.serviceName} | {selectedMetric.label} | {selectedMetric.iface} ]
                </div>
              )}
            </div>
            
            <div className="flex-grow flex items-center justify-center p-4">
              {!selectedMetric ? (
                <div className="text-center opacity-30 select-none pointer-events-none">
                  <Activity size={64} className="mx-auto mb-4" />
                  <p className="text-2xl font-bold uppercase tracking-tighter italic">No Probe Selected</p>
                  <p className="text-sm mt-2">Click a variable name in the service panels for live graphing</p>
                </div>
              ) : (
                <div className="w-full h-full min-h-[250px]">
                  <ResponsiveContainer width="100%" height="100%">
                    <LineChart data={history}>
                      <CartesianGrid strokeDasharray="3 3" stroke="#003300" vertical={false} />
                      <XAxis 
                        dataKey="time" 
                        stroke="#33ff33" 
                        fontSize={12} 
                        tickLine={false} 
                        axisLine={false}
                        minTickGap={30}
                      />
                      <YAxis 
                        stroke="#33ff33" 
                        fontSize={12} 
                        tickLine={false} 
                        axisLine={false}
                        tickFormatter={(val) => val.toLocaleString()}
                      />
                      <ChartTooltip 
                        contentStyle={{ 
                          backgroundColor: '#001a00', 
                          border: '1px solid #33ff33',
                          borderRadius: '0px',
                          color: '#33ff33',
                          fontFamily: 'monospace'
                        }}
                        itemStyle={{ color: '#fff' }}
                        labelFormatter={(label) => `Time: ${label}`}
                        formatter={(value: number) => [`${value >= 0 ? '+' : ''}${value.toLocaleString()}`, 'Change']}
                      />
                      <Line 
                        type="monotone" 
                        dataKey="value" 
                        stroke="#33ff33" 
                        strokeWidth={2} 
                        dot={false}
                        animationDuration={300}
                        isAnimationActive={false}
                      />
                    </LineChart>
                  </ResponsiveContainer>
                </div>
              )}
            </div>
          </section>
        </div>

        {/* RIGHT COLUMN */}
        <div className="col-span-12 lg:col-span-6 flex flex-col gap-6">
          <ServiceSection 
            state={services['XOT Server']} 
            interfaces={SERVICE_INTERFACES['XOT Server']} 
            onSelectMetric={handleSelectMetric}
            selectedMetric={selectedMetric}
            offset={offsets['XOT Server']}
          />
          <ServiceSection 
            state={services['TUN Gateway']} 
            interfaces={SERVICE_INTERFACES['TUN Gateway']} 
            onSelectMetric={handleSelectMetric}
            selectedMetric={selectedMetric}
            offset={offsets['TUN Gateway']}
          />
          <ServiceSection 
            state={services['XOT Gateway']} 
            interfaces={SERVICE_INTERFACES['XOT Gateway']} 
            onSelectMetric={handleSelectMetric}
            selectedMetric={selectedMetric}
            offset={offsets['XOT Gateway']}
          />
        </div>
      </main>

      <footer className="mt-8 py-4 border-t border-green-screen/20 text-[18px] opacity-40 flex justify-between uppercase tracking-widest">
        <span>Goxot Core Protocol :: Scoped {new Date().toLocaleTimeString()}</span>
        <span>Secure Terminal Interface 7a-99</span>
        <span>Status: Scraped 0.99s ago</span>
      </footer>
    </div>
  );
}

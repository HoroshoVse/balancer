export interface BackendMetrics {
  rps?: number
  avg_latency_ms?: number
  error_rate?: number
  total_requests?: number
}

export interface BackendNode {
  id?: number
  name: string
  address: string
  port: number | ""
  weight: number
  enabled: boolean
  backup: boolean
  max_conns: number
  tls_enabled: boolean
  sni: string
  hc_enabled: boolean
  hc_protocol: string
  hc_port: number | ""
  hc_path: string
  hc_interval: number
  hc_timeout: number
  hc_failure_threshold: number
  hc_recovery_threshold: number
  status?: string
  metrics?: BackendMetrics
}

export interface LoadBalancer {
  id: number
  name: string
  protocol: string
  listen_ip: string
  listen_port: number
  algorithm: string
  backup_mode?: string // "all" (default) or "ordered": backups take over one at a time in list order
  ssl_enabled: boolean
  auto_ssl: boolean
  cert_path: string
  key_path: string
  cert_data: string
  has_key_data: boolean
  acme_enabled: boolean
  acme_email: string
  acme_domains: string
  acme_status?: string
  acme_error?: string
  http3_enabled: boolean
  backend_http2_enabled: boolean
  proxy_protocol_enabled: boolean
  proxy_protocol_version: number
  dns_mode: boolean
  http_redirect: boolean
  connect_timeout: number
  idle_timeout: number
  response_timeout: number
  sticky_sessions_enabled: boolean
  sticky_session_type: string
  status?: string
  status_error?: string
  backend_group: { id?: number; name: string; backends: BackendNode[] }
  metrics?: { total_requests?: number; average_latency_ms?: number; error_rate_percent?: number }
}

export interface MetricsSnapshot {
  timestamp: string
  ts: number
  rps: number
  avg_latency_ms: number
  error_rate: number
}

export interface HostSnapshot {
  ts: number
  cpu_percent: number
  mem_used: number // bytes
  net_rx_bps: number // bytes per second
  net_tx_bps: number
}

export interface HostStats {
  available: boolean
  cpus: number
  load: [number, number, number]
  mem_total: number
  swap_total: number
  swap_used: number
  interface: string
  uptime_seconds: number
  history: HostSnapshot[]
}

export interface LogEntry {
  timestamp: string
  level: string
  message: string
  lb_name?: string
}

export const formatTime = (ts: unknown) => {
  const d = new Date(Number(ts))
  return isNaN(d.getTime()) ? "" : d.toLocaleTimeString()
}

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
  /** "" everyone may connect; "allow" only access_countries; "deny" everyone except access_countries */
  access_mode?: "" | "allow" | "deny"
  access_countries?: string // "RU,BY"
  access_allow?: string // always let through: addresses and subnets, one per line
  access_deny?: string // always refuse
  status?: string
  status_error?: string
  backend_group: { id?: number; name: string; backends: BackendNode[] }
  metrics?: {
    total_requests?: number
    average_latency_ms?: number
    error_rate_percent?: number
    /** connections refused by the access rules since the start, and where from */
    blocked?: number
    blocked_top?: { country: string; count: number }[]
    /** what the refused DNS queries (UDP) asked for in the last 10 minutes, by registrable domain and type */
    blocked_domains?: { domain: string; type: string; count: number }[]
    /** the busiest client addresses of the last 10 minutes; absent until someone connects */
    top_clients?: ClientStat[]
  }
}

export interface ClientStat {
  ip: string
  /** "" when unknown, when the address is internal or while the country database is not loaded */
  country: string
  /** a private, loopback or link-local address */
  internal?: boolean
  accepted: number
  refused: number
}

export interface GeoSource {
  name: string
  url: string
  present: boolean
  downloading?: boolean
  ranges: number
  data_date?: string
  updated_at?: string
  error?: string
}

export interface GeoStatus {
  loaded: boolean
  updating: boolean
  updated_at?: string
  countries: number
  ranges: number
  sources: GeoSource[]
  last_attempt?: string
  last_error?: string
  next_attempt?: string
  auto_update: boolean
  interval_hours: number
  country_codes: string[]
}

export interface GeoLookup {
  ip: string
  you?: boolean
  country: string
  loaded: boolean
  internal?: boolean
  decision?: { allowed: boolean; country: string; reason: string; rule?: string }
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

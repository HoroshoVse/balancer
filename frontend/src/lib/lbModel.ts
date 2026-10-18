// Presentation helpers for load balancers: health, labels, listening sockets.
import type { BackendNode, LoadBalancer } from "@/types"

export type Health = "ok" | "degraded" | "down" | "error" | "starting"

export interface HealthSummary {
  state: Health
  up: number // enabled nodes that are up
  total: number // enabled nodes
  standby: number // healthy backups waiting while primaries serve
}

const isUp = (b: BackendNode) => b.enabled && b.status === "UP"

/** Nodes that receive traffic: healthy primaries, or healthy backups when no primary is left (mirrors the engine). */
export function activeNodes(lb: LoadBalancer): BackendNode[] {
  const nodes = lb.backend_group?.backends ?? []
  const primaries = nodes.filter((b) => isUp(b) && !b.backup)
  return primaries.length > 0 ? primaries : nodes.filter((b) => isUp(b) && b.backup)
}

export function lbHealth(lb: LoadBalancer): HealthSummary {
  const nodes = (lb.backend_group?.backends ?? []).filter((b) => b.enabled)
  const up = nodes.filter(isUp).length
  const primariesUp = nodes.filter((b) => isUp(b) && !b.backup).length
  const standby = primariesUp > 0 ? nodes.filter((b) => isUp(b) && b.backup).length : 0
  let state: Health = "ok"
  if (lb.status === "error") state = "error"
  else if (lb.status === "starting") state = "starting"
  else if (nodes.length === 0 || up === 0) state = "down"
  else if (primariesUp === 0 || up < nodes.length) state = "degraded"
  return { state, up, total: nodes.length, standby }
}

/** Tailwind classes for a status LED in the dark rail and on light surfaces. */
export const ledClass: Record<Health, string> = {
  ok: "bg-ok-bright",
  degraded: "bg-warn",
  down: "bg-down",
  error: "bg-down",
  starting: "bg-rail-muted",
}

export type Kind = "http" | "https_term" | "https_pass" | "tcp" | "udp" | "dns"

export function kindOf(lb: Pick<LoadBalancer, "protocol" | "ssl_enabled" | "dns_mode">): Kind {
  if (lb.protocol === "udp") return lb.dns_mode ? "dns" : "udp"
  if (lb.protocol === "tcp") return "tcp"
  if (lb.protocol === "https") return lb.ssl_enabled ? "https_term" : "https_pass"
  return lb.ssl_enabled ? "https_term" : "http"
}

export const kindLabel: Record<Kind, string> = {
  http: "HTTP",
  https_term: "HTTPS",
  https_pass: "HTTPS насквозь",
  tcp: "TCP",
  udp: "UDP",
  dns: "DNS · UDP+TCP",
}

export const protocolLabel = (lb: LoadBalancer) => kindLabel[kindOf(lb)]

export const algorithmLabel: Record<string, string> = {
  round_robin: "по очереди",
  weighted_round_robin: "по весу",
  least_conn: "меньше соединений",
  ip_hash: "по IP клиента",
  failover: "резервирование",
}

export const listenAddr = (lb: Pick<LoadBalancer, "listen_ip" | "listen_port">) =>
  `${lb.listen_ip || "0.0.0.0"}:${lb.listen_port}`

export const roleLabel: Record<string, string> = {
  Admin: "Администратор",
  Operator: "Оператор",
  Viewer: "Наблюдатель",
}

// --- Listening sockets, same rules as the server's ConflictsWith ---

type ListenSpec = Pick<LoadBalancer, "protocol" | "listen_ip" | "listen_port" | "ssl_enabled" | "http3_enabled" | "dns_mode" | "http_redirect">

export function listenSockets(lb: ListenSpec): string[] {
  const port = lb.listen_port
  let sockets: string[]
  if (lb.protocol === "udp") sockets = lb.dns_mode ? [`udp/${port}`, `tcp/${port}`] : [`udp/${port}`]
  else if (lb.protocol === "https" && lb.ssl_enabled && lb.http3_enabled) sockets = [`tcp/${port}`, `udp/${port}`]
  else sockets = [`tcp/${port}`]
  if (lb.http_redirect && lb.protocol === "https" && lb.ssl_enabled) sockets.push("tcp/80")
  return sockets
}

const wildcard = (ip: string) => ip === "" || ip === "0.0.0.0" || ip === "::"

/** Returns the balancer that would bind one of the same sockets, and the clashing port. */
export function findConflict(lb: ListenSpec & { id?: number }, others: LoadBalancer[]): { other: LoadBalancer; port: number } | null {
  const mine = listenSockets(lb)
  for (const other of others) {
    if (other.id === lb.id) continue
    const a = (lb.listen_ip || "").trim()
    const b = (other.listen_ip || "").trim()
    if (!wildcard(a) && !wildcard(b) && a !== b) continue
    const theirs = listenSockets(other)
    const hit = mine.find((s) => theirs.includes(s))
    if (hit) return { other, port: Number(hit.split("/")[1]) }
  }
  return null
}

/** Russian plural: plural(5, "узел", "узла", "узлов") → "узлов". */
export function plural(n: number, one: string, few: string, many: string) {
  const m10 = n % 10
  const m100 = n % 100
  if (m10 === 1 && m100 !== 11) return one
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few
  return many
}

export const formatNumber = (v: number, digits = 0) =>
  v.toLocaleString("ru-RU", { minimumFractionDigits: digits, maximumFractionDigits: digits })

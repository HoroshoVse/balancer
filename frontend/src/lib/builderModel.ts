// Builder form model: defaults, conversions to/from the API, and pre-flight checks.
import type { BackendNode, LoadBalancer } from "@/types"
import { findConflict, kindOf, listenAddr, plural, type Kind } from "./lbModel"
import { blankNode, type Check, type Recipe } from "./recipes"

export interface NodeForm extends BackendNode {
  key: string // stable React key
}

export interface BuilderForm {
  name: string
  kind: Kind
  listen_ip: string
  listen_port: string
  algorithm: string
  backup_mode: "all" | "ordered"
  check: Check
  interval: string
  acme_enabled: boolean
  acme_email: string
  acme_domains: string
  cert_data: string
  key_data: string
  has_key_data: boolean
  cert_path: string
  key_path: string
  http3_enabled: boolean
  http_redirect: boolean
  backend_http2_enabled: boolean
  proxy_protocol_enabled: boolean
  proxy_protocol_version: number
  sticky_sessions_enabled: boolean
  sticky_session_type: string
  connect_timeout: number
  idle_timeout: number
  response_timeout: number
  nodes: NodeForm[]
}

let seq = 0
export const withKey = (n: BackendNode): NodeForm => ({ ...n, key: `n${++seq}` })

export const terminatesHTTP = (k: Kind) => k === "http" || k === "https_term"
export const isDatagram = (k: Kind) => k === "udp" || k === "dns"
export const hasTLS = (k: Kind) => k === "https_term"

export const defaultPort: Record<Kind, number> = { http: 80, https_term: 443, https_pass: 443, tcp: 443, udp: 53, dns: 53 }
export const defaultCheck: Record<Kind, Check> = { http: "http", https_term: "http", https_pass: "tcp", tcp: "tcp", udp: "udp", dns: "dns" }

export function defaultTimeouts(kind: Kind) {
  return {
    connect_timeout: 5000,
    idle_timeout: isDatagram(kind) ? 60000 : 600000,
    response_timeout: kind === "dns" ? 5000 : terminatesHTTP(kind) ? 60000 : 0,
  }
}

/** The sentence's health check applies to every node. */
export function applyCheck(nodes: NodeForm[], check: Check, interval: string): NodeForm[] {
  return nodes.map((n) => ({
    ...n,
    hc_enabled: check !== "none",
    hc_protocol: check === "none" ? n.hc_protocol : check,
    hc_interval: Number(interval) || 10,
  }))
}

export function fromRecipe(r: Recipe): BuilderForm {
  return {
    name: r.name,
    kind: r.kind,
    listen_ip: "0.0.0.0",
    listen_port: String(r.port),
    algorithm: r.algorithm,
    backup_mode: "all",
    check: r.check,
    interval: "10",
    acme_enabled: !!r.acme,
    acme_email: "",
    acme_domains: "",
    cert_data: "",
    key_data: "",
    has_key_data: false,
    cert_path: "",
    key_path: "",
    http3_enabled: false,
    http_redirect: !!r.redirect,
    backend_http2_enabled: false,
    proxy_protocol_enabled: false,
    proxy_protocol_version: 2,
    sticky_sessions_enabled: false,
    sticky_session_type: "ip",
    ...defaultTimeouts(r.kind),
    nodes: applyCheck(r.nodes.map(withKey), r.check, "10"),
  }
}

export function fromLB(lb: LoadBalancer): BuilderForm {
  const kind = kindOf(lb)
  const nodes = (lb.backend_group?.backends ?? []).map((b) => withKey({ ...blankNode(), ...b, sni: b.sni ?? "", hc_port: b.hc_port || "" }))
  const first = nodes[0]
  const check: Check = first ? (first.hc_enabled ? ((first.hc_protocol as Check) || defaultCheck[kind]) : "none") : defaultCheck[kind]
  return {
    name: lb.name,
    kind,
    listen_ip: lb.listen_ip || "0.0.0.0",
    listen_port: String(lb.listen_port),
    algorithm: lb.algorithm || "round_robin",
    backup_mode: lb.backup_mode === "ordered" ? "ordered" : "all",
    check,
    interval: String(first?.hc_interval || 10),
    acme_enabled: !!(lb.acme_enabled || lb.auto_ssl),
    acme_email: lb.acme_email ?? "",
    acme_domains: lb.acme_domains ?? "",
    cert_data: lb.cert_data ?? "",
    key_data: "",
    has_key_data: !!lb.has_key_data,
    cert_path: lb.cert_path ?? "",
    key_path: lb.key_path ?? "",
    http3_enabled: !!lb.http3_enabled,
    http_redirect: !!lb.http_redirect,
    backend_http2_enabled: !!lb.backend_http2_enabled,
    proxy_protocol_enabled: !!lb.proxy_protocol_enabled,
    proxy_protocol_version: lb.proxy_protocol_version || 2,
    sticky_sessions_enabled: !!lb.sticky_sessions_enabled,
    sticky_session_type: lb.sticky_session_type || "ip",
    connect_timeout: lb.connect_timeout ?? 0,
    idle_timeout: lb.idle_timeout ?? 0,
    response_timeout: lb.response_timeout ?? 0,
    nodes: nodes.length ? nodes : [withKey(blankNode({ port: lb.listen_port }))],
  }
}

/** Switches protocol, moving every value the user hasn't changed to the new protocol's default. */
export function changeKind(f: BuilderForm, kind: Kind): BuilderForm {
  const oldT = defaultTimeouts(f.kind)
  const newT = defaultTimeouts(kind)
  const retime = (k: keyof typeof oldT) => (f[k] === oldT[k] ? newT[k] : f[k])
  const check = f.check === defaultCheck[f.kind] ? defaultCheck[kind] : f.check
  return {
    ...f,
    kind,
    listen_port: f.listen_port === String(defaultPort[f.kind]) ? String(defaultPort[kind]) : f.listen_port,
    check,
    connect_timeout: retime("connect_timeout"),
    idle_timeout: retime("idle_timeout"),
    response_timeout: retime("response_timeout"),
    sticky_session_type: terminatesHTTP(kind) ? f.sticky_session_type : "ip",
    proxy_protocol_version: isDatagram(kind) ? 2 : f.proxy_protocol_version,
    nodes: applyCheck(isDatagram(kind) ? f.nodes.map((n) => ({ ...n, tls_enabled: false })) : f.nodes, check, f.interval),
  }
}

export function protoFields(kind: Kind) {
  switch (kind) {
    case "http": return { protocol: "http", ssl_enabled: false, dns_mode: false }
    case "https_term": return { protocol: "https", ssl_enabled: true, dns_mode: false }
    case "https_pass": return { protocol: "https", ssl_enabled: false, dns_mode: false }
    case "tcp": return { protocol: "tcp", ssl_enabled: false, dns_mode: false }
    case "udp": return { protocol: "udp", ssl_enabled: false, dns_mode: false }
    case "dns": return { protocol: "udp", ssl_enabled: false, dns_mode: true }
  }
}

export function toPayload(f: BuilderForm, id?: number): Record<string, unknown> {
  const proto = protoFields(f.kind)
  const ssl = proto.ssl_enabled
  const name = f.name.trim()
  return {
    ...(id ? { id } : {}),
    name,
    ...proto,
    listen_ip: f.listen_ip.trim(),
    listen_port: Number(f.listen_port),
    algorithm: f.algorithm,
    backup_mode: f.backup_mode,
    acme_enabled: ssl && f.acme_enabled,
    auto_ssl: ssl && f.acme_enabled,
    acme_email: f.acme_email.trim(),
    acme_domains: f.acme_domains.trim(),
    cert_data: f.cert_data,
    key_data: f.key_data, // empty keeps the stored key
    cert_path: f.cert_path.trim(),
    key_path: f.key_path.trim(),
    http3_enabled: ssl && f.http3_enabled,
    http_redirect: ssl && f.http_redirect,
    backend_http2_enabled: terminatesHTTP(f.kind) && f.backend_http2_enabled,
    proxy_protocol_enabled: f.proxy_protocol_enabled,
    proxy_protocol_version: f.proxy_protocol_version,
    sticky_sessions_enabled: f.sticky_sessions_enabled,
    sticky_session_type: terminatesHTTP(f.kind) ? f.sticky_session_type : "ip",
    connect_timeout: f.connect_timeout,
    idle_timeout: f.idle_timeout,
    response_timeout: f.response_timeout,
    backend_group: {
      name: `${name} Group`,
      backends: applyCheck(f.nodes, f.check, f.interval).map(({ key: _key, status: _status, metrics: _metrics, ...b }) => ({
        ...b,
        address: String(b.address).trim(),
        port: Number(b.port) || 0,
        hc_port: Number(b.hc_port) || 0,
        tls_enabled: isDatagram(f.kind) ? false : b.tls_enabled,
      })),
    },
  }
}

// --- Pre-flight ---

export type CheckLevel = "ok" | "warn" | "error"

export interface Preflight {
  level: CheckLevel
  text: string
  fix?: { label: string; apply?: (f: BuilderForm) => BuilderForm; open?: string }
}

const isWildcard = (ip: string) => ip === "" || ip === "0.0.0.0" || ip === "::"

export function preflight(f: BuilderForm, lbs: LoadBalancer[], editingId?: number): Preflight[] {
  const out: Preflight[] = []
  const proto = protoFields(f.kind)
  const ssl = proto.ssl_enabled
  const port = Number(f.listen_port)
  const ip = f.listen_ip.trim()

  if (!f.name.trim()) out.push({ level: "error", text: "Дайте балансировщику название." })

  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    out.push({ level: "error", text: "Порт приёма — число от 1 до 65535." })
  } else {
    const spec = { ...proto, listen_ip: ip, listen_port: port, http3_enabled: ssl && f.http3_enabled, http_redirect: ssl && f.http_redirect, id: editingId }
    const hit = findConflict(spec, lbs)
    if (hit && hit.port !== port) {
      out.push({
        level: "error",
        text: `Порт ${hit.port} для переадресации на HTTPS уже занят «${hit.other.name}».`,
        fix: { label: "Выключить переадресацию", apply: (x) => ({ ...x, http_redirect: false }) },
      })
    } else if (hit) {
      let free = port + 1
      while (free < 65535 && findConflict({ ...spec, listen_port: free }, lbs)) free++
      out.push({
        level: "error",
        text: `Порт ${port} уже занят балансировщиком «${hit.other.name}» (${listenAddr(hit.other)}).`,
        fix: { label: `Взять порт ${free}`, apply: (x) => ({ ...x, listen_port: String(free) }) },
      })
    } else {
      out.push({ level: "ok", text: `Адрес ${ip || "0.0.0.0"}:${port} не занят другими балансировщиками.` })
    }
    if (port === 53 && isWildcard(ip)) {
      out.push({ level: "warn", text: "На Ubuntu порт 53 часто держит systemd-resolved. Если запуск не удастся, укажите IP сетевого интерфейса вместо 0.0.0.0." })
    }
    if (ssl && f.http_redirect && port === 80) {
      out.push({ level: "error", text: "Переадресация слушает 80-й порт, а балансировщик уже на нём.", fix: { label: "Выключить переадресацию", apply: (x) => ({ ...x, http_redirect: false }) } })
    }
  }

  if (f.nodes.length === 0) {
    out.push({ level: "error", text: "Добавьте хотя бы один узел.", fix: { label: "Добавить узел", apply: (x) => ({ ...x, nodes: applyCheck([withKey(blankNode({ name: "node1", port: Number(x.listen_port) || "" }))], x.check, x.interval) }) } })
  } else {
    if (!f.nodes.some((n) => !n.backup)) {
      out.push({ level: "error", text: "Все узлы в резерве — трафику некуда идти.", fix: { label: "Сделать первый основным", apply: (x) => ({ ...x, nodes: x.nodes.map((n, i) => (i === 0 ? { ...n, backup: false } : n)) }) } })
    }
    const noAddr = f.nodes.filter((n) => !String(n.address).trim()).length
    if (noAddr) out.push({ level: "error", text: `У ${noAddr} ${plural(noAddr, "узла", "узлов", "узлов")} не указан адрес.` })
    const badPort = f.nodes.filter((n) => !(Number(n.port) >= 1 && Number(n.port) <= 65535)).length
    if (badPort) out.push({ level: "error", text: `Проверьте порт у ${badPort} ${plural(badPort, "узла", "узлов", "узлов")}.` })
    if (!noAddr && !badPort) {
      const backups = f.nodes.filter((n) => n.backup).length
      out.push({ level: "ok", text: `${f.nodes.length} ${plural(f.nodes.length, "узел", "узла", "узлов")}${backups ? `, из них в резерве ${backups}` : ""}.` })
    }
  }

  if (ssl) {
    if (f.acme_enabled && !f.acme_domains.trim()) {
      out.push({ level: "error", text: "Для Let's Encrypt нужен хотя бы один домен.", fix: { label: "Указать домен", open: "tls" } })
    } else if (!f.acme_enabled && f.cert_data.trim() && !f.key_data.trim() && !f.has_key_data) {
      out.push({ level: "error", text: "К сертификату нужен закрытый ключ.", fix: { label: "Открыть TLS", open: "tls" } })
    } else if (!f.acme_enabled && !f.cert_data.trim() && !f.cert_path.trim()) {
      out.push({ level: "warn", text: "Сертификат не задан — клиенты увидят самоподписанный.", fix: { label: "Настроить TLS", open: "tls" } })
    }
  }

  if (f.check === "none") {
    out.push({ level: "warn", text: "Узлы не проверяются — упавший узел продолжит получать трафик.", fix: { label: "Включить проверку", apply: (x) => ({ ...x, check: defaultCheck[x.kind] }) } })
  } else if (f.kind === "dns" && f.check !== "dns") {
    out.push({ level: "warn", text: "Зависший DNS-сервер заметит только проверка DNS-запросом.", fix: { label: "Проверять DNS-запросом", apply: (x) => ({ ...x, check: "dns" }) } })
  } else if (f.check === "udp") {
    out.push({ level: "warn", text: "UDP-проверка видит только закрытый порт: молчащий узел считается живым." })
  }

  if (f.proxy_protocol_enabled && (f.kind === "dns" || f.check === "dns")) {
    out.push({ level: "warn", text: "AdGuard Home не понимает PROXY protocol — для его узлов выключите.", fix: { label: "Выключить PROXY", apply: (x) => ({ ...x, proxy_protocol_enabled: false }) } })
  }
  return out
}

const how: Record<string, string> = {
  failover: "всё идёт на первый живой из",
  round_robin: "запросы по очереди идут на",
  weighted_round_robin: "запросы делятся по весу между",
  least_conn: "соединение получает самый свободный из",
  ip_hash: "каждый клиент закреплён за одним из",
}

const what: Record<Kind, string> = {
  http: "HTTP",
  https_term: "HTTPS и расшифровывает TLS",
  https_pass: "HTTPS без расшифровки",
  tcp: "TCP",
  udp: "UDP",
  dns: "DNS по UDP и TCP",
}

/** One-paragraph plain-language description of the result. */
export function describe(f: BuilderForm): string {
  // Disabled nodes get no traffic, so they are left out of the sentence
  const names = (backup: boolean) => f.nodes.filter((n) => n.enabled && n.backup === backup).map((n) => n.name || n.address || "узел без имени")
  const primaries = names(false)
  const backups = names(true)
  let s = `${f.name.trim() || "Балансировщик"} принимает ${what[f.kind]} на ${f.listen_ip.trim() || "0.0.0.0"}:${f.listen_port}; ${how[f.algorithm] ?? "запросы идут на"} ${primaries.length ? primaries.join(", ") : "— основных узлов нет"}.`
  if (backups.length === 1) s += ` Если основные не ответят, подхватит ${backups[0]}.`
  else if (backups.length > 1 && f.backup_mode === "ordered") s += ` Если основные не ответят, подхватит ${backups[0]}${backups.slice(1).map((b) => `, а если и он упадёт — ${b}`).join("")}.`
  else if (backups.length > 1) s += ` Если основные не ответят, подхватят ${backups.join(", ")} вместе.`
  if (hasTLS(f.kind) && f.acme_enabled && f.acme_domains.trim()) s += ` Сертификат для ${f.acme_domains.trim()} выпустит Let's Encrypt.`
  if (hasTLS(f.kind) && f.http_redirect) s += " Запросы на 80-й порт переадресуются на HTTPS."
  return s
}

/** "5 с", "10 мин", "без огр." for timeout summaries. */
export function formatMs(ms: number, zero: string) {
  if (!ms) return zero
  if (ms % 60000 === 0) return `${ms / 60000} мин`
  if (ms % 1000 === 0) return `${ms / 1000} с`
  return `${ms} мс`
}

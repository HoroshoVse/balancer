import { useRef } from "react"
import { Link } from "react-router-dom"
import { ArrowDown, ArrowUp, Check, Globe, Plus, ShieldCheck, Terminal, TriangleAlert } from "lucide-react"
import { api, canEdit } from "@/lib/api"
import { across, branch, down, flowOf, shareWidth, useMeasured, type FlowPath } from "@/lib/flow"
import { humanize, type EventKind } from "@/lib/humanize"
import { useLBs } from "@/lib/lbs"
import { activeNodes, algorithmLabel, backupQueue, formatNumber, kindLabel, kindOf, lbHealth, listenAddr, orderedBackups, plural } from "@/lib/lbModel"
import { recipeList } from "@/lib/recipes"
import { usePoll } from "@/lib/usePoll"
import { Bars } from "@/components/Bars"
import { FlowPaths } from "@/components/Flow"
import type { BackendNode, HostStats, LoadBalancer, LogEntry, MetricsSnapshot } from "@/types"

function Readout({ label, value, unit, children, last }: { label: string; value: string; unit: string; children: React.ReactNode; last?: boolean }) {
  return (
    <div className={`flex min-w-[200px] flex-1 flex-col gap-3 px-6 py-5 ${last ? "" : "border-b border-line-soft sm:border-b-0 sm:border-r"}`}>
      <span className="text-[13px] text-ink-3">{label}</span>
      <span className="flex items-baseline gap-2">
        <span className="font-display text-[40px] font-semibold leading-none tracking-tight">{value}</span>
        <span className="text-sm text-ink-3">{unit}</span>
      </span>
      {children}
    </div>
  )
}

// --- Flow ---

type NodeView = { node: BackendNode; state: "active" | "standby" | "down" | "off"; rank?: number }

function nodeViews(lb: LoadBalancer): NodeView[] {
  const active = new Set(activeNodes(lb).map((b) => b.id))
  const order = { active: 0, standby: 1, down: 2, off: 3 }
  const nodes = lb.backend_group?.backends ?? []
  // Backups taking over one at a time are numbered by their place in the queue
  const queue = orderedBackups(lb) ? backupQueue(nodes) : new Map<BackendNode, number>()
  return nodes
    .map((node): NodeView => ({
      node,
      state: !node.enabled ? "off" : node.status === "DOWN" ? "down" : active.has(node.id) ? "active" : "standby",
      rank: queue.get(node),
    }))
    .sort((a, b) => order[a.state] - order[b.state])
}

function NodeChip({ view, nodeRef }: { view: NodeView; nodeRef: (el: HTMLDivElement | null) => void }) {
  const { node, state, rank } = view
  const backup = rank ? `резерв ${rank}` : "резерв"
  const addr = `${node.address}:${node.port}`
  const styles = {
    active: { box: "border border-line bg-white", dot: "bg-ok", role: node.backup ? `${backup} в работе` : "основной", meta: `${addr} · ${formatNumber(node.metrics?.avg_latency_ms ?? 0, 1)} мс` },
    standby: { box: "border border-dashed border-[#B7C0CC] bg-white", dot: "border-2 border-ok", role: backup, meta: `${addr} · ждёт` },
    down: { box: "border border-down/40 bg-down-soft", dot: "bg-down", role: "не отвечает", meta: addr },
    off: { box: "border border-dashed border-line-strong bg-accent", dot: "bg-line-strong", role: "выключен", meta: addr },
  }[state]
  return (
    <div ref={nodeRef} data-flow className={`flex min-w-0 flex-col gap-0.5 rounded-xl px-3.5 py-2.5 ${styles.box}`}>
      <span className="flex items-center gap-2 text-sm font-semibold">
        <span className={`box-border h-2 w-2 flex-none rounded-full ${styles.dot}`} />
        <span className="truncate">{node.name || node.address}</span>
        <span className="truncate font-normal text-ink-3">{styles.role}</span>
      </span>
      <span className="truncate font-mono text-xs text-ink-2">{styles.meta}</span>
    </div>
  )
}

// Room for clients 150 + balancer 250 + nodes 260 side by side, with links between them
const FLOW_WIDE_MIN = 780

function FlowRow({ lb, rps, maxRps }: { lb: LoadBalancer; rps: number; maxRps: number }) {
  const kind = kindOf(lb)
  const failed = lb.status === "error"
  const live = failed ? 0 : rps
  const views = nodeViews(lb)
  const flow = flowOf(lb, live)
  const trunk = live > 0 ? Math.round(6 + 8 * (live / maxRps)) : 0
  const domain = (lb.acme_domains || "").split(",")[0]?.trim()

  const rowRef = useRef<HTMLDivElement>(null)
  const clientsRef = useRef<HTMLSpanElement>(null)
  const lbRef = useRef<HTMLAnchorElement>(null)
  const nodeRefs = useRef<(HTMLDivElement | null)[]>([])
  const { wide, paths } = useMeasured(
    rowRef,
    (box, width) => {
      const wide = width >= FLOW_WIDE_MIN
      const clients = box(clientsRef.current)
      const balancer = box(lbRef.current)
      const paths: FlowPath[] = []
      if (clients && balancer) {
        paths.push({ d: wide ? across(clients, balancer, 10 + trunk / 2) : down(clients, clients.l + 9, balancer), kind: live > 0 ? "live" : "idle", width: trunk, share: 1 })
        views.forEach((v, i) => {
          const node = box(nodeRefs.current[i])
          if (!node) return
          const f = flow(v.node)
          paths.push({ d: wide ? across(balancer, node) : branch(balancer, balancer.l + 24, node), kind: f.kind, width: shareWidth(trunk, f.share), share: f.share })
        })
      }
      return { wide, paths }
    },
    { wide: true, paths: [] as FlowPath[] },
  )

  return (
    <div ref={rowRef} className={`relative flex ${wide ? "items-center" : "flex-col items-stretch gap-8"}`}>
      <FlowPaths paths={paths} />
      <div className={`relative flex-none ${wide ? "w-[150px]" : ""}`}>
        <span ref={clientsRef} data-flow className={`inline-flex ${wide ? "flex-col gap-1" : "items-center gap-3"}`}>
          <span className="inline-flex items-center gap-2 text-[15px] font-semibold">
            <Globe className="h-[18px] w-[18px] text-ink-2" aria-hidden="true" />
            Клиенты
          </span>
          <span className="font-mono text-xs text-ink-3">{live > 0 ? `${formatNumber(live, live < 10 ? 1 : 0)} запр/с` : "нет трафика"}</span>
        </span>
      </div>
      {wide && <div aria-hidden="true" className="min-w-[48px] flex-1" />}
      <Link
        ref={lbRef}
        data-flow
        to={`/lbs/${lb.id}`}
        title={lb.status_error || undefined}
        className={`relative flex flex-none flex-col gap-1.5 rounded-[14px] bg-rail px-4 py-3.5 text-rail-text ${wide ? "w-[250px]" : ""} ${failed ? "ring-2 ring-down" : ""}`}
      >
        <span className="flex items-center justify-between gap-2">
          <span className="truncate text-base font-semibold text-white">{lb.name}</span>
          <span className="flex-none rounded-md bg-rail-4 px-1.5 py-0.5 font-mono text-[11px] text-[#C6CEDA]">{kindLabel[kind]}</span>
        </span>
        <span className="truncate font-mono text-xs text-[#A9B3C1]">
          {listenAddr(lb)}
          {kind === "https_term" && domain ? ` · ${domain}` : ""}
        </span>
        <span className={`text-xs ${failed ? "text-[#FF9A6B]" : "text-cobalt-light"}`}>{failed ? "не запущен — подробности внутри" : algorithmLabel[lb.algorithm] ?? lb.algorithm}</span>
      </Link>
      {wide && <div aria-hidden="true" className="min-w-[56px] flex-1" />}
      <div className={`relative flex flex-none flex-col gap-3 ${wide ? "w-[260px]" : "pl-12"}`}>
        {views.length === 0 && <span className="text-sm text-ink-3">узлов нет</span>}
        {views.map((v, i) => (
          <NodeChip
            key={v.node.id ?? v.node.address}
            view={v}
            nodeRef={(el) => {
              nodeRefs.current[i] = el
            }}
          />
        ))}
      </div>
    </div>
  )
}

// --- Seismograph ---

function Seismograph({ lb, history }: { lb: LoadBalancer; history: MetricsSnapshot[] }) {
  const ticks = history.slice(-60)
  const maxRps = Math.max(...ticks.map((t) => t.rps), 0)
  const lats = ticks.map((t) => t.avg_latency_ms).filter((v) => v > 0).sort((a, b) => a - b)
  const median = lats.length ? lats[Math.floor(lats.length / 2)] : 0
  const isSlow = (t: MetricsSnapshot) => median > 0 && t.avg_latency_ms > Math.max(median * 3, median + 50)
  const errors = ticks.filter((t) => t.error_rate > 0).length
  const slow = ticks.filter((t) => t.error_rate === 0 && isSlow(t)).length
  const pad = Array.from({ length: Math.max(0, 60 - ticks.length) })
  const summary = errors
    ? `${errors} ${plural(errors, "отрезок", "отрезка", "отрезков")} с ошибками`
    : slow
      ? `${slow} ${plural(slow, "медленный", "медленных", "медленных")}`
      : maxRps > 0
        ? "без ошибок"
        : "нет трафика"
  return (
    <div className="flex flex-wrap items-center gap-4">
      <Link to={`/lbs/${lb.id}`} className="w-24 flex-none truncate text-sm font-semibold text-ink hover:text-cobalt">{lb.name}</Link>
      <div aria-hidden="true" className="flex h-11 min-w-0 flex-[1_1_300px] items-center gap-[3px] overflow-hidden">
        {pad.map((_, i) => <div key={`p${i}`} className="h-2 flex-[1_0_3px] rounded-sm bg-line" />)}
        {ticks.map((t, i) => (
          <div
            key={t.ts || i}
            className={`flex-[1_0_3px] rounded-sm ${t.error_rate > 0 ? "bg-down" : isSlow(t) ? "bg-warn" : t.rps > 0 ? "bg-ok" : "bg-line-strong"}`}
            style={{ height: maxRps > 0 ? Math.round(8 + (t.rps / maxRps) * 36) : 8 }}
          />
        ))}
      </div>
      <span className={`w-36 flex-none text-[13px] ${errors ? "text-down-text" : "text-ink-2"}`}>{summary}</span>
    </div>
  )
}

// --- Events ---

const eventStyle: Record<EventKind, { box: string; icon: React.ReactNode }> = {
  ok: { box: "bg-ok-soft text-ok-text", icon: <Check className="h-4 w-4" aria-hidden="true" /> },
  warn: { box: "bg-warn-soft text-warn-text", icon: <TriangleAlert className="h-4 w-4" aria-hidden="true" /> },
  down: { box: "bg-down-soft text-down-text", icon: <TriangleAlert className="h-4 w-4" aria-hidden="true" /> },
  cert: { box: "bg-cobalt-soft text-cobalt", icon: <ShieldCheck className="h-4 w-4" aria-hidden="true" /> },
  info: { box: "bg-ground text-ink-2", icon: <Terminal className="h-4 w-4" aria-hidden="true" /> },
}

function Events({ logs }: { logs: LogEntry[] }) {
  const events = [...logs].reverse().map((e) => ({ e, h: humanize(e) })).filter((x) => x.h.notable).slice(0, 6)
  return (
    <section aria-labelledby="ev-title" className="flex min-w-0 flex-[1_1_360px] flex-col gap-4 rounded-[18px] border border-line bg-white p-6">
      <div className="flex items-baseline justify-between">
        <h2 id="ev-title" className="font-display text-xl font-semibold tracking-tight">События</h2>
        <Link to="/logs" className="text-sm font-medium text-cobalt hover:text-cobalt-dark">Весь журнал →</Link>
      </div>
      {events.length === 0 ? (
        <p className="text-sm text-ink-3">Ничего важного не происходило.</p>
      ) : (
        <ol className="flex flex-col">
          {events.map(({ e, h }, i) => (
            <li key={i} className="flex gap-3.5 border-b border-ground py-3 last:border-b-0">
              <span className={`grid h-7 w-7 flex-none place-items-center rounded-lg ${eventStyle[h.kind].box}`}>{eventStyle[h.kind].icon}</span>
              <span className="flex min-w-0 flex-col gap-0.5">
                <span className="text-sm">{h.text}</span>
                <span className="font-mono text-xs text-ink-3">
                  {new Date(e.timestamp).toLocaleTimeString("ru-RU")} · {e.lb_name || "система"}
                </span>
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}

// --- Server ---

const MB = 1024 ** 2
const GB = 1024 ** 3

// From 1000 MB up in gigabytes, so a "1 GB" swap that the kernel reports a few KB short isn't "1 024 МБ"
function bytesParts(bytes: number) {
  return bytes >= 1000 * MB ? { value: formatNumber(bytes / GB, 1), unit: "ГБ" } : { value: formatNumber(bytes / MB, 0), unit: "МБ" }
}

function formatBytes(bytes: number) {
  const { value, unit } = bytesParts(bytes)
  return `${value} ${unit}`
}

// Interfaces are measured in bits per second, decimal units
function formatBits(bytesPerSec: number) {
  const bits = bytesPerSec * 8
  const [value, unit] = bits >= 1e9 ? [bits / 1e9, "Гбит/с"] : bits >= 1e6 ? [bits / 1e6, "Мбит/с"] : bits >= 1e3 ? [bits / 1e3, "Кбит/с"] : [bits, "бит/с"]
  return { value: formatNumber(value, value < 10 && bits >= 1e3 ? 1 : 0), unit }
}

function formatUptime(seconds: number) {
  const days = Math.floor(seconds / 86400)
  if (days > 0) return `${days} ${plural(days, "день", "дня", "дней")}`
  const hours = Math.floor(seconds / 3600)
  if (hours > 0) return `${hours} ${plural(hours, "час", "часа", "часов")}`
  const minutes = Math.max(1, Math.floor(seconds / 60))
  return `${minutes} ${plural(minutes, "минуту", "минуты", "минут")}`
}

const loadColor = (percent: number) => (percent >= 90 ? "bg-down" : percent >= 70 ? "bg-warn" : undefined)

function HostPanel({ label, icon, value, unit, chart, note, last }: { label: string; icon?: React.ReactNode; value: string; unit: string; chart: React.ReactNode; note: React.ReactNode; last?: boolean }) {
  return (
    <div className={`flex min-w-[240px] flex-1 flex-col gap-3 py-5 sm:px-6 sm:first:pl-0 sm:last:pr-0 ${last ? "" : "border-b border-line-soft sm:border-b-0 sm:border-r"}`}>
      <span className="text-[13px] text-ink-3">{label}</span>
      <span className="flex items-baseline gap-2">
        {icon && <span className="self-center">{icon}</span>}
        <span className="font-display text-[40px] font-semibold leading-none tracking-tight">{value}</span>
        <span className="text-sm text-ink-3">{unit}</span>
      </span>
      {chart}
      <span className="text-[13px] leading-snug text-ink-2">{note}</span>
    </div>
  )
}

function Server({ stats }: { stats: HostStats | null }) {
  const history = stats?.history ?? []
  const last = history[history.length - 1]
  const memTotal = stats?.mem_total ?? 0
  const cpu = history.map((h) => h.cpu_percent)
  const mem = history.map((h) => (memTotal > 0 ? (h.mem_used / memTotal) * 100 : 0))
  const rx = history.map((h) => h.net_rx_bps)
  const tx = history.map((h) => h.net_tx_bps)
  const netScale = Math.max(...rx, ...tx, 0)
  const memPercent = mem[mem.length - 1] ?? 0
  // The headline is whichever direction is busier right now
  const inbound = (last?.net_rx_bps ?? 0) >= (last?.net_tx_bps ?? 0)
  const peak = formatBits(inbound ? last?.net_rx_bps ?? 0 : last?.net_tx_bps ?? 0)
  const memUsed = bytesParts(last?.mem_used ?? 0)
  const rxNow = formatBits(last?.net_rx_bps ?? 0)
  const txNow = formatBits(last?.net_tx_bps ?? 0)

  return (
    <section aria-labelledby="host-title" className="flex flex-col gap-1 rounded-[18px] border border-line bg-white px-7 pt-6 pb-2">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <div className="flex flex-col gap-1">
          <h2 id="host-title" className="font-display text-xl font-semibold tracking-tight">Сервер</h2>
          <span className="text-sm text-ink-3">Нагрузка на машину, где работает балансировщик.</span>
        </div>
        {stats?.available && (
          <span className="text-[13px] text-ink-3">
            {stats.cpus} {plural(stats.cpus, "ядро", "ядра", "ядер")} · {formatBytes(memTotal)} · работает {formatUptime(stats.uptime_seconds)} · 5 минут, столбик = 5 с
          </span>
        )}
      </div>

      {!stats ? (
        <p className="py-5 text-sm text-ink-3">Загружаю…</p>
      ) : !stats.available ? (
        <p className="py-5 text-sm text-ink-3">Показатели сервера собираются, только когда бэкенд работает на Linux.</p>
      ) : !last ? (
        <p className="py-5 text-sm text-ink-3">Собираю данные — первые точки появятся через несколько секунд.</p>
      ) : (
        <div className="flex flex-wrap">
          <HostPanel
            label="Процессор"
            value={formatNumber(last.cpu_percent, 0)}
            unit="%"
            chart={<Bars values={cpu} scale={100} color="bg-cobalt" colorOf={(i) => loadColor(cpu[i])} height={56} gap={2} fade={false} slots={60} />}
            note={<>Средняя загрузка <span className="font-mono">{stats.load.map((l) => formatNumber(l, 2)).join(" · ")}</span></>}
          />
          <HostPanel
            label="Память"
            value={memUsed.value}
            unit={`${memUsed.unit} из ${formatBytes(memTotal)}`}
            chart={<Bars values={mem} scale={100} color="bg-ink" colorOf={(i) => loadColor(mem[i])} height={56} gap={2} fade={false} slots={60} />}
            note={
              <>
                Занято {formatNumber(memPercent, 0)} % · {stats.swap_total > 0 ? `своп ${formatBytes(stats.swap_used)} из ${formatBytes(stats.swap_total)}` : "своп выключен"}
              </>
            }
          />
          <HostPanel
            label={stats.interface ? `Сеть · ${stats.interface}` : "Сеть"}
            icon={
              inbound ? (
                <ArrowUp className="h-7 w-7 text-cobalt" strokeWidth={2.5} aria-label="вход" />
              ) : (
                <ArrowDown className="h-7 w-7 text-ok" strokeWidth={2.5} aria-label="выход" />
              )
            }
            value={peak.value}
            unit={peak.unit}
            chart={
              <div aria-hidden="true" className="flex flex-col gap-0.5">
                <Bars values={rx} scale={netScale} color="bg-cobalt" height={27} gap={2} fade={false} slots={60} />
                <div className="-scale-y-100">
                  <Bars values={tx} scale={netScale} color="bg-ok" height={27} gap={2} fade={false} slots={60} />
                </div>
              </div>
            }
            note={
              <span className="flex flex-wrap gap-x-4">
                <span className={`inline-flex items-center gap-1 ${inbound ? "font-medium text-ink" : ""}`}>
                  <ArrowUp className="h-4 w-4 text-cobalt" strokeWidth={2.5} aria-hidden="true" />
                  вход {rxNow.value} {rxNow.unit}
                </span>
                <span className={`inline-flex items-center gap-1 ${inbound ? "" : "font-medium text-ink"}`}>
                  <ArrowDown className="h-4 w-4 text-ok" strokeWidth={2.5} aria-hidden="true" />
                  выход {txNow.value} {txNow.unit}
                </span>
              </span>
            }
            last
          />
        </div>
      )}
    </section>
  )
}

// --- Page ---

export default function Dashboard() {
  const { lbs, rps, loaded } = useLBs()
  const ids = lbs.slice(0, 12).map((lb) => lb.id)
  const history = usePoll(() => api<MetricsSnapshot[]>("/metrics/history"), [] as MetricsSnapshot[], "global")
  const perLB = usePoll(
    async () => {
      const entries = await Promise.all(ids.map(async (id) => [id, (await api<MetricsSnapshot[]>(`/load-balancers/history?id=${id}`)) || []] as const))
      return Object.fromEntries(entries) as Record<number, MetricsSnapshot[]>
    },
    {} as Record<number, MetricsSnapshot[]>,
    ids.join(","),
  )
  const logs = usePoll(() => api<LogEntry[]>("/logs"), [] as LogEntry[], "logs", 10000)
  const host = usePoll(() => api<HostStats>("/metrics/host"), null as HostStats | null, "host")

  const health = lbs.map(lbHealth)
  const nodesUp = health.reduce((s, h) => s + h.up, 0)
  const nodesTotal = health.reduce((s, h) => s + h.total, 0)
  const standby = health.reduce((s, h) => s + h.standby, 0)
  const failedLBs = lbs.filter((lb) => lb.status === "error")
  const nodesDown = nodesTotal - nodesUp
  const state = failedLBs.length || health.some((h) => h.state === "down") ? "bad" : nodesDown > 0 || health.some((h) => h.state === "degraded") ? "warn" : "ok"

  const last = history[history.length - 1]
  const recent = history.slice(-30)
  const maxRps = Math.max(1, ...lbs.map((lb) => rps[lb.id] ?? 0))
  const running = lbs.length - failedLBs.length
  const errorRate = last?.error_rate ?? 0

  let sentence: React.ReactNode
  if (lbs.length === 0) {
    sentence = "Здесь появится схема трафика, как только вы создадите первый балансировщик."
  } else if (state === "ok") {
    sentence = (
      <>
        {running} {plural(running, "балансировщик принимает", "балансировщика принимают", "балансировщиков принимают")} трафик.{" "}
        <strong className="text-ink">{nodesTotal === 1 ? "Единственный узел здоров" : `Все ${nodesTotal} ${plural(nodesTotal, "узел", "узла", "узлов")} здоровы`}</strong>
        {standby > 0 ? `, ${standby} ${plural(standby, "ждёт", "ждут", "ждут")} в резерве.` : "."}
      </>
    )
  } else {
    sentence = (
      <>
        {failedLBs.length > 0 && <>Не запущен: {failedLBs.map((lb) => `«${lb.name}»`).join(", ")}. </>}
        {nodesDown > 0 && (
          <strong className="text-down-text">
            Не {plural(nodesDown, "отвечает", "отвечают", "отвечают")} {nodesDown} из {nodesTotal} {plural(nodesTotal, "узла", "узлов", "узлов")}.
          </strong>
        )}
      </>
    )
  }

  const pill = {
    ok: { box: "bg-ok-soft text-ok-text", dot: "bg-[#0E9F6E] animate-led", text: "Всё работает" },
    warn: { box: "bg-warn-soft text-warn-text", dot: "bg-warn", text: "Есть замечания" },
    bad: { box: "bg-down-soft text-down-text", dot: "bg-down", text: "Есть сбои" },
  }[state]

  return (
    <div className="flex flex-col gap-7">
      <header className="flex flex-wrap items-end justify-between gap-5">
        <div className="flex max-w-[760px] flex-col gap-2.5">
          <div className="flex flex-wrap items-center gap-2.5 text-[13px] text-ink-3">
            {lbs.length > 0 && (
              <span className={`inline-flex h-[26px] items-center gap-1.5 rounded-full px-2.5 font-semibold ${pill.box}`}>
                <span className={`h-[7px] w-[7px] rounded-full ${pill.dot}`} />
                {pill.text}
              </span>
            )}
            <span>обновляется каждые 5 с · окно 5 минут</span>
          </div>
          <h1 className="font-display text-[44px] font-semibold leading-[1.05] tracking-tight">Пульс</h1>
          <p className="text-lg leading-relaxed text-ink-2">{sentence}</p>
        </div>
        <div className="flex flex-wrap gap-2.5">
          <Link to="/logs" className="inline-flex h-11 items-center rounded-[10px] border border-line bg-white px-4 text-[15px] font-medium text-ink hover:bg-accent">Журнал</Link>
          {canEdit() && (
            <Link to="/lbs/new" className="inline-flex h-11 items-center gap-2 rounded-[10px] bg-cobalt px-[18px] text-[15px] font-semibold text-white hover:bg-cobalt-dark">
              <Plus className="h-[18px] w-[18px]" aria-hidden="true" />
              Новый балансировщик
            </Link>
          )}
        </div>
      </header>

      {loaded && lbs.length === 0 ? (
        <section className="flex flex-col gap-5 rounded-[20px] border-2 border-dashed border-line-strong p-8">
          <h2 className="font-display text-2xl font-semibold tracking-tight">Что балансируем?</h2>
          <p className="max-w-xl text-ink-2">Выберите рецепт — настройки подставятся сами, останется указать адреса узлов.</p>
          <div className="flex flex-wrap gap-2.5">
            {recipeList.map((r) => (
              <Link key={r.key} to={`/lbs/new?recipe=${r.key}`} className="inline-flex h-[52px] items-center rounded-[14px] border border-line bg-white px-[18px] text-[15px] font-medium text-ink hover:border-cobalt">
                {r.label}
              </Link>
            ))}
          </div>
        </section>
      ) : (
        <>
          <section aria-label="Показатели" className="flex flex-wrap overflow-hidden rounded-[18px] border border-line bg-white">
            <Readout label="Запросов в секунду" value={formatNumber(last?.rps ?? 0, (last?.rps ?? 0) < 10 ? 1 : 0)} unit="запр/с">
              <Bars values={recent.map((s) => s.rps)} color="bg-cobalt" />
            </Readout>
            <Readout label="Задержка узлов" value={formatNumber(last?.avg_latency_ms ?? 0, 1)} unit="мс">
              <Bars values={recent.map((s) => s.avg_latency_ms)} color="bg-ink" />
            </Readout>
            <Readout label="Ошибки" value={formatNumber(errorRate, 2)} unit="%">
              <div className={`flex h-9 items-center gap-2 text-[13px] ${errorRate < 1 ? "text-ok-text" : "text-down-text"}`}>
                {errorRate < 1 ? <Check className="h-[18px] w-[18px]" aria-hidden="true" /> : <TriangleAlert className="h-[18px] w-[18px]" aria-hidden="true" />}
                {errorRate < 1 ? "ниже порога 1 %" : "выше порога 1 %"}
              </div>
            </Readout>
            <Readout label="Узлы" value={`${nodesUp}/${nodesTotal}`} unit="здоровы" last>
              <div aria-hidden="true" className="flex h-9 items-center gap-1.5">
                {lbs.flatMap((lb) => nodeViews(lb)).slice(0, 16).map((v, i) => (
                  <span
                    key={i}
                    className={`box-border h-2.5 flex-1 rounded-[3px] ${v.state === "active" ? "bg-ok" : v.state === "standby" ? "border-2 border-ok bg-white" : v.state === "down" ? "bg-down" : "bg-line"}`}
                  />
                ))}
              </div>
            </Readout>
          </section>

          <section aria-labelledby="flow-title" className="flex flex-col gap-5 rounded-[18px] border border-line bg-white px-7 py-6">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex flex-col gap-1">
                <h2 id="flow-title" className="font-display text-xl font-semibold tracking-tight">Поток</h2>
                <span className="text-sm text-ink-3">Точки — запросы, толщина линии — доля трафика узла. Пунктир — узлы, которые сейчас не получают запросов.</span>
              </div>
              <div className="flex gap-4 text-[13px] text-ink-2">
                <span className="inline-flex items-center gap-1.5">
                  <svg aria-hidden="true" width="26" height="10" fill="none">
                    <path d="M 5 5 H 21" stroke="#E7EBFF" strokeWidth={10} strokeLinecap="round" />
                    <path d="M 5 5 H 21" stroke="#2E4BFF" strokeWidth={5} strokeLinecap="round" strokeDasharray="0 8" />
                  </svg>
                  трафик
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <svg aria-hidden="true" width="22" height="10" fill="none"><path d="M 0 5 H 22" stroke="#B7C0CC" strokeWidth={2} strokeDasharray="5 6" /></svg>
                  ожидание
                </span>
              </div>
            </div>
            {lbs.map((lb, i) => (
              <div key={lb.id} className="flex flex-col gap-5">
                {i > 0 && <div className="h-px bg-line-soft" />}
                <FlowRow lb={lb} rps={rps[lb.id] ?? 0} maxRps={maxRps} />
              </div>
            ))}
          </section>

          <div className="flex flex-wrap items-stretch gap-6">
            <section aria-labelledby="seis-title" className="flex min-w-0 flex-[2_1_560px] flex-col gap-5 rounded-[18px] border border-line bg-white px-7 py-6">
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <h2 id="seis-title" className="font-display text-xl font-semibold tracking-tight">Сейсмограф</h2>
                <span className="text-[13px] text-ink-3">5 минут · черта = 5 секунд · высота = нагрузка</span>
              </div>
              <div className="flex flex-col gap-[18px]">
                {lbs.slice(0, 12).map((lb) => <Seismograph key={lb.id} lb={lb} history={perLB[lb.id] ?? []} />)}
              </div>
              <div className="flex flex-wrap gap-[18px] text-[13px] text-ink-2">
                <span className="inline-flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-sm bg-ok" />норма</span>
                <span className="inline-flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-sm bg-warn" />медленно</span>
                <span className="inline-flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-sm bg-down" />ошибки</span>
                <span className="inline-flex items-center gap-1.5"><span className="h-2.5 w-2.5 rounded-sm bg-line-strong" />тишина</span>
              </div>
            </section>
            <Events logs={logs} />
          </div>
        </>
      )}

      <Server stats={host} />
    </div>
  )
}

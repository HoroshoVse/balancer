import { useRef, useState } from "react"
import { Link, useNavigate, useParams } from "react-router-dom"
import { toast } from "sonner"
import { Ellipsis, Globe, ShieldCheck } from "lucide-react"
import { Bars } from "@/components/Bars"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { FlowPaths } from "@/components/Flow"
import { Switch } from "@/components/Switch"
import { AdviceCard, StatusPill } from "@/components/LBBits"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { adviceFor } from "@/lib/advice"
import { api, canEdit, errorMessage } from "@/lib/api"
import { accessText, countryName, flag } from "@/lib/countries"
import { describe, formatMs, fromLB } from "@/lib/builderModel"
import { across, branch, down, flowOf, shareWidth, useMeasured, type FlowKind, type FlowPath } from "@/lib/flow"
import { humanize } from "@/lib/humanize"
import { deleteLB, mapNodes, testNode, updateLB } from "@/lib/lbActions"
import { useLBs } from "@/lib/lbs"
import { activeNodes, algorithmLabel, backupQueue, formatNumber, kindLabel, kindOf, listenAddr, orderedBackups } from "@/lib/lbModel"
import { usePoll } from "@/lib/usePoll"
import type { BackendNode, LoadBalancer, LogEntry, MetricsSnapshot } from "@/types"

const checkLabel: Record<string, string> = { dns: "DNS-запрос", http: "HTTP-запрос", https: "HTTPS-запрос", tcp: "подключение TCP", udp: "пакет UDP" }

type NodeState = "active" | "standby" | "down" | "off"

function nodeState(lb: LoadBalancer, b: BackendNode): NodeState {
  if (!b.enabled) return "off"
  if (b.status === "DOWN") return "down"
  return activeNodes(lb).some((a) => a.id === b.id) ? "active" : "standby"
}

// --- Topology ---

type Geometry = { wide: boolean; paths: FlowPath[]; label?: { x: number; y: number; wide: boolean } }

// Left to right when everything fits: clients 64 + listen 180 + rule 180 + nodes 240,
// gaps of at least 64 + 36 + 56 for the links, 64 of padding. Narrower diagrams stack.
const WIDE_MIN = 884
const TRUNK = 32 // stacked layout: x of the vertical line, the centre of the clients circle

const PIPE = 10 // px, the width of a link carrying all the traffic

function Topology({ lb, rps }: { lb: LoadBalancer; rps: number }) {
  const kind = kindOf(lb)
  const failed = lb.status === "error"
  const live = failed ? 0 : rps
  const nodes = lb.backend_group?.backends ?? []
  const domain = (lb.acme_domains || "").split(",")[0]?.trim()
  const ring: Record<NodeState, { ring: string; label: string; card: string; text: string }> = {
    active: { ring: "border-[3px] border-ok-bright text-ok-pale", label: "UP", card: "border border-[#1F6B52] bg-rail-2", text: "" },
    standby: { ring: "border-[3px] border-dashed border-ok-bright text-ok-pale", label: "ЖДЁТ", card: "border border-dashed border-[#3A4555] bg-rail", text: "здоров, трафика нет" },
    down: { ring: "border-[3px] border-[#FF8A5B] text-[#FFB08F]", label: "DOWN", card: "border border-[#7A2E12] bg-[#1A0F0B]", text: "не отвечает на проверки" },
    off: { ring: "border-[3px] border-[#3A4555] text-rail-muted", label: "ВЫКЛ", card: "border border-dashed border-[#2A3340] bg-rail", text: "выведен из работы" },
  }
  const states = nodes.map((b) => nodeState(lb, b))
  const flow = flowOf(lb, live)
  const ordered = orderedBackups(lb)
  const queue = backupQueue(nodes)
  const queueNodes = [...queue.keys()]
  const roleOf = (b: BackendNode) => (!b.backup ? "основной" : ordered && queue.has(b) ? `резерв ${queue.get(b)}` : "резерв")
  // In ordered mode a waiting backup says where it stands in the queue
  const waitingText = (b: BackendNode) => {
    if (!ordered || !queue.has(b)) return ring.standby.text
    const before = queueNodes[(queue.get(b) ?? 1) - 2]
    return before ? `следующий после ${before.name || before.address}` : "первый на замену"
  }

  const sectionRef = useRef<HTMLElement>(null)
  const clientsRef = useRef<HTMLSpanElement>(null)
  const listenRef = useRef<HTMLDivElement>(null)
  const ruleRef = useRef<HTMLDivElement>(null)
  const nodeRefs = useRef<(HTMLDivElement | null)[]>([])
  const geo = useMeasured<Geometry>(
    sectionRef,
    (box, width) => {
      const wide = width >= WIDE_MIN
      const clients = box(clientsRef.current)
      const listen = box(listenRef.current)
      const rule = box(ruleRef.current)
      if (!clients || !listen || !rule) return { wide, paths: [] }
      const edge = { kind: (live > 0 ? "live" : "idle") as FlowKind, width: PIPE, share: 1 }
      const x = clients.l + TRUNK
      const paths: FlowPath[] = wide
        ? [{ d: across(clients, listen), ...edge }, { d: across(listen, rule), ...edge }]
        : [{ d: down(clients, x, listen), ...edge }, { d: down(listen, x, rule), ...edge }]
      nodes.forEach((b, i) => {
        const node = box(nodeRefs.current[i])
        const f = flow(b)
        if (node) paths.push({ d: wide ? across(rule, node) : branch(rule, rule.l + TRUNK, node), kind: f.kind, width: shareWidth(PIPE, f.share), share: f.share })
      })
      const label = wide ? { x: (clients.r + listen.l) / 2, y: clients.cy - 10, wide } : { x: x + 14, y: (clients.b + listen.t) / 2, wide }
      return { wide, paths, label }
    },
    { wide: true, paths: [] },
  )

  const { wide } = geo
  return (
    <section ref={sectionRef} aria-label="Схема балансировщика" className="relative rounded-[22px] bg-rail px-6 py-8 text-rail-text sm:px-8">
      <FlowPaths paths={geo.paths} tone="dark" />
      {geo.label && (
        <span
          aria-hidden="true"
          className="pointer-events-none absolute whitespace-nowrap font-mono text-[11px] text-cobalt-light"
          style={{ left: geo.label.x, top: geo.label.y, transform: geo.label.wide ? "translate(-50%, -100%)" : "translateY(-50%)" }}
        >
          {live > 0 ? `${formatNumber(live, live < 10 ? 1 : 0)} запр/с` : "тихо"}
        </span>
      )}

      <div className={`relative flex ${wide ? "items-center gap-0" : "flex-col items-stretch gap-12"}`}>
        <div className={`flex flex-none items-center ${wide ? "relative w-16 justify-center" : "gap-3"}`}>
          <span ref={clientsRef} data-flow className="grid h-16 w-16 flex-none place-items-center rounded-full border border-[#2A3340] bg-rail">
            <Globe className="h-7 w-7 text-cobalt-light" aria-hidden="true" />
          </span>
          <span className={`text-[13px] text-[#A9B3C1] ${wide ? "absolute left-1/2 top-full mt-2 -translate-x-1/2" : ""}`}>Клиенты</span>
        </div>
        {wide && <div aria-hidden="true" className="min-w-[64px] flex-1" />}
        <div ref={listenRef} data-flow className={`flex flex-none flex-col gap-2 rounded-2xl border bg-rail-2 p-4 ${wide ? "w-[180px]" : ""} ${failed ? "border-down" : "border-[#222B36]"}`}>
          <span className="font-mono text-[11px] uppercase tracking-[0.12em] text-rail-muted">Приём</span>
          <span className="break-all font-mono text-base text-white">{listenAddr(lb)}</span>
          <span className="text-[13px] text-[#C6CEDA]">{kindLabel[kind]}{lb.http3_enabled ? " · HTTP/3" : ""}</span>
          {kind === "https_term" && (
            <span className="inline-flex items-center gap-1.5 text-[13px] text-ok-pale">
              <ShieldCheck className="h-3.5 w-3.5 flex-none" aria-hidden="true" />
              {lb.acme_enabled || lb.auto_ssl ? `${domain || "домен не указан"} · Let's Encrypt` : lb.cert_data || lb.cert_path ? "свой сертификат" : "самоподписанный"}
            </span>
          )}
          {failed && <span className="text-[13px] text-[#FF9A6B]">не запущен</span>}
        </div>
        {wide && <div aria-hidden="true" className="min-w-[36px] flex-1" />}
        <div ref={ruleRef} data-flow className={`flex flex-none flex-col gap-2 rounded-2xl border border-[#3A4DB8] bg-cobalt-deep p-4 ${wide ? "w-[180px]" : ""}`}>
          <span className="font-mono text-[11px] uppercase tracking-[0.12em] text-[#A8B6FF]">Правило</span>
          <span className="text-base font-semibold text-white">{algorithmLabel[lb.algorithm] ?? lb.algorithm}</span>
          {ordered && queueNodes.length > 1 ? (
            <span className="text-[13px] leading-snug text-[#C9D2FF]">резерв по порядку: {queueNodes.map((b) => b.name || b.address).join(" → ")}</span>
          ) : (
            nodes.some((b) => b.backup) && <span className="text-[13px] leading-snug text-[#C9D2FF]">резерв ждёт отказа основных</span>
          )}
          {lb.sticky_sessions_enabled && <span className="text-[13px] text-[#C9D2FF]">закрепление по {lb.sticky_session_type === "cookie" ? "cookie" : "IP"}</span>}
        </div>
        {wide && <div aria-hidden="true" className="min-w-[56px] flex-1" />}
        <div className={`flex flex-none flex-col gap-3 ${wide ? "w-[240px]" : "pl-14"}`}>
          {nodes.length === 0 && <span className="text-sm text-[#A9B3C1]">узлов нет</span>}
          {nodes.map((b, i) => {
            const st = states[i]
            const r = ring[st]
            return (
              <div
                key={b.id ?? b.address}
                ref={(el) => {
                  nodeRefs.current[i] = el
                }}
                data-flow
                className={`flex min-w-0 items-center gap-3.5 rounded-2xl px-4 py-3.5 ${r.card}`}
              >
                <span className={`grid h-11 w-11 flex-none place-items-center rounded-full font-mono text-[10px] ${r.ring}`}>{r.label}</span>
                <span className="flex min-w-0 flex-col gap-0.5">
                  <span className="truncate text-[15px] font-semibold text-white">
                    {b.name || b.address} <span className="text-[13px] font-normal text-[#A9B3C1]">· {roleOf(b)}</span>
                  </span>
                  <span className="truncate font-mono text-xs text-[#A9B3C1]">{b.address}:{b.port}{b.tls_enabled ? " · TLS" : ""}</span>
                  <span className="truncate text-xs text-[#C6CEDA]">
                    {st === "active" ? `${formatNumber(b.metrics?.avg_latency_ms ?? 0, 1)} мс · ${formatNumber(b.metrics?.rps ?? 0, (b.metrics?.rps ?? 0) < 10 ? 1 : 0)} запр/с` : st === "standby" ? waitingText(b) : r.text}
                  </span>
                </span>
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}

function Chart({ label, value, values, color, colorOf }: { label: string; value: string; values: number[]; color: string; colorOf?: (i: number) => string | undefined }) {
  return (
    <div className="flex min-w-[200px] flex-1 flex-col gap-2.5">
      <span className="flex justify-between text-[13px] text-ink-3">
        <span>{label}</span>
        <span className="font-mono text-ink">{value}</span>
      </span>
      <div className="border-b border-line-soft">
        {values.length > 1 ? <Bars values={values} color={color} height={72} gap={2} fade={false} colorOf={colorOf} /> : <div className="grid h-[72px] place-items-center text-xs text-ink-3">копим данные…</div>}
      </div>
    </div>
  )
}

function NodesTable({ lb, onChanged }: { lb: LoadBalancer; onChanged: () => void }) {
  const editable = canEdit()
  const nodes = lb.backend_group?.backends ?? []
  const queue = backupQueue(nodes)
  const [busy, setBusy] = useState<number | null>(null)
  const [confirmOff, setConfirmOff] = useState<BackendNode | null>(null)
  const [tests, setTests] = useState<Record<string, string>>({})

  const setEnabled = async (node: BackendNode, enabled: boolean) => {
    setBusy(node.id ?? null)
    try {
      await updateLB(lb, (x) => mapNodes(x, (b) => (b.id === node.id ? { ...b, enabled } : b)))
      toast.success(enabled ? `${node.name || node.address} снова принимает трафик` : `${node.name || node.address} выведен из работы`)
      onChanged()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setBusy(null)
      setConfirmOff(null)
    }
  }
  const toggle = (node: BackendNode) => {
    if (node.enabled && nodes.filter((b) => b.enabled).length === 1) setConfirmOff(node)
    else setEnabled(node, !node.enabled)
  }
  const runTest = async (node: BackendNode) => {
    const key = `${node.address}:${node.port}`
    setTests((t) => ({ ...t, [key]: "проверяю…" }))
    const r = await testNode(node, lb.backend_http2_enabled)
    setTests((t) => ({ ...t, [key]: r.ok ? "отвечает" : "не отвечает" }))
    if (!r.ok) toast.error(r.logs.split("\n").filter(Boolean).slice(-2).join(" "))
  }
  const stateText: Record<NodeState, { text: string; cls: string }> = {
    active: { text: "в работе", cls: "text-ok-text" },
    standby: { text: "ждёт в резерве", cls: "text-ink-2" },
    down: { text: "не отвечает", cls: "text-down-text" },
    off: { text: "выключен", cls: "text-ink-3" },
  }

  return (
    <section aria-labelledby="nodes-title" className="overflow-hidden rounded-[18px] border border-line bg-white">
      <div className="flex items-center justify-between px-6 py-5">
        <h2 id="nodes-title" className="font-display text-lg font-semibold tracking-tight">Узлы</h2>
        {editable && <Link to={`/lbs/${lb.id}/edit`} className="text-sm font-medium text-cobalt hover:text-cobalt-dark">+ Добавить узел</Link>}
      </div>
      <div className="relative overflow-x-auto">
        <table className="w-full min-w-[760px] border-collapse text-sm">
          <thead>
            <tr className="text-left text-xs text-ink-3">
              <th scope="col" className="border-t border-line-soft px-6 py-2.5 font-medium">Узел</th>
              <th scope="col" className="border-t border-line-soft px-3 py-2.5 font-medium">Роль</th>
              <th scope="col" className="border-t border-line-soft px-3 py-2.5 font-medium">Состояние</th>
              <th scope="col" className="border-t border-line-soft px-3 py-2.5 font-medium">Нагрузка</th>
              <th scope="col" className="border-t border-line-soft px-3 py-2.5 font-medium">Задержка</th>
              <th scope="col" className="border-t border-line-soft px-3 py-2.5 font-medium">Принимает</th>
              <th scope="col" className="border-t border-line-soft px-6 py-2.5"><span className="sr-only">Проверка</span></th>
            </tr>
          </thead>
          <tbody>
            {nodes.map((b) => {
              const st = nodeState(lb, b)
              const key = `${b.address}:${b.port}`
              return (
                <tr key={b.id ?? key}>
                  <td className="border-t border-ground px-6 py-3.5">
                    <span className="flex items-center gap-2.5">
                      <span className={`box-border h-[9px] w-[9px] flex-none rounded-full ${st === "active" ? "bg-ok" : st === "standby" ? "border-2 border-ok" : st === "down" ? "bg-down" : "bg-line-strong"}`} />
                      <span className="flex flex-col">
                        <strong className="font-semibold">{b.name || b.address}</strong>
                        <span className="font-mono text-xs text-ink-3">{key}{b.tls_enabled ? " · TLS" : ""}</span>
                      </span>
                    </span>
                  </td>
                  <td className="border-t border-ground px-3 py-3.5">{b.backup && orderedBackups(lb) && queue.has(b) ? `резерв ${queue.get(b)}` : b.backup ? "резерв" : "основной"}</td>
                  <td className={`border-t border-ground px-3 py-3.5 ${stateText[st].cls}`}>{stateText[st].text}</td>
                  <td className="border-t border-ground px-3 py-3.5 font-mono">{formatNumber(b.metrics?.rps ?? 0, (b.metrics?.rps ?? 0) < 10 ? 1 : 0)}/с</td>
                  <td className="border-t border-ground px-3 py-3.5 font-mono">{st === "active" ? `${formatNumber(b.metrics?.avg_latency_ms ?? 0, 1)} мс` : "—"}</td>
                  <td className="border-t border-ground px-3 py-3.5">
                    <span className="inline-flex items-center gap-2">
                      <Switch
                        tone="ok"
                        checked={b.enabled}
                        disabled={!editable || busy === b.id}
                        onChange={() => toggle(b)}
                        label={`${b.name || b.address} принимает трафик`}
                      />
                      <span className="text-[13px] text-ink-2">{b.enabled ? "да" : "нет"}</span>
                    </span>
                  </td>
                  <td className="border-t border-ground px-6 py-3.5 text-right">
                    {kindOf(lb) !== "udp" && (
                      <span className="inline-flex items-center gap-2">
                        {tests[key] && <span className={`text-xs ${tests[key] === "отвечает" ? "text-ok-text" : tests[key] === "не отвечает" ? "text-down-text" : "text-ink-3"}`}>{tests[key]}</span>}
                        <button type="button" onClick={() => runTest(b)} className="h-9 rounded-lg border border-line px-3 text-[13px] font-medium hover:bg-accent">Проверить</button>
                      </span>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <ConfirmDialog
        open={!!confirmOff}
        onOpenChange={(open) => !open && setConfirmOff(null)}
        title="Выключить последний узел?"
        text="Других работающих узлов нет — запросы будет некуда отправить, пока вы не включите узел обратно."
        confirmLabel="Выключить"
        danger
        busy={busy !== null}
        onConfirm={() => confirmOff && setEnabled(confirmOff, false)}
      />
    </section>
  )
}

/** Who the balancer lets in, and who it has turned away since it started. */
function AccessCard({ lb }: { lb: LoadBalancer }) {
  const blocked = lb.metrics?.blocked ?? 0
  const top = lb.metrics?.blocked_top ?? []
  const biggest = Math.max(1, ...top.map((t) => t.count))
  const ruled = !!(lb.access_mode || lb.access_allow?.trim() || lb.access_deny?.trim())
  if (!ruled && blocked === 0) return null
  return (
    <section aria-labelledby="acc-title" className="flex flex-col gap-3.5 rounded-[18px] border border-line bg-white px-6 py-5">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="acc-title" className="font-display text-lg font-semibold tracking-tight">Фильтр доступа</h2>
        <span className="text-sm text-ink-2">{accessText(lb)}</span>
      </div>
      {blocked > 0 ? (
        <>
          <p className="text-sm text-ink-2">
            Отсечено с запуска: <strong className="font-mono text-ink">{formatNumber(blocked)}</strong>. Откуда:
          </p>
          <ul className="flex flex-col gap-2">
            {top.map((t) => (
              <li key={t.country || "none"} className="flex items-center gap-3 text-sm">
                <span className="w-44 flex-none truncate">
                  {t.country === "list" ? (
                    "Список запрещённых"
                  ) : t.country === "limit" ? (
                    "Предел UDP-сессий"
                  ) : (
                    <>
                      <span aria-hidden="true">{flag(t.country)}</span> {t.country ? countryName(t.country) : "страна не определена"}
                    </>
                  )}
                </span>
                <span className="h-2.5 flex-1 overflow-hidden rounded-full bg-ground">
                  <span className="block h-full rounded-full bg-down" style={{ width: `${Math.max(2, (t.count / biggest) * 100)}%` }} />
                </span>
                <span className="w-16 flex-none text-right font-mono text-ink-2">{formatNumber(t.count)}</span>
              </li>
            ))}
          </ul>
        </>
      ) : (
        <p className="text-sm text-ink-3">С момента запуска никого не отсекали.</p>
      )}
    </section>
  )
}

function Summary({ lb }: { lb: LoadBalancer }) {
  const kind = kindOf(lb)
  const first = lb.backend_group?.backends?.[0]
  const http = kind === "http" || kind === "https_term"
  const rows: [string, string][] = [
    ["Приём", `${kindLabel[kind]} · ${listenAddr(lb)}`],
    ["Доступ", accessText(lb)],
    ["Алгоритм", algorithmLabel[lb.algorithm] ?? lb.algorithm],
    ["Закрепление", lb.sticky_sessions_enabled ? (lb.sticky_session_type === "cookie" ? "по cookie" : "по IP клиента") : "выключено"],
    ["Проверка узлов", first?.hc_enabled ? `${checkLabel[first.hc_protocol] ?? first.hc_protocol} · ${first.hc_interval || 5} с · ${first.hc_failure_threshold || 1}/${first.hc_recovery_threshold || 1}` : "выключена"],
    ["Подключение", formatMs(lb.connect_timeout, "5 с")],
    ["Простой", formatMs(lb.idle_timeout, kind === "udp" ? "60 с" : http ? "120 с" : "без огр.")],
  ]
  if (http || kind === "dns") rows.push(["Ответ узла", formatMs(lb.response_timeout, kind === "dns" ? "5 с" : "без огр.")])
  rows.push(["PROXY protocol", lb.proxy_protocol_enabled ? `v${lb.proxy_protocol_version}` : "выключен"])
  if (kind === "https_term") rows.push(["Переадресация :80", lb.http_redirect ? "включена" : "выключена"])
  return (
    <dl className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)] gap-x-4 gap-y-3 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-ink-3">{k}</dt>
          <dd className="font-medium">{v}</dd>
        </div>
      ))}
    </dl>
  )
}

export default function LoadBalancerDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { lbs, rps, loaded, refresh } = useLBs()
  const lb = lbs.find((l) => l.id === Number(id))
  const [tab, setTab] = useState<"overview" | "logs" | "settings">("overview")
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [testing, setTesting] = useState(false)

  const history = usePoll(() => api<MetricsSnapshot[]>(`/load-balancers/history?id=${encodeURIComponent(id ?? "")}`), [] as MetricsSnapshot[], `h-${id}`)
  const logs = usePoll(() => (lb ? api<LogEntry[]>(`/logs?lb_name=${encodeURIComponent(lb.name)}`) : Promise.resolve([] as LogEntry[])), [] as LogEntry[], `l-${lb?.name}`, 10000)

  if (loaded && !lb) {
    return (
      <div className="flex flex-col items-start gap-4">
        <h1 className="font-display text-3xl font-semibold">Не найден</h1>
        <p className="text-ink-2">Такого балансировщика нет — возможно, его удалили.</p>
        <Link to="/lbs" className="font-medium text-cobalt">К списку</Link>
      </div>
    )
  }
  if (!lb) return <div className="py-16 text-center text-ink-3">Загрузка…</div>

  const editable = canEdit()
  const advice = adviceFor(lb)
  const last = history[history.length - 1]
  const recent = history.slice(-60)

  const checkNodes = async () => {
    if (kindOf(lb) === "udp") {
      toast.info("Для UDP-узлов проверка соединения недоступна — смотрите их состояние на схеме")
      return
    }
    setTesting(true)
    const results = await Promise.all((lb.backend_group?.backends ?? []).filter((b) => b.enabled).map((b) => testNode(b, lb.backend_http2_enabled)))
    setTesting(false)
    const ok = results.filter((r) => r.ok).length
    const text = results.map((r) => `${r.node.name || r.node.address} — ${r.ok ? "отвечает" : "не отвечает"}`).join(", ")
    if (ok === results.length) toast.success(`Все узлы отвечают: ${text}`)
    else toast.error(`Отвечают ${ok} из ${results.length}: ${text}`)
  }
  const remove = async () => {
    setDeleting(true)
    try {
      await deleteLB(lb)
      toast.success(`«${lb.name}» удалён`)
      await refresh()
      navigate("/lbs")
    } catch (err) {
      toast.error(errorMessage(err))
      setDeleting(false)
    }
  }

  const tabs = [
    { key: "overview" as const, label: "Обзор" },
    { key: "logs" as const, label: `Журнал${logs.length ? ` · ${logs.length}` : ""}` },
    { key: "settings" as const, label: "Настройки" },
  ]

  return (
    <div className="flex flex-col gap-6">
      <nav aria-label="Путь" className="text-sm text-ink-3">
        <Link to="/lbs" className="text-cobalt hover:text-cobalt-dark">Балансировщики</Link> <span aria-hidden="true">/</span> {lb.name}
      </nav>

      <header className="flex flex-wrap items-center justify-between gap-4">
        <div className="flex flex-wrap items-center gap-3.5">
          <h1 className="font-display text-[40px] font-semibold leading-tight tracking-tight">{lb.name}</h1>
          <StatusPill lb={lb} size="md" />
        </div>
        <div className="flex flex-wrap gap-2.5">
          <button type="button" disabled={testing} onClick={checkNodes} className="h-11 rounded-[10px] border border-line bg-white px-4 text-[15px] font-medium hover:bg-accent disabled:opacity-60">
            {testing ? "Проверяю…" : "Проверить узлы"}
          </button>
          {editable && (
            <>
              <Link to={`/lbs/${lb.id}/edit`} className="inline-flex h-11 items-center rounded-[10px] bg-ink px-[18px] text-[15px] font-semibold text-white hover:bg-ink-4">Изменить</Link>
              <DropdownMenu>
                <DropdownMenuTrigger aria-label="Ещё действия" className="grid h-11 w-11 place-items-center rounded-[10px] border border-line bg-white text-ink-2 hover:bg-accent">
                  <Ellipsis className="h-[18px] w-[18px]" aria-hidden="true" />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="min-w-[200px] rounded-xl p-1.5">
                  <DropdownMenuItem onSelect={() => setConfirmDelete(true)} className="h-10 cursor-pointer rounded-lg px-3 text-[15px] text-down focus:text-down">Удалить…</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </>
          )}
        </div>
      </header>

      {lb.status === "error" && lb.status_error && (
        <div role="alert" className="rounded-[14px] border border-down/30 bg-down-soft px-5 py-4 text-down-text">
          <strong className="font-semibold">Балансировщик не запустился.</strong> {lb.status_error}
        </div>
      )}

      <Topology lb={lb} rps={rps[lb.id] ?? 0} />

      <div role="tablist" aria-label="Разделы балансировщика" className="flex flex-wrap gap-1.5 border-b border-line">
        {tabs.map((t) => (
          <button
            key={t.key}
            type="button"
            role="tab"
            aria-selected={tab === t.key}
            onClick={() => setTab(t.key)}
            className={`-mb-px h-11 px-4 text-[15px] ${tab === t.key ? "border-b-2 border-ink font-semibold text-ink" : "font-medium text-ink-3 hover:text-ink"}`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "overview" && (
        <div className="flex flex-wrap items-start gap-6">
          <div className="flex min-w-0 flex-[2_1_560px] flex-col gap-6">
            <section aria-label="Метрики за 5 минут" className="flex flex-wrap gap-7 rounded-[18px] border border-line bg-white px-6 py-5">
              <Chart label="Запросы" value={`${formatNumber(last?.rps ?? 0, (last?.rps ?? 0) < 10 ? 1 : 0)}/с`} values={recent.map((s) => s.rps)} color="bg-cobalt" />
              <Chart label="Задержка" value={`${formatNumber(last?.avg_latency_ms ?? 0, 1)} мс`} values={recent.map((s) => s.avg_latency_ms)} color="bg-ink" />
              <Chart
                label="Ошибки"
                value={`${formatNumber(last?.error_rate ?? 0, 2)} %`}
                values={recent.map((s) => (s.error_rate > 0 ? s.error_rate : 0.0001))}
                color="bg-line-strong"
                colorOf={(i) => (recent[i]?.error_rate > 0 ? "bg-down" : undefined)}
              />
            </section>
            <AccessCard lb={lb} />
            <NodesTable lb={lb} onChanged={refresh} />
          </div>
          <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-6">
            {advice.length > 0 && (
              <section aria-labelledby="adv-title" className="flex flex-col gap-3.5 rounded-[18px] border border-line bg-white p-6">
                <h2 id="adv-title" className="font-display text-lg font-semibold tracking-tight">Советы · {advice.length}</h2>
                {advice.map((a) => <AdviceCard key={a.id} advice={a} lb={lb} onApplied={refresh} />)}
              </section>
            )}
            <section aria-labelledby="sum-title" className="flex flex-col gap-3.5 rounded-[18px] border border-line bg-white p-6">
              <h2 id="sum-title" className="font-display text-lg font-semibold tracking-tight">Коротко о настройках</h2>
              <p className="text-sm leading-relaxed text-ink-2">{describe(fromLB(lb))}</p>
              <Summary lb={lb} />
              {editable && <Link to={`/lbs/${lb.id}/edit`} className="self-start text-sm font-medium text-cobalt hover:text-cobalt-dark">Изменить настройки →</Link>}
            </section>
          </div>
        </div>
      )}

      {tab === "logs" && (
        <section aria-label="Журнал балансировщика" className="flex flex-col rounded-[18px] bg-rail py-2 text-[#CAD3DF]">
          {logs.length === 0 && <p className="px-6 py-8 text-rail-muted">Записей пока нет.</p>}
          {[...logs].reverse().slice(0, 200).map((e, i) => {
            const h = humanize(e)
            return (
              <div key={i} className={`flex flex-wrap items-baseline gap-x-3.5 gap-y-1 px-6 py-2.5 ${e.level === "ERROR" ? "bg-[#170D0A]" : e.level === "WARN" ? "bg-[#14110A]" : ""}`}>
                <span className="w-[72px] flex-none font-mono text-[13px] text-rail-muted">{new Date(e.timestamp).toLocaleTimeString("ru-RU")}</span>
                <span className={`flex-none rounded px-1.5 py-0.5 font-mono text-[11px] ${e.level === "ERROR" ? "bg-[#3D1607] text-[#FF9A6B]" : e.level === "WARN" ? "bg-[#3A2A05] text-[#F5C451]" : "bg-rail-4 text-[#C6CEDA]"}`}>
                  {e.level === "ERROR" ? "ОШИБКА" : e.level === "WARN" ? "ВНИМ" : "ИНФО"}
                </span>
                <span className="min-w-0 flex-[1_1_320px] text-[15px] text-rail-text [overflow-wrap:anywhere]" title={e.message}>{h.text}</span>
              </div>
            )
          })}
          <Link to="/logs" className="px-6 py-3 text-sm font-medium text-cobalt-light hover:text-white">Весь журнал →</Link>
        </section>
      )}

      {tab === "settings" && (
        <section className="flex max-w-2xl flex-col gap-4 rounded-[18px] border border-line bg-white p-6">
          <p className="leading-relaxed text-ink-2">{describe(fromLB(lb))}</p>
          <Summary lb={lb} />
          {editable && <Link to={`/lbs/${lb.id}/edit`} className="inline-flex h-11 items-center self-start rounded-[10px] bg-cobalt px-4 text-[15px] font-semibold text-white hover:bg-cobalt-dark">Открыть в конструкторе</Link>}
        </section>
      )}

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Удалить «${lb.name}»?`}
        text="Балансировщик сразу перестанет принимать трафик. Узлы и их настройки удалятся вместе с ним."
        confirmLabel="Удалить"
        danger
        busy={deleting}
        onConfirm={remove}
      />
    </div>
  )
}

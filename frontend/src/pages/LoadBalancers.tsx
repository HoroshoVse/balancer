import { useRef, useState } from "react"
import { Link } from "react-router-dom"
import { toast } from "sonner"
import { Ellipsis, Plus, Search } from "lucide-react"
import { Bars } from "@/components/Bars"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { FlowPaths } from "@/components/Flow"
import { AdviceCard, StatusPill } from "@/components/LBBits"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { adviceFor } from "@/lib/advice"
import { api, canEdit, errorMessage } from "@/lib/api"
import { accessTag } from "@/lib/countries"
import { describe, fromLB } from "@/lib/builderModel"
import { across, branch, flowOf, shareWidth, useMeasured, type FlowPath } from "@/lib/flow"
import { deleteLB, testNode } from "@/lib/lbActions"
import { useLBs } from "@/lib/lbs"
import { backupQueue, formatNumber, kindLabel, kindOf, listenAddr, orderedBackups, type Kind } from "@/lib/lbModel"
import { recipeList } from "@/lib/recipes"
import { usePoll } from "@/lib/usePoll"
import type { BackendNode, LoadBalancer, MetricsSnapshot } from "@/types"

type Filter = "all" | "http" | "tcp" | "udp"
const filterOf = (k: Kind): Filter => (k === "http" || k === "https_term" || k === "https_pass" ? "http" : k === "tcp" ? "tcp" : "udp")
const filters: { key: Filter; label: string }[] = [
  { key: "all", label: "Все" },
  { key: "http", label: "HTTP(S)" },
  { key: "tcp", label: "TCP" },
  { key: "udp", label: "UDP / DNS" },
]

function Tags({ lb }: { lb: LoadBalancer }) {
  const kind = kindOf(lb)
  const tags: { text: string; tone?: "cobalt" | "warn" | "down" }[] = [{ text: kindLabel[kind] }]
  if (kind === "https_term" && (lb.acme_enabled || lb.auto_ssl)) {
    const st = lb.acme_status
    tags.push({ text: `Let's Encrypt · ${st === "ok" ? "активен" : st === "error" ? "ошибка" : "выпускается"}`, tone: st === "error" ? "down" : st === "ok" ? "cobalt" : "warn" })
  }
  const access = accessTag(lb)
  if (access) tags.push({ text: access, tone: "cobalt" })
  if (lb.http_redirect) tags.push({ text: ":80 → HTTPS" })
  if (lb.http3_enabled) tags.push({ text: "HTTP/3" })
  if (lb.sticky_sessions_enabled) tags.push({ text: `sticky: ${lb.sticky_session_type === "cookie" ? "cookie" : "IP"}` })
  if (lb.proxy_protocol_enabled) tags.push({ text: `PROXY v${lb.proxy_protocol_version}` })
  const toneClass = { cobalt: "bg-cobalt-soft text-[#2338C7]", warn: "bg-warn-soft text-warn-text", down: "bg-down-soft text-down-text" }
  return (
    <div className="flex flex-wrap gap-1.5 font-mono text-xs">
      {tags.map((t) => (
        <span key={t.text} className={`rounded-md px-2 py-1 ${t.tone ? toneClass[t.tone] : "bg-ground text-ink-4"}`}>{t.text}</span>
      ))}
    </div>
  )
}

function MiniTopology({ lb, rps }: { lb: LoadBalancer; rps: number }) {
  const nodes = lb.backend_group?.backends ?? []
  const shown = nodes.slice(0, 4)
  const queue = orderedBackups(lb) ? backupQueue(nodes) : new Map<BackendNode, number>()
  const live = lb.status === "error" ? 0 : rps
  const flow = flowOf(lb, live)
  const base = live > 0 ? Math.round(Math.min(12, 8 + live / 25)) : 0

  const boxRef = useRef<HTMLDivElement>(null)
  const portRef = useRef<HTMLSpanElement>(null)
  const nodeRefs = useRef<(HTMLSpanElement | null)[]>([])
  const { wide, paths } = useMeasured(
    boxRef,
    (box, width) => {
      const port = box(portRef.current)
      const pills = shown.map((_, i) => box(nodeRefs.current[i]))
      // Side by side when the port, a link of 48 px and the widest node fit inside the padding
      const widest = Math.max(0, ...pills.map((p) => (p ? p.r - p.l : 0)))
      const wide = !port || width - 32 >= port.r - port.l + 48 + widest
      const paths: FlowPath[] = []
      if (port) {
        pills.forEach((pill, i) => {
          if (!pill) return
          const f = flow(shown[i])
          paths.push({ d: wide ? across(port, pill) : branch(port, port.l + 16, pill), kind: f.kind, width: shareWidth(base, f.share), share: f.share })
        })
      }
      return { wide, paths }
    },
    { wide: true, paths: [] as FlowPath[] },
  )

  return (
    <div ref={boxRef} aria-label="Схема" className={`relative flex rounded-[14px] bg-accent p-4 ${wide ? "items-center" : "flex-col items-start gap-4"}`}>
      <FlowPaths paths={paths} />
      <span ref={portRef} data-flow className="relative flex-none rounded-lg bg-rail px-2.5 py-1.5 font-mono text-xs text-rail-text">:{lb.listen_port}</span>
      {wide && <span aria-hidden="true" className="min-w-12 flex-1" />}
      {/* Equal widths side by side; natural widths when stacked, so the widest node measures the same either way */}
      <span className={`relative flex flex-none flex-col gap-2 ${wide ? "" : "items-start pl-10"}`}>
        {nodes.length === 0 && <span className="text-[13px] text-ink-3">узлов нет</span>}
        {shown.map((b, i) => {
          const down = b.enabled && b.status === "DOWN"
          return (
            <span
              key={b.id ?? b.address}
              ref={(el) => {
                nodeRefs.current[i] = el
              }}
              data-flow
              className={`inline-flex h-[30px] items-center gap-2 rounded-lg px-2.5 text-[13px] ${b.backup ? "border border-dashed" : "border"} ${down ? "border-down/40 bg-down-soft" : "border-line bg-white"}`}
            >
              <span className={`box-border h-[7px] w-[7px] flex-none rounded-full ${!b.enabled ? "bg-line-strong" : down ? "bg-down" : b.backup ? "border-2 border-ok" : "bg-ok"}`} />
              {b.name || b.address}
              <span className="font-mono text-ink-3">{!b.enabled ? "выкл." : down ? "не отвечает" : b.backup ? (queue.has(b) ? `резерв ${queue.get(b)}` : "резерв") : b.address}</span>
            </span>
          )
        })}
        {nodes.length > shown.length && <span className="text-xs text-ink-3">и ещё {nodes.length - shown.length}</span>}
      </span>
    </div>
  )
}

function Metric({ label, value, unit, first }: { label: string; value: string; unit: string; first?: boolean }) {
  return (
    <div className={`flex min-w-[110px] flex-1 flex-col gap-1 ${first ? "pr-4" : "border-l border-line-soft px-4"}`}>
      <span className="text-xs text-ink-3">{label}</span>
      <span className="font-display text-[22px] font-semibold">
        {value} <span className="font-sans text-[13px] font-normal text-ink-3">{unit}</span>
      </span>
    </div>
  )
}

function Card({ lb, rps, history, onChanged, onDelete }: { lb: LoadBalancer; rps: number; history: MetricsSnapshot[]; onChanged: () => void; onDelete: () => void }) {
  const editable = canEdit()
  const [testing, setTesting] = useState(false)
  const advice = adviceFor(lb)
  const last = history[history.length - 1]
  const latency = last?.avg_latency_ms ?? lb.metrics?.average_latency_ms ?? 0
  const errors = last?.error_rate ?? lb.metrics?.error_rate_percent ?? 0

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

  return (
    <article className="flex min-w-0 flex-[1_1_520px] flex-col gap-[18px] rounded-[20px] border border-line bg-white p-6 sm:p-7">
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-2.5">
          <div className="flex flex-wrap items-center gap-2.5">
            <h2 className="font-display text-2xl font-semibold tracking-tight">
              <Link to={`/lbs/${lb.id}`} className="text-ink hover:text-cobalt">{lb.name}</Link>
            </h2>
            <StatusPill lb={lb} />
          </div>
          <Tags lb={lb} />
        </div>
        {editable && (
          <DropdownMenu>
            <DropdownMenuTrigger aria-label={`Действия с «${lb.name}»`} className="grid h-11 w-11 flex-none place-items-center rounded-[10px] border border-line text-ink-2 hover:bg-accent">
              <Ellipsis className="h-[18px] w-[18px]" aria-hidden="true" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-[200px] rounded-xl p-1.5">
              <DropdownMenuItem asChild className="h-10 cursor-pointer rounded-lg px-3 text-[15px]"><Link to={`/lbs/${lb.id}/edit`}>Изменить</Link></DropdownMenuItem>
              <DropdownMenuItem onSelect={checkNodes} className="h-10 cursor-pointer rounded-lg px-3 text-[15px]">Проверить узлы</DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={onDelete} className="h-10 cursor-pointer rounded-lg px-3 text-[15px] text-down focus:text-down">Удалить…</DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>

      {lb.status === "error" && lb.status_error && (
        <p className="rounded-[14px] bg-down-soft px-4 py-3 text-sm text-down-text">Не запустился: {lb.status_error}</p>
      )}

      <p className="text-[17px] leading-relaxed text-ink-4">{describe(fromLB(lb))}</p>

      <MiniTopology lb={lb} rps={rps} />

      {advice.filter((a) => a.tone !== "info").slice(0, 1).map((a) => (
        <AdviceCard key={a.id} advice={a} lb={lb} onApplied={onChanged} compact />
      ))}

      <div className="flex flex-wrap">
        <Metric first label="Нагрузка" value={formatNumber(rps, rps > 0 && rps < 10 ? 1 : 0)} unit="запр/с" />
        <Metric label="Задержка" value={formatNumber(latency, 1)} unit="мс" />
        <Metric label="Ошибки" value={formatNumber(errors, errors > 0 && errors < 1 ? 2 : 0)} unit="%" />
      </div>
      {history.length > 1 && <Bars values={history.slice(-48).map((s) => s.rps)} color="bg-cobalt" height={40} gap={2} fade={false} />}

      <div className="mt-auto flex flex-wrap gap-2.5">
        <Link to={`/lbs/${lb.id}`} className="inline-flex h-11 items-center rounded-[10px] bg-ink px-4 text-[15px] font-medium text-white hover:bg-ink-4">Открыть</Link>
        {editable && <Link to={`/lbs/${lb.id}/edit`} className="inline-flex h-11 items-center rounded-[10px] border border-line px-4 text-[15px] font-medium text-ink hover:bg-accent">Изменить</Link>}
        <button type="button" disabled={testing} onClick={checkNodes} className="h-11 rounded-[10px] border border-line px-4 text-[15px] font-medium text-ink hover:bg-accent disabled:opacity-60">
          {testing ? "Проверяю…" : "Проверить узлы"}
        </button>
        {advice.length > 1 && <Link to={`/lbs/${lb.id}`} className="ml-auto inline-flex h-11 items-center text-sm font-medium text-cobalt hover:text-cobalt-dark">Советов: {advice.length}</Link>}
      </div>
    </article>
  )
}

function Recipes({ title, text }: { title: string; text: string }) {
  return (
    <section className="flex flex-wrap items-center gap-x-8 gap-y-5 rounded-[20px] border-2 border-dashed border-line-strong p-6 sm:p-7">
      <div className="flex flex-[1_1_280px] flex-col gap-1.5">
        <h2 className="font-display text-xl font-semibold tracking-tight">{title}</h2>
        <span className="text-[15px] text-ink-2">{text}</span>
      </div>
      <div className="flex flex-[2_1_520px] flex-wrap gap-2.5">
        {recipeList.map((r) => (
          <Link key={r.key} to={`/lbs/new?recipe=${r.key}`} className="inline-flex h-[52px] items-center rounded-[14px] border border-line bg-white px-[18px] text-[15px] font-medium text-ink hover:border-cobalt">
            {r.label}
          </Link>
        ))}
      </div>
    </section>
  )
}

export default function LoadBalancers() {
  const { lbs, rps, loaded, refresh } = useLBs()
  const editable = canEdit()
  const [filter, setFilter] = useState<Filter>("all")
  const [query, setQuery] = useState("")
  const [toDelete, setToDelete] = useState<LoadBalancer | null>(null)
  const [deleting, setDeleting] = useState(false)

  const ids = lbs.slice(0, 20).map((lb) => lb.id)
  const history = usePoll(
    async () => Object.fromEntries(await Promise.all(ids.map(async (id) => [id, (await api<MetricsSnapshot[]>(`/load-balancers/history?id=${id}`)) || []] as const))) as Record<number, MetricsSnapshot[]>,
    {} as Record<number, MetricsSnapshot[]>,
    ids.join(","),
  )

  const q = query.trim().toLowerCase()
  const counts = { all: lbs.length, http: 0, tcp: 0, udp: 0 } as Record<Filter, number>
  for (const lb of lbs) counts[filterOf(kindOf(lb))]++
  const shown = lbs.filter((lb) => {
    if (filter !== "all" && filterOf(kindOf(lb)) !== filter) return false
    if (!q) return true
    const hay = [lb.name, listenAddr(lb), lb.acme_domains, ...(lb.backend_group?.backends ?? []).flatMap((b) => [b.name, b.address])].join(" ").toLowerCase()
    return hay.includes(q)
  })

  const confirmDelete = async () => {
    if (!toDelete) return
    setDeleting(true)
    try {
      await deleteLB(toDelete)
      toast.success(`«${toDelete.name}» удалён`)
      setToDelete(null)
      refresh()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-5">
        <div className="flex flex-col gap-2">
          <h1 className="font-display text-[28px] font-semibold sm:text-[44px] leading-[1.05] tracking-tight">Балансировщики</h1>
          <p className="text-[17px] text-ink-2">Каждый объясняет, что делает, — без таблиц из флажков.</p>
        </div>
        {editable && (
          <Link to="/lbs/new" className="inline-flex h-11 items-center gap-2 rounded-[10px] bg-cobalt px-[18px] text-[15px] font-semibold text-white hover:bg-cobalt-dark">
            <Plus className="h-[18px] w-[18px]" aria-hidden="true" />
            Новый балансировщик
          </Link>
        )}
      </header>

      {loaded && lbs.length === 0 ? (
        <Recipes title="Пока ни одного балансировщика" text="Выберите рецепт — настройки подставятся сами, останется указать адреса узлов." />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div role="group" aria-label="Фильтр по протоколу" className="inline-flex flex-wrap gap-1 rounded-xl border border-line bg-white p-1">
              {filters.map((f) => (
                <button
                  key={f.key}
                  type="button"
                  aria-pressed={filter === f.key}
                  onClick={() => setFilter(f.key)}
                  className={`h-9 rounded-lg px-3.5 text-sm font-medium ${filter === f.key ? "bg-ink text-white" : "text-ink-2 hover:text-ink"}`}
                >
                  {f.label} <span className="opacity-70">{counts[f.key]}</span>
                </button>
              ))}
            </div>
            <label className="flex h-11 w-80 max-w-full items-center gap-2.5 rounded-xl border border-line bg-white px-3.5 text-ink-3 focus-within:border-cobalt">
              <Search className="h-[18px] w-[18px]" aria-hidden="true" />
              <span className="sr-only">Поиск по имени, адресу или узлу</span>
              <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Имя, адрес или узел" className="min-w-0 flex-1 bg-transparent text-[15px] text-ink outline-none" />
            </label>
          </div>

          <div className="flex flex-wrap items-stretch gap-6">
            {shown.map((lb) => (
              <Card key={lb.id} lb={lb} rps={rps[lb.id] ?? 0} history={history[lb.id] ?? []} onChanged={refresh} onDelete={() => setToDelete(lb)} />
            ))}
            {shown.length === 0 && <p className="text-ink-3">Под фильтр ничего не попало.</p>}
          </div>

          {editable && <Recipes title="Что ещё балансируем?" text="Выберите рецепт — настройки подставятся сами, останется указать узлы." />}
        </>
      )}

      <ConfirmDialog
        open={!!toDelete}
        onOpenChange={(open) => !open && setToDelete(null)}
        title={`Удалить «${toDelete?.name ?? ""}»?`}
        text="Балансировщик сразу перестанет принимать трафик. Узлы и их настройки удалятся вместе с ним."
        confirmLabel="Удалить"
        danger
        busy={deleting}
        onConfirm={confirmDelete}
      />
    </div>
  )
}

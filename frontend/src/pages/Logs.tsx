import { useState } from "react"
import { Search } from "lucide-react"
import { api } from "@/lib/api"
import { humanize } from "@/lib/humanize"
import { useLBs } from "@/lib/lbs"
import { usePoll } from "@/lib/usePoll"
import type { LogEntry } from "@/types"

type Level = "all" | "INFO" | "WARN" | "ERROR"
const SYSTEM = "__system__"

const levelMeta: Record<Exclude<Level, "all">, { label: string; short: string; badge: string; row: string; dot: string }> = {
  INFO: { label: "Инфо", short: "ИНФО", badge: "bg-rail-4 text-[#C6CEDA]", row: "", dot: "bg-line-strong" },
  WARN: { label: "Внимание", short: "ВНИМ", badge: "bg-[#3A2A05] text-[#F5C451]", row: "bg-[#14110A]", dot: "bg-warn" },
  ERROR: { label: "Ошибки", short: "ОШИБКА", badge: "bg-[#3D1607] text-[#FF9A6B]", row: "bg-[#170D0A]", dot: "bg-down" },
}

const HISTOGRAM_MINUTES = 40

function Histogram({ logs, now }: { logs: LogEntry[]; now: number }) {
  const buckets = Array.from({ length: HISTOGRAM_MINUTES }, () => ({ INFO: 0, WARN: 0, ERROR: 0 }))
  for (const e of logs) {
    const ago = Math.floor((now - new Date(e.timestamp).getTime()) / 60000)
    if (ago >= 0 && ago < HISTOGRAM_MINUTES) {
      const lv = (e.level in buckets[0] ? e.level : "INFO") as keyof (typeof buckets)[number]
      buckets[HISTOGRAM_MINUTES - 1 - ago][lv]++
    }
  }
  const max = Math.max(1, ...buckets.map((b) => b.INFO + b.WARN + b.ERROR))
  const label = (minutesAgo: number) => new Date(now - minutesAgo * 60000).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })
  return (
    <section aria-label={`События по минутам за последние ${HISTOGRAM_MINUTES} минут`} className="flex flex-col gap-2.5 rounded-[18px] border border-line bg-white px-6 py-[18px]">
      <div aria-hidden="true" className="flex h-16 items-end gap-1">
        {buckets.map((b, i) => (
          <div key={i} className="flex h-full flex-1 flex-col justify-end gap-px">
            {b.ERROR > 0 && <div className="rounded-sm bg-down" style={{ height: `${(b.ERROR / max) * 100}%` }} />}
            {b.WARN > 0 && <div className="rounded-sm bg-warn" style={{ height: `${(b.WARN / max) * 100}%` }} />}
            {b.INFO > 0 && <div className="rounded-sm bg-line-strong" style={{ height: `${(b.INFO / max) * 100}%` }} />}
            {b.INFO + b.WARN + b.ERROR === 0 && <div className="h-0.5 rounded-sm bg-ground" />}
          </div>
        ))}
      </div>
      <div className="flex justify-between font-mono text-xs text-ink-3">
        <span>{now ? label(HISTOGRAM_MINUTES) : ""}</span>
        <span>{now ? label(HISTOGRAM_MINUTES / 2) : ""}</span>
        <span>сейчас</span>
      </div>
    </section>
  )
}

export default function Logs() {
  const { lbs } = useLBs()
  const [level, setLevel] = useState<Level>("all")
  const [source, setSource] = useState("all")
  const [query, setQuery] = useState("")
  const [human, setHuman] = useState(true)
  const [follow, setFollow] = useState(true)
  // The fetch time is kept with the data, so render stays pure
  const { logs, at } = usePoll(
    async () => ({ logs: await api<LogEntry[]>("/logs"), at: Date.now() }),
    { logs: [] as LogEntry[], at: 0 },
    "logs",
    2000,
    follow,
  )

  const sourceOf = (e: LogEntry) => e.lb_name || SYSTEM
  const q = query.trim().toLowerCase()
  const bySource = logs.filter((e) => source === "all" || sourceOf(e) === source)
  const matches = (e: LogEntry) => !q || `${e.message} ${humanize(e).text} ${e.lb_name ?? ""}`.toLowerCase().includes(q)
  const count = (lv: Level) => bySource.filter((e) => (lv === "all" || e.level === lv) && matches(e)).length
  const rows = bySource.filter((e) => (level === "all" || e.level === level) && matches(e)).reverse().slice(0, 500)

  const sources = ["all", ...new Set([...lbs.map((lb) => lb.name), ...logs.map(sourceOf)])]
  const levels: { key: Level; label: string; dot: string }[] = [
    { key: "all", label: "Все", dot: "bg-ink" },
    { key: "INFO", label: levelMeta.INFO.label, dot: levelMeta.INFO.dot },
    { key: "WARN", label: levelMeta.WARN.label, dot: levelMeta.WARN.dot },
    { key: "ERROR", label: levelMeta.ERROR.label, dot: levelMeta.ERROR.dot },
  ]

  const minuteOf = (e: LogEntry) => new Date(e.timestamp).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })
  const grouped = rows.map((e, i) => {
    const minute = minuteOf(e)
    return { e, d: new Date(e.timestamp), minute, showMinute: i === 0 || minuteOf(rows[i - 1]) !== minute }
  })

  const segBtn = (active: boolean) => `h-9 rounded-lg px-3.5 text-sm font-medium ${active ? "bg-ink text-white" : "text-ink-2 hover:text-ink"}`

  return (
    <div className="flex flex-col gap-5">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-2">
          <h1 className="font-display text-[44px] font-semibold leading-[1.05] tracking-tight">Журнал</h1>
          <p className="text-base text-ink-2">
            Последние 1000 записей. Те же строки уходят в <code className="font-mono">docker logs</code>.
          </p>
        </div>
        <div className="flex flex-wrap gap-2.5">
          <div role="group" aria-label="Вид записей" className="inline-flex gap-0.5 rounded-xl border border-line bg-white p-1">
            <button type="button" aria-pressed={human} onClick={() => setHuman(true)} className={segBtn(human)}>Понятно</button>
            <button type="button" aria-pressed={!human} onClick={() => setHuman(false)} className={segBtn(!human)}>Как есть</button>
          </div>
          <button
            type="button"
            aria-pressed={follow}
            onClick={() => setFollow((f) => !f)}
            className={`inline-flex h-[46px] items-center gap-2 rounded-xl border border-line px-4 text-sm font-semibold ${follow ? "bg-ok-soft text-[#0B5A43]" : "bg-white text-ink-4"}`}
          >
            <span className={`h-2 w-2 rounded-full ${follow ? "animate-led bg-[#0E9F6E]" : "bg-[#8B95A4]"}`} />
            {follow ? "Следим за новыми" : "Пауза"}
          </button>
        </div>
      </header>

      <Histogram logs={logs} now={at} />

      <div className="flex flex-wrap items-center gap-2.5">
        <div role="group" aria-label="Уровень" className="inline-flex flex-wrap gap-1 rounded-xl border border-line bg-white p-1">
          {levels.map((l) => (
            <button key={l.key} type="button" aria-pressed={level === l.key} onClick={() => setLevel(l.key)} className={`inline-flex items-center gap-1.5 ${segBtn(level === l.key)}`}>
              <span className={`h-2 w-2 rounded-sm ${l.dot}`} />
              {l.label} <span className="opacity-65">{count(l.key)}</span>
            </button>
          ))}
        </div>
        <div role="group" aria-label="Источник" className="inline-flex flex-wrap gap-1.5">
          {sources.map((s) => (
            <button
              key={s}
              type="button"
              aria-pressed={source === s}
              onClick={() => setSource(s)}
              className={`h-9 rounded-full border px-3.5 text-sm font-medium ${source === s ? "border-cobalt bg-cobalt-soft text-cobalt-dark" : "border-line bg-white text-ink-4 hover:border-cobalt"}`}
            >
              {s === "all" ? "Все источники" : s === SYSTEM ? "Система" : s}
            </button>
          ))}
        </div>
        <label className="flex h-11 w-72 max-w-full items-center gap-2.5 rounded-xl border border-line bg-white px-3.5 text-ink-3 focus-within:border-cobalt lg:ml-auto">
          <Search className="h-[18px] w-[18px]" aria-hidden="true" />
          <span className="sr-only">Поиск в журнале</span>
          <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Узел, адрес, текст" className="min-w-0 flex-1 bg-transparent text-[15px] text-ink outline-none" />
        </label>
      </div>

      <section aria-label="Записи журнала" aria-live={follow ? "polite" : "off"} className="flex flex-col rounded-[18px] bg-rail py-2 text-[#CAD3DF]">
        {grouped.length === 0 && <p className="px-6 py-8 text-rail-muted">{logs.length === 0 ? "Записей пока нет." : "Под фильтр ничего не попало."}</p>}
        {grouped.map(({ e, d, minute, showMinute }, i) => {
          const meta = levelMeta[(e.level as Exclude<Level, "all">)] ?? levelMeta.INFO
          return (
            <div key={i} className="flex flex-col">
              {showMinute && <div className="px-6 pb-1.5 pt-3.5 font-mono text-xs tracking-[0.08em] text-rail-muted">{minute}</div>}
              <div className={`flex flex-wrap items-baseline gap-x-3.5 gap-y-1.5 px-6 py-2 ${meta.row}`}>
                <span className="w-16 flex-none font-mono text-[13px] text-rail-muted">{d.toLocaleTimeString("ru-RU")}</span>
                <span className={`flex-none rounded-[5px] px-1.5 py-0.5 font-mono text-[11px] font-medium ${meta.badge}`}>{meta.short}</span>
                <span className="flex-none text-[13px] font-medium text-[#A8B6FF]">{e.lb_name || "Система"}</span>
                <span
                  title={human ? e.message : undefined}
                  className={`min-w-0 flex-[1_1_360px] leading-normal text-rail-text [overflow-wrap:anywhere] ${human ? "text-[15px]" : "font-mono text-[13px]"}`}
                >
                  {human ? humanize(e).text : e.message}
                </span>
              </div>
            </div>
          )
        })}
      </section>
    </div>
  )
}

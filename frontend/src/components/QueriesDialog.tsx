import { useState } from "react"
import { ChevronRight } from "lucide-react"
import { BlockClientDialog } from "@/components/BlockClientDialog"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { api, canEdit } from "@/lib/api"
import { CLIENT_WINDOW_MIN, clientWhere, listCovers } from "@/lib/clients"
import { formatNumber } from "@/lib/lbModel"
import { usePoll } from "@/lib/usePoll"
import type { ClientStat, LoadBalancer, QueryDomain, QueryReport } from "@/types"

type Filter = "all" | "passed" | "refused"

/** What happened to the queries of one name or one address, in a word. */
function verdict(accepted: number, refused: number) {
  if (refused === 0) return { text: "проходит", cls: "bg-ok-soft text-ok-text" }
  if (accepted === 0) return { text: "отклоняется", cls: "bg-down-soft text-down-text" }
  return { text: "частично", cls: "bg-warn-soft text-warn-text" }
}

function Verdict({ accepted, refused }: { accepted: number; refused: number }) {
  const v = verdict(accepted, refused)
  return <span className={`inline-block whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-medium ${v.cls}`}>{v.text}</span>
}

/** A thin bar split into the served and refused share. */
function Split({ accepted, refused }: { accepted: number; refused: number }) {
  const total = Math.max(1, accepted + refused)
  return (
    <span className="flex h-2 w-full overflow-hidden rounded-full bg-ground" aria-hidden="true">
      <span className="bg-ok" style={{ width: `${(accepted / total) * 100}%` }} />
      <span className="bg-down" style={{ width: `${(refused / total) * 100}%` }} />
    </span>
  )
}

function DomainRow({
  d, lb, open, onToggle, onBlock,
}: {
  d: QueryDomain
  lb: LoadBalancer
  open: boolean
  onToggle: () => void
  onBlock: (c: ClientStat) => void
}) {
  const editable = canEdit()
  const otherAccepted = d.other_accepted ?? 0
  const otherRefused = d.other_refused ?? 0
  return (
    <li className="rounded-xl border border-line-soft">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1.5 rounded-xl px-4 py-3 text-left hover:bg-accent sm:grid-cols-[auto_minmax(0,1.4fr)_minmax(0,1fr)_auto]"
      >
        <ChevronRight className={`h-4 w-4 flex-none text-ink-3 transition-transform ${open ? "rotate-90" : ""}`} aria-hidden="true" />
        <span className="flex min-w-0 items-baseline gap-2">
          <span className="truncate font-mono text-sm font-medium" title={d.domain}>{d.domain}</span>
          <span className="flex-none font-mono text-xs text-ink-3">{d.type}</span>
        </span>
        <span className="col-span-2 col-start-2 flex flex-col gap-1 sm:col-span-1 sm:col-start-auto">
          <Split accepted={d.accepted} refused={d.refused} />
          <span className="font-mono text-xs text-ink-2">
            прошло {formatNumber(d.accepted)} · отклонено {formatNumber(d.refused)}
          </span>
        </span>
        <span className="row-start-1 col-start-3 sm:row-start-auto sm:col-start-auto">
          <Verdict accepted={d.accepted} refused={d.refused} />
        </span>
      </button>
      {open && (
        <div className="border-t border-line-soft">
          <ul>
            {d.clients.map((c) => {
              const denied = listCovers(lb.access_deny, c.ip)
              return (
                <li key={c.ip} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 border-t border-ground px-4 py-2.5 first:border-t-0">
                  <span className="flex min-w-0 flex-wrap items-baseline gap-x-3">
                    <span className="font-mono text-sm">{c.ip}</span>
                    <span className="text-[13px] text-ink-2">{clientWhere(c)}</span>
                  </span>
                  <span className="justify-self-end"><Verdict accepted={c.accepted} refused={c.refused} /></span>
                  <span className="font-mono text-xs text-ink-2">
                    прошло {formatNumber(c.accepted)} · <span className={c.refused > 0 ? "text-down-text" : ""}>отклонено {formatNumber(c.refused)}</span>
                  </span>
                  <span className="justify-self-end">
                    {denied ? (
                      <span className="whitespace-nowrap text-[13px] text-ink-3" title="Адрес в списке запрещённых этого балансировщика">заблокирован</span>
                    ) : (
                      editable && (
                        <button
                          type="button"
                          onClick={() => onBlock(c)}
                          aria-label={`Заблокировать ${c.ip}`}
                          className="h-8 rounded-lg border border-line px-3 text-[13px] font-medium text-down-text hover:bg-down-soft"
                        >
                          Заблокировать
                        </button>
                      )
                    )}
                  </span>
                </li>
              )
            })}
            {d.clients.length === 0 && <li className="px-4 py-3 text-sm text-ink-3">Адреса не успели записаться.</li>}
          </ul>
          {otherAccepted + otherRefused > 0 && (
            <p className="border-t border-ground px-4 py-2.5 text-xs text-ink-3">
              И ещё запросы других адресов: прошло {formatNumber(otherAccepted)}, отклонено {formatNumber(otherRefused)}. Их слишком много или они слишком редкие, чтобы перечислять.
            </p>
          )}
        </div>
      )}
    </li>
  )
}

/** Which names were asked for, by whom, and whether each query was let through. */
export function QueriesDialog({
  lb, open, onOpenChange, onChanged,
}: {
  lb: LoadBalancer
  open: boolean
  onOpenChange: (open: boolean) => void
  onChanged: () => void
}) {
  const [filter, setFilter] = useState<Filter>("all")
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [target, setTarget] = useState<ClientStat | null>(null)
  const report = usePoll<QueryReport | null>(() => api<QueryReport>(`/load-balancers/queries?id=${lb.id}`), null, String(lb.id), 5000, open)

  const all = report?.domains ?? []
  const passed = all.filter((d) => d.accepted > 0).sort((a, b) => b.accepted - a.accepted)
  const refused = all.filter((d) => d.refused > 0).sort((a, b) => b.refused - a.refused)
  const rows = filter === "passed" ? passed : filter === "refused" ? refused : all
  const key = (d: QueryDomain) => `${d.domain} ${d.type}`
  const isOpen = (d: QueryDomain, i: number) => expanded[key(d)] ?? (rows.length === 1 || (i === 0 && filter !== "all"))

  const filters: [Filter, string, number][] = [
    ["all", "Все", all.length],
    ["passed", "Проходят", passed.length],
    ["refused", "Отклоняются", refused.length],
  ]

  return (
    <>
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[88vh] w-[calc(100%-2rem)] max-w-3xl flex-col gap-0 overflow-hidden rounded-[20px] bg-white p-0">
        <DialogHeader className="gap-1.5 px-6 pb-4 pt-6 text-left sm:px-7">
          <DialogTitle className="font-display text-xl font-semibold tracking-tight">Кто что спрашивал</DialogTitle>
          <DialogDescription className="pr-6 text-[15px] leading-relaxed text-ink-2">
            DNS-запросы по UDP за последние {CLIENT_WINDOW_MIN} минут: какие имена спрашивали, с каких адресов и прошёл ли запрос.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-wrap items-center gap-3 border-y border-line-soft px-6 py-3 sm:px-7">
          <div role="group" aria-label="Какие имена показывать" className="inline-flex gap-0.5 rounded-xl border border-line bg-white p-1">
            {filters.map(([f, label, n]) => (
              <button
                key={f}
                type="button"
                aria-pressed={filter === f}
                onClick={() => setFilter(f)}
                className={`h-8 rounded-lg px-3 text-[13px] font-medium ${filter === f ? "bg-ink text-white" : "text-ink-2 hover:text-ink"}`}
              >
                {label} <span className={filter === f ? "text-white/70" : "text-ink-3"}>{n}</span>
              </button>
            ))}
          </div>
          <span className="text-xs text-ink-3">Имя можно раскрыть и увидеть, кто его спрашивал</span>
        </div>

        <div className="min-h-[180px] flex-1 overflow-y-auto px-6 py-4 sm:px-7">
          {report === null ? (
            <p className="text-sm text-ink-3">Загружаю…</p>
          ) : rows.length === 0 ? (
            <p className="text-sm leading-relaxed text-ink-3">
              {all.length === 0
                ? "DNS-запросов по UDP за последние 10 минут не было. Имена видны только у DNS по UDP: подключения TCP и DoH проходят, не раскрывая запрос."
                : filter === "passed"
                  ? "Ни один запрос не прошёл: всё, что спрашивали, отклонено."
                  : "Ни один запрос не отклонён."}
            </p>
          ) : (
            <ul className="flex flex-col gap-2">
              {rows.map((d, i) => (
                <DomainRow
                  key={key(d)}
                  d={d}
                  lb={lb}
                  open={isOpen(d, i)}
                  onToggle={() => setExpanded((e) => ({ ...e, [key(d)]: !isOpen(d, i) }))}
                  onBlock={setTarget}
                />
              ))}
            </ul>
          )}
        </div>

        <p className="border-t border-line-soft px-6 py-3 text-xs leading-relaxed text-ink-3 sm:px-7">
          Поддомены сворачиваются: все имена вида *.example.com считаются как example.com. Цифры хранятся в памяти и обнуляются при перезапуске. Если запросы летят с множества подменённых адресов, перечислены самые активные из них, остальные учтены строкой ниже.
        </p>
      </DialogContent>
    </Dialog>
    <BlockClientDialog lb={lb} target={target} onClose={() => setTarget(null)} onChanged={onChanged} />
    </>
  )
}

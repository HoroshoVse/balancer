import { useState } from "react"
import { Link } from "react-router-dom"
import { toast } from "sonner"
import { Lightbulb } from "lucide-react"
import type { Advice } from "@/lib/advice"
import { canEdit, errorMessage } from "@/lib/api"
import { updateLB } from "@/lib/lbActions"
import { lbHealth, type Health } from "@/lib/lbModel"
import type { LoadBalancer } from "@/types"

const pillStyle: Record<Health, { box: string; dot: string; text: string }> = {
  ok: { box: "bg-ok-soft text-ok-text", dot: "bg-[#0E9F6E] animate-led", text: "Работает" },
  degraded: { box: "bg-warn-soft text-warn-text", dot: "bg-warn", text: "С замечаниями" },
  down: { box: "bg-down-soft text-down-text", dot: "bg-down", text: "Узлы не отвечают" },
  error: { box: "bg-down-soft text-down-text", dot: "bg-down", text: "Не запущен" },
  starting: { box: "bg-ground text-ink-2", dot: "bg-line-strong", text: "Запускается" },
}

export function StatusPill({ lb, size = "sm" }: { lb: LoadBalancer; size?: "sm" | "md" }) {
  const s = pillStyle[lbHealth(lb).state]
  return (
    <span
      title={lb.status_error || undefined}
      className={`inline-flex flex-none items-center gap-1.5 rounded-full font-semibold ${s.box} ${size === "md" ? "h-7 px-3 text-[13px]" : "h-6 px-2.5 text-xs"}`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${s.dot}`} />
      {s.text}
    </span>
  )
}

/** One piece of advice; applies its fix through the API or links to the builder. */
export function AdviceCard({ advice, lb, onApplied, compact }: { advice: Advice; lb: LoadBalancer; onApplied: () => void; compact?: boolean }) {
  const [busy, setBusy] = useState(false)
  const warn = advice.tone === "warn"
  const apply = async () => {
    if (!advice.fix) return
    setBusy(true)
    try {
      await updateLB(lb, advice.fix.apply)
      toast.success("Готово — настройки сохранены")
      onApplied()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className={`flex gap-3 rounded-[14px] ${compact ? "px-3.5 py-3" : "p-4"} ${warn ? "bg-warn-soft text-warn-text" : "bg-ground text-ink-4"}`}>
      <Lightbulb className="mt-0.5 h-5 w-5 flex-none" aria-hidden="true" />
      <div className="flex min-w-0 flex-col gap-1.5 text-sm leading-snug">
        <strong className="font-semibold">{advice.title}</strong>
        <span>{advice.text}</span>
        {canEdit() && advice.fix && (
          <button
            type="button"
            disabled={busy}
            onClick={apply}
            className={`mt-1 self-start rounded-lg px-3 py-2 text-[13px] font-semibold text-white disabled:opacity-60 ${warn ? "bg-warn-text" : "bg-ink"}`}
          >
            {busy ? "Сохраняю…" : advice.fix.label}
          </button>
        )}
        {canEdit() && !advice.fix && advice.edit && (
          <Link to={`/lbs/${lb.id}/edit`} className="mt-1 self-start text-[13px] font-semibold text-cobalt hover:text-cobalt-dark">
            Открыть конструктор →
          </Link>
        )}
      </div>
    </div>
  )
}

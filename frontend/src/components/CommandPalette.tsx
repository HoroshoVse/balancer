import { useState } from "react"
import { useNavigate } from "react-router-dom"
import * as DialogPrimitive from "@radix-ui/react-dialog"
import { Activity, CornerDownLeft, Plus, Search, Settings2, Terminal } from "lucide-react"
import { canEdit } from "@/lib/api"
import { useLBs } from "@/lib/lbs"
import { lbHealth, ledClass, listenAddr, protocolLabel } from "@/lib/lbModel"

interface Item {
  id: string
  group: "Балансировщики" | "Действия" | "Узлы"
  title: string
  hint: string
  icon?: React.ReactNode
  led?: string
  keywords: string
  run: () => void
}

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-rail/60 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
        {/* Content unmounts when closed, so the search starts empty every time */}
        <PaletteBody onClose={() => onOpenChange(false)} />
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}

function PaletteBody({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate()
  const { lbs } = useLBs()
  const [query, setQuery] = useState("")
  const [active, setActive] = useState(0)

  const go = (path: string) => () => {
    onClose()
    navigate(path)
  }

  const items: Item[] = (() => {
    const out: Item[] = lbs.map((lb) => ({
      id: `lb-${lb.id}`,
      group: "Балансировщики",
      title: lb.name,
      hint: `${protocolLabel(lb)} · ${listenAddr(lb)}`,
      led: ledClass[lbHealth(lb).state],
      keywords: `${lb.name} ${listenAddr(lb)} ${lb.protocol} ${lb.acme_domains}`,
      run: go(`/lbs/${lb.id}`),
    }))
    if (canEdit()) {
      out.push({ id: "new", group: "Действия", title: "Создать балансировщик", hint: "конструктор", icon: <Plus className="h-5 w-5 text-cobalt" />, keywords: "создать новый добавить dns сайт tcp udp doh", run: go("/lbs/new") })
      for (const lb of lbs) {
        out.push({ id: `edit-${lb.id}`, group: "Действия", title: `Изменить «${lb.name}»`, hint: "конструктор", icon: <Settings2 className="h-5 w-5 text-cobalt" />, keywords: `изменить настроить ${lb.name}`, run: go(`/lbs/${lb.id}/edit`) })
      }
    }
    out.push({ id: "pulse", group: "Действия", title: "Открыть Пульс", hint: "главная", icon: <Activity className="h-5 w-5 text-cobalt" />, keywords: "пульс главная обзор дашборд", run: go("/") })
    out.push({ id: "logs", group: "Действия", title: "Открыть журнал", hint: "события и ошибки", icon: <Terminal className="h-5 w-5 text-cobalt" />, keywords: "журнал логи ошибки события", run: go("/logs") })
    out.push({ id: "settings", group: "Действия", title: "Открыть настройки", hint: "уведомления", icon: <Settings2 className="h-5 w-5 text-cobalt" />, keywords: "настройки telegram уведомления", run: go("/settings") })

    const seen = new Set<string>()
    for (const lb of lbs) {
      for (const b of lb.backend_group?.backends ?? []) {
        const key = `${b.address}:${b.port}`
        if (seen.has(key)) continue
        seen.add(key)
        out.push({
          id: `node-${key}`,
          group: "Узлы",
          title: b.name || b.address,
          hint: `${key} · ${lb.name}`,
          led: b.status === "UP" ? "bg-ok" : b.status === "DOWN" ? "bg-down" : "bg-line-strong",
          keywords: `${b.name} ${key} ${lb.name}`,
          run: go(`/lbs/${lb.id}`),
        })
      }
    }
    return out
  })()

  const q = query.trim().toLowerCase()
  const results = q ? items.filter((i) => `${i.title} ${i.keywords}`.toLowerCase().includes(q)) : items.filter((i) => i.group !== "Узлы")
  const activeIndex = Math.min(active, Math.max(results.length - 1, 0))

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault()
      setActive((activeIndex + 1) % Math.max(results.length, 1))
    } else if (e.key === "ArrowUp") {
      e.preventDefault()
      setActive((activeIndex - 1 + results.length) % Math.max(results.length, 1))
    } else if (e.key === "Enter" && results[activeIndex]) {
      e.preventDefault()
      results[activeIndex].run()
    }
  }

  const rows = results.map((item, i) => ({ item, header: i === 0 || results[i - 1].group !== item.group }))
  return (
        <DialogPrimitive.Content
          aria-describedby={undefined}
          onKeyDown={onKeyDown}
          className="fixed left-1/2 top-[10vh] z-50 flex max-h-[80vh] w-[calc(100%-2rem)] max-w-[720px] -translate-x-1/2 flex-col overflow-hidden rounded-[22px] bg-white shadow-[0_30px_80px_rgba(11,15,20,0.45)] focus:outline-none"
        >
          <DialogPrimitive.Title className="sr-only">Командная строка</DialogPrimitive.Title>
          <label className="flex h-16 items-center gap-3 border-b border-line-soft px-5">
            <Search className="h-5 w-5 text-cobalt" aria-hidden="true" />
            <span className="sr-only">Найти или выполнить</span>
            <input
              autoFocus
              value={query}
              onChange={(e) => {
                setQuery(e.target.value)
                setActive(0)
              }}
              placeholder="Найти балансировщик, узел или действие"
              role="combobox"
              aria-expanded="true"
              aria-controls="palette-results"
              aria-activedescendant={results[activeIndex] ? `palette-${results[activeIndex].id}` : undefined}
              className="h-full min-w-0 flex-1 bg-transparent text-lg text-ink outline-none placeholder:text-ink-3"
            />
            <kbd className="rounded-md border border-line px-2 py-1 font-mono text-xs text-ink-2">Esc</kbd>
          </label>

          <div id="palette-results" role="listbox" aria-label="Результаты" className="flex-1 overflow-y-auto p-2.5">
            {results.length === 0 && <p className="px-3 py-8 text-center text-ink-3">Ничего не нашлось</p>}
            {rows.map(({ item, header }, i) => {
              const selected = i === activeIndex
              return (
                <div key={item.id}>
                  {header && <div className="px-3 pb-1.5 pt-3 font-mono text-[11px] uppercase tracking-[0.12em] text-ink-3">{item.group}</div>}
                  <button
                    type="button"
                    id={`palette-${item.id}`}
                    role="option"
                    aria-selected={selected}
                    onMouseEnter={() => setActive(i)}
                    onClick={item.run}
                    className={`flex min-h-[52px] w-full items-center gap-3.5 rounded-xl px-3 text-left ${selected ? "bg-cobalt-soft" : ""}`}
                  >
                    {item.led ? <span className={`h-2.5 w-2.5 flex-none rounded-full ${item.led}`} /> : item.icon}
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate text-[15px] text-ink">{item.title}</span>
                      <span className="truncate font-mono text-xs text-ink-2">{item.hint}</span>
                    </span>
                    {selected && (
                      <span className="flex h-6 items-center rounded-md bg-cobalt px-2 text-white">
                        <CornerDownLeft className="h-3.5 w-3.5" aria-hidden="true" />
                      </span>
                    )}
                  </button>
                </div>
              )
            })}
          </div>

          <div className="flex flex-wrap gap-4 border-t border-line-soft bg-accent px-5 py-3 text-[13px] text-ink-2">
            <span><kbd className="rounded border border-line bg-white px-1.5 font-mono">↑↓</kbd> выбрать</span>
            <span><kbd className="rounded border border-line bg-white px-1.5 font-mono">↵</kbd> открыть</span>
            <span className="ml-auto hidden sm:inline">ищет по именам, адресам и узлам</span>
          </div>
        </DialogPrimitive.Content>
  )
}

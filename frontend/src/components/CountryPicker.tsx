import { useId, useMemo, useState } from "react"
import { Search, X } from "lucide-react"
import { byName, countryName, flag, knownCodes } from "@/lib/countries"

interface CountryPickerProps {
  value: string[]
  onChange: (codes: string[]) => void
  /** Codes the server has data for; empty while the database is not loaded yet. */
  available: string[]
  /** Short list of one-click choices */
  presets?: { label: string; codes: string[] }[]
}

/** Pick countries: chips for the chosen ones, a searchable checklist for the rest. */
export function CountryPicker({ value, onChange, available, presets = [] }: CountryPickerProps) {
  const [query, setQuery] = useState("")
  const listId = useId()
  const all = useMemo(() => {
    const codes = new Set(available.length ? available : knownCodes())
    value.forEach((c) => codes.add(c)) // keep a chosen country even if the data lacks it
    return [...codes].filter((c) => c !== "ZZ").sort(byName)
  }, [available, value])

  const chosen = new Set(value)
  const q = query.trim().toLowerCase()
  const shown = q ? all.filter((c) => countryName(c).toLowerCase().includes(q) || c.toLowerCase() === q) : all
  const toggle = (code: string) => onChange(chosen.has(code) ? value.filter((c) => c !== code) : [...value, code])

  return (
    <div className="flex w-full flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2" aria-live="polite">
        {value.length === 0 && <span className="text-sm text-ink-3">Ни одной страны не выбрано</span>}
        {[...value].sort(byName).map((code) => (
          <span key={code} className="inline-flex items-center gap-1.5 rounded-full bg-cobalt-soft py-1 pl-3 pr-1 text-sm font-medium text-[#2338C7]">
            <span aria-hidden="true">{flag(code)}</span>
            {countryName(code)}
            <button type="button" onClick={() => toggle(code)} aria-label={`Убрать: ${countryName(code)}`} className="grid h-6 w-6 place-items-center rounded-full hover:bg-white/70">
              <X className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          </span>
        ))}
        {presets.map((p) => (
          <button key={p.label} type="button" onClick={() => onChange(p.codes)} className="h-8 rounded-full border border-line bg-white px-3 text-[13px] font-medium text-ink-2 hover:border-cobalt hover:text-ink">
            {p.label}
          </button>
        ))}
        {value.length > 0 && (
          <button type="button" onClick={() => onChange([])} className="h-8 rounded-full px-3 text-[13px] font-medium text-ink-3 hover:text-ink">
            Снять все
          </button>
        )}
      </div>

      <label className="flex h-10 items-center gap-2.5 rounded-lg border border-line bg-white px-3 text-ink-3 focus-within:border-cobalt">
        <Search className="h-4 w-4 flex-none" aria-hidden="true" />
        <span className="sr-only">Найти страну</span>
        <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Найти страну" aria-controls={listId} className="min-w-0 flex-1 bg-transparent text-[15px] text-ink outline-none" />
      </label>

      <ul id={listId} className="grid max-h-64 grid-cols-1 gap-x-4 overflow-y-auto rounded-lg border border-line bg-white p-1.5 sm:grid-cols-2">
        {shown.length === 0 && <li className="px-2.5 py-3 text-sm text-ink-3">Такой страны нет</li>}
        {shown.map((code) => (
          <li key={code}>
            <label className="flex min-h-9 cursor-pointer items-center gap-2.5 rounded-md px-2.5 py-1.5 text-[15px] hover:bg-ground">
              <input type="checkbox" checked={chosen.has(code)} onChange={() => toggle(code)} className="h-[18px] w-[18px] flex-none accent-cobalt" />
              <span aria-hidden="true" className="w-6 text-center">{flag(code)}</span>
              <span className="min-w-0 flex-1 truncate">{countryName(code)}</span>
              <span className="font-mono text-xs text-ink-3">{code}</span>
            </label>
          </li>
        ))}
      </ul>
    </div>
  )
}

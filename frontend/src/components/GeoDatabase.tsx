import { useState } from "react"
import { toast } from "sonner"
import { Check, Copy, LoaderCircle, TriangleAlert } from "lucide-react"
import { api, errorMessage } from "@/lib/api"
import { countryName } from "@/lib/countries"
import { plural } from "@/lib/lbModel"
import { usePoll } from "@/lib/usePoll"
import { Switch } from "@/components/Switch"
import type { GeoLookup, GeoStatus } from "@/types"

const registryInfo: Record<string, { title: string; area: string }> = {
  ripencc: { title: "RIPE NCC", area: "Европа, Россия, Ближний Восток, Центральная Азия" },
  arin: { title: "ARIN", area: "США, Канада" },
  apnic: { title: "APNIC", area: "Азия и Океания" },
  lacnic: { title: "LACNIC", area: "Латинская Америка" },
  afrinic: { title: "AFRINIC", area: "Африка" },
}

const intervals = [
  { hours: 6, label: "каждые 6 часов" },
  { hours: 12, label: "каждые 12 часов" },
  { hours: 24, label: "раз в сутки" },
  { hours: 72, label: "раз в 3 суток" },
  { hours: 168, label: "раз в неделю" },
]

const smallField = "h-10 rounded-lg border border-line bg-white px-3 text-[15px] text-ink outline-none focus:border-cobalt focus:ring-2 focus:ring-cobalt/20"

function when(iso?: string) {
  if (!iso) return "—"
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString("ru-RU", { day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" })
}

/** "20261023" -> "23.10.2026" */
function registryDate(s?: string) {
  return s && /^\d{8}$/.test(s) ? `${s.slice(6)}.${s.slice(4, 6)}.${s.slice(0, 4)}` : ""
}

function Command({ command }: { command: string }) {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      toast.success("Команда скопирована")
    } catch {
      toast.error("Не удалось скопировать — выделите команду вручную")
    }
  }
  return (
    <div className="flex items-center gap-3 rounded-xl bg-rail py-3 pl-4 pr-3 text-rail-text">
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-[13px]">{command}</code>
      <button type="button" onClick={copy} aria-label="Скопировать команду" className="grid h-10 w-10 flex-none place-items-center rounded-lg border border-[#2A3340] bg-rail-3 text-[#C6CEDA] hover:text-white">
        <Copy className="h-4 w-4" aria-hidden="true" />
      </button>
    </div>
  )
}

/** The country database behind the access rules: what is loaded, how fresh, how it is kept up to date. */
export function GeoDatabase() {
  const [tick, setTick] = useState(0)
  const geo = usePoll(() => api<GeoStatus>("/geo").catch(() => null), null as GeoStatus | null, `geo-${tick}`, 3000)
  const [busy, setBusy] = useState(false)
  const [ip, setIp] = useState("")
  const [found, setFound] = useState("")

  const update = async () => {
    setBusy(true)
    try {
      await api("/geo/update", { method: "POST" })
      toast.success("Загрузка началась. Первая занимает несколько минут")
      setTick((t) => t + 1)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  const saveSchedule = async (auto: boolean, hours: number) => {
    try {
      await api("/geo/settings", { method: "POST", json: { auto_update: auto, interval_hours: hours } })
      setTick((t) => t + 1)
    } catch (err) {
      toast.error(errorMessage(err))
    }
  }

  const lookup = async () => {
    try {
      const r = await api<GeoLookup>(`/geo/lookup?ip=${encodeURIComponent(ip.trim())}`)
      setFound(r.internal ? `${r.ip}: внутренний адрес, у него нет страны` : !r.loaded ? "База ещё не загружена" : r.country ? `${r.ip}: ${countryName(r.country)} (${r.country})` : `${r.ip}: ни один реестр не знает этот адрес`)
    } catch (err) {
      setFound(errorMessage(err))
    }
  }

  const downloading = geo?.updating ?? false
  const problems = geo?.sources.filter((s) => s.error) ?? []

  return (
    <section id="geo" aria-labelledby="geo-title" className="flex scroll-mt-6 flex-col gap-[18px] rounded-[20px] border border-line bg-white p-6 sm:p-7">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex max-w-xl flex-col gap-1">
          <h2 id="geo-title" className="font-display text-xl font-semibold tracking-tight">Базы стран</h2>
          <span className="text-sm text-ink-3">
            Нужны для правил «пускать только из этих стран». Это официальные списки пяти региональных регистраторов, которые распределяют все IP-адреса мира. Скачиваются по HTTPS с проверкой контрольной суммы.
          </span>
        </div>
        <button type="button" onClick={update} disabled={busy || downloading || !geo} className="inline-flex h-11 items-center gap-2 rounded-[10px] bg-cobalt px-[18px] text-[15px] font-semibold text-white hover:bg-cobalt-dark disabled:opacity-60">
          {downloading && <LoaderCircle className="h-4 w-4 animate-spin" aria-hidden="true" />}
          {downloading ? "Скачиваю…" : "Обновить сейчас"}
        </button>
      </div>

      {!geo ? (
        <p className="text-ink-3">Загрузка…</p>
      ) : (
        <>
          <div className={`flex items-start gap-3 rounded-xl px-4 py-3 text-sm ${geo.loaded ? "bg-ok-soft text-[#0B5A43]" : "bg-warn-soft text-warn-text"}`} role="status">
            {geo.loaded ? <Check className="mt-0.5 h-5 w-5 flex-none" aria-hidden="true" /> : <TriangleAlert className="mt-0.5 h-5 w-5 flex-none" aria-hidden="true" />}
            <span>
              {geo.loaded
                ? `База загружена: ${geo.countries} ${plural(geo.countries, "страна", "страны", "стран")}, ${geo.ranges.toLocaleString("ru-RU")} диапазонов. Подтверждена ${when(geo.updated_at)}.`
                : downloading
                  ? "Идёт загрузка. Первый раз это 5–15 минут: реестры отдают файлы медленно. Пока база не полная, правила по странам пропускают всех."
                  : "База ещё не полная. Пока её нет, правила по странам пропускают всех. Нажмите «Обновить сейчас» или создайте балансировщик с правилом по странам: загрузка начнётся сама."}
            </span>
          </div>

          <ul className="flex flex-col divide-y divide-ground">
            {geo.sources.map((s) => {
              const info = registryInfo[s.name]
              return (
                <li key={s.name} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 py-3">
                  <span className="flex flex-col">
                    <span className="text-[15px] font-medium">{info?.title ?? s.name}</span>
                    <span className="text-[13px] text-ink-3">{info?.area}</span>
                  </span>
                  <span className="flex flex-col items-end text-right text-sm">
                    {s.downloading ? (
                      <span className="inline-flex items-center gap-1.5 text-cobalt-dark">
                        <LoaderCircle className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                        скачивается
                      </span>
                    ) : s.present ? (
                      <span className="text-ink-2">
                        {s.ranges.toLocaleString("ru-RU")} диапазонов{registryDate(s.data_date) && <span className="text-ink-3"> · данные от {registryDate(s.data_date)}</span>}
                      </span>
                    ) : (
                      <span className="text-ink-3">нет данных</span>
                    )}
                    {s.error && <span className="max-w-md text-[13px] text-down-text">{s.error}</span>}
                  </span>
                </li>
              )
            })}
          </ul>
          {problems.length > 0 && !downloading && geo.next_attempt && (
            <p className="text-[13px] text-ink-3">Автоматически попробую снова после {when(geo.next_attempt)}. Прежние данные остаются в работе.</p>
          )}

          <div className="flex flex-wrap items-center gap-x-6 gap-y-3 border-t border-ground pt-4">
            <span className="inline-flex items-center gap-2.5 text-sm font-medium">
              <Switch checked={geo.auto_update} onChange={(v) => saveSchedule(v, geo.interval_hours)} label="Обновлять автоматически" />
              Обновлять автоматически
            </span>
            <label className="inline-flex items-center gap-2.5 text-sm text-ink-2">
              как часто
              <select value={intervals.some((i) => i.hours === geo.interval_hours) ? geo.interval_hours : 24} disabled={!geo.auto_update} onChange={(e) => saveSchedule(geo.auto_update, Number(e.target.value))} className={`${smallField} disabled:opacity-60`}>
                {intervals.map((i) => (
                  <option key={i.hours} value={i.hours}>
                    {i.label}
                  </option>
                ))}
              </select>
            </label>
            <span className="text-[13px] text-ink-3">Скачивается, только пока хотя бы один балансировщик фильтрует по странам.</span>
          </div>

          <div className="flex flex-col gap-2 border-t border-ground pt-4">
            <span className="text-sm font-medium">Какая это страна?</span>
            <div className="flex flex-wrap items-center gap-2">
              <input value={ip} onChange={(e) => setIp(e.target.value)} onKeyDown={(e) => e.key === "Enter" && ip.trim() && lookup()} placeholder="103.54.46.15" aria-label="IP-адрес" className={`${smallField} w-52 font-mono`} />
              <button type="button" onClick={lookup} disabled={!ip.trim() || !geo.loaded} className="h-10 rounded-lg border border-line bg-white px-4 text-sm font-medium hover:border-cobalt disabled:opacity-60">
                Узнать
              </button>
              {found && (
                <span role="status" className="text-sm text-ink-2">
                  {found}
                </span>
              )}
            </div>
          </div>

          <details className="group border-t border-ground pt-4">
            <summary className="cursor-pointer select-none text-sm font-medium text-ink-2 hover:text-ink">Закрыть страны ещё и в файрволе</summary>
            <div className="mt-3 flex flex-col gap-3 text-sm text-ink-2">
              <p>
                Файрвол отбрасывает пакеты в ядре, до балансировщика, и выдерживает поток куда больше. Те же проверенные списки можно отдать ему. Выгрузка делается из этой же базы, команды выполняются на сервере из папки проекта.
              </p>
              <span>Скачать или обновить базу вручную:</span>
              <Command command="docker compose exec backend ./balancer-cli geo update" />
              <span>Пустить на порт 53 только Россию и Беларусь (ipset):</span>
              <Command command="docker compose exec -T backend ./balancer-cli geo export RU,BY ipset geo_ok | sudo ipset restore" />
              <Command command="sudo iptables -I INPUT -p udp --dport 53 ! -i lo ! -s 10.0.0.0/8 -m set ! --match-set geo_ok src -j DROP" />
              <p className="text-[13px] text-ink-3">
                Для IPv6 то же с <code className="font-mono">ip6tables</code> и множеством <code className="font-mono">geo_ok_v6</code>. Вместо ipset можно взять формат <code className="font-mono">nft</code> (в конце выгрузки есть пример правила). Список нужно обновлять по расписанию (cron), иначе он устареет.
              </p>
            </div>
          </details>
        </>
      )}
    </section>
  )
}

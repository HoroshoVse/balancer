import { useEffect, useState, type FormEvent } from "react"
import { Link } from "react-router-dom"
import { toast } from "sonner"
import { api, canEdit, errorMessage } from "@/lib/api"
import { usePoll } from "@/lib/usePoll"
import { plural } from "@/lib/lbModel"
import { ConfirmDialog } from "@/components/ConfirmDialog"

export interface AccessList {
  id: number
  name: string
  entries: string
  source_url: string
  update_hours: number
  entry_count: number
  last_updated_at: string | null
  last_error: string
}

const field = "w-full rounded-lg border border-line bg-white px-3 py-2 text-sm outline-none focus:border-cobalt disabled:opacity-60"
const button = "rounded-lg border border-line px-3 py-2 text-sm font-medium hover:border-cobalt disabled:opacity-60"
const empty = { name: "", entries: "", source_url: "", update_hours: 24 }

/** Shared sources are managed here and selected independently for each balancer. */
export function AccessLists() {
  const editable = canEdit()
  const [lists, setLists] = useState<AccessList[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState("")
  const [editing, setEditing] = useState<number | "new" | null>(null)
  const [form, setForm] = useState(empty)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  const [remove, setRemove] = useState<AccessList | null>(null)

  const load = async () => {
    try { setLists(await api<AccessList[]>("/access-lists")); setLoadError("") }
    catch (err) { setLoadError(errorMessage(err)) }
    finally { setLoading(false) }
  }
  useEffect(() => {
    let alive = true
    const tick = () => api<AccessList[]>("/access-lists").then((v) => { if (alive) { setLists(v); setLoadError("") } }).catch((err) => { if (alive) setLoadError(errorMessage(err)) }).finally(() => { if (alive) setLoading(false) })
    void tick()
    const timer = setInterval(tick, 30000)
    return () => { alive = false; clearInterval(timer) }
  }, [])

  const edit = (list?: AccessList) => {
    setEditing(list?.id ?? "new")
    setForm(list ? { name: list.name, entries: list.entries, source_url: list.source_url, update_hours: list.update_hours } : empty)
    setError("")
  }
  const save = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setError("")
    try {
      await api("/access-lists", { method: "POST", json: { ...form, id: editing === "new" ? 0 : editing } })
      setEditing(null)
      await load()
      toast.success("Список сохранён и применён к балансировщикам")
    } catch (err) { setError(errorMessage(err)) }
    finally { setBusy(false) }
  }
  const refresh = async (list: AccessList) => {
    setBusy(true)
    try {
      await api(`/access-lists/refresh?id=${list.id}`, { method: "POST" })
      toast.success(`Список «${list.name}» обновлён`)
    } catch (err) { toast.error(errorMessage(err)) }
    finally { await load(); setBusy(false) }
  }
  const deleteList = async () => {
    if (!remove) return
    setBusy(true)
    try {
      await api(`/access-lists/delete?id=${remove.id}`, { method: "POST" })
      setRemove(null)
      await load()
      toast.success("Список удалён")
    } catch (err) { toast.error(errorMessage(err)) }
    finally { setBusy(false) }
  }

  return (
    <div id="ip-lists" className="flex scroll-mt-6 flex-col gap-4 border-t border-ground pt-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h3 className="text-base font-semibold">Списки IP и подсетей</h3>
          <p className="max-w-2xl text-sm text-ink-3">Создайте список вручную или по ссылке, затем выберите его в разрешённых или запрещённых адресах балансировщика.</p>
        </div>
        {editable && <button type="button" onClick={() => edit()} disabled={busy || editing !== null || loading || !!loadError} className={button}>Создать список</button>}
      </div>
      {loading && <p className="text-sm text-ink-3">Загружаю списки…</p>}
      {loadError && <p role="alert" className="text-sm text-down-text">{loadError} <button type="button" onClick={load} className="underline">Повторить</button></p>}
      {!loading && !loadError && lists.length === 0 && editing === null && <p className="text-sm text-ink-3">Списков пока нет.</p>}
      {lists.map((list) => (
        <div key={list.id} className="flex flex-col gap-2 rounded-xl border border-line p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="font-medium">{list.name} <span className="text-sm font-normal text-ink-3">· {list.entry_count.toLocaleString("ru-RU")} {plural(list.entry_count, "запись", "записи", "записей")}</span></span>
            {editable && <div className="flex flex-wrap gap-2">
              {list.source_url && <button type="button" disabled={busy} onClick={() => refresh(list)} className={button}>Обновить</button>}
              <button type="button" disabled={busy || editing !== null} onClick={() => edit(list)} className={button}>Изменить</button>
              <button type="button" disabled={busy} onClick={() => setRemove(list)} className={`${button} text-down-text`}>Удалить</button>
            </div>}
          </div>
          {list.source_url && <>
            <span className="break-all font-mono text-xs text-ink-3">{list.source_url}</span>
            <span className="text-xs text-ink-3">{list.update_hours ? `Обновление каждые ${list.update_hours} ч.` : "Обновление вручную."} Последняя загрузка: {list.last_updated_at ? new Date(list.last_updated_at).toLocaleString("ru-RU") : "ещё не было"}.</span>
          </>}
          {list.last_error && <p role="status" className="rounded-lg bg-warn-soft p-3 text-sm text-warn-text">{list.last_error}. Используется последняя успешно загруженная версия.</p>}
        </div>
      ))}
      {editing !== null && <form onSubmit={save} className="flex flex-col gap-4 rounded-xl border border-cobalt p-4">
        <h4 className="font-medium">{editing === "new" ? "Новый список" : "Изменить список"}</h4>
        <label className="flex flex-col gap-1.5 text-sm">Название<input autoFocus required maxLength={100} disabled={busy} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Например, доверенные сети" className={field} /></label>
        <label className="flex flex-col gap-1.5 text-sm">Ссылка на текстовый список<input type="url" disabled={busy} value={form.source_url} onChange={(e) => setForm({ ...form, source_url: e.target.value })} placeholder="https://example.com/networks.txt" className={`${field} font-mono`} /><span className="text-xs text-ink-3">Прямая публичная HTTP/HTTPS-ссылка на текст с IPv4, IPv6 и CIDR. По одному адресу или подсети в строке; можно использовать комментарии после #.</span></label>
        {form.source_url.trim() && <label className="flex flex-col gap-1.5 text-sm">Обновлять автоматически<select disabled={busy} value={form.update_hours} onChange={(e) => setForm({ ...form, update_hours: Number(e.target.value) })} className={field}>
          {[0, 1, 6, 12, 24, 168, 720].map((h) => <option key={h} value={h}>{h === 0 ? "Только вручную" : `Каждые ${h} ч.`}</option>)}
          {![0, 1, 6, 12, 24, 168, 720].includes(form.update_hours) && <option value={form.update_hours}>Каждые {form.update_hours} ч.</option>}
        </select></label>}
        <label className="flex flex-col gap-1.5 text-sm">IP и подсети вручную<textarea disabled={busy} value={form.entries} onChange={(e) => setForm({ ...form, entries: e.target.value })} placeholder={"203.0.113.7\n198.51.100.0/24\n2001:db8::/32"} className={`${field} min-h-[120px] font-mono text-xs`} /><span className="text-xs text-ink-3">Можно дополнить адреса из ссылки своими. Повторяющиеся записи будут объединены.</span></label>
        <p className="text-xs text-ink-3">Изменения применятся ко всем балансировщикам, использующим этот список. Если загрузка не удастся, действующие адреса сохранятся.</p>
        {error && <p role="alert" className="rounded-lg bg-down-soft p-3 text-sm text-down-text">{error}</p>}
        <div className="flex gap-2">
          <button type="submit" disabled={busy} className="rounded-lg bg-cobalt px-4 py-2 text-sm font-semibold text-white disabled:opacity-60">{busy ? "Сохраняю и загружаю…" : "Сохранить список"}</button>
          <button type="button" disabled={busy} onClick={() => setEditing(null)} className={button}>Отмена</button>
        </div>
      </form>}
      <ConfirmDialog open={remove !== null} onOpenChange={(open) => !open && !busy && setRemove(null)} title={`Удалить список «${remove?.name ?? ""}»?`} danger text="Список можно удалить, только если он не выбран ни в одном балансировщике." confirmLabel="Удалить" onConfirm={deleteList} busy={busy} />
    </div>
  )
}

export function AccessListPicker({ value, onChange, label }: { value: number[]; onChange: (ids: number[]) => void; label: string }) {
  const lists = usePoll(() => api<AccessList[]>("/access-lists"), null as AccessList[] | null, "access-list-picker", 30000)
  return <fieldset className="flex flex-col gap-2">
    <legend className="mb-1 text-[13px] text-ink-2">{label}</legend>
    {lists === null && <p className="text-xs text-ink-3">Списки ещё не загружены. <Link to="/settings#ip-lists" className="underline">Открыть списки</Link></p>}
    {lists?.map((list) => <label key={list.id} className="flex items-start gap-2 text-sm text-ink-2">
      <input type="checkbox" checked={value.includes(list.id)} onChange={(e) => onChange(e.target.checked ? [...value, list.id] : value.filter((id) => id !== list.id))} className="mt-0.5 h-4 w-4 accent-cobalt" />
      <span>{list.name} <span className="text-xs text-ink-3">({list.entry_count.toLocaleString("ru-RU")})</span>{list.last_error && <span className="block text-xs text-warn-text">Ошибка обновления, используется сохранённая версия</span>}</span>
    </label>)}
    {value.filter((id) => lists !== null && !lists.some((l) => l.id === id)).map((id) => <label key={id} className="flex gap-2 text-sm text-down-text"><input type="checkbox" checked onChange={() => onChange(value.filter((v) => v !== id))} />Список #{id} не найден — снимите выбор</label>)}
    <Link to="/settings#ip-lists" className="text-xs text-cobalt underline underline-offset-2">{lists?.length ? "Управлять списками" : "Создать список вручную или по ссылке"}</Link>
  </fieldset>
}

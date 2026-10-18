import { useState } from "react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom"
import { toast } from "sonner"
import { Check as CheckIcon, ChevronDown, Plus, TriangleAlert, X } from "lucide-react"
import { api, canEdit, errorMessage } from "@/lib/api"
import {
  applyCheck,
  changeKind,
  describe,
  formatMs,
  fromLB,
  fromRecipe,
  hasTLS,
  isDatagram,
  preflight,
  terminatesHTTP,
  toPayload,
  withKey,
  type BuilderForm,
  type NodeForm,
  type Preflight,
} from "@/lib/builderModel"
import { kindLabel, type Kind } from "@/lib/lbModel"
import { useLBs } from "@/lib/lbs"
import { blankNode, recipeList, type Check } from "@/lib/recipes"
import { cn } from "@/lib/utils"

const pill =
  "rounded-t-[10px] border-0 border-b-2 border-dashed border-cobalt bg-cobalt-soft px-2.5 py-0.5 font-medium text-ink outline-none focus:border-solid focus:bg-[#DCE2FF]"
const field = "h-11 w-full rounded-[10px] border border-line bg-white px-3.5 text-[15px] text-ink outline-none focus:border-cobalt focus:ring-2 focus:ring-cobalt/20"
const smallField = "h-10 w-full rounded-lg border border-line bg-white px-3 text-[15px] text-ink outline-none focus:border-cobalt focus:ring-2 focus:ring-cobalt/20"
const checkbox = "h-[18px] w-[18px] flex-none accent-cobalt"

const kindOptions: { value: Kind; label: string }[] = [
  { value: "http", label: "HTTP" },
  { value: "https_term", label: "HTTPS с расшифровкой" },
  { value: "https_pass", label: "HTTPS насквозь" },
  { value: "tcp", label: "TCP" },
  { value: "udp", label: "UDP" },
  { value: "dns", label: "DNS (UDP + TCP)" },
]

const checkOptions: { value: Check; label: string }[] = [
  { value: "dns", label: "DNS-запросом" },
  { value: "http", label: "HTTP-запросом" },
  { value: "https", label: "HTTPS-запросом" },
  { value: "tcp", label: "подключением TCP" },
  { value: "udp", label: "пакетом UDP" },
  { value: "none", label: "— не проверять" },
]

function StepTitle({ n, children }: { n: number; children: React.ReactNode }) {
  return (
    <h2 className="flex items-center gap-2.5 text-[15px] font-semibold text-ink-2">
      <span className="grid h-6 w-6 place-items-center rounded-full bg-ink text-xs text-white">{n}</span>
      {children}
    </h2>
  )
}

function Accordion({ id, title, summary, open, onToggle, children }: { id: string; title: string; summary: string; open: boolean; onToggle: (id: string) => void; children: React.ReactNode }) {
  return (
    <div className="overflow-hidden rounded-[14px] border border-line bg-white">
      <button type="button" aria-expanded={open} aria-controls={`acc-${id}`} onClick={() => onToggle(id)} className="flex min-h-14 w-full items-center gap-3 px-[18px] text-left">
        <span className="flex-1 text-[15px] font-semibold text-ink">{title}</span>
        <span className="hidden font-mono text-[13px] text-ink-2 sm:inline">{summary}</span>
        <ChevronDown className={`h-5 w-5 text-ink-3 transition-transform ${open ? "rotate-180" : ""}`} aria-hidden="true" />
      </button>
      {open && (
        <div id={`acc-${id}`} className="flex flex-wrap gap-4 px-[18px] pb-[18px] pt-1">
          {children}
        </div>
      )}
    </div>
  )
}

function NumberField({ label, value, onChange, hint, min = 0 }: { label: string; value: number; onChange: (v: number) => void; hint?: string; min?: number }) {
  return (
    <label className="flex min-w-[170px] flex-1 flex-col gap-1.5 text-[13px] text-ink-2">
      {label}
      <input type="number" min={min} value={value} onChange={(e) => onChange(Math.max(min, parseInt(e.target.value) || 0))} className={`${smallField} font-mono`} />
      {hint && <span className="text-xs text-ink-3">{hint}</span>}
    </label>
  )
}

function Toggle({ label, checked, onChange, hint }: { label: string; checked: boolean; onChange: (v: boolean) => void; hint?: string }) {
  return (
    <label className="flex w-full cursor-pointer items-start gap-3 text-[15px]">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} className={`${checkbox} mt-0.5`} />
      <span className="flex flex-col gap-0.5">
        <span className="font-medium text-ink">{label}</span>
        {hint && <span className="text-[13px] text-ink-3">{hint}</span>}
      </span>
    </label>
  )
}

function NodeRow({
  node, index, kind, check, open, onToggle, onChange, onRemove,
}: {
  node: NodeForm; index: number; kind: Kind; check: Check; open: boolean
  onToggle: () => void; onChange: (patch: Partial<NodeForm>) => void; onRemove: () => void
}) {
  const [test, setTest] = useState("")
  const runTest = async () => {
    if (!String(node.address).trim() || !node.port) {
      setTest("Сначала укажите адрес и порт.")
      return
    }
    setTest("Проверяю…")
    try {
      const res = await api<{ logs: string }>("/tools/test-connection", { method: "POST", json: { address: node.address, port: Number(node.port), tls_enabled: node.tls_enabled, http2_enabled: false } })
      setTest(res.logs)
    } catch (err) {
      setTest(errorMessage(err))
    }
  }
  const roleBtn = (active: boolean) => `h-[34px] rounded-[7px] px-3 text-[13px] font-medium ${active ? "bg-ink text-white" : "text-ink-2 hover:text-ink"}`
  return (
    <div className="rounded-[14px] bg-accent p-3">
      <div className="flex flex-wrap items-end gap-3">
        <span className="grid h-8 w-8 flex-none place-items-center self-center rounded-lg bg-ink font-mono text-[13px] text-white">{index + 1}</span>
        <label className="flex min-w-[96px] flex-[1_1_110px] flex-col gap-1 text-xs text-ink-3">
          Имя
          <input value={node.name} onChange={(e) => onChange({ name: e.target.value })} className={smallField} />
        </label>
        <label className="flex min-w-[140px] flex-[2_1_160px] flex-col gap-1 text-xs text-ink-3">
          Адрес
          <input
            value={node.address}
            onChange={(e) => onChange({ address: e.target.value })}
            placeholder="IP или домен"
            aria-invalid={!String(node.address).trim()}
            className={`${smallField} font-mono ${String(node.address).trim() ? "" : "border-warn"}`}
          />
        </label>
        <label className="flex w-24 flex-none flex-col gap-1 text-xs text-ink-3">
          Порт
          <input inputMode="numeric" value={node.port} onChange={(e) => onChange({ port: e.target.value === "" ? "" : parseInt(e.target.value) || 0 })} className={`${smallField} font-mono`} />
        </label>
        <div className="flex items-end gap-1.5">
        <div role="group" aria-label={`Роль узла ${index + 1}`} className="inline-flex gap-0.5 rounded-[10px] border border-line bg-white p-[3px]">
          <button type="button" aria-pressed={!node.backup} onClick={() => onChange({ backup: false })} className={roleBtn(!node.backup)}>основной</button>
          <button type="button" aria-pressed={node.backup} onClick={() => onChange({ backup: true })} className={roleBtn(node.backup)}>резерв</button>
        </div>
        <button type="button" aria-expanded={open} onClick={onToggle} className="inline-flex h-10 items-center gap-1 rounded-lg px-2.5 text-[13px] font-medium text-ink-2 hover:bg-white">
          Ещё <ChevronDown className={`h-4 w-4 transition-transform ${open ? "rotate-180" : ""}`} aria-hidden="true" />
        </button>
        <button type="button" aria-label={`Убрать узел ${index + 1}`} onClick={onRemove} className="grid h-10 w-10 place-items-center rounded-lg text-ink-3 hover:bg-white hover:text-down">
          <X className="h-[18px] w-[18px]" aria-hidden="true" />
        </button>
        </div>
      </div>

      {open && (
        <div className="mt-3 flex flex-col gap-4 rounded-xl bg-white p-4">
          <div className="flex flex-wrap gap-4">
            <NumberField label="Вес" value={node.weight} min={1} onChange={(v) => onChange({ weight: v })} hint="для «по весу»" />
            <NumberField label="Максимум соединений" value={node.max_conns} onChange={(v) => onChange({ max_conns: v })} hint="0 — без ограничения" />
            {!isDatagram(kind) && (
              <label className="flex min-w-[170px] flex-1 flex-col gap-1.5 text-[13px] text-ink-2">
                SNI / Host
                <input value={node.sni} onChange={(e) => onChange({ sni: e.target.value })} placeholder="как у клиента" className={smallField} />
                <span className="text-xs text-ink-3">подменить имя для узла</span>
              </label>
            )}
          </div>
          <div className="flex flex-wrap gap-x-8 gap-y-3">
            <label className="flex cursor-pointer items-center gap-2.5 text-[15px]">
              <input type="checkbox" checked={node.enabled} onChange={(e) => onChange({ enabled: e.target.checked })} className={checkbox} />
              Принимает трафик
            </label>
            {!isDatagram(kind) && (
              <label className="flex cursor-pointer items-center gap-2.5 text-[15px]">
                <input type="checkbox" checked={node.tls_enabled} onChange={(e) => onChange({ tls_enabled: e.target.checked })} className={checkbox} />
                {terminatesHTTP(kind) ? "К узлу по HTTPS" : "К узлу по TLS"}
              </label>
            )}
          </div>
          {check !== "none" && (
            <div className="flex flex-wrap gap-4 border-t border-line-soft pt-4">
              <label className="flex min-w-[120px] flex-1 flex-col gap-1.5 text-[13px] text-ink-2">
                Порт проверки
                <input inputMode="numeric" value={node.hc_port} placeholder={String(node.port || "")} onChange={(e) => onChange({ hc_port: e.target.value === "" ? "" : parseInt(e.target.value) || 0 })} className={`${smallField} font-mono`} />
              </label>
              {(check === "http" || check === "https") && (
                <label className="flex min-w-[140px] flex-1 flex-col gap-1.5 text-[13px] text-ink-2">
                  Путь
                  <input value={node.hc_path} onChange={(e) => onChange({ hc_path: e.target.value })} className={`${smallField} font-mono`} />
                </label>
              )}
              {check === "dns" && (
                <label className="flex min-w-[140px] flex-1 flex-col gap-1.5 text-[13px] text-ink-2">
                  Имя для запроса
                  <input value={node.hc_path === "/" ? "" : node.hc_path} placeholder="." onChange={(e) => onChange({ hc_path: e.target.value })} className={`${smallField} font-mono`} />
                </label>
              )}
              <NumberField label="Ожидание, с" value={node.hc_timeout} min={1} onChange={(v) => onChange({ hc_timeout: v })} />
              <NumberField label="Неудач до DOWN" value={node.hc_failure_threshold} min={1} onChange={(v) => onChange({ hc_failure_threshold: v })} />
              <NumberField label="Успехов до UP" value={node.hc_recovery_threshold} min={1} onChange={(v) => onChange({ hc_recovery_threshold: v })} />
            </div>
          )}
          {kind !== "udp" && (
            <div className="flex flex-col gap-2">
              <button type="button" onClick={runTest} className="self-start rounded-lg border border-line px-3 py-2 text-[13px] font-medium text-ink hover:bg-accent">Проверить соединение</button>
              {test && <pre className="whitespace-pre-wrap rounded-lg bg-rail p-3 font-mono text-xs text-[#CAD3DF]">{test}</pre>}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

const levelStyle: Record<Preflight["level"], { box: string; dot: string; mark: React.ReactNode; sr: string }> = {
  ok: { box: "bg-ok-soft text-[#0B5A43]", dot: "bg-ok", mark: <CheckIcon className="h-3.5 w-3.5" aria-hidden="true" />, sr: "Готово" },
  warn: { box: "bg-warn-soft text-warn-text", dot: "bg-[#B07A00]", mark: "!", sr: "Внимание" },
  error: { box: "bg-down-soft text-down-text", dot: "bg-down", mark: <X className="h-3.5 w-3.5" aria-hidden="true" />, sr: "Ошибка" },
}

export default function Builder() {
  const { id } = useParams()
  const editingId = id ? Number(id) : undefined
  const [search] = useSearchParams()
  const navigate = useNavigate()
  const { lbs, loaded, refresh } = useLBs()

  const initialRecipe = recipeList.find((r) => r.key === search.get("recipe")) ?? recipeList[1]
  const [recipe, setRecipe] = useState(initialRecipe.key)
  const [form, setForm] = useState<BuilderForm | null>(editingId ? null : fromRecipe(initialRecipe))
  const [openNode, setOpenNode] = useState<string | null>(null)
  const [openSection, setOpenSection] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [serverError, setServerError] = useState("")

  const editing = editingId ? lbs.find((lb) => lb.id === editingId) : undefined
  // Seed the form once the balancer being edited has loaded
  if (editingId && !form && editing) setForm(fromLB(editing))

  const closeTo = editingId ? `/lbs/${editingId}` : "/lbs"

  if (!canEdit()) {
    return (
      <div className="grid min-h-screen place-items-center bg-ground p-6 text-center">
        <div className="flex max-w-md flex-col gap-4">
          <p className="text-lg">Создавать и менять балансировщики могут администраторы и операторы.</p>
          <Link to="/lbs" className="font-medium text-cobalt">Вернуться к списку</Link>
        </div>
      </div>
    )
  }
  if (editingId && loaded && !editing) {
    return (
      <div className="grid min-h-screen place-items-center bg-ground p-6 text-center">
        <div className="flex flex-col gap-4">
          <p className="text-lg">Балансировщик не найден.</p>
          <Link to="/lbs" className="font-medium text-cobalt">К списку</Link>
        </div>
      </div>
    )
  }
  if (!form) return <div className="grid min-h-screen place-items-center bg-ground text-ink-3">Загрузка…</div>

  const set = <K extends keyof BuilderForm>(key: K, value: BuilderForm[K]) => setForm((f) => (f ? { ...f, [key]: value } : f))
  const update = (fn: (f: BuilderForm) => BuilderForm) => setForm((f) => (f ? fn(f) : f))
  const setNode = (key: string, patch: Partial<NodeForm>) => update((f) => ({ ...f, nodes: f.nodes.map((n) => (n.key === key ? { ...n, ...patch } : n)) }))
  const setCheck = (check: Check) => update((f) => ({ ...f, check, nodes: applyCheck(f.nodes, check, f.interval) }))
  const setInterval_ = (interval: string) => update((f) => ({ ...f, interval, nodes: applyCheck(f.nodes, f.check, interval) }))
  const addNode = () =>
    update((f) => {
      const node = withKey(blankNode({ name: `node${f.nodes.length + 1}`, port: Number(f.listen_port) || "", tls_enabled: f.nodes[0]?.tls_enabled ?? false, hc_port: f.nodes[0]?.hc_port ?? "" }))
      return { ...f, nodes: applyCheck([...f.nodes, node], f.check, f.interval) }
    })
  const pickRecipe = (key: typeof recipe) => {
    setRecipe(key)
    setForm(fromRecipe(recipeList.find((r) => r.key === key)!))
    setServerError("")
  }
  const toggleSection = (sid: string) => setOpenSection((cur) => (cur === sid ? null : sid))

  const checks = preflight(form, lbs, editingId)
  const errors = checks.filter((c) => c.level === "error").length
  const tls = hasTLS(form.kind)

  const applyFix = (c: Preflight) => {
    if (c.fix?.apply) update(c.fix.apply)
    if (c.fix?.open) setOpenSection(c.fix.open)
  }

  const save = async () => {
    if (errors) {
      toast.error("Сначала исправьте ошибки в проверке справа")
      return
    }
    setSaving(true)
    setServerError("")
    try {
      if (editingId) {
        await api("/load-balancers", { method: "PUT", json: toPayload(form, editingId) })
        toast.success("Изменения сохранены")
        await refresh()
        navigate(`/lbs/${editingId}`)
      } else {
        const res = await api<{ id: number }>("/load-balancers", { method: "POST", json: toPayload(form) })
        toast.success(`«${form.name.trim()}» создан`)
        await refresh()
        navigate(res?.id ? `/lbs/${res.id}` : "/lbs")
      }
    } catch (err) {
      setServerError(errorMessage(err))
      toast.error(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const timeoutsSummary = [
    formatMs(form.connect_timeout, "5 с"),
    formatMs(form.idle_timeout, isDatagram(form.kind) ? "60 с" : terminatesHTTP(form.kind) ? "120 с" : "без огр."),
    ...(terminatesHTTP(form.kind) || form.kind === "dns" ? [formatMs(form.response_timeout, form.kind === "dns" ? "5 с" : "без огр.")] : []),
  ].join(" · ")
  const tlsSummary = form.acme_enabled ? `Let's Encrypt${form.acme_domains.trim() ? ` · ${form.acme_domains.split(",")[0].trim()}` : ""}` : form.cert_data.trim() || form.has_key_data ? "свой сертификат" : form.cert_path.trim() ? "файлы на сервере" : "самоподписанный"

  return (
    <div className="flex min-h-screen flex-col bg-ground text-ink">
      <header className="flex flex-wrap items-center justify-between gap-3 bg-rail px-4 py-4 text-rail-text sm:px-10">
        <div className="flex items-center gap-3.5">
          <Link to={closeTo} aria-label="Закрыть конструктор" className="grid h-11 w-11 place-items-center rounded-[10px] bg-rail-3 text-[#C6CEDA] hover:text-white">
            <X className="h-[18px] w-[18px]" aria-hidden="true" />
          </Link>
          <span className="flex flex-col">
            <span className="font-display text-lg font-semibold tracking-tight text-white">{editingId ? `Изменить «${editing?.name ?? form.name}»` : "Новый балансировщик"}</span>
            <span className="text-[13px] text-rail-muted">{editingId ? "фраза → узлы → тонкая настройка" : "рецепт → фраза → узлы. Остальное можно не трогать."}</span>
          </span>
        </div>
        <div className="flex items-center gap-3">
          <span className="hidden text-[13px] text-[#A9B3C1] sm:inline">{errors ? `ошибок в проверке: ${errors}` : "всё готово"}</span>
          <button
            type="button"
            onClick={save}
            disabled={saving}
            aria-disabled={errors > 0}
            className={`h-11 rounded-[10px] px-5 text-[15px] font-semibold text-white ${errors ? "bg-ink-3" : "bg-cobalt hover:bg-cobalt-dark"} disabled:opacity-70`}
          >
            {saving ? "Сохраняю…" : editingId ? "Сохранить" : "Создать"}
          </button>
        </div>
      </header>

      <div className="flex flex-1 flex-wrap items-start gap-7 px-4 pb-14 pt-8 sm:px-10">
        <div className="flex min-w-0 flex-[3_1_640px] flex-col gap-7">
          {!editingId && (
            <section aria-labelledby="step-recipe" className="flex flex-col gap-3.5">
              <div id="step-recipe"><StepTitle n={1}>Что балансируем?</StepTitle></div>
              <div className="flex flex-wrap gap-3">
                {recipeList.map((r) => {
                  const on = r.key === recipe
                  return (
                    <button
                      key={r.key}
                      type="button"
                      aria-pressed={on}
                      onClick={() => pickRecipe(r.key)}
                      className={`flex min-h-[104px] flex-[1_1_170px] flex-col gap-1.5 rounded-2xl p-4 text-left transition-colors ${on ? "border-2 border-cobalt bg-cobalt text-white" : "border border-line bg-white text-ink hover:border-cobalt"}`}
                    >
                      <span className="text-base font-semibold">{r.label}</span>
                      <span className="text-[13px] leading-snug opacity-80">{r.desc}</span>
                    </button>
                  )
                })}
              </div>
            </section>
          )}

          <section aria-labelledby="step-sentence" className="flex flex-col gap-5 rounded-[22px] border border-line bg-white p-6 sm:p-7">
            <div id="step-sentence"><StepTitle n={editingId ? 1 : 2}>Скажите, как должно работать</StepTitle></div>
            <p className="text-[20px] leading-[2] tracking-[-0.01em] sm:text-[26px] sm:leading-[1.9]">
              Принимать{" "}
              <label className="inline-block">
                <span className="sr-only">Протокол</span>
                <select value={form.kind} onChange={(e) => update((f) => changeKind(f, e.target.value as Kind))} className={cn(pill, "cursor-pointer bg-ink text-white focus:bg-ink")}>
                  {kindOptions.map((o) => <option key={o.value} value={o.value} className="bg-white text-ink">{o.label}</option>)}
                </select>
              </label>{" "}
              на{" "}
              <label className="inline-block">
                <span className="sr-only">IP-адрес приёма</span>
                <input value={form.listen_ip} onChange={(e) => set("listen_ip", e.target.value)} size={Math.max(7, form.listen_ip.length)} className={`${pill} font-mono text-[0.85em]`} />
              </label>
              :
              <label className="inline-block">
                <span className="sr-only">Порт приёма</span>
                <input inputMode="numeric" value={form.listen_port} onChange={(e) => set("listen_port", e.target.value.replace(/\D/g, ""))} size={Math.max(3, form.listen_port.length)} className={`${pill} font-mono text-[0.85em]`} />
              </label>
              , отправлять{" "}
              <label className="inline-block">
                <span className="sr-only">Алгоритм распределения</span>
                <select value={form.algorithm} onChange={(e) => set("algorithm", e.target.value)} className={`${pill} cursor-pointer`}>
                  <option value="failover">на первый живой узел</option>
                  <option value="round_robin">по очереди на все узлы</option>
                  <option value="weighted_round_robin">по весу узлов</option>
                  <option value="least_conn">туда, где меньше соединений</option>
                  <option value="ip_hash">по IP клиента</option>
                </select>
              </label>{" "}
              и проверять узлы{" "}
              <label className="inline-block">
                <span className="sr-only">Способ проверки узлов</span>
                <select value={form.check} onChange={(e) => setCheck(e.target.value as Check)} className={`${pill} cursor-pointer`}>
                  {checkOptions.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
                </select>
              </label>
              {form.check !== "none" && (
                <>
                  {" "}каждые{" "}
                  <label className="inline-block">
                    <span className="sr-only">Интервал проверки, секунд</span>
                    <input inputMode="numeric" value={form.interval} onChange={(e) => setInterval_(e.target.value.replace(/\D/g, ""))} size={Math.max(2, form.interval.length)} className={`${pill} font-mono text-[0.85em]`} />
                  </label>{" "}
                  секунд
                </>
              )}
              .
            </p>
            <label className="flex flex-wrap items-center gap-3 text-[15px] text-ink-2">
              Название
              <input value={form.name} onChange={(e) => set("name", e.target.value)} className={`${field} max-w-xs`} />
            </label>
          </section>

          <section aria-labelledby="step-nodes" className="flex flex-col gap-4 rounded-[22px] border border-line bg-white p-6 sm:p-7">
            <div className="flex items-center justify-between gap-3">
              <div id="step-nodes"><StepTitle n={editingId ? 2 : 3}>Куда отправлять</StepTitle></div>
              <button type="button" onClick={addNode} className="inline-flex h-10 items-center gap-1.5 rounded-[10px] border border-line px-3.5 text-sm font-medium hover:bg-accent">
                <Plus className="h-4 w-4" aria-hidden="true" /> Узел
              </button>
            </div>
            {form.nodes.map((n, i) => (
              <NodeRow
                key={n.key}
                node={n}
                index={i}
                kind={form.kind}
                check={form.check}
                open={openNode === n.key}
                onToggle={() => setOpenNode((cur) => (cur === n.key ? null : n.key))}
                onChange={(patch) => setNode(n.key, patch)}
                onRemove={() => update((f) => ({ ...f, nodes: f.nodes.filter((x) => x.key !== n.key) }))}
              />
            ))}
            {form.nodes.length === 0 && <p className="text-sm text-ink-3">Узлов пока нет.</p>}
          </section>

          <section aria-labelledby="step-tuning" className="flex flex-col gap-2.5">
            <h2 id="step-tuning" className="text-[15px] font-semibold text-ink-2">
              Тонкая настройка <span className="font-normal text-ink-3">— значения по умолчанию уже подходят</span>
            </h2>
            <Accordion id="timeouts" title="Таймауты" summary={timeoutsSummary} open={openSection === "timeouts"} onToggle={toggleSection}>
              <NumberField label="Подключение к узлу, мс" value={form.connect_timeout} onChange={(v) => set("connect_timeout", v)} hint="0 — 5000" />
              <NumberField
                label={isDatagram(form.kind) && form.kind !== "dns" ? "Простой сессии, мс" : terminatesHTTP(form.kind) ? "Простой keep-alive, мс" : "Простой соединения, мс"}
                value={form.idle_timeout}
                onChange={(v) => set("idle_timeout", v)}
                hint={form.kind === "udp" ? "0 — 60000" : terminatesHTTP(form.kind) ? "0 — 120000" : "нет данных ни в одну сторону; 0 — не закрывать"}
              />
              {(terminatesHTTP(form.kind) || form.kind === "dns") && (
                <NumberField
                  label={form.kind === "dns" ? "Ответ DNS, мс" : "Ответ узла, мс"}
                  value={form.response_timeout}
                  onChange={(v) => set("response_timeout", v)}
                  hint={form.kind === "dns" ? "0 — 5000" : "до заголовков ответа, иначе 504; 0 — без ограничения"}
                />
              )}
            </Accordion>

            {tls && (
              <Accordion id="tls" title="TLS и сертификат" summary={tlsSummary} open={openSection === "tls"} onToggle={toggleSection}>
                <Toggle label="Сертификат от Let's Encrypt" checked={form.acme_enabled} onChange={(v) => set("acme_enabled", v)} hint="выпускается и продлевается сам" />
                {form.acme_enabled ? (
                  <>
                    <label className="flex min-w-[220px] flex-[2_1_260px] flex-col gap-1.5 text-[13px] text-ink-2">
                      Домены через запятую
                      <input value={form.acme_domains} onChange={(e) => set("acme_domains", e.target.value)} placeholder="example.com, www.example.com" className={smallField} />
                    </label>
                    <label className="flex min-w-[200px] flex-[1_1_200px] flex-col gap-1.5 text-[13px] text-ink-2">
                      Почта (необязательно)
                      <input value={form.acme_email} onChange={(e) => set("acme_email", e.target.value)} placeholder="admin@example.com" className={smallField} />
                    </label>
                  </>
                ) : (
                  <>
                    <label className="flex w-full flex-col gap-1.5 text-[13px] text-ink-2">
                      Сертификат (PEM)
                      <textarea value={form.cert_data} onChange={(e) => set("cert_data", e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" className="min-h-[88px] rounded-lg border border-line p-3 font-mono text-xs outline-none focus:border-cobalt" />
                    </label>
                    <label className="flex w-full flex-col gap-1.5 text-[13px] text-ink-2">
                      Закрытый ключ (PEM)
                      <textarea value={form.key_data} onChange={(e) => set("key_data", e.target.value)} placeholder={form.has_key_data ? "Ключ сохранён — оставьте пустым, чтобы не менять" : "-----BEGIN PRIVATE KEY-----"} className="min-h-[88px] rounded-lg border border-line p-3 font-mono text-xs outline-none focus:border-cobalt" />
                    </label>
                    <label className="flex min-w-[200px] flex-1 flex-col gap-1.5 text-[13px] text-ink-2">
                      или путь к сертификату
                      <input value={form.cert_path} onChange={(e) => set("cert_path", e.target.value)} placeholder="/app/certs/server.crt" className={`${smallField} font-mono`} />
                    </label>
                    <label className="flex min-w-[200px] flex-1 flex-col gap-1.5 text-[13px] text-ink-2">
                      и к ключу
                      <input value={form.key_path} onChange={(e) => set("key_path", e.target.value)} placeholder="/app/certs/server.key" className={`${smallField} font-mono`} />
                    </label>
                  </>
                )}
                <Toggle label="Переадресация с HTTP (порт 80) на HTTPS" checked={form.http_redirect} onChange={(v) => set("http_redirect", v)} hint="заодно отвечает на проверки Let's Encrypt HTTP-01" />
                <Toggle label="HTTP/3 (QUIC)" checked={form.http3_enabled} onChange={(v) => set("http3_enabled", v)} hint="браузеры узнают о нём из заголовка Alt-Svc" />
              </Accordion>
            )}

            {terminatesHTTP(form.kind) && (
              <Accordion id="http" title="HTTP к узлам" summary={form.backend_http2_enabled ? "HTTP/2" : "HTTP/1.1"} open={openSection === "http"} onToggle={toggleSection}>
                <Toggle label="HTTP/2 к узлам" checked={form.backend_http2_enabled} onChange={(v) => set("backend_http2_enabled", v)} hint="работает через TLS, то есть для узлов с HTTPS" />
              </Accordion>
            )}

            <Accordion
              id="sticky"
              title="Закрепление клиентов"
              summary={form.sticky_sessions_enabled ? (form.sticky_session_type === "cookie" ? "по cookie" : "по IP") : "выключено"}
              open={openSection === "sticky"}
              onToggle={toggleSection}
            >
              <Toggle label="Отправлять клиента на тот же узел" checked={form.sticky_sessions_enabled} onChange={(v) => set("sticky_sessions_enabled", v)} />
              {form.sticky_sessions_enabled && (
                <label className="flex min-w-[200px] flex-col gap-1.5 text-[13px] text-ink-2">
                  Как узнавать клиента
                  <select value={form.sticky_session_type} onChange={(e) => set("sticky_session_type", e.target.value)} className={smallField}>
                    <option value="ip">по IP-адресу</option>
                    {terminatesHTTP(form.kind) && <option value="cookie">по cookie</option>}
                  </select>
                </label>
              )}
            </Accordion>

            <Accordion
              id="proxy"
              title="PROXY protocol"
              summary={form.proxy_protocol_enabled ? `v${form.proxy_protocol_version}` : "выключен"}
              open={openSection === "proxy"}
              onToggle={toggleSection}
            >
              <Toggle
                label="Передавать узлам IP клиента"
                checked={form.proxy_protocol_enabled}
                onChange={(v) => set("proxy_protocol_enabled", v)}
                hint={form.kind === "dns" || form.check === "dns" ? "AdGuard Home его не понимает — для его узлов не включайте" : "узел должен ждать заголовок PROXY"}
              />
              {form.proxy_protocol_enabled && (
                <label className="flex min-w-[200px] flex-col gap-1.5 text-[13px] text-ink-2">
                  Версия
                  <select value={form.proxy_protocol_version} onChange={(e) => set("proxy_protocol_version", Number(e.target.value))} className={smallField}>
                    {!isDatagram(form.kind) && <option value={1}>v1 (текст)</option>}
                    <option value={2}>v2 (двоичная)</option>
                  </select>
                </label>
              )}
            </Accordion>
          </section>
        </div>

        <aside aria-label="Что получится" className="flex min-w-0 flex-[2_1_380px] flex-col gap-5 lg:sticky lg:top-6">
          <section className="flex flex-col gap-4 rounded-[22px] bg-rail p-6 text-rail-text">
            <span className="font-mono text-[11px] uppercase tracking-[0.12em] text-rail-muted">Что получится</span>
            <div className="flex items-center">
              <span className="flex flex-none flex-col gap-0.5 rounded-xl border border-[#222B36] bg-rail-2 px-3 py-2.5">
                <span className="font-mono text-sm text-white">{form.listen_ip || "0.0.0.0"}:{form.listen_port || "—"}</span>
                <span className="text-xs text-[#A9B3C1]">{kindLabel[form.kind]}</span>
              </span>
              <span aria-hidden="true" className="h-1 min-w-6 flex-1 bg-cobalt" />
              <span className="flex flex-none flex-col gap-1.5">
                {form.nodes.length === 0 && <span className="text-sm text-[#A9B3C1]">узлов нет</span>}
                {form.nodes.slice(0, 6).map((n) => (
                  <span key={n.key} className={`inline-flex items-center gap-2 rounded-[10px] bg-rail-2 px-2.5 py-1.5 text-[13px] ${n.backup ? "border border-dashed border-[#3A4555]" : "border border-[#1F6B52]"}`}>
                    <span className={`box-border h-2 w-2 rounded-full border-2 border-ok-bright ${n.backup ? "" : "bg-ok-bright"}`} />
                    <span className="max-w-[120px] truncate text-white">{n.name || n.address || "узел"}</span>
                    <span className="font-mono text-rail-muted">{n.backup ? "резерв" : "основной"}</span>
                  </span>
                ))}
                {form.nodes.length > 6 && <span className="text-xs text-rail-muted">и ещё {form.nodes.length - 6}</span>}
              </span>
            </div>
            <p className="text-base leading-relaxed">{describe(form)}</p>
          </section>

          <section aria-labelledby="pf-title" className="flex flex-col gap-3 rounded-[22px] border border-line bg-white p-6">
            <h2 id="pf-title" className="font-display text-[17px] font-semibold tracking-tight">Предполётная проверка</h2>
            <ul className="flex flex-col gap-2.5">
              {serverError && (
                <li className="flex gap-3 rounded-xl bg-down-soft px-3.5 py-3 text-down-text">
                  <TriangleAlert className="mt-0.5 h-5 w-5 flex-none" aria-hidden="true" />
                  <span className="text-sm leading-snug"><span className="sr-only">Ошибка сервера: </span>{serverError}</span>
                </li>
              )}
              {checks.map((c, i) => (
                <li key={i} className={`flex gap-3 rounded-xl px-3.5 py-3 ${levelStyle[c.level].box}`}>
                  <span aria-hidden="true" className={`grid h-[22px] w-[22px] flex-none place-items-center rounded-full text-[13px] font-bold text-white ${levelStyle[c.level].dot}`}>{levelStyle[c.level].mark}</span>
                  <span className="flex flex-col gap-1.5 text-sm leading-snug">
                    <span><span className="sr-only">{levelStyle[c.level].sr}: </span>{c.text}</span>
                    {c.fix && (
                      <button type="button" onClick={() => applyFix(c)} className="self-start rounded-lg bg-current px-3 py-1.5 text-[13px] font-semibold">
                        <span className="text-white">{c.fix.label}</span>
                      </button>
                    )}
                  </span>
                </li>
              ))}
            </ul>
          </section>
        </aside>
      </div>
    </div>
  )
}

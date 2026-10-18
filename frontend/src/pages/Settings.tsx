import { useEffect, useState, type ReactNode } from "react"
import { toast } from "sonner"
import { Check, Copy, TriangleAlert, X } from "lucide-react"
import { api, errorMessage, isAdmin, tokenClaims } from "@/lib/api"
import { roleLabel } from "@/lib/lbModel"
import { usePoll } from "@/lib/usePoll"
import { Switch } from "@/components/Switch"

interface SettingsResponse {
  telegram_bot_token_set: boolean
  telegram_chat_id: string
  notifications_enabled: boolean
}

interface SystemInfo {
  version: string
  api_addr: string
  api_loopback: boolean
  jwt_from_env: boolean
  db_driver: string
  default_admin_password: boolean
  login_max_failures: number
  login_lockout_minutes: number
}

const sections = [
  { id: "notify", label: "Уведомления", admin: true },
  { id: "access", label: "Доступ", admin: false },
  { id: "security", label: "Безопасность", admin: true },
  { id: "about", label: "О системе", admin: true },
]

const cardClass = "flex scroll-mt-6 flex-col gap-[18px] rounded-[20px] border border-line bg-white p-6 sm:p-7"
const h2Class = "font-display text-xl font-semibold tracking-tight"
const fieldClass = "h-[46px] rounded-[10px] border border-line px-3.5 text-[15px] text-ink outline-none focus:border-cobalt focus:ring-2 focus:ring-cobalt/20"

function CopyCommand({ command }: { command: string }) {
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

function Telegram() {
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [token, setToken] = useState("")
  const [tokenSet, setTokenSet] = useState(false)
  const [chatId, setChatId] = useState("")
  const [enabled, setEnabled] = useState(false)

  useEffect(() => {
    api<SettingsResponse>("/settings")
      .then((data) => {
        setTokenSet(data.telegram_bot_token_set)
        setChatId(data.telegram_chat_id || "")
        setEnabled(data.notifications_enabled || false)
      })
      .catch((err) => toast.error(errorMessage(err)))
      .finally(() => setLoading(false))
  }, [])

  const save = async () => {
    setSaving(true)
    try {
      // An empty token keeps the stored one
      await api("/settings/update", { method: "POST", json: { telegram_bot_token: token, telegram_chat_id: chatId.trim(), notifications_enabled: enabled } })
      if (token) setTokenSet(true)
      setToken("")
      toast.success("Настройки уведомлений сохранены")
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <section id="notify" aria-labelledby="notify-title" className={`${cardClass} sm:flex-row sm:flex-wrap sm:gap-7`}>
      <div className="flex min-w-0 flex-[3_1_380px] flex-col gap-[18px]">
        <div className="flex items-start justify-between gap-4">
          <div className="flex flex-col gap-1">
            <h2 id="notify-title" className={h2Class}>Telegram</h2>
            <span className="text-sm text-ink-3">Сообщения о сбоях, переключении на резерв и восстановлении узлов.</span>
          </div>
          <span className="inline-flex flex-none items-center gap-2.5 text-sm font-medium">
            <Switch checked={enabled} disabled={loading} onChange={setEnabled} label="Уведомления в Telegram" />
            {enabled ? "Включены" : "Выключены"}
          </span>
        </div>
        <label className="flex flex-col gap-1.5 text-sm font-medium">
          Токен бота
          <input
            type="password"
            autoComplete="off"
            value={token}
            disabled={loading}
            onChange={(e) => setToken(e.target.value)}
            placeholder={tokenSet ? "Токен сохранён — оставьте пустым, чтобы не менять" : "1234567890:AAH…"}
            className={fieldClass}
          />
          <span className="text-[13px] font-normal text-ink-3">Создаётся у @BotFather. Обратно в интерфейс не показывается.</span>
        </label>
        <label className="flex flex-col gap-1.5 text-sm font-medium">
          ID чата
          <input value={chatId} disabled={loading} onChange={(e) => setChatId(e.target.value)} placeholder="например, 12345678" className={`${fieldClass} font-mono`} />
          <span className="text-[13px] font-normal text-ink-3">Чат или канал, куда бот будет писать.</span>
        </label>
        <button type="button" onClick={save} disabled={saving || loading} className="h-11 self-start rounded-[10px] bg-cobalt px-[18px] text-[15px] font-semibold text-white hover:bg-cobalt-dark disabled:opacity-60">
          {saving ? "Сохраняю…" : "Сохранить"}
        </button>
      </div>

      <div aria-label="Так выглядят сообщения" className="flex min-w-0 flex-[2_1_260px] flex-col justify-end gap-2.5 self-stretch rounded-2xl bg-[#DDE6EE] p-5">
        <span className="self-center rounded-full bg-ink/10 px-2.5 py-0.5 text-xs text-ink-4">пример</span>
        {[
          <><b>DNS</b>: узел <b>DNS1</b> (10.0.1.8:53) не отвечает 🔴</>,
          <>⚠️ <b>DNS</b>: основные узлы не отвечают — трафик переключён на резерв.</>,
          <><b>DNS</b>: узел <b>DNS1</b> (10.0.1.8:53) снова в строю 🟢</>,
        ].map((text, i) => (
          <div key={i} className="flex max-w-[92%] flex-col gap-1.5 self-start rounded-[14px_14px_14px_4px] bg-white px-3.5 py-3 shadow-[0_1px_0_rgba(13,17,23,0.08)]">
            <span className="text-[13px] font-semibold text-cobalt">Balancer</span>
            <span className="text-sm leading-snug">{text}</span>
          </div>
        ))}
      </div>
    </section>
  )
}

function Access() {
  const role = tokenClaims()?.role ?? ""
  const roles = [
    { key: "Admin", text: "Всё, включая эти настройки и уведомления." },
    { key: "Operator", text: "Создаёт и меняет балансировщики, проверяет и выключает узлы." },
    { key: "Viewer", text: "Только смотрит: пульс, схемы, журнал." },
  ]
  return (
    <section id="access" aria-labelledby="access-title" className={cardClass}>
      <h2 id="access-title" className={h2Class}>Доступ</h2>
      <div className="flex flex-wrap gap-3">
        {roles.map((r) => (
          <div key={r.key} className={`flex flex-[1_1_200px] flex-col gap-1.5 rounded-[14px] p-4 ${r.key === role ? "border-2 border-cobalt" : "border border-line"}`}>
            <strong className="text-[15px]">{roleLabel[r.key]}</strong>
            <span className="text-[13px] leading-snug text-ink-2">{r.text}</span>
            {r.key === role && <span className="mt-1 text-[13px] text-cobalt-dark">это вы</span>}
          </div>
        ))}
      </div>
      <div className="flex flex-col gap-2">
        <span className="text-sm text-ink-2">Пользователей добавляют и меняют на сервере:</span>
        <CopyCommand command="docker compose exec backend ./balancer-cli users add имя пароль Operator" />
        <span className="text-[13px] text-ink-3">Ещё есть команды users list, passwd, role и delete.</span>
      </div>
    </section>
  )
}

function Security({ info }: { info: SystemInfo | null }) {
  if (!info) return null
  const items: { ok: boolean; title: string; detail?: ReactNode }[] = [
    {
      ok: info.api_loopback,
      title: info.api_loopback ? "API доступен только с этого сервера" : "API открыт для сети",
      detail: info.api_loopback ? <code className="font-mono">{info.api_addr}</code> : <>Слушает {info.api_addr}. Если доступ снаружи не нужен, задайте <code className="font-mono">API_ADDR=127.0.0.1:8080</code>.</>,
    },
    { ok: true, title: "Секрет токенов уникален для этой установки", detail: info.jwt_from_env ? "из JWT_SECRET" : "сгенерирован и хранится в базе" },
    {
      ok: !info.default_admin_password,
      title: info.default_admin_password ? "У admin стандартный пароль «admin»" : "Пароль admin не стандартный",
      detail: info.default_admin_password ? <CopyCommand command="docker compose exec backend ./balancer-cli users passwd admin новый-пароль" /> : undefined,
    },
    { ok: true, title: "Перебор паролей блокируется", detail: `${info.login_max_failures} попыток · ${info.login_lockout_minutes} мин` },
  ]
  const okCount = items.filter((i) => i.ok).length
  return (
    <section id="security" aria-labelledby="sec-title" className={cardClass}>
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="sec-title" className={h2Class}>Безопасность</h2>
        <span className="text-sm text-ink-2">{okCount} из {items.length} в порядке</span>
      </div>
      <ul className="flex flex-col">
        {items.map((it) => (
          <li key={it.title} className={`flex gap-3.5 ${it.ok ? "border-b border-ground py-3 last:border-b-0" : "my-1.5 rounded-xl bg-down-soft p-3.5 text-down-text"}`}>
            <span className={`grid h-6 w-6 flex-none place-items-center rounded-full text-white ${it.ok ? "bg-ok" : "bg-down"}`}>
              {it.ok ? <Check className="h-3.5 w-3.5" aria-label="в порядке" /> : <X className="h-3.5 w-3.5" aria-label="требует внимания" />}
            </span>
            <span className="flex min-w-0 flex-1 flex-col gap-1.5">
              <span className="text-[15px] font-medium">{it.title}</span>
              {it.detail && <span className={`text-sm ${it.ok ? "text-ink-2" : ""}`}>{it.detail}</span>}
            </span>
          </li>
        ))}
        <li className="mt-2 flex gap-3.5 rounded-xl bg-warn-soft p-3.5 text-warn-text">
          <TriangleAlert className="mt-0.5 h-5 w-5 flex-none" aria-hidden="true" />
          <span className="text-sm leading-snug">Интерфейс отсюда не видит файрвол. Откройте порт 3000 только своим адресам у хостера или заходите через VPN.</span>
        </li>
      </ul>
    </section>
  )
}

function About({ info }: { info: SystemInfo | null }) {
  if (!info) return null
  const rows: [string, string][] = [
    ["Версия", info.version === "dev" ? "dev (собрано локально)" : info.version.slice(0, 12)],
    ["База данных", info.db_driver === "postgres" ? "PostgreSQL" : info.db_driver === "sqlite" ? "SQLite" : info.db_driver],
    ["Метрики Prometheus", `${info.api_addr}/metrics`],
    ["Сертификаты Let's Encrypt", "./backend/certs"],
  ]
  return (
    <section id="about" aria-labelledby="about-title" className={cardClass}>
      <h2 id="about-title" className={h2Class}>О системе</h2>
      <dl className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)] gap-x-6 gap-y-3 text-sm">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-ink-3">{k}</dt>
            <dd className="font-mono">{v}</dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

export function Settings() {
  const admin = isAdmin()
  const info = usePoll(() => (admin ? api<SystemInfo>("/system") : Promise.resolve(null)), null as SystemInfo | null, "system", 30000)
  const insecure = info ? Number(!info.api_loopback) + Number(info.default_admin_password) : 0

  return (
    <div className="flex flex-col gap-6">
      <h1 className="font-display text-[44px] font-semibold leading-[1.05] tracking-tight">Настройки</h1>
      <div className="flex flex-col items-start gap-6 lg:flex-row lg:gap-8">
        <nav aria-label="Разделы настроек" className="flex flex-wrap gap-1 lg:sticky lg:top-6 lg:w-56 lg:flex-none lg:flex-col">
          {sections.filter((s) => admin || !s.admin).map((s) => (
            <a key={s.id} href={`#${s.id}`} className="flex h-11 items-center justify-between gap-2.5 rounded-[10px] px-3.5 text-[15px] text-ink-2 hover:bg-white hover:text-ink">
              {s.label}
              {s.id === "security" && insecure > 0 && (
                <span className="grid h-[22px] min-w-[22px] place-items-center rounded-full bg-down-soft px-1.5 text-xs font-bold text-down-text">{insecure}</span>
              )}
            </a>
          ))}
        </nav>
        <div className="flex w-full min-w-0 flex-1 flex-col gap-6">
          {!admin && (
            <p className="rounded-[14px] bg-ground px-5 py-4 text-ink-2">Уведомления и сведения о системе видят только администраторы.</p>
          )}
          {admin && <Telegram />}
          <Access />
          {admin && <Security info={info} />}
          {admin && <About info={info} />}
        </div>
      </div>
    </div>
  )
}

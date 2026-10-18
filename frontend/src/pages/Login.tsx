import { useState, type FormEvent } from "react"
import { Navigate, useNavigate } from "react-router-dom"
import { Eye, EyeOff } from "lucide-react"
import { ApiError, api, errorMessage, isLoggedIn, setToken } from "@/lib/api"
import { LogoMark, Wordmark } from "@/components/Logo"

function loginError(err: unknown) {
  if (err instanceof ApiError) {
    if (err.status === 401) return "Неверный логин или пароль."
    if (err.status === 429) return "Слишком много неудачных попыток. Вход для этого логина закрыт на 15 минут."
    if (err.status >= 500) return "Сервер не ответил. Проверьте, что контейнер backend запущен."
  }
  if (err instanceof TypeError) return "Нет связи с сервером."
  return errorMessage(err)
}

// Three streams converging into one: the same picture as the logo, animated
function Streams() {
  return (
    <svg viewBox="0 0 480 300" className="w-full max-w-[440px]" fill="none" aria-hidden="true">
      {[
        "M10 40 C 180 40, 220 150, 470 150",
        "M10 150 H 470",
        "M10 260 C 180 260, 220 150, 470 150",
      ].map((d, i) => (
        <g key={i}>
          <path d={d} stroke="#1E2631" strokeWidth={10} strokeLinecap="round" />
          <path d={d} stroke={i === 1 ? "#4C6FFF" : "#2E7DFF"} strokeWidth={3} strokeLinecap="round" className="stream-dashes" style={{ animationDelay: `${i * -0.4}s` }} />
        </g>
      ))}
      <circle cx="470" cy="150" r="9" fill="#32D49B" className="animate-led" />
    </svg>
  )
}

export default function Login() {
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [show, setShow] = useState(false)
  const [error, setError] = useState("")
  const [loading, setLoading] = useState(false)
  const navigate = useNavigate()

  if (isLoggedIn()) return <Navigate to="/" replace />

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setLoading(true)
    setError("")
    try {
      const data = await api<{ token: string }>("/auth/login", { method: "POST", json: { username: username.trim(), password } })
      setToken(data.token)
      navigate("/", { replace: true })
    } catch (err) {
      setError(loginError(err))
    } finally {
      setLoading(false)
    }
  }

  const field = "h-[50px] w-full rounded-xl border border-line bg-white px-4 text-base text-ink outline-none focus:border-cobalt focus:ring-2 focus:ring-cobalt/20"

  return (
    <div className="flex min-h-screen flex-col bg-ground lg:flex-row">
      <aside className="relative flex flex-col justify-between gap-10 overflow-hidden bg-rail px-8 py-8 text-rail-text lg:w-[46%] lg:px-14 lg:py-12">
        <div className="flex items-center gap-2.5 text-white">
          <LogoMark className="h-7 w-7" color="#4C6FFF" />
          <Wordmark className="text-xl" />
        </div>
        <div className="hidden flex-col gap-10 lg:flex">
          <Streams />
          <div className="flex flex-col gap-4">
            <p className="font-display text-[40px] font-semibold leading-[1.1] tracking-tight text-white">
              Много узлов.
              <br />
              Один адрес.
            </p>
            <p className="max-w-[400px] text-base leading-relaxed text-rail-muted">
              HTTP, HTTPS, TCP, UDP и DNS — с проверками здоровья, резервом и уведомлениями в Telegram.
            </p>
          </div>
        </div>
        <span className="hidden font-mono text-xs text-rail-muted lg:block">self-hosted · open source</span>
      </aside>

      <main className="flex flex-1 items-center justify-center px-4 py-12 sm:px-8">
        <form onSubmit={submit} className="flex w-full max-w-[400px] flex-col gap-6" noValidate>
          <div className="flex flex-col gap-2">
            <h1 className="font-display text-[34px] font-semibold leading-tight tracking-tight">Вход</h1>
            <p className="text-base text-ink-2">Панель управления балансировщиком.</p>
          </div>

          {error && (
            <p role="alert" className="rounded-xl bg-down-soft px-4 py-3 text-[15px] text-down-text">
              {error}
            </p>
          )}

          <label className="flex flex-col gap-2 text-sm font-medium">
            Логин
            <input
              autoFocus
              autoComplete="username"
              autoCapitalize="none"
              spellCheck={false}
              required
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              className={field}
            />
          </label>

          <label className="flex flex-col gap-2 text-sm font-medium">
            Пароль
            <span className="relative">
              <input
                type={show ? "text" : "password"}
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className={`${field} pr-12`}
              />
              <button
                type="button"
                onClick={() => setShow((s) => !s)}
                aria-label={show ? "Скрыть пароль" : "Показать пароль"}
                aria-pressed={show}
                className="absolute right-1.5 top-1/2 grid h-10 w-10 -translate-y-1/2 place-items-center rounded-lg text-ink-3 hover:text-ink"
              >
                {show ? <EyeOff className="h-5 w-5" aria-hidden="true" /> : <Eye className="h-5 w-5" aria-hidden="true" />}
              </button>
            </span>
          </label>

          <button
            type="submit"
            disabled={loading || !username.trim() || !password}
            className="h-[50px] rounded-xl bg-cobalt text-base font-semibold text-white hover:bg-cobalt-dark disabled:opacity-60"
          >
            {loading ? "Входим…" : "Войти"}
          </button>

          <details className="text-sm text-ink-3">
            <summary className="cursor-pointer select-none hover:text-ink-2">Забыли пароль?</summary>
            <div className="mt-3 flex flex-col gap-2 leading-relaxed">
              <span>Новый пароль задаётся на сервере:</span>
              <code className="overflow-x-auto whitespace-nowrap rounded-lg bg-rail px-3 py-2.5 font-mono text-[13px] text-rail-text">
                docker compose exec backend ./balancer-cli users passwd admin новый-пароль
              </code>
              <span>После 5 неудачных попыток вход для логина закрывается на 15 минут.</span>
            </div>
          </details>
        </form>
      </main>
    </div>
  )
}

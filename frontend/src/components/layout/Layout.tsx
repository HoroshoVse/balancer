import { useEffect, useState } from "react"
import { Link, NavLink, Outlet, useLocation } from "react-router-dom"
import { Activity, LogOut, Menu, Plus, Search, Server, SlidersHorizontal, Terminal, X } from "lucide-react"
import { CommandPalette } from "@/components/CommandPalette"
import { LogoMark, Wordmark } from "@/components/Logo"
import { api, canEdit, logout, tokenClaims } from "@/lib/api"
import { useLBs } from "@/lib/lbs"
import { formatNumber, kindOf, lbHealth, ledClass, roleLabel } from "@/lib/lbModel"

const navItems = [
  { to: "/", label: "Пульс", icon: Activity, end: true },
  { to: "/lbs", label: "Балансировщики", icon: Server, end: false },
  { to: "/logs", label: "Журнал", icon: Terminal, end: false },
  { to: "/settings", label: "Настройки", icon: SlidersHorizontal, end: false },
]

const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform)

function Rack() {
  const { lbs, rps, loaded } = useLBs()
  const location = useLocation()
  return (
    <section aria-label="Стойка" className="flex flex-col gap-2">
      <div className="flex items-center justify-between px-2 font-mono text-[11px] uppercase tracking-[0.12em] text-rail-muted">
        <span>Стойка</span>
        <span>{lbs.length} юн.</span>
      </div>
      {!loaded && <div className="h-12 animate-pulse rounded-lg bg-rail-2" />}
      {lbs.map((lb) => {
        const health = lbHealth(lb)
        const current = location.pathname === `/lbs/${lb.id}`
        const kind = kindOf(lb)
        return (
          <Link
            key={lb.id}
            to={`/lbs/${lb.id}`}
            aria-current={current ? "true" : undefined}
            title={lb.status_error || undefined}
            className={`flex h-12 items-center gap-2.5 rounded-lg border px-3 text-rail-text transition-colors ${current ? "border-[#3A4DB8] bg-rail-4" : "border-rail-line bg-rail-2 hover:bg-rail-3"}`}
          >
            <span className={`h-2 w-2 flex-none rounded-full ${ledClass[health.state]} ${health.state === "ok" ? "animate-led" : ""}`} />
            <span className="flex min-w-0 flex-1 flex-col">
              <span className="truncate text-sm font-medium">{lb.name}</span>
              <span className="truncate font-mono text-[11px] text-rail-muted">
                {kind === "dns" ? "DNS" : lb.protocol.toUpperCase()} · :{lb.listen_port}
              </span>
            </span>
            <span className="font-mono text-xs text-[#C6CEDA]">{rps[lb.id] === undefined ? "—" : `${formatNumber(rps[lb.id])}/с`}</span>
          </Link>
        )
      })}
      {canEdit() && (
        <Link to="/lbs/new" className="flex h-11 items-center justify-center gap-2 rounded-lg border border-dashed border-[#2E3846] text-sm text-[#A9B3C1] hover:text-white">
          <Plus className="h-4 w-4" aria-hidden="true" />
          Добавить юнит
        </Link>
      )}
    </section>
  )
}

function Rail({ onSearch }: { onSearch: () => void }) {
  const claims = tokenClaims()
  const [user, setUser] = useState({ username: claims?.username ?? "", role: claims?.role ?? "" })
  useEffect(() => {
    api<{ username: string; role: string }>("/auth/me").then(setUser).catch(() => {})
  }, [])

  return (
    <div className="flex h-full flex-col gap-6 px-4 pb-5 pt-6">
      <Link to="/" className="hidden items-center gap-2.5 px-2 text-[#F4F7FA] lg:flex">
        <LogoMark color="#8EA2FF" />
        <Wordmark className="text-[19px]" />
      </Link>

      <button
        type="button"
        onClick={onSearch}
        className="flex h-11 items-center gap-2.5 rounded-[10px] border border-[#232B36] bg-rail-3 px-3 text-left text-sm text-[#A9B3C1] hover:text-white"
      >
        <Search className="h-[18px] w-[18px]" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate">Поиск и команды</span>
        <kbd className="rounded-md border border-[#2A3340] bg-rail px-1.5 py-0.5 font-mono text-xs text-[#C6CEDA]">{isMac ? "⌘K" : "Ctrl K"}</kbd>
      </button>

      <nav aria-label="Разделы" className="flex flex-col gap-1">
        {navItems.map(({ to, label, icon: Icon, end }) => (
          <NavLink
            key={to}
            to={to}
            end={end}
            className={({ isActive }) =>
              `flex h-11 items-center gap-3 rounded-[10px] px-3 text-[15px] transition-colors ${isActive ? "bg-rail-4 font-medium text-white" : "text-rail-soft hover:text-white"}`
            }
          >
            {({ isActive }) => (
              <>
                <Icon className={`h-5 w-5 ${isActive ? "text-cobalt-light" : ""}`} aria-hidden="true" />
                {label}
              </>
            )}
          </NavLink>
        ))}
      </nav>

      <Rack />

      <div className="mt-auto flex items-center gap-2.5 rounded-[10px] bg-rail-2 p-3">
        <span className="grid h-8 w-8 flex-none place-items-center rounded-full bg-cobalt text-sm font-semibold text-white">
          {(user.username || "?").slice(0, 1).toUpperCase()}
        </span>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-sm font-medium text-rail-text">{user.username}</span>
          <span className="truncate text-xs text-rail-muted">{roleLabel[user.role] ?? user.role}</span>
        </span>
        <button type="button" onClick={logout} aria-label="Выйти" className="grid h-11 w-11 place-items-center rounded-lg text-[#A9B3C1] hover:text-white">
          <LogOut className="h-[18px] w-[18px]" aria-hidden="true" />
        </button>
      </div>
    </div>
  )
}

export function Layout() {
  const location = useLocation()
  const [paletteOpen, setPaletteOpen] = useState(false)
  // The phone menu stays open only on the page where it was opened
  const [menuPath, setMenuPath] = useState<string | null>(null)
  const menuOpen = menuPath === location.pathname

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault()
        setPaletteOpen((open) => !open)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  return (
    <div className="min-h-screen bg-ground text-ink lg:flex">
      {/* Phone / tablet bar */}
      <header className="sticky top-0 z-30 flex h-14 items-center justify-between gap-2 bg-rail px-4 text-rail-text lg:hidden">
        <Link to="/" className="flex items-center gap-2">
          <LogoMark className="h-6 w-6" color="#8EA2FF" />
          <Wordmark className="text-base" />
        </Link>
        <div className="flex items-center gap-1">
          <button type="button" onClick={() => setPaletteOpen(true)} aria-label="Найти или выполнить" className="grid h-11 w-11 place-items-center rounded-lg">
            <Search className="h-5 w-5" aria-hidden="true" />
          </button>
          <button type="button" onClick={() => setMenuPath(menuOpen ? null : location.pathname)} aria-label={menuOpen ? "Закрыть меню" : "Открыть меню"} aria-expanded={menuOpen} className="grid h-11 w-11 place-items-center rounded-lg">
            {menuOpen ? <X className="h-5 w-5" aria-hidden="true" /> : <Menu className="h-5 w-5" aria-hidden="true" />}
          </button>
        </div>
      </header>

      <aside
        className={`${menuOpen ? "block" : "hidden"} bg-rail lg:sticky lg:top-0 lg:block lg:h-screen lg:w-[264px] lg:flex-none lg:overflow-y-auto`}
      >
        <Rail onSearch={() => setPaletteOpen(true)} />
      </aside>

      <main className="min-w-0 flex-1 px-4 pb-14 pt-6 sm:px-8 lg:px-10 lg:pt-7">
        <Outlet />
      </main>

      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </div>
  )
}

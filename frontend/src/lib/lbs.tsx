// Shared, polled list of load balancers with a client-side requests/second estimate.
import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react"
import { api } from "@/lib/api"
import type { LoadBalancer } from "@/types"

interface LBState {
  lbs: LoadBalancer[]
  /** Requests per second per balancer id, from the change in total_requests between polls. */
  rps: Record<number, number>
  loaded: boolean
  refresh: () => Promise<void>
}

const LBContext = createContext<LBState | null>(null)

const POLL_MS = 5000

export function LBProvider({ children }: { children: React.ReactNode }) {
  const [lbs, setLbs] = useState<LoadBalancer[]>([])
  const [rps, setRps] = useState<Record<number, number>>({})
  const [loaded, setLoaded] = useState(false)
  const prev = useRef<{ at: number; totals: Record<number, number> } | null>(null)

  const refresh = useCallback(async () => {
    try {
      const data = (await api<LoadBalancer[]>("/load-balancers")) || []
      const now = Date.now()
      const totals: Record<number, number> = {}
      for (const lb of data) totals[lb.id] = lb.metrics?.total_requests ?? 0
      if (prev.current) {
        const seconds = (now - prev.current.at) / 1000
        const next: Record<number, number> = {}
        for (const lb of data) {
          const before = prev.current.totals[lb.id]
          next[lb.id] = before === undefined || seconds <= 0 ? 0 : Math.max(0, (totals[lb.id] - before) / seconds)
        }
        setRps(next)
      }
      prev.current = { at: now, totals }
      setLbs(data)
      setLoaded(true)
    } catch (err) {
      console.error(err)
    }
  }, [])

  useEffect(() => {
    const first = setTimeout(refresh, 0)
    const id = setInterval(refresh, POLL_MS)
    return () => {
      clearTimeout(first)
      clearInterval(id)
    }
  }, [refresh])

  return <LBContext.Provider value={{ lbs, rps, loaded, refresh }}>{children}</LBContext.Provider>
}

// eslint-disable-next-line react/only-export-components
export function useLBs(): LBState {
  const ctx = useContext(LBContext)
  if (!ctx) throw new Error("useLBs must be used inside <LBProvider>")
  return ctx
}

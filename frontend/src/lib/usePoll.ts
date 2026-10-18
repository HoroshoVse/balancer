import { useEffect, useState } from "react"

/**
 * Polls `load` every `every` ms while `enabled`; restarts when `key` changes.
 * `key` must capture everything `load` depends on.
 */
export function usePoll<T>(load: () => Promise<T>, initial: T, key: string, every = 5000, enabled = true): T {
  const [value, setValue] = useState<T>(initial)
  useEffect(() => {
    if (!enabled) return
    let alive = true
    const tick = () => load().then((v) => alive && setValue(v)).catch(() => {})
    const first = setTimeout(tick, 0)
    const id = setInterval(tick, every)
    return () => {
      alive = false
      clearTimeout(first)
      clearInterval(id)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, every, enabled])
  return value
}

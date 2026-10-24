// Helpers for the "who is hitting this balancer" card.

import { countryName, flag } from "@/lib/countries"
import type { ClientStat } from "@/types"

/** Where an address is from, in words. */
export const clientWhere = (c: Pick<ClientStat, "internal" | "country">) =>
  c.internal ? "внутренняя сеть" : c.country ? `${flag(c.country)} ${countryName(c.country)}`.trim() : "страна не определена"

/** How far back the server counts (ClientWindow in the engine), in minutes. */
export const CLIENT_WINDOW_MIN = 10

const entries = (list: string | undefined) => (list ?? "").split(/[\s,;]+/).filter(Boolean)

const v4 = (s: string): number | null => {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(s)
  if (!m || m.slice(1).some((p) => Number(p) > 255)) return null
  return ((Number(m[1]) << 24) | (Number(m[2]) << 16) | (Number(m[3]) << 8) | Number(m[4])) >>> 0
}

/** Whether `list` (addresses and subnets, one per line) already names `ip`. IPv6 is matched as written. */
export function listCovers(list: string | undefined, ip: string): boolean {
  const addr = v4(ip)
  return entries(list).some((entry) => {
    const [base, bits] = entry.split("/")
    if (addr === null || bits === undefined) return base.toLowerCase() === ip.toLowerCase() && (bits === undefined || /^\d+$/.test(bits))
    const net = v4(base)
    const n = Number(bits)
    if (net === null || !Number.isInteger(n) || n < 0 || n > 32) return false
    const mask = n === 0 ? 0 : (~0 << (32 - n)) >>> 0
    return ((addr & mask) >>> 0) === ((net & mask) >>> 0)
  })
}

/** The deny list with one more address on its own line. */
export const withDenied = (list: string | undefined, ip: string) => [...(list ?? "").split("\n").map((l) => l.trimEnd()).filter(Boolean), ip].join("\n")

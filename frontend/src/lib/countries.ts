// Country names (in Russian, from the browser's own data) and flags for the access rules.

const names = (() => {
  try {
    return new Intl.DisplayNames(["ru"], { type: "region", fallback: "none" })
  } catch {
    return null
  }
})()

export function countryName(code: string): string {
  try {
    return names?.of(code) ?? code
  } catch {
    return code
  }
}

/** The flag emoji of a two-letter code; Windows has no flag glyphs and shows the letters. */
export function flag(code: string): string {
  if (!/^[A-Z]{2}$/.test(code)) return ""
  return String.fromCodePoint(...[...code].map((c) => 0x1f1a5 + c.charCodeAt(0)))
}

/** Every two-letter code the browser knows a name for: used while the server has no country data yet. */
export function knownCodes(): string[] {
  const out: string[] = []
  for (let a = 65; a <= 90; a++) {
    for (let b = 65; b <= 90; b++) {
      const code = String.fromCharCode(a, b)
      if (code !== "ZZ" && names?.of(code)) out.push(code)
    }
  }
  return out
}

export const byName = (a: string, b: string) => countryName(a).localeCompare(countryName(b), "ru")

/** "Россия", "Россия и Беларусь", "Россия, Беларусь и ещё 3". */
export function countryList(codes: string[], max = 3): string {
  const sorted = [...codes].sort(byName).map(countryName)
  if (sorted.length <= max) return sorted.length > 1 ? `${sorted.slice(0, -1).join(", ")} и ${sorted[sorted.length - 1]}` : sorted[0] ?? ""
  return `${sorted.slice(0, max).join(", ")} и ещё ${sorted.length - max}`
}

export const splitCodes = (s: string | undefined) => (s ?? "").split(/[\s,;]+/).filter(Boolean).map((c) => c.toUpperCase())

interface AccessRules {
  access_mode?: string
  access_countries?: string
  access_allow?: string
  access_deny?: string
}

const hasLists = (lb: AccessRules) => !!(lb.access_allow?.trim() || lb.access_deny?.trim())

/** A short label for a balancer card: "только RU, BY", "без US, HK", "списки адресов". */
export function accessTag(lb: AccessRules): string | null {
  const codes = splitCodes(lb.access_countries)
  const short = codes.length > 3 ? `${codes.slice(0, 3).join(", ")} +${codes.length - 3}` : codes.join(", ")
  if (lb.access_mode === "allow" && codes.length) return `только ${short}`
  if (lb.access_mode === "deny" && codes.length) return `без ${short}`
  return hasLists(lb) ? "списки адресов" : null
}

/** The rule in words, for the settings summary. */
export function accessText(lb: AccessRules): string {
  const codes = splitCodes(lb.access_countries)
  let text = "всем"
  if (lb.access_mode === "allow" && codes.length) text = `только из: ${countryList(codes, 4)}`
  else if (lb.access_mode === "deny" && codes.length) text = `из всех, кроме: ${countryList(codes, 4)}`
  const extra: string[] = []
  const count = (list?: string) => (list ?? "").split(/[\s,;]+/).filter(Boolean).length
  if (count(lb.access_allow)) extra.push(`всегда пускать ${count(lb.access_allow)}`)
  if (count(lb.access_deny)) extra.push(`всегда блокировать ${count(lb.access_deny)}`)
  return extra.length ? `${text}; ${extra.join(", ")}` : text
}

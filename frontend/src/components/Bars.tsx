/**
 * Tiny bar chart: one bar per value, scaled to `scale` or the largest value.
 * With `slots`, missing older values show as grey stubs so bars keep their width.
 * Decorative; pair it with a number.
 */
export function Bars({
  values, color, height = 36, gap = 3, fade = true, colorOf, scale, slots = 0,
}: {
  values: number[]
  color: string
  height?: number
  gap?: number
  fade?: boolean
  colorOf?: (index: number) => string | undefined
  scale?: number
  slots?: number
}) {
  const max = scale ?? Math.max(...values, 0)
  return (
    <div aria-hidden="true" className="flex items-end" style={{ height, gap }}>
      {Array.from({ length: Math.max(0, slots - values.length) }, (_, i) => (
        <div key={`pad${i}`} className="flex-1 rounded-sm bg-line" style={{ height: "6%" }} />
      ))}
      {values.map((v, i) => (
        <div
          key={i}
          className={`flex-1 rounded-sm ${colorOf?.(i) ?? color}`}
          style={{
            height: max > 0 ? `${Math.min(100, Math.max(6, (v / max) * 100))}%` : "6%",
            opacity: fade ? (i === values.length - 1 ? 1 : 0.35 + 0.5 * (i / values.length)) : 1,
          }}
        />
      ))}
    </div>
  )
}

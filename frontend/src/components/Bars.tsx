/** Tiny bar chart: one bar per value, scaled to the largest. Decorative; pair it with a number. */
export function Bars({
  values, color, height = 36, gap = 3, fade = true, colorOf,
}: {
  values: number[]
  color: string
  height?: number
  gap?: number
  fade?: boolean
  colorOf?: (index: number) => string | undefined
}) {
  const max = Math.max(...values, 0)
  return (
    <div aria-hidden="true" className="flex items-end" style={{ height, gap }}>
      {values.map((v, i) => (
        <div
          key={i}
          className={`flex-1 rounded-sm ${colorOf?.(i) ?? color}`}
          style={{
            height: max > 0 ? `${Math.max(6, (v / max) * 100)}%` : "6%",
            opacity: fade ? (i === values.length - 1 ? 1 : 0.35 + 0.5 * (i / values.length)) : 1,
          }}
        />
      ))}
    </div>
  )
}

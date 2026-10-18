import type { FlowKind, FlowPath } from "@/lib/flow"

const waitingStroke: Record<Exclude<FlowKind, "live">, string> = { idle: "#B7C0CC", down: "#C2410C", off: "#D9DEE5" }

/**
 * Links on a light background: a pale pipe with requests running along it as
 * dots (denser for a bigger share), dashed lines for nodes that get nothing.
 */
export function FlowPaths({ paths }: { paths: FlowPath[] }) {
  // Traffic last, so waiting links that share its start don't cross over it
  const ordered = [...paths].sort((a, b) => Number(a.kind === "live") - Number(b.kind === "live"))
  return (
    <svg aria-hidden="true" className="pointer-events-none absolute inset-0 h-full w-full overflow-visible" fill="none">
      {ordered.map((p, i) =>
        p.kind === "live" ? (
          <g key={i}>
            <path d={p.d} stroke="#E7EBFF" strokeWidth={p.width} strokeLinecap="round" />
            <path
              d={p.d}
              stroke="#2E4BFF"
              strokeWidth={Math.max(3, Math.round(p.width / 2))}
              strokeLinecap="round"
              className={p.share >= 0.75 ? "flow-packets" : "flow-packets-sparse"}
            />
          </g>
        ) : (
          <path key={i} d={p.d} stroke={waitingStroke[p.kind]} strokeWidth={2} strokeDasharray="5 6" opacity={p.kind === "down" ? 0.7 : 1} />
        ),
      )}
    </svg>
  )
}

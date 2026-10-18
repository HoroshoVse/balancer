import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react"
import { activeNodes } from "@/lib/lbModel"
import type { BackendNode, LoadBalancer } from "@/types"

// Traffic diagrams: blocks are laid out with flex, the links between them are
// drawn in SVG from the blocks' measured positions, so they always meet.

export type Box = { l: number; t: number; r: number; b: number; cy: number }
export type BoxOf = (el: Element | null) => Box | null

/**
 * Calls `build` with element boxes relative to `container` after every render
 * and whenever the container or an element marked `data-flow` resizes.
 */
export function useMeasured<T>(container: RefObject<HTMLElement | null>, build: (box: BoxOf, width: number) => T, initial: T): T {
  const [value, setValue] = useState(initial)
  const measure = useRef<() => void>(() => {})

  useLayoutEffect(() => {
    measure.current = () => {
      const root = container.current
      if (!root) return
      const origin = root.getBoundingClientRect()
      const box: BoxOf = (el) => {
        if (!el) return null
        const r = el.getBoundingClientRect()
        return { l: r.left - origin.left, t: r.top - origin.top, r: r.right - origin.left, b: r.bottom - origin.top, cy: (r.top + r.bottom) / 2 - origin.top }
      }
      const next = build(box, root.clientWidth)
      setValue((prev) => (JSON.stringify(prev) === JSON.stringify(next) ? prev : next))
    }
    measure.current()
  })

  useEffect(() => {
    const root = container.current
    if (!root) return
    const ro = new ResizeObserver(() => measure.current())
    ro.observe(root)
    root.querySelectorAll("[data-flow]").forEach((el) => ro.observe(el))
    return () => ro.disconnect()
  })

  return value
}

/**
 * From the right middle of `a` to the left middle of `b`, curved when they differ
 * in height. `gap` keeps the line's round end off `a` when `a` is bare text.
 */
export function across(a: Box, b: Box, gap = 0) {
  const start = a.r + gap
  const mid = (start + b.l) / 2
  return `M ${start} ${a.cy} C ${mid} ${a.cy}, ${mid} ${b.cy}, ${b.l} ${b.cy}`
}

/** Stacked layouts: straight down at `x` from the bottom of `a` to the top of `b`. */
export function down(a: Box, x: number, b: Box) {
  return `M ${x} ${a.b} V ${b.t}`
}

/** Stacked layouts: down at `x` from the bottom of `a`, then right into the left middle of `b`. */
export function branch(a: Box, x: number, b: Box) {
  const r = Math.max(0, Math.min(14, b.l - x, b.cy - a.b))
  return `M ${x} ${a.b} V ${b.cy - r} Q ${x} ${b.cy}, ${x + r} ${b.cy} H ${b.l}`
}

export type FlowKind = "live" | "idle" | "down" | "off"
export type NodeFlow = { kind: FlowKind; share: number }

/**
 * How each node of a balancer takes part in the traffic right now: "live" with
 * its share of requests, idle (healthy but waiting), down or switched off.
 */
export function flowOf(lb: LoadBalancer, rps: number): (b: BackendNode) => NodeFlow {
  const live = lb.status !== "error" && rps > 0
  const active = activeNodes(lb)
  const total = active.reduce((sum, b) => sum + (b.metrics?.rps ?? 0), 0)
  return (b) => {
    if (!b.enabled) return { kind: "off", share: 0 }
    if (b.status === "DOWN") return { kind: "down", share: 0 }
    if (!live || !active.some((a) => a.id === b.id)) return { kind: "idle", share: 0 }
    // No per-node numbers yet: split evenly between the active nodes
    if (total === 0) return { kind: "live", share: 1 / active.length }
    const own = b.metrics?.rps ?? 0
    return own > 0 ? { kind: "live", share: own / total } : { kind: "idle", share: 0 }
  }
}

export type FlowPath = { d: string; kind: FlowKind; width: number; share: number }

/** Width of a live link: thinner for a smaller share of the traffic. */
export const shareWidth = (base: number, share: number) => Math.max(4, Math.round(base * (0.45 + 0.55 * share)))

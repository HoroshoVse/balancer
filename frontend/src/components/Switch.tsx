import { cn } from "@/lib/utils"

interface SwitchProps {
  checked: boolean
  onChange: (checked: boolean) => void
  disabled?: boolean
  label?: string
  /** Track colour when on */
  tone?: "cobalt" | "ok"
}

/** An on/off toggle: a real button with role="switch", so it is keyboard- and screen-reader-friendly. */
export function Switch({ checked, onChange, disabled, label, tone = "cobalt" }: SwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        "relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-cobalt/40 disabled:cursor-not-allowed disabled:opacity-50",
        checked ? (tone === "ok" ? "bg-ok" : "bg-cobalt") : "bg-line-strong",
      )}
    >
      <span className={cn("h-5 w-5 rounded-full bg-white shadow-sm transition-transform", checked ? "translate-x-[22px]" : "translate-x-0.5")} />
    </button>
  )
}

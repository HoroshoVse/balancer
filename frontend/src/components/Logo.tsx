/** Three streams merging into one — the Balancer mark. */
export function LogoMark({ className = "h-7 w-7", color = "currentColor" }: { className?: string; color?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke={color} strokeWidth={2} strokeLinecap="round" aria-hidden="true">
      <path d="M3 5c6 0 8 7 18 7M3 12h18M3 19c6 0 8-7 18-7" />
    </svg>
  )
}

export function Wordmark({ className = "" }: { className?: string }) {
  return <span className={`font-display font-semibold tracking-tight ${className}`}>balancer</span>
}

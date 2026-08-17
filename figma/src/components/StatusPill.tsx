type Status = "active" | "paused" | "completed" | "saved" | "live" | "connecting"

const configs: Record<Status, { label: string; color: string; bg: string; dot?: boolean }> = {
  active: { label: "Active", color: "#16A34A", bg: "#DCFCE7", dot: true },
  live: { label: "Live", color: "#16A34A", bg: "#DCFCE7", dot: true },
  connecting: { label: "Connecting", color: "#D97706", bg: "#FEF3C7", dot: true },
  paused: { label: "Paused", color: "#D97706", bg: "#FEF3C7" },
  completed: { label: "Completed", color: "#64748B", bg: "#F1F5F9" },
  saved: { label: "Saved", color: "#64748B", bg: "#F1F5F9" },
}

export default function StatusPill({ status }: { status: Status }) {
  const c = configs[status]
  return (
    <span
      className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-600"
      style={{ color: c.color, background: c.bg }}
    >
      {c.dot && <span className="pulse-dot" style={{ background: c.color }} />}
      {c.label}
    </span>
  )
}

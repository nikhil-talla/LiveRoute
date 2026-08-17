import { Link, useLocation } from "react-router-dom"
import Logo from "./Logo"

const user = { name: "Alex Rivera", initials: "AR" }

export default function Nav({ hasActiveTrip = false }: { hasActiveTrip?: boolean }) {
  const loc = useLocation()
  const is = (path: string) => loc.pathname === path || loc.pathname.startsWith(path + "/")

  return (
    <header className="sticky top-0 z-50 bg-white border-b border-[rgba(30,58,138,0.10)]">
      <div className="max-w-[1440px] mx-auto px-6 h-14 flex items-center gap-6">
        <Link to="/trips" className="shrink-0">
          <Logo size="sm" />
        </Link>

        <nav className="flex items-center gap-1 flex-1">
          <NavLink to="/trips" active={is("/trips") && !is("/trips/new") && !is("/trips/planner")}>
            My Trips
          </NavLink>
          <NavLink to="/trips/new" active={is("/trips/new") || is("/trips/planner")}>
            Planner
          </NavLink>
          <NavLink
            to="/live"
            active={is("/live")}
            disabled={!hasActiveTrip}
          >
            Live Trip
          </NavLink>
        </nav>

        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <div
              className="w-7 h-7 rounded-full bg-[#1D4ED8] text-white text-xs font-700 flex items-center justify-center"
            >
              {user.initials}
            </div>
            <div className="hidden sm:block text-right">
              <p className="text-xs font-600 text-[#0C1A3A] leading-none">{user.name}</p>
              <p className="text-[10px] text-[#64748B] leading-none mt-0.5">Signed in</p>
            </div>
          </div>
          <Link
            to="/"
            className="text-xs text-[#64748B] hover:text-[#0C1A3A] font-500 transition-colors"
          >
            Sign out
          </Link>
        </div>
      </div>
    </header>
  )
}

function NavLink({
  to,
  active,
  disabled,
  children,
}: {
  to: string
  active: boolean
  disabled?: boolean
  children: React.ReactNode
}) {
  if (disabled) {
    return (
      <span className="px-3 py-1.5 text-sm font-500 text-[#CBD5E1] cursor-not-allowed rounded-lg">
        {children}
      </span>
    )
  }
  return (
    <Link
      to={to}
      className={`px-3 py-1.5 text-sm font-500 rounded-lg transition-colors ${
        active
          ? "bg-[#EFF6FF] text-[#1D4ED8]"
          : "text-[#64748B] hover:text-[#0C1A3A] hover:bg-[#F8FAFC]"
      }`}
    >
      {children}
    </Link>
  )
}

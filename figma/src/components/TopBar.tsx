import liveRouteLogo from '../imports/LiveRouteLogo.png'
import type { View } from '../App'

interface TopBarProps {
  currentView: View
  onNavigate: (v: View) => void
  onSignOut: () => void
  hasActiveTrip?: boolean
}

export function Logo() {
  return (
    <div className="flex items-center gap-2">
      <img
        src={liveRouteLogo}
        alt="LiveRoute"
        className="w-7 h-7 rounded-lg object-cover"
      />
      <span className="text-base font-800 tracking-tight" style={{ color: '#0C1A3A' }}>
        LiveRoute
      </span>
    </div>
  )
}

const navItems: { id: View; label: string }[] = [
  { id: 'trips', label: 'My Trips' },
  { id: 'planner', label: 'Plan a Trip' },
  { id: 'live', label: 'Live Trip' },
]

export default function TopBar({ currentView, onNavigate, onSignOut, hasActiveTrip = false }: TopBarProps) {
  return (
    <header
      className="h-14 flex items-center px-6 border-b shrink-0 bg-white"
      style={{ borderColor: 'rgba(30,58,138,0.12)' }}
    >
      <button onClick={() => onNavigate('trips')} className="mr-8 cursor-pointer">
        <Logo />
      </button>

      <nav className="flex items-center gap-1 flex-1">
        {navItems.map((item) => {
          const active = currentView === item.id
          const isLive = item.id === 'live'
          const disabled = isLive && !hasActiveTrip
          return (
            <button
              key={item.id}
              onClick={() => !disabled && onNavigate(item.id)}
              disabled={disabled}
              className="px-3.5 py-2 rounded-lg text-sm transition-colors cursor-pointer"
              style={{
                fontWeight: active ? 600 : 500,
                color: disabled ? '#CBD5E1' : active ? '#1D4ED8' : '#64748B',
                background: active ? '#EFF6FF' : 'transparent',
                cursor: disabled ? 'not-allowed' : 'pointer',
              }}
              onMouseEnter={(e) => {
                if (!active && !disabled) e.currentTarget.style.background = '#F8FAFF'
              }}
              onMouseLeave={(e) => {
                if (!active) e.currentTarget.style.background = 'transparent'
              }}
            >
              {item.label}
              {isLive && !hasActiveTrip && (
                <span className="ml-1.5 text-xs" style={{ color: '#CBD5E1' }}>—</span>
              )}
            </button>
          )
        })}
      </nav>

      <div className="flex items-center gap-3">
        <div
          className="w-7 h-7 rounded-full flex items-center justify-center text-xs font-700 text-white"
          style={{ background: '#1D4ED8' }}
        >
          A
        </div>
        <div className="hidden sm:block text-right">
          <div className="text-xs font-600 leading-tight" style={{ color: '#0C1A3A' }}>Alex Chen</div>
          <div className="text-xs leading-tight" style={{ color: '#64748B' }}>Signed in</div>
        </div>
        <button
          onClick={onSignOut}
          className="ml-2 text-xs font-500 px-3 py-1.5 rounded-lg border transition-colors cursor-pointer"
          style={{
            color: '#64748B',
            borderColor: 'rgba(30,58,138,0.15)',
          }}
          onMouseEnter={(e) => { e.currentTarget.style.background = '#F8FAFF' }}
          onMouseLeave={(e) => { e.currentTarget.style.background = 'transparent' }}
        >
          Sign out
        </button>
      </div>
    </header>
  )
}

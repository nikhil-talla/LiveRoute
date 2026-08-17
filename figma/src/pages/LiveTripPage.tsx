import { useState } from 'react'
import TopBar from '../components/TopBar'
import type { View } from '../App'

interface LiveTripPageProps {
  onNavigate: (v: View) => void
  onSignOut: () => void
}

type ConnectionStatus = 'connected' | 'reconnecting' | 'disconnected'
type ActivityStatus = 'completed' | 'active' | 'upcoming' | 'skipped'
type ProposalState = 'none' | 'pending' | 'accepted' | 'rejected'

interface LiveActivity {
  id: string
  label: string
  time: string
  status: ActivityStatus
  arrival: string
}

const liveActivities: LiveActivity[] = [
  { id: 'a1', label: 'Ferry Building', time: '9:00 AM', status: 'completed', arrival: '9:04 AM' },
  { id: 'a2', label: 'SFMOMA', time: '10:30 AM', status: 'active', arrival: 'Planned 10:28 AM' },
  { id: 'a3', label: 'Tartine Bakery', time: '12:30 PM', status: 'upcoming', arrival: 'Planned 12:32 PM' },
  { id: 'a4', label: 'Dolores Park', time: '2:00 PM', status: 'upcoming', arrival: 'Planned 2:05 PM' },
]

const statusStyle: Record<ActivityStatus, { color: string; bg: string; label: string }> = {
  completed: { color: '#64748B', bg: '#F1F5F9', label: 'Completed' },
  active: { color: '#16A34A', bg: '#DCFCE7', label: 'Current stop' },
  upcoming: { color: '#64748B', bg: 'transparent', label: 'Upcoming' },
  skipped: { color: '#D97706', bg: '#FEF3C7', label: 'Skipped' },
}

const connStyle: Record<ConnectionStatus, { color: string; label: string }> = {
  connected: { color: '#16A34A', label: 'Live trip connected' },
  reconnecting: { color: '#D97706', label: 'Reconnecting…' },
  disconnected: { color: '#DC2626', label: 'Connection lost' },
}

export default function LiveTripPage({ onNavigate, onSignOut }: LiveTripPageProps) {
  const [connStatus, setConnStatus] = useState<ConnectionStatus>('connected')
  const [proposalState, setProposalState] = useState<ProposalState>('none')
  const [showStopConfirm, setShowStopConfirm] = useState(false)
  const [activities, setActivities] = useState<LiveActivity[]>(liveActivities)

  const markCompleted = (id: string) => {
    setActivities((prev) =>
      prev.map((a) =>
        a.id === id ? { ...a, status: 'completed' } :
        a.status === 'upcoming' && prev.findIndex(x => x.id === a.id) === prev.findIndex(x => x.id === id) + 1
          ? { ...a, status: 'active' }
          : a
      )
    )
  }

  const skipActivity = (id: string) => {
    setActivities((prev) =>
      prev.map((a) => a.id === id ? { ...a, status: 'skipped' } : a)
    )
  }

  const currentActivity = activities.find((a) => a.status === 'active')

  return (
    <div className="min-h-screen bg-white flex flex-col">
      <TopBar
        currentView="live"
        onNavigate={onNavigate}
        onSignOut={onSignOut}
        hasActiveTrip
      />

      {/* Active trip status bar */}
      <div
        className="flex items-center gap-4 px-6 py-2.5 border-b"
        style={{ background: '#F0FDF4', borderColor: 'rgba(22,163,74,0.15)' }}
      >
        <div className="flex items-center gap-2">
          <span className="relative flex w-2.5 h-2.5">
            <span className="ripple-ring absolute inset-0 rounded-full" style={{ background: connStyle[connStatus].color, opacity: 0.5 }} />
            <span className="pulse-dot w-2.5 h-2.5 rounded-full" style={{ background: connStyle[connStatus].color }} />
          </span>
          <span className="text-xs font-600" style={{ color: connStyle[connStatus].color }}>
            {connStyle[connStatus].label}
          </span>
        </div>
        <span className="text-xs" style={{ color: '#64748B' }}>Mission District Walk</span>
        <span className="text-xs" style={{ color: '#94A3B8' }}>·</span>
        <span className="text-xs" style={{ color: '#64748B' }}>Live location · ±18 m</span>

        {/* Conn status demo */}
        <div className="ml-auto flex gap-1.5">
          {(['connected', 'reconnecting', 'disconnected'] as ConnectionStatus[]).map((s) => (
            <button
              key={s}
              onClick={() => setConnStatus(s)}
              className="text-xs px-2 py-0.5 rounded cursor-pointer"
              style={{
                background: connStatus === s ? '#DBEAFE' : 'transparent',
                color: connStatus === s ? '#1D4ED8' : '#94A3B8',
              }}
            >
              {s}
            </button>
          ))}
        </div>
      </div>

      <div className="flex flex-1 overflow-hidden">
        {/* Left panel */}
        <div
          className="w-80 xl:w-96 flex flex-col border-r overflow-y-auto"
          style={{ borderColor: 'rgba(30,58,138,0.10)' }}
        >
          {/* Next activity */}
          {currentActivity && (
            <div className="px-5 py-5 border-b" style={{ borderColor: 'rgba(30,58,138,0.10)' }}>
              <div className="text-xs font-600 uppercase tracking-wide mb-3" style={{ color: '#64748B' }}>
                Current stop
              </div>
              <div
                className="rounded-xl border p-4"
                style={{ borderColor: 'rgba(22,163,74,0.20)', background: '#F0FDF4' }}
              >
                <div className="text-base font-700 mb-1" style={{ color: '#0C1A3A' }}>
                  {currentActivity.label}
                </div>
                <div className="text-xs mb-3" style={{ color: '#64748B' }}>
                  {currentActivity.arrival}
                </div>
                <div className="flex gap-2">
                  <button
                    onClick={() => markCompleted(currentActivity.id)}
                    className="flex-1 py-2 rounded-lg text-xs font-600 text-white cursor-pointer"
                    style={{ background: '#16A34A' }}
                  >
                    Mark completed
                  </button>
                  <button
                    onClick={() => skipActivity(currentActivity.id)}
                    className="px-3 py-2 rounded-lg text-xs font-500 border cursor-pointer"
                    style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#64748B' }}
                  >
                    Skip
                  </button>
                </div>
              </div>
            </div>
          )}

          {/* Remaining itinerary */}
          <div className="px-5 py-4 flex-1">
            <div className="text-xs font-600 uppercase tracking-wide mb-3" style={{ color: '#64748B' }}>
              Remaining itinerary
            </div>
            <div className="space-y-0">
              {activities.map((act, i) => {
                const st = statusStyle[act.status]
                return (
                  <div key={act.id} className="flex gap-3">
                    <div className="flex flex-col items-center">
                      <div
                        className="w-5 h-5 rounded-full flex items-center justify-center text-xs font-700 shrink-0 mt-0.5"
                        style={{
                          background: act.status === 'completed' ? '#DBEAFE' : act.status === 'active' ? '#16A34A' : '#F1F5F9',
                          color: act.status === 'completed' ? '#1D4ED8' : act.status === 'active' ? 'white' : '#94A3B8',
                        }}
                      >
                        {act.status === 'completed' ? (
                          <svg width="9" height="9" viewBox="0 0 9 9" fill="none">
                            <path d="M1.5 4.5l2 2L7.5 2" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
                          </svg>
                        ) : act.status === 'skipped' ? '—' : i + 1}
                      </div>
                      {i < activities.length - 1 && (
                        <div
                          className="w-px flex-1 my-1"
                          style={{ background: act.status === 'completed' ? '#DBEAFE' : 'rgba(30,58,138,0.10)', minHeight: 20 }}
                        />
                      )}
                    </div>
                    <div className={`pb-4 flex-1 ${i === activities.length - 1 ? 'pb-0' : ''}`}>
                      <div className="flex items-center justify-between">
                        <div
                          className="text-sm font-500"
                          style={{ color: act.status === 'upcoming' ? '#64748B' : act.status === 'completed' ? '#94A3B8' : '#0C1A3A' }}
                        >
                          {act.label}
                        </div>
                        {act.status !== 'upcoming' && (
                          <span
                            className="text-xs font-600 px-2 py-0.5 rounded-lg"
                            style={{ background: st.bg || '#F1F5F9', color: st.color }}
                          >
                            {st.label}
                          </span>
                        )}
                      </div>
                      <div className="text-xs mt-0.5" style={{ color: '#94A3B8' }}>{act.time}</div>
                    </div>
                  </div>
                )
              })}
            </div>
          </div>

          {/* Proposal panel */}
          {proposalState === 'pending' && (
            <div
              className="mx-5 mb-4 rounded-xl border p-4"
              style={{ borderColor: 'rgba(217,119,6,0.25)', background: '#FFFBEB' }}
            >
              <div className="text-xs font-600 uppercase tracking-wide mb-1" style={{ color: '#D97706' }}>
                Suggested change
              </div>
              <div className="text-sm font-700 mb-2" style={{ color: '#0C1A3A' }}>
                Review the proposed plan
              </div>
              <p className="text-xs mb-3 leading-relaxed" style={{ color: '#64748B' }}>
                Traffic ahead affects stop #3. This suggestion keeps 2 activities in place and adjusts the remaining route.
              </p>
              <p className="text-xs mb-3 italic" style={{ color: '#94A3B8' }}>
                Your current plan remains unchanged until you accept this suggestion.
              </p>
              <div className="flex gap-2">
                <button
                  onClick={() => setProposalState('accepted')}
                  className="flex-1 py-2 rounded-lg text-xs font-600 text-white cursor-pointer"
                  style={{ background: '#1D4ED8' }}
                >
                  Accept suggestion
                </button>
                <button
                  onClick={() => setProposalState('rejected')}
                  className="flex-1 py-2 rounded-lg text-xs font-500 border cursor-pointer"
                  style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#64748B' }}
                >
                  Keep current plan
                </button>
              </div>
            </div>
          )}

          {proposalState === 'accepted' && (
            <div
              className="mx-5 mb-4 rounded-xl border p-4"
              style={{ borderColor: 'rgba(22,163,74,0.20)', background: '#F0FDF4' }}
            >
              <div className="text-xs font-600" style={{ color: '#16A34A' }}>
                ✓ Suggestion accepted — route updated
              </div>
            </div>
          )}

          {proposalState === 'rejected' && (
            <div
              className="mx-5 mb-4 rounded-xl border p-4"
              style={{ borderColor: 'rgba(30,58,138,0.12)', background: '#EFF6FF' }}
            >
              <div className="text-xs font-600" style={{ color: '#64748B' }}>
                Current plan kept — suggestion dismissed
              </div>
            </div>
          )}

          {/* Demo: trigger proposal */}
          {proposalState === 'none' && (
            <div className="px-5 pb-4">
              <button
                onClick={() => setProposalState('pending')}
                className="w-full py-2 rounded-xl text-xs font-500 border cursor-pointer transition-colors"
                style={{ borderColor: 'rgba(217,119,6,0.25)', color: '#D97706', background: '#FFFBEB' }}
              >
                Demo: trigger a suggestion
              </button>
            </div>
          )}

          {/* Stats row */}
          <div
            className="flex items-center gap-0 border-t"
            style={{ borderColor: 'rgba(30,58,138,0.10)' }}
          >
            {[
              { label: 'Remaining', value: '2.4 mi' },
              { label: 'ETA final stop', value: '2:05 PM' },
              { label: 'Duration left', value: '1h 37m' },
            ].map((stat, i) => (
              <div
                key={stat.label}
                className="flex-1 px-4 py-3 text-center border-r last:border-r-0"
                style={{ borderColor: 'rgba(30,58,138,0.08)' }}
              >
                <div className="text-xs font-700" style={{ color: '#0C1A3A' }}>{stat.value}</div>
                <div className="text-xs mt-0.5" style={{ color: '#94A3B8' }}>{stat.label}</div>
              </div>
            ))}
          </div>

          {/* Stop trip */}
          <div className="px-5 py-4 border-t" style={{ borderColor: 'rgba(30,58,138,0.10)' }}>
            {!showStopConfirm ? (
              <button
                onClick={() => setShowStopConfirm(true)}
                className="w-full py-2.5 rounded-xl text-sm font-600 border cursor-pointer transition-colors"
                style={{ borderColor: 'rgba(220,38,38,0.20)', color: '#DC2626', background: '#FFF5F5' }}
              >
                Stop trip
              </button>
            ) : (
              <div
                className="rounded-xl border p-4"
                style={{ borderColor: 'rgba(220,38,38,0.20)', background: '#FFF5F5' }}
              >
                <div className="text-sm font-600 mb-1" style={{ color: '#0C1A3A' }}>
                  Stop this trip?
                </div>
                <p className="text-xs mb-3" style={{ color: '#64748B' }}>
                  Your saved itinerary will be preserved. Live state will be reset.
                </p>
                <div className="flex gap-2">
                  <button
                    onClick={() => onNavigate('trips')}
                    className="flex-1 py-2 rounded-lg text-xs font-600 text-white cursor-pointer"
                    style={{ background: '#DC2626' }}
                  >
                    Stop trip
                  </button>
                  <button
                    onClick={() => setShowStopConfirm(false)}
                    className="flex-1 py-2 rounded-lg text-xs font-500 border cursor-pointer"
                    style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#64748B' }}
                  >
                    Keep going
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>

        {/* Map */}
        <div className="flex-1 relative overflow-hidden" style={{ background: '#EFF6FF' }}>
          <svg width="100%" height="100%" viewBox="0 0 800 700" preserveAspectRatio="xMidYMid slice" fill="none">
            <rect width="800" height="700" fill="#EFF6FF" />
            {[80,160,240,320,400,480,560,640,720].map(x=>(
              <line key={x} x1={x} y1="0" x2={x} y2="700" stroke="rgba(30,58,138,0.04)" strokeWidth="1" />
            ))}
            {[70,140,210,280,350,420,490,560,630].map(y=>(
              <line key={y} x1="0" y1={y} x2="800" y2={y} stroke="rgba(30,58,138,0.04)" strokeWidth="1" />
            ))}
            <path d="M0 420 H800" stroke="white" strokeWidth="14" />
            <path d="M0 280 H800" stroke="white" strokeWidth="9" />
            <path d="M0 560 H800" stroke="white" strokeWidth="9" />
            <path d="M300 0 V700" stroke="white" strokeWidth="14" />
            <path d="M520 0 V700" stroke="white" strokeWidth="9" />
            <path d="M150 0 V700" stroke="white" strokeWidth="7" />
            <path d="M680 0 V700" stroke="white" strokeWidth="7" />
            {/* Accepted route */}
            <path
              d="M300 620 V420 H520 V280 H300 V160"
              stroke="#DBEAFE"
              strokeWidth="10"
              strokeLinecap="round"
              strokeLinejoin="round"
              fill="none"
            />
            <path
              d="M300 620 V420 H520 V280 H300 V160"
              stroke="#1D4ED8"
              strokeWidth="3"
              strokeLinecap="round"
              strokeLinejoin="round"
              fill="none"
            />
            {/* Proposed route (dashed) */}
            {proposalState === 'pending' && (
              <path
                d="M300 620 V420 H150 V200 H300 V160"
                stroke="#D97706"
                strokeWidth="2.5"
                strokeDasharray="8 5"
                strokeLinecap="round"
                strokeLinejoin="round"
                fill="none"
                opacity="0.7"
              />
            )}
            {/* Activity markers */}
            {[
              { cx: 300, cy: 620, i: 1, status: 'completed' },
              { cx: 520, cy: 280, i: 2, status: 'active' },
              { cx: 300, cy: 280, i: 3, status: 'upcoming' },
              { cx: 300, cy: 160, i: 4, status: 'upcoming' },
            ].map((pt) => (
              <g key={pt.i}>
                <circle cx={pt.cx} cy={pt.cy} r="16"
                  fill="white"
                  style={{ filter: 'drop-shadow(0 2px 8px rgba(30,58,138,0.15))' }}
                />
                <circle cx={pt.cx} cy={pt.cy} r="13"
                  fill={pt.status === 'completed' ? '#DBEAFE' : pt.status === 'active' ? '#1D4ED8' : '#F1F5F9'}
                />
                <text x={pt.cx} y={pt.cy + 4} textAnchor="middle" fontSize="9" fontWeight="700"
                  fill={pt.status === 'completed' ? '#1D4ED8' : pt.status === 'active' ? 'white' : '#94A3B8'}
                >
                  {pt.status === 'completed' ? '✓' : pt.i}
                </text>
              </g>
            ))}
            {/* Current location marker */}
            <circle cx="300" cy="480" r="10" fill="white" opacity="0.6" />
            <circle cx="300" cy="480" r="8" fill="#1D4ED8" />
            <circle cx="300" cy="480" r="4" fill="white" />
          </svg>

          {/* Map overlays */}
          <div className="absolute top-4 right-4 flex flex-col gap-2">
            {proposalState === 'pending' && (
              <div
                className="rounded-xl border p-3 text-xs"
                style={{ background: 'white', borderColor: 'rgba(217,119,6,0.25)', boxShadow: '0 2px 12px rgba(0,0,0,0.08)' }}
              >
                <div className="font-600 mb-0.5" style={{ color: '#D97706' }}>Suggested route</div>
                <div style={{ color: '#64748B' }}>Dashed = proposed · Solid = current</div>
              </div>
            )}
            <div
              className="rounded-xl border p-2 text-xs flex flex-col gap-1"
              style={{ background: 'white', borderColor: 'rgba(30,58,138,0.12)', boxShadow: '0 2px 8px rgba(30,58,138,0.07)' }}
            >
              <button className="px-2 py-1 rounded cursor-pointer text-sm font-500" style={{ color: '#64748B' }}>+</button>
              <div style={{ borderTop: '1px solid rgba(30,58,138,0.10)' }} />
              <button className="px-2 py-1 rounded cursor-pointer text-sm font-500" style={{ color: '#64748B' }}>−</button>
            </div>
          </div>

          {/* GPS status */}
          <div
            className="absolute bottom-4 left-4 flex items-center gap-2 px-3 py-2 rounded-xl text-xs"
            style={{ background: 'white', boxShadow: '0 2px 12px rgba(30,58,138,0.10)', border: '1px solid rgba(30,58,138,0.10)' }}
          >
            <span className="pulse-dot w-2 h-2 rounded-full" style={{ background: '#1D4ED8' }} />
            <span style={{ color: '#0C1A3A', fontWeight: 500 }}>Live location · ±18 m</span>
          </div>
        </div>
      </div>
    </div>
  )
}

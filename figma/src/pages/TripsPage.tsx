import { useState } from 'react'
import TopBar from '../components/TopBar'
import type { View, TripData } from '../App'

interface TripsPageProps {
  onNavigate: (v: View, trip?: TripData) => void
  onSignOut: () => void
}

type PageState = 'loaded' | 'loading' | 'empty' | 'error'

const savedTrips: TripData[] = [
  { id: 't1', name: 'SF Arts Day', status: 'saved', lastUpdated: 'Aug 14', activityCount: 4 },
  { id: 't2', name: 'Berkeley Bookstores', status: 'saved', lastUpdated: 'Aug 10', activityCount: 3 },
  { id: 't3', name: 'Marin Headlands Hike', status: 'saved', lastUpdated: 'Jul 28', activityCount: 5 },
]

const activeTrip: TripData = {
  id: 'active1',
  name: 'Mission District Walk',
  status: 'active',
  lastUpdated: 'Today, 11:04 AM',
  activityCount: 4,
}

export default function TripsPage({ onNavigate, onSignOut }: TripsPageProps) {
  const [pageState, setPageState] = useState<PageState>('loaded')
  const [showActive] = useState(true)

  return (
    <div className="min-h-screen bg-white flex flex-col">
      <TopBar
        currentView="trips"
        onNavigate={onNavigate}
        onSignOut={onSignOut}
        hasActiveTrip={showActive}
      />

      <div
        className="flex-1"
        style={{ background: 'linear-gradient(180deg, #EFF6FF 0%, #FFFFFF 80px)' }}
      >
        <div className="max-w-3xl mx-auto px-6 pt-10 pb-16">
          {/* Page header */}
          <div className="flex items-start justify-between mb-8">
            <div>
              <div className="text-xs font-600 uppercase tracking-widest mb-1" style={{ color: '#1D4ED8' }}>
                Your itineraries
              </div>
              <h1 className="text-2xl font-800" style={{ color: '#0C1A3A' }}>Trips</h1>
              <p className="mt-1 text-sm" style={{ color: '#64748B' }}>
                Open a saved plan, or start shaping a new day.
              </p>
            </div>
            <button
              onClick={() => onNavigate('planner')}
              className="flex items-center gap-2 px-4 py-2.5 rounded-xl text-sm font-600 text-white hover:opacity-90 transition-opacity cursor-pointer"
              style={{ background: '#1D4ED8' }}
            >
              <svg width="13" height="13" viewBox="0 0 13 13" fill="none">
                <path d="M6.5 1v11M1 6.5h11" stroke="white" strokeWidth="1.8" strokeLinecap="round" />
              </svg>
              New trip
            </button>
          </div>

          {/* State switcher (demo) */}
          <div className="flex items-center gap-2 mb-8">
            <span className="text-xs" style={{ color: '#94A3B8' }}>Demo state:</span>
            {(['loaded', 'empty', 'loading', 'error'] as PageState[]).map((s) => (
              <button
                key={s}
                onClick={() => setPageState(s)}
                className="px-2.5 py-1 rounded-lg text-xs font-500 cursor-pointer border transition-colors"
                style={{
                  background: pageState === s ? '#EFF6FF' : 'transparent',
                  color: pageState === s ? '#1D4ED8' : '#94A3B8',
                  borderColor: pageState === s ? '#DBEAFE' : 'rgba(30,58,138,0.10)',
                  fontWeight: pageState === s ? 600 : 400,
                }}
              >
                {s}
              </button>
            ))}
          </div>

          {/* Loading state */}
          {pageState === 'loading' && (
            <div className="space-y-3">
              {[1, 2, 3].map((i) => (
                <div
                  key={i}
                  className="h-20 rounded-2xl animate-pulse"
                  style={{ background: '#F1F5F9' }}
                />
              ))}
            </div>
          )}

          {/* Error state */}
          {pageState === 'error' && (
            <div
              className="rounded-2xl border p-8 text-center"
              style={{ borderColor: 'rgba(30,58,138,0.12)', boxShadow: '0 4px 24px rgba(30,58,138,0.06)' }}
            >
              <div
                className="w-10 h-10 rounded-xl flex items-center justify-center mx-auto mb-4"
                style={{ background: '#FEE2E2' }}
              >
                <svg width="18" height="18" viewBox="0 0 18 18" fill="none">
                  <circle cx="9" cy="9" r="7.5" stroke="#DC2626" strokeWidth="1.5" />
                  <line x1="9" y1="5" x2="9" y2="10" stroke="#DC2626" strokeWidth="1.5" strokeLinecap="round" />
                  <circle cx="9" cy="12.5" r="0.75" fill="#DC2626" />
                </svg>
              </div>
              <div className="text-sm font-600 mb-1" style={{ color: '#0C1A3A' }}>
                We couldn't load your trips.
              </div>
              <div className="text-sm mb-5" style={{ color: '#64748B' }}>
                Connection interrupted. Check your network and try again.
              </div>
              <button
                onClick={() => setPageState('loaded')}
                className="px-5 py-2.5 rounded-xl text-sm font-600 text-white cursor-pointer hover:opacity-90 transition-opacity"
                style={{ background: '#1D4ED8' }}
              >
                Try again
              </button>
            </div>
          )}

          {/* Empty state */}
          {pageState === 'empty' && (
            <div
              className="rounded-2xl border p-12 text-center"
              style={{ borderColor: 'rgba(30,58,138,0.12)' }}
            >
              <div
                className="w-14 h-14 rounded-2xl flex items-center justify-center mx-auto mb-5"
                style={{ background: '#EFF6FF' }}
              >
                <svg width="26" height="26" viewBox="0 0 26 26" fill="none">
                  <circle cx="13" cy="9" r="4" stroke="#1D4ED8" strokeWidth="1.6" />
                  <path d="M5 22c0-4 3.5-7 8-7s8 3 8 7" stroke="#1D4ED8" strokeWidth="1.6" strokeLinecap="round" />
                  <path d="M18 5l3 3M21 5l-3 3" stroke="#DBEAFE" strokeWidth="1.4" strokeLinecap="round" />
                </svg>
              </div>
              <div className="text-base font-700 mb-2" style={{ color: '#0C1A3A' }}>
                No saved trips yet
              </div>
              <p className="text-sm mb-6 max-w-xs mx-auto" style={{ color: '#64748B' }}>
                Plan your first trip and save it for whenever you're ready to go.
              </p>
              <button
                onClick={() => onNavigate('planner')}
                className="px-5 py-2.5 rounded-xl text-sm font-600 text-white cursor-pointer hover:opacity-90 transition-opacity"
                style={{ background: '#1D4ED8' }}
              >
                Plan your first trip
              </button>
            </div>
          )}

          {/* Loaded state */}
          {pageState === 'loaded' && (
            <div className="space-y-6">
              {/* Active trip */}
              {showActive && (
                <div>
                  <div className="text-xs font-600 uppercase tracking-widest mb-3" style={{ color: '#64748B' }}>
                    In progress
                  </div>
                  <div
                    className="rounded-2xl border p-5 cursor-pointer hover:shadow-md transition-shadow"
                    style={{
                      borderColor: 'rgba(29,78,216,0.2)',
                      background: '#F0F5FF',
                      boxShadow: '0 4px 24px rgba(29,78,216,0.08)',
                    }}
                    onClick={() => onNavigate('live')}
                  >
                    <div className="flex items-start justify-between">
                      <div>
                        <div className="text-xs font-600 mb-1" style={{ color: '#1D4ED8' }}>Active trip</div>
                        <div className="text-base font-700 mb-0.5" style={{ color: '#0C1A3A' }}>
                          {activeTrip.name}
                        </div>
                        <div className="text-xs" style={{ color: '#64748B' }}>
                          Started {activeTrip.lastUpdated} · {activeTrip.activityCount} stops
                        </div>
                      </div>
                      <div className="flex flex-col items-end gap-2">
                        <span
                          className="flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-xs font-600"
                          style={{ background: '#DCFCE7', color: '#16A34A' }}
                        >
                          <span className="relative flex w-2 h-2">
                            <span className="ripple-ring absolute inset-0 rounded-full" style={{ background: '#16A34A', opacity: 0.5 }} />
                            <span className="pulse-dot w-2 h-2 rounded-full" style={{ background: '#16A34A' }} />
                          </span>
                          Active
                        </span>
                        <button
                          className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-600 text-white cursor-pointer"
                          style={{ background: '#1D4ED8' }}
                        >
                          Open live view
                          <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                            <path d="M2 6h8M6 2l4 4-4 4" stroke="white" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
                          </svg>
                        </button>
                      </div>
                    </div>
                  </div>
                </div>
              )}

              {/* Saved trips */}
              <div>
                <div className="text-xs font-600 uppercase tracking-widest mb-3" style={{ color: '#64748B' }}>
                  Saved trips
                </div>
                <div
                  className="rounded-2xl border overflow-hidden bg-white"
                  style={{
                    borderColor: 'rgba(30,58,138,0.12)',
                    boxShadow: '0 4px 24px rgba(30,58,138,0.06)',
                  }}
                >
                  {savedTrips.map((trip, i) => (
                    <div
                      key={trip.id}
                      className="flex items-center gap-4 px-5 py-4 cursor-pointer hover:bg-slate-50 transition-colors"
                      style={{
                        borderTop: i > 0 ? '1px solid rgba(30,58,138,0.08)' : 'none',
                      }}
                      onClick={() => onNavigate('planner', trip)}
                    >
                      <div
                        className="w-9 h-9 rounded-xl flex items-center justify-center shrink-0"
                        style={{ background: '#EFF6FF' }}
                      >
                        <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                          <circle cx="8" cy="6" r="3" stroke="#1D4ED8" strokeWidth="1.4" />
                          <path d="M3 14c0-3 2.5-5 5-5s5 2 5 5" stroke="#1D4ED8" strokeWidth="1.4" strokeLinecap="round" />
                        </svg>
                      </div>
                      <div className="flex-1 min-w-0">
                        <div className="text-sm font-600 truncate" style={{ color: '#0C1A3A' }}>
                          {trip.name}
                        </div>
                        <div className="text-xs mt-0.5" style={{ color: '#64748B' }}>
                          {trip.activityCount} stops · Saved {trip.lastUpdated}
                        </div>
                      </div>
                      <span
                        className="text-xs font-500 px-2.5 py-1 rounded-lg shrink-0"
                        style={{ background: '#F1F5F9', color: '#64748B' }}
                      >
                        Saved
                      </span>
                      <svg width="14" height="14" viewBox="0 0 14 14" fill="none" style={{ color: '#CBD5E1' }}>
                        <path d="M5 3l4 4-4 4" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
                      </svg>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

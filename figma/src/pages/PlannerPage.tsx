import { useState } from 'react'
import TopBar from '../components/TopBar'
import type { View, TripData } from '../App'

interface PlannerPageProps {
  trip: TripData | null
  onNavigate: (v: View, trip?: TripData) => void
  onSignOut: () => void
}

type SearchState = 'idle' | 'searching' | 'temp-selected' | 'durable'
type ActivityClass = 'flexible' | 'fixed'
type TravelMode = 'driving' | 'walking'

interface Activity {
  id: string
  label: string
  address: string
  time: string
  duration: number
  travelMode: TravelMode
  activityClass: ActivityClass
  scheduled: boolean
  expanded: boolean
}

const defaultActivities: Activity[] = [
  {
    id: 'a1',
    label: 'Ferry Building Marketplace',
    address: '1 Ferry Building, San Francisco, CA',
    time: '9:00 AM',
    duration: 60,
    travelMode: 'walking',
    activityClass: 'flexible',
    scheduled: true,
    expanded: false,
  },
  {
    id: 'a2',
    label: 'SFMOMA',
    address: '151 3rd St, San Francisco, CA',
    time: '10:30 AM',
    duration: 90,
    travelMode: 'driving',
    activityClass: 'fixed',
    scheduled: true,
    expanded: false,
  },
]

export default function PlannerPage({ trip, onNavigate, onSignOut }: PlannerPageProps) {
  const isNew = !trip
  const [tripName, setTripName] = useState(trip?.name ?? '')
  const [searchState, setSearchState] = useState<SearchState>('idle')
  const [searchQuery, setSearchQuery] = useState('')
  const [activities, setActivities] = useState<Activity[]>(isNew ? [] : defaultActivities)
  const [saveState, setSaveState] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle')
  const [expandedId, setExpandedId] = useState<string | null>(null)

  const mockResults = [
    { name: 'Tartine Bakery', address: '600 Guerrero St, San Francisco, CA' },
    { name: 'Dolores Park', address: 'Dolores St & 19th St, San Francisco, CA' },
    { name: 'Mission Dolores Basilica', address: '3321 16th St, San Francisco, CA' },
  ]

  const handleSave = () => {
    setSaveState('saving')
    setTimeout(() => setSaveState('saved'), 1000)
  }

  const handleGo = () => {
    onNavigate('live')
  }

  const canGo = activities.length > 0 && activities.every((a) => a.scheduled)

  const removeActivity = (id: string) => {
    setActivities((prev) => prev.filter((a) => a.id !== id))
  }

  const toggleExpand = (id: string) => {
    setExpandedId(expandedId === id ? null : id)
  }

  const addTempActivity = (name: string, address: string) => {
    const newActivity: Activity = {
      id: `a${Date.now()}`,
      label: name,
      address,
      time: 'Unscheduled',
      duration: 60,
      travelMode: 'driving',
      activityClass: 'flexible',
      scheduled: false,
      expanded: false,
    }
    setActivities((prev) => [...prev, newActivity])
    setSearchState('idle')
    setSearchQuery('')
  }

  return (
    <div className="min-h-screen bg-white flex flex-col">
      <TopBar
        currentView="planner"
        onNavigate={onNavigate}
        onSignOut={onSignOut}
        hasActiveTrip={false}
      />

      <div
        className="flex-1 grid lg:grid-cols-2"
        style={{ minHeight: 0 }}
      >
        {/* Left: planner */}
        <div
          className="overflow-y-auto border-r"
          style={{ borderColor: 'rgba(30,58,138,0.10)' }}
        >
          <div className="px-6 pt-8 pb-4">
            <button
              onClick={() => onNavigate('trips')}
              className="flex items-center gap-1.5 text-xs mb-6 cursor-pointer"
              style={{ color: '#64748B' }}
            >
              <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                <path d="M8 2L4 6l4 4" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
              Back to Trips
            </button>

            <div className="text-xs font-600 uppercase tracking-widest mb-1" style={{ color: '#1D4ED8' }}>
              {isNew ? 'New itinerary' : 'Edit itinerary'}
            </div>
            <h1 className="text-xl font-800 mb-1" style={{ color: '#0C1A3A' }}>
              {isNew ? 'Plan a new trip' : trip?.name}
            </h1>
            <p className="text-sm mb-6" style={{ color: '#64748B' }}>
              Start with a name and add your stops.
            </p>

            {/* Trip name */}
            <div className="mb-5">
              <label className="block text-xs font-600 mb-1.5" style={{ color: '#64748B' }}>
                Trip name
              </label>
              <input
                type="text"
                placeholder="e.g. Saturday in the Mission"
                value={tripName}
                onChange={(e) => setTripName(e.target.value)}
                className="w-full px-4 py-3 rounded-xl border text-sm outline-none transition-all"
                style={{
                  borderColor: 'rgba(30,58,138,0.18)',
                  color: '#0C1A3A',
                  background: '#F8FAFF',
                }}
                onFocus={(e) => (e.currentTarget.style.borderColor = '#1D4ED8')}
                onBlur={(e) => (e.currentTarget.style.borderColor = 'rgba(30,58,138,0.18)')}
              />
            </div>

            {/* Display-only date/time note */}
            <div
              className="flex items-start gap-2.5 px-3.5 py-3 rounded-xl mb-6 text-xs"
              style={{ background: '#EFF6FF', color: '#64748B', border: '1px solid rgba(30,58,138,0.10)' }}
            >
              <svg width="13" height="13" viewBox="0 0 13 13" fill="none" className="mt-0.5 shrink-0">
                <circle cx="6.5" cy="6.5" r="5.5" stroke="#94A3B8" strokeWidth="1.3" />
                <line x1="6.5" y1="3.5" x2="6.5" y2="7" stroke="#94A3B8" strokeWidth="1.3" strokeLinecap="round" />
                <circle cx="6.5" cy="9" r="0.6" fill="#94A3B8" />
              </svg>
              Adding a date and time is optional — it's for planning reference only and doesn't activate the trip.
            </div>
          </div>

          {/* Activity list */}
          <div className="px-6 pb-4">
            <div className="flex items-center justify-between mb-3">
              <span className="text-xs font-600 uppercase tracking-widest" style={{ color: '#64748B' }}>
                Stops ({activities.length})
              </span>
            </div>

            {activities.length === 0 && searchState === 'idle' && (
              <div
                className="rounded-2xl border border-dashed p-8 text-center mb-4"
                style={{ borderColor: 'rgba(30,58,138,0.20)' }}
              >
                <div className="text-sm font-500 mb-1" style={{ color: '#94A3B8' }}>No stops yet</div>
                <div className="text-xs" style={{ color: '#CBD5E1' }}>
                  Search below to add your first stop.
                </div>
              </div>
            )}

            <div className="space-y-2 mb-4">
              {activities.map((act, i) => (
                <div
                  key={act.id}
                  className="rounded-xl border bg-white overflow-hidden"
                  style={{
                    borderColor: 'rgba(30,58,138,0.12)',
                    boxShadow: '0 2px 12px rgba(30,58,138,0.05)',
                  }}
                >
                  {/* Row */}
                  <div className="flex items-center gap-3 px-4 py-3">
                    <div
                      className="w-6 h-6 rounded-lg flex items-center justify-center text-xs font-700 shrink-0"
                      style={{ background: '#DBEAFE', color: '#1D4ED8' }}
                    >
                      {i + 1}
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="text-sm font-600 truncate" style={{ color: '#0C1A3A' }}>
                        {act.label}
                      </div>
                      <div className="text-xs truncate mt-0.5" style={{ color: '#64748B' }}>
                        {act.scheduled ? act.time : (
                          <span style={{ color: '#D97706' }}>Unscheduled</span>
                        )}
                        {' · '}{act.duration} min
                      </div>
                    </div>
                    <div className="flex items-center gap-1.5 shrink-0">
                      <button
                        onClick={() => toggleExpand(act.id)}
                        className="p-1.5 rounded-lg cursor-pointer transition-colors"
                        style={{ color: '#64748B' }}
                        onMouseEnter={(e) => (e.currentTarget.style.background = '#EFF6FF')}
                        onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                      >
                        <svg width="13" height="13" viewBox="0 0 13 13" fill="none">
                          <path
                            d={expandedId === act.id ? 'M2 8l4.5-4 4.5 4' : 'M2 5l4.5 4 4.5-4'}
                            stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round"
                          />
                        </svg>
                      </button>
                      <button
                        onClick={() => removeActivity(act.id)}
                        className="p-1.5 rounded-lg cursor-pointer transition-colors"
                        style={{ color: '#94A3B8' }}
                        onMouseEnter={(e) => (e.currentTarget.style.color = '#DC2626')}
                        onMouseLeave={(e) => (e.currentTarget.style.color = '#94A3B8')}
                      >
                        <svg width="13" height="13" viewBox="0 0 13 13" fill="none">
                          <path d="M2 2l9 9M11 2L2 11" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
                        </svg>
                      </button>
                    </div>
                  </div>

                  {/* Expanded editor */}
                  {expandedId === act.id && (
                    <div
                      className="px-4 pb-4 border-t"
                      style={{ borderColor: 'rgba(30,58,138,0.08)', background: '#FAFBFF' }}
                    >
                      <div className="grid grid-cols-2 gap-3 pt-3">
                        <div>
                          <label className="text-xs font-600 block mb-1" style={{ color: '#64748B' }}>Schedule</label>
                          <select
                            className="w-full text-xs px-3 py-2 rounded-lg border outline-none"
                            style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#0C1A3A', background: 'white' }}
                            defaultValue={act.scheduled ? 'scheduled' : 'unscheduled'}
                          >
                            <option value="unscheduled">Unscheduled</option>
                            <option value="scheduled">Scheduled</option>
                          </select>
                        </div>
                        <div>
                          <label className="text-xs font-600 block mb-1" style={{ color: '#64748B' }}>Travel mode</label>
                          <select
                            className="w-full text-xs px-3 py-2 rounded-lg border outline-none"
                            style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#0C1A3A', background: 'white' }}
                            defaultValue={act.travelMode}
                          >
                            <option value="driving">Driving</option>
                            <option value="walking">Walking</option>
                          </select>
                        </div>
                        <div>
                          <label className="text-xs font-600 block mb-1" style={{ color: '#64748B' }}>Activity class</label>
                          <select
                            className="w-full text-xs px-3 py-2 rounded-lg border outline-none"
                            style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#0C1A3A', background: 'white' }}
                            defaultValue={act.activityClass}
                          >
                            <option value="flexible">Flexible</option>
                            <option value="fixed">Fixed</option>
                          </select>
                        </div>
                        <div>
                          <label className="text-xs font-600 block mb-1" style={{ color: '#64748B' }}>Duration (min)</label>
                          <input
                            type="number"
                            defaultValue={act.duration}
                            className="w-full text-xs px-3 py-2 rounded-lg border outline-none"
                            style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#0C1A3A', background: 'white' }}
                          />
                        </div>
                      </div>
                      <div className="flex flex-wrap gap-3 mt-3">
                        {['Movable', 'Skippable', 'Shortening allowed'].map((opt) => (
                          <label key={opt} className="flex items-center gap-1.5 text-xs cursor-pointer" style={{ color: '#64748B' }}>
                            <input type="checkbox" defaultChecked className="rounded" style={{ accentColor: '#1D4ED8' }} />
                            {opt}
                          </label>
                        ))}
                        <label className="flex items-center gap-1.5 text-xs cursor-pointer" style={{ color: '#64748B' }}>
                          <input type="checkbox" className="rounded" style={{ accentColor: '#1D4ED8' }} />
                          Mandatory
                        </label>
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>

            {/* Search UI */}
            <div
              className="rounded-xl border overflow-hidden"
              style={{ borderColor: 'rgba(30,58,138,0.15)', boxShadow: '0 2px 12px rgba(30,58,138,0.05)' }}
            >
              <div className="flex items-center gap-2.5 px-4 py-3 bg-white">
                <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
                  <circle cx="6" cy="6" r="4.5" stroke="#94A3B8" strokeWidth="1.4" />
                  <path d="M9.5 9.5l2.5 2.5" stroke="#94A3B8" strokeWidth="1.4" strokeLinecap="round" />
                </svg>
                <input
                  type="text"
                  placeholder="Search for a place to add…"
                  value={searchQuery}
                  onChange={(e) => {
                    setSearchQuery(e.target.value)
                    setSearchState(e.target.value ? 'searching' : 'idle')
                  }}
                  className="flex-1 text-sm outline-none bg-transparent"
                  style={{ color: '#0C1A3A' }}
                />
                {searchQuery && (
                  <button
                    onClick={() => { setSearchQuery(''); setSearchState('idle') }}
                    className="cursor-pointer"
                    style={{ color: '#94A3B8' }}
                  >
                    <svg width="12" height="12" viewBox="0 0 12 12" fill="none">
                      <path d="M1 1l10 10M11 1L1 11" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
                    </svg>
                  </button>
                )}
              </div>

              {searchState === 'idle' && !searchQuery && (
                <div className="px-4 py-3 border-t" style={{ borderColor: 'rgba(30,58,138,0.08)', background: '#F8FAFF' }}>
                  <div className="text-xs mb-2" style={{ color: '#94A3B8' }}>Try searching for</div>
                  <div className="flex flex-wrap gap-1.5">
                    {['Restaurants', 'Museums', 'Parks', 'Hotels', 'Addresses'].map((cat) => (
                      <button
                        key={cat}
                        onClick={() => { setSearchQuery(cat); setSearchState('searching') }}
                        className="px-2.5 py-1 rounded-lg text-xs font-500 border cursor-pointer transition-colors"
                        style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#64748B', background: 'white' }}
                        onMouseEnter={(e) => (e.currentTarget.style.background = '#EFF6FF')}
                        onMouseLeave={(e) => (e.currentTarget.style.background = 'white')}
                      >
                        {cat}
                      </button>
                    ))}
                  </div>
                </div>
              )}

              {searchState === 'searching' && (
                <div className="border-t" style={{ borderColor: 'rgba(30,58,138,0.08)' }}>
                  {mockResults.map((r) => (
                    <button
                      key={r.name}
                      onClick={() => {
                        setSearchState('temp-selected')
                        setSearchQuery(r.name)
                      }}
                      className="w-full text-left px-4 py-3 border-b last:border-b-0 cursor-pointer transition-colors hover:bg-slate-50"
                      style={{ borderColor: 'rgba(30,58,138,0.07)' }}
                    >
                      <div className="text-sm font-500" style={{ color: '#0C1A3A' }}>{r.name}</div>
                      <div className="text-xs mt-0.5" style={{ color: '#64748B' }}>{r.address}</div>
                    </button>
                  ))}
                </div>
              )}

              {searchState === 'temp-selected' && (
                <div className="border-t px-4 py-3" style={{ borderColor: 'rgba(30,58,138,0.08)', background: '#FFFBEB' }}>
                  <div className="text-xs font-600 mb-2" style={{ color: '#D97706' }}>
                    Temporary selection — not yet saved
                  </div>
                  <div className="text-sm font-600 mb-0.5" style={{ color: '#0C1A3A' }}>{searchQuery}</div>
                  <div className="text-xs mb-3" style={{ color: '#64748B' }}>37.7746° N, 122.4186° W (temporary)</div>
                  <div className="flex gap-2">
                    <button
                      onClick={() => setSearchState('durable')}
                      className="flex-1 py-2 rounded-lg text-xs font-600 text-white cursor-pointer"
                      style={{ background: '#1D4ED8' }}
                    >
                      Use this location
                    </button>
                    <button
                      onClick={() => setSearchState('searching')}
                      className="flex-1 py-2 rounded-lg text-xs font-500 border cursor-pointer"
                      style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#64748B' }}
                    >
                      Choose another
                    </button>
                  </div>
                </div>
              )}

              {searchState === 'durable' && (
                <div className="border-t px-4 py-3" style={{ borderColor: 'rgba(30,58,138,0.08)', background: '#F0FDF4' }}>
                  <div className="text-xs font-600 mb-2" style={{ color: '#16A34A' }}>
                    Location confirmed
                  </div>
                  <div className="text-sm font-600 mb-0.5" style={{ color: '#0C1A3A' }}>{searchQuery}</div>
                  <div className="text-xs mb-0.5" style={{ color: '#64748B' }}>37.7746° N, 122.4186° W</div>
                  <div className="text-xs mb-3" style={{ color: '#64748B' }}>Timezone: America/Los_Angeles</div>
                  <div className="flex gap-2">
                    <button
                      onClick={() => addTempActivity(searchQuery, '37.7746° N, 122.4186° W')}
                      className="flex-1 py-2 rounded-lg text-xs font-600 text-white cursor-pointer"
                      style={{ background: '#16A34A' }}
                    >
                      Confirm location
                    </button>
                    <button
                      onClick={() => setSearchState('idle')}
                      className="px-3 py-2 rounded-lg text-xs font-500 border cursor-pointer"
                      style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#64748B' }}
                    >
                      Cancel
                    </button>
                    <button
                      onClick={() => setSearchState('idle')}
                      className="px-3 py-2 rounded-lg text-xs font-500 border cursor-pointer"
                      style={{ borderColor: 'rgba(30,58,138,0.15)', color: '#64748B' }}
                    >
                      Search again
                    </button>
                  </div>
                </div>
              )}
            </div>
          </div>

          {/* Actions */}
          <div
            className="sticky bottom-0 flex items-center gap-3 px-6 py-4 border-t bg-white"
            style={{ borderColor: 'rgba(30,58,138,0.10)' }}
          >
            <button
              onClick={handleSave}
              disabled={saveState === 'saving'}
              className="flex-1 py-2.5 rounded-xl text-sm font-600 border cursor-pointer transition-colors"
              style={{
                borderColor: 'rgba(30,58,138,0.20)',
                color: saveState === 'saved' ? '#16A34A' : '#0C1A3A',
                background: saveState === 'saved' ? '#F0FDF4' : 'white',
              }}
            >
              {saveState === 'saving' ? 'Saving…' : saveState === 'saved' ? '✓ Saved' : 'Save trip'}
            </button>
            <button
              onClick={handleGo}
              disabled={!canGo}
              className="flex-1 py-2.5 rounded-xl text-sm font-600 text-white cursor-pointer transition-opacity hover:opacity-90"
              style={{
                background: canGo ? '#1D4ED8' : '#CBD5E1',
                cursor: canGo ? 'pointer' : 'not-allowed',
              }}
            >
              Go
            </button>
          </div>
        </div>

        {/* Right: map preview */}
        <div
          className="hidden lg:flex flex-col items-center justify-center"
          style={{ background: '#EFF6FF', minHeight: '100%' }}
        >
          <div className="w-full h-full relative overflow-hidden">
            <svg width="100%" height="100%" viewBox="0 0 600 700" preserveAspectRatio="xMidYMid slice" fill="none">
              {/* Map base */}
              <rect width="600" height="700" fill="#EFF6FF" />
              {/* Grid */}
              {[50,100,150,200,250,300,350,400,450,500,550,600].map(x=>(
                <line key={x} x1={x} y1="0" x2={x} y2="700" stroke="rgba(30,58,138,0.05)" strokeWidth="1" />
              ))}
              {[50,100,150,200,250,300,350,400,450,500,550,600,650].map(y=>(
                <line key={y} x1="0" y1={y} x2="600" y2={y} stroke="rgba(30,58,138,0.05)" strokeWidth="1" />
              ))}
              {/* Streets */}
              <path d="M0 320 H600" stroke="white" strokeWidth="12" />
              <path d="M0 420 H600" stroke="white" strokeWidth="8" />
              <path d="M0 220 H600" stroke="white" strokeWidth="8" />
              <path d="M200 0 V700" stroke="white" strokeWidth="12" />
              <path d="M380 0 V700" stroke="white" strokeWidth="8" />
              <path d="M100 0 V700" stroke="white" strokeWidth="6" />
              <path d="M480 0 V700" stroke="white" strokeWidth="6" />
              {/* Route path */}
              <path
                d="M200 580 Q200 320 200 320 Q200 320 380 320 Q380 320 380 200 Q380 150 380 120"
                stroke="#DBEAFE"
                strokeWidth="10"
                strokeLinecap="round"
                fill="none"
              />
              <path
                d="M200 580 Q200 320 200 320 Q200 320 380 320 Q380 320 380 200 Q380 150 380 120"
                stroke="#1D4ED8"
                strokeWidth="3"
                strokeLinecap="round"
                strokeDasharray="0"
                fill="none"
              />
              {/* Stops */}
              {activities.map((_, i) => {
                const pts = [{cx:200,cy:580},{cx:200,cy:320},{cx:380,cy:200},{cx:380,cy:120}]
                const pt = pts[Math.min(i, pts.length-1)]
                return (
                  <g key={i}>
                    <circle cx={pt.cx} cy={pt.cy} r="14" fill="white" style={{ filter: 'drop-shadow(0 2px 6px rgba(30,58,138,0.15))' }} />
                    <circle cx={pt.cx} cy={pt.cy} r="11" fill="#1D4ED8" />
                    <text x={pt.cx} y={pt.cy+4} textAnchor="middle" fontSize="9" fontWeight="700" fill="white">{i+1}</text>
                  </g>
                )
              })}
            </svg>

            {activities.length === 0 && (
              <div className="absolute inset-0 flex items-center justify-center">
                <div className="text-center">
                  <div
                    className="w-12 h-12 rounded-2xl flex items-center justify-center mx-auto mb-3"
                    style={{ background: 'rgba(255,255,255,0.8)' }}
                  >
                    <svg width="22" height="22" viewBox="0 0 22 22" fill="none">
                      <circle cx="11" cy="9" r="4" stroke="#94A3B8" strokeWidth="1.5" />
                      <path d="M5 20c0-3 3-6 6-6s6 3 6 6" stroke="#94A3B8" strokeWidth="1.5" strokeLinecap="round" />
                    </svg>
                  </div>
                  <div className="text-sm font-500" style={{ color: '#94A3B8' }}>Add stops to see the route</div>
                </div>
              </div>
            )}

            {/* Map attribution */}
            <div className="absolute bottom-3 right-3">
              <span
                className="text-xs px-2 py-1 rounded-lg font-500"
                style={{ background: 'rgba(255,255,255,0.9)', color: '#94A3B8', boxShadow: '0 1px 4px rgba(0,0,0,0.08)' }}
              >
                Map preview
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

import { useState } from "react"
import { Link } from "react-router-dom"
import Nav from "../components/Nav"
import MapPlaceholder from "../components/MapPlaceholder"

type SearchStage = "idle" | "searching" | "temporary" | "confirming" | "confirmed"
type PlannerView = "empty" | "activities" | "search" | "editor"

interface Activity {
  id: number
  label: string
  coord: string
  timezone: string
  time: string
  mode: "driving" | "walking"
  scheduled: boolean
}

const defaultActivities: Activity[] = [
  { id: 1, label: "City Museum", coord: "48.2093° N, 16.3731° E", timezone: "CET", time: "10:00 AM", mode: "driving", scheduled: true },
  { id: 2, label: "Garden Café", coord: "48.2154° N, 16.3601° E", timezone: "CET", time: "12:30 PM", mode: "walking", scheduled: true },
  { id: 3, label: "Riverside Park", coord: "48.2009° N, 16.3690° E", timezone: "CET", time: "2:00 PM", mode: "driving", scheduled: false },
]

const searchResults = [
  { id: "r1", label: "Schönbrunn Palace", address: "Schönbrunner Schloßstr., 1130 Vienna" },
  { id: "r2", label: "Belvedere Museum", address: "Prinz Eugen-Str. 27, 1030 Vienna" },
  { id: "r3", label: "Prater Park", address: "Prater, 1020 Vienna" },
]

export default function Planner() {
  const [tripName, setTripName] = useState("Weekend in Vienna")
  const [activities, setActivities] = useState<Activity[]>(defaultActivities)
  const [view, setView] = useState<PlannerView>("activities")
  const [searchStage, setSearchStage] = useState<SearchStage>("idle")
  const [query, setQuery] = useState("")
  const [tempResult, setTempResult] = useState<(typeof searchResults)[0] | null>(null)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [saveState, setSaveState] = useState<"idle" | "saving" | "saved">("idle")

  const allScheduled = activities.every((a) => a.scheduled)

  function handleSearch(e: React.FormEvent) {
    e.preventDefault()
    if (!query.trim()) return
    setSearchStage("searching")
    setTimeout(() => setSearchStage("confirmed"), 1000)
  }

  function handleSelectResult(r: (typeof searchResults)[0]) {
    setTempResult(r)
    setSearchStage("temporary")
  }

  function handleUseLocation() {
    setSearchStage("confirming")
  }

  function handleConfirm() {
    if (!tempResult) return
    const newAct: Activity = {
      id: Date.now(),
      label: tempResult.address,
      coord: "48.2093° N, 16.3731° E",
      timezone: "CET",
      time: "Unscheduled",
      mode: "driving",
      scheduled: false,
    }
    setActivities((prev) => [...prev, newAct])
    setView("activities")
    setSearchStage("idle")
    setQuery("")
    setTempResult(null)
  }

  function handleRemove(id: number) {
    setActivities((prev) => prev.filter((a) => a.id !== id))
  }

  function handleSave() {
    setSaveState("saving")
    setTimeout(() => {
      setSaveState("saved")
      setTimeout(() => setSaveState("idle"), 2000)
    }, 1200)
  }

  const mapStops = activities.map((a, i) => ({
    id: i + 1,
    x: 15 + i * 22,
    y: 60 - (i % 2) * 25,
    label: a.label,
  }))

  return (
    <div className="min-h-screen bg-white">
      <Nav />

      <main className="max-w-[1440px] mx-auto">
        <div className="grid lg:grid-cols-[480px_1fr]">
          {/* Left panel */}
          <div className="border-r border-[rgba(30,58,138,0.10)] min-h-[calc(100vh-56px)] flex flex-col">
            <div className="p-6 border-b border-[rgba(30,58,138,0.08)]">
              <Link
                to="/trips"
                className="inline-flex items-center gap-1.5 text-xs text-[#64748B] hover:text-[#0C1A3A] font-500 mb-4 transition-colors"
              >
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none">
                  <path d="M15 18l-6-6 6-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
                </svg>
                Back to Trips
              </Link>
              <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-1">
                New itinerary
              </p>
              <h1 className="text-2xl font-800 text-[#0C1A3A] tracking-tight">Plan a new trip</h1>
              <p className="text-xs text-[#64748B] mt-1">Start with a name and a confirmed destination.</p>
            </div>

            <div className="p-6 border-b border-[rgba(30,58,138,0.08)] space-y-4">
              <div>
                <label className="block text-xs font-600 text-[#0C1A3A] mb-1.5">Trip name</label>
                <input
                  value={tripName}
                  onChange={(e) => setTripName(e.target.value)}
                  className="w-full px-3 py-2.5 rounded-lg border border-[rgba(30,58,138,0.15)] text-sm text-[#0C1A3A] bg-[#F8FAFC] focus:outline-none focus:border-[#1D4ED8] focus:bg-white transition-colors"
                  placeholder="e.g. Weekend in Vienna"
                />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs font-600 text-[#0C1A3A] mb-1.5">Timezone</label>
                  <select className="w-full px-3 py-2.5 rounded-lg border border-[rgba(30,58,138,0.15)] text-sm text-[#0C1A3A] bg-[#F8FAFC] focus:outline-none focus:border-[#1D4ED8]">
                    <option>CET (UTC+1)</option>
                    <option>UTC</option>
                    <option>EST (UTC-5)</option>
                  </select>
                </div>
                <div>
                  <label className="block text-xs font-600 text-[#64748B] mb-1.5">
                    Date <span className="font-400 text-[#94A3B8]">(preview only)</span>
                  </label>
                  <input
                    type="date"
                    defaultValue="2026-08-17"
                    className="w-full px-3 py-2.5 rounded-lg border border-[rgba(30,58,138,0.15)] text-sm text-[#0C1A3A] bg-[#F8FAFC] focus:outline-none focus:border-[#1D4ED8]"
                  />
                </div>
              </div>
              <p className="text-[10px] text-[#94A3B8]">
                Date and time are for planning preview only. They don't activate the trip.
              </p>
            </div>

            {/* Search / activity area */}
            <div className="flex-1 p-6 overflow-y-auto">
              {/* Search bar */}
              <form onSubmit={handleSearch} className="flex gap-2 mb-5">
                <input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Search for a place…"
                  className="flex-1 px-3 py-2.5 rounded-lg border border-[rgba(30,58,138,0.15)] text-sm bg-[#F8FAFC] focus:outline-none focus:border-[#1D4ED8] focus:bg-white transition-colors"
                />
                <button
                  type="submit"
                  className="bg-[#1D4ED8] text-white px-4 py-2.5 rounded-lg text-sm font-600 hover:bg-[#1E40AF] transition-colors"
                >
                  Search
                </button>
              </form>

              {/* Category chips */}
              {searchStage === "idle" && (
                <div className="flex flex-wrap gap-2 mb-5">
                  {["Restaurants", "Museums", "Attractions", "Hotels"].map((c) => (
                    <button
                      key={c}
                      onClick={() => setQuery(c)}
                      className="text-xs px-3 py-1.5 rounded-full bg-[#EFF6FF] text-[#1D4ED8] font-500 hover:bg-[#DBEAFE] transition-colors"
                    >
                      {c}
                    </button>
                  ))}
                </div>
              )}

              {/* Search results */}
              {searchStage === "searching" && (
                <div className="space-y-2 mb-5">
                  {[1, 2, 3].map((i) => (
                    <div key={i} className="skeleton h-14 rounded-lg" />
                  ))}
                </div>
              )}

              {searchStage === "confirmed" && (
                <div className="mb-5 space-y-1.5">
                  <p className="text-xs font-600 text-[#64748B] mb-2">Results for "{query}"</p>
                  {searchResults.map((r) => (
                    <button
                      key={r.id}
                      onClick={() => handleSelectResult(r)}
                      className="w-full text-left p-3 rounded-lg border border-[rgba(30,58,138,0.10)] hover:border-[rgba(30,58,138,0.25)] hover:bg-[#F8FAFC] transition-all"
                    >
                      <p className="text-sm font-600 text-[#0C1A3A]">{r.label}</p>
                      <p className="text-xs text-[#64748B] mt-0.5">{r.address}</p>
                    </button>
                  ))}
                </div>
              )}

              {/* Temporary selection */}
              {searchStage === "temporary" && tempResult && (
                <div className="mb-5 border border-[rgba(30,58,138,0.20)] rounded-xl p-4 bg-[#EFF6FF]">
                  <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-1">
                    Temporary selection
                  </p>
                  <p className="text-sm font-700 text-[#0C1A3A] mb-0.5">{tempResult.label}</p>
                  <p className="text-xs text-[#64748B] mb-3">{tempResult.address}</p>
                  <p className="text-[10px] text-[#94A3B8] mb-3 italic">
                    This location is not yet saved to your trip.
                  </p>
                  <div className="flex gap-2">
                    <button
                      onClick={handleUseLocation}
                      className="flex-1 bg-[#1D4ED8] text-white text-xs font-600 py-2 rounded-lg hover:bg-[#1E40AF] transition-colors"
                    >
                      Use this location
                    </button>
                    <button
                      onClick={() => setSearchStage("confirmed")}
                      className="flex-1 border border-[rgba(30,58,138,0.20)] text-[#0C1A3A] text-xs font-600 py-2 rounded-lg hover:bg-white transition-colors"
                    >
                      Choose another
                    </button>
                  </div>
                </div>
              )}

              {/* Confirming / durable */}
              {searchStage === "confirming" && tempResult && (
                <div className="mb-5 border border-[rgba(30,58,138,0.20)] rounded-xl p-4 bg-white">
                  <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-1">
                    Confirm location
                  </p>
                  <p className="text-sm font-700 text-[#0C1A3A] mb-0.5">{tempResult.address}</p>
                  <p className="text-xs text-[#64748B]">48.2101° N, 16.3694° E · CET (UTC+1)</p>
                  <div className="my-3 h-px bg-[rgba(30,58,138,0.08)]" />
                  <div className="flex gap-2">
                    <button
                      onClick={handleConfirm}
                      className="flex-1 bg-[#1D4ED8] text-white text-xs font-600 py-2 rounded-lg hover:bg-[#1E40AF] transition-colors"
                    >
                      Confirm location
                    </button>
                    <button
                      onClick={() => { setSearchStage("idle"); setTempResult(null); setQuery("") }}
                      className="border border-[rgba(30,58,138,0.20)] text-[#64748B] text-xs font-600 py-2 px-3 rounded-lg hover:bg-[#F8FAFC] transition-colors"
                    >
                      Cancel
                    </button>
                  </div>
                </div>
              )}

              {/* Activity list */}
              <div>
                <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-2">
                  Activities · {activities.length}
                </p>

                {activities.length === 0 ? (
                  <div className="text-center py-8 border-2 border-dashed border-[rgba(30,58,138,0.15)] rounded-xl">
                    <p className="text-sm text-[#94A3B8]">No activities yet — search for a place above.</p>
                  </div>
                ) : (
                  <div className="space-y-2">
                    {activities.map((act, i) => (
                      <div key={act.id}>
                        <div className="flex items-start gap-3 p-3 rounded-xl border border-[rgba(30,58,138,0.10)] bg-white hover:border-[rgba(30,58,138,0.20)] transition-colors">
                          <div className="flex flex-col items-center gap-1 shrink-0 pt-0.5">
                            <span className="w-6 h-6 rounded-full bg-[#EFF6FF] text-[#1D4ED8] text-xs font-700 flex items-center justify-center">
                              {i + 1}
                            </span>
                            <svg width="10" height="16" viewBox="0 0 10 16" fill="none" className="text-[#CBD5E1] cursor-grab">
                              <circle cx="3" cy="3" r="1.5" fill="currentColor" />
                              <circle cx="7" cy="3" r="1.5" fill="currentColor" />
                              <circle cx="3" cy="8" r="1.5" fill="currentColor" />
                              <circle cx="7" cy="8" r="1.5" fill="currentColor" />
                              <circle cx="3" cy="13" r="1.5" fill="currentColor" />
                              <circle cx="7" cy="13" r="1.5" fill="currentColor" />
                            </svg>
                          </div>
                          <div className="flex-1 min-w-0">
                            <p className="text-sm font-600 text-[#0C1A3A] truncate">{act.label}</p>
                            <p className="text-xs text-[#64748B] mt-0.5">{act.coord} · {act.timezone}</p>
                            <div className="flex items-center gap-2 mt-1.5">
                              {act.scheduled ? (
                                <span className="text-xs text-[#0C1A3A] font-500">{act.time}</span>
                              ) : (
                                <span className="text-xs text-[#D97706] font-500 bg-[#FEF3C7] px-2 py-0.5 rounded-full">
                                  Unscheduled
                                </span>
                              )}
                              <span className="text-xs text-[#94A3B8]">
                                {act.mode === "driving" ? "🚗 Driving" : "🚶 Walking"}
                              </span>
                            </div>
                          </div>
                          <div className="flex flex-col gap-1 shrink-0">
                            <button
                              onClick={() => setEditingId(editingId === act.id ? null : act.id)}
                              className="text-xs text-[#64748B] hover:text-[#1D4ED8] font-500 transition-colors"
                            >
                              Edit
                            </button>
                            <button
                              onClick={() => handleRemove(act.id)}
                              className="text-xs text-[#94A3B8] hover:text-[#DC2626] font-500 transition-colors"
                            >
                              Remove
                            </button>
                          </div>
                        </div>

                        {/* Inline editor */}
                        {editingId === act.id && (
                          <div className="ml-9 mt-1 p-4 rounded-xl border border-[rgba(30,58,138,0.15)] bg-[#F8FAFC] space-y-4">
                            <p className="text-xs font-700 text-[#0C1A3A] mb-2">Activity settings</p>
                            <div className="grid grid-cols-2 gap-3">
                              <div>
                                <label className="block text-xs font-600 text-[#64748B] mb-1">Schedule</label>
                                <select className="w-full px-2.5 py-2 rounded-lg border border-[rgba(30,58,138,0.15)] text-xs bg-white focus:outline-none focus:border-[#1D4ED8]">
                                  <option>Unscheduled</option>
                                  <option>Scheduled</option>
                                </select>
                              </div>
                              <div>
                                <label className="block text-xs font-600 text-[#64748B] mb-1">Travel mode</label>
                                <select className="w-full px-2.5 py-2 rounded-lg border border-[rgba(30,58,138,0.15)] text-xs bg-white focus:outline-none focus:border-[#1D4ED8]">
                                  <option>Driving</option>
                                  <option>Walking</option>
                                </select>
                              </div>
                              <div>
                                <label className="block text-xs font-600 text-[#64748B] mb-1">Activity class</label>
                                <select className="w-full px-2.5 py-2 rounded-lg border border-[rgba(30,58,138,0.15)] text-xs bg-white focus:outline-none focus:border-[#1D4ED8]">
                                  <option>Flexible</option>
                                  <option>Fixed</option>
                                </select>
                              </div>
                              <div>
                                <label className="block text-xs font-600 text-[#64748B] mb-1">Priority rank</label>
                                <input type="number" defaultValue="0" className="w-full px-2.5 py-2 rounded-lg border border-[rgba(30,58,138,0.15)] text-xs bg-white focus:outline-none focus:border-[#1D4ED8]" />
                              </div>
                            </div>
                            <div>
                              <label className="block text-xs font-600 text-[#64748B] mb-2">Duration</label>
                              <div className="grid grid-cols-3 gap-2">
                                {["Min", "Preferred", "Max"].map((d) => (
                                  <div key={d}>
                                    <p className="text-[10px] text-[#94A3B8] mb-1">{d}</p>
                                    <input
                                      type="text"
                                      defaultValue="60 min"
                                      className="w-full px-2 py-1.5 rounded-lg border border-[rgba(30,58,138,0.15)] text-xs bg-white focus:outline-none focus:border-[#1D4ED8]"
                                    />
                                  </div>
                                ))}
                              </div>
                            </div>
                            <div className="flex flex-wrap gap-x-4 gap-y-2">
                              {["Movable", "Skippable", "Mandatory", "Shortening allowed"].map((opt) => (
                                <label key={opt} className="flex items-center gap-1.5 text-xs text-[#0C1A3A] cursor-pointer">
                                  <input
                                    type="checkbox"
                                    defaultChecked={opt === "Movable" || opt === "Skippable"}
                                    className="rounded accent-[#1D4ED8]"
                                  />
                                  {opt}
                                </label>
                              ))}
                            </div>
                            <button
                              onClick={() => setEditingId(null)}
                              className="w-full text-xs font-600 text-[#1D4ED8] hover:text-[#1E40AF] transition-colors"
                            >
                              Done
                            </button>
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>

            {/* Footer actions */}
            <div className="p-4 border-t border-[rgba(30,58,138,0.08)] flex gap-2">
              <button
                onClick={handleSave}
                disabled={activities.length === 0}
                className="flex-1 bg-[#1D4ED8] disabled:bg-[#CBD5E1] text-white text-sm font-600 py-2.5 rounded-xl transition-colors hover:bg-[#1E40AF] flex items-center justify-center gap-2"
              >
                {saveState === "saving" && <Spinner />}
                {saveState === "saved" ? "Saved!" : "Save trip"}
              </button>
              <button
                disabled={!allScheduled}
                className="flex-1 border border-[rgba(30,58,138,0.25)] disabled:opacity-40 text-[#0C1A3A] text-sm font-600 py-2.5 rounded-xl transition-colors hover:bg-[#F8FAFC]"
                title={!allScheduled ? "All activities must be scheduled before going live" : undefined}
              >
                Go
              </button>
            </div>
          </div>

          {/* Map panel */}
          <div className="hidden lg:flex flex-col bg-[#F8FAFF] p-6">
            <MapPlaceholder
              stops={mapStops.length > 0 ? mapStops : undefined}
              className="flex-1 min-h-[400px]"
            />
            {!allScheduled && (
              <div className="mt-4 flex items-start gap-2 p-3 rounded-lg bg-[#FFFBEB] border border-[#FDE68A]">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" className="shrink-0 mt-0.5">
                  <path d="M12 9v4M12 17v.5" stroke="#D97706" strokeWidth="1.5" strokeLinecap="round" />
                  <path d="M10.29 4l-7.45 13A2 2 0 0 0 4.58 20h14.84a2 2 0 0 0 1.74-3L13.71 4a2 2 0 0 0-3.42 0z" stroke="#D97706" strokeWidth="1.5" fill="none" />
                </svg>
                <p className="text-xs text-[#D97706] font-500">
                  Some activities are unscheduled. Schedule all activities before starting the trip.
                </p>
              </div>
            )}
          </div>
        </div>
      </main>
    </div>
  )
}

function Spinner() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" className="animate-spin">
      <circle cx="12" cy="12" r="9" stroke="white" strokeWidth="2.5" strokeOpacity="0.3" />
      <path d="M12 3a9 9 0 0 1 9 9" stroke="white" strokeWidth="2.5" strokeLinecap="round" />
    </svg>
  )
}

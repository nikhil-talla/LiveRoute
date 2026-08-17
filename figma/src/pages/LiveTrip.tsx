import { useState } from "react"
import Nav from "../components/Nav"
import MapPlaceholder from "../components/MapPlaceholder"
import StatusPill from "../components/StatusPill"

type LiveState = "connected" | "connecting" | "reconnecting" | "gps-denied" | "gps-stale"
type ProposalState = "none" | "pending" | "accepted" | "rejected" | "stale"

const activities = [
  { id: 1, label: "City Museum", time: "10:00 AM", status: "completed" as const, addr: "Burgring 5, 1010 Vienna" },
  { id: 2, label: "Garden Café", time: "12:30 PM", status: "active" as const, addr: "Schleifmühlgasse 14, Vienna" },
  { id: 3, label: "Riverside Park", time: "2:00 PM", status: "upcoming" as const, addr: "Donauinsel, 1210 Vienna" },
  { id: 4, label: "Old Town Hall", time: "4:00 PM", status: "upcoming" as const, addr: "Wipplingerstr. 8, Vienna" },
]

const mapStops = [
  { id: 1, x: 15, y: 70, label: "City Museum" },
  { id: 2, x: 35, y: 45, label: "Garden Café" },
  { id: 3, x: 58, y: 62, label: "Riverside Park" },
  { id: 4, x: 78, y: 30, label: "Old Town Hall" },
]

export default function LiveTrip() {
  const [liveState, setLiveState] = useState<LiveState>("connected")
  const [proposalState, setProposalState] = useState<ProposalState>("pending")
  const [stopDialog, setStopDialog] = useState(false)
  const [activityControls, setActivityControls] = useState<string>("idle")

  const currentActivity = activities[1]

  return (
    <div className="min-h-screen bg-white">
      <Nav hasActiveTrip />

      {/* State bar */}
      <div className="bg-[#F8FAFC] border-b border-[rgba(30,58,138,0.08)] px-6 py-2 flex items-center gap-2 flex-wrap">
        <p className="text-xs font-600 text-[#64748B] mr-1">Preview:</p>
        {(["connected", "connecting", "reconnecting", "gps-denied", "gps-stale"] as LiveState[]).map((s) => (
          <button
            key={s}
            onClick={() => setLiveState(s)}
            className={`text-xs px-2.5 py-1 rounded-lg font-500 transition-colors ${liveState === s ? "bg-[#1D4ED8] text-white" : "bg-white border border-[rgba(30,58,138,0.12)] text-[#64748B]"}`}
          >
            {s}
          </button>
        ))}
        <span className="mx-2 text-[#E2E8F0]">|</span>
        <p className="text-xs font-600 text-[#64748B] mr-1">Proposal:</p>
        {(["none", "pending", "accepted", "rejected", "stale"] as ProposalState[]).map((s) => (
          <button
            key={s}
            onClick={() => setProposalState(s)}
            className={`text-xs px-2.5 py-1 rounded-lg font-500 transition-colors ${proposalState === s ? "bg-[#1D4ED8] text-white" : "bg-white border border-[rgba(30,58,138,0.12)] text-[#64748B]"}`}
          >
            {s}
          </button>
        ))}
      </div>

      <div className="max-w-[1440px] mx-auto">
        <div className="grid lg:grid-cols-[400px_1fr] min-h-[calc(100vh-96px)]">
          {/* Left panel */}
          <div className="border-r border-[rgba(30,58,138,0.10)] flex flex-col overflow-y-auto">
            {/* Trip header */}
            <div className="p-5 border-b border-[rgba(30,58,138,0.08)]">
              <div className="flex items-center justify-between mb-1">
                <h1 className="text-lg font-800 text-[#0C1A3A]">Amsterdam Canal Tour</h1>
                <StatusPill status={liveState === "connected" ? "live" : "connecting"} />
              </div>
              <ConnectionStatus state={liveState} />
            </div>

            {/* Next activity */}
            <div className="p-5 border-b border-[rgba(30,58,138,0.08)] bg-[#F8FAFF]">
              <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-3">
                Now · Activity 2 of 4
              </p>
              <div className="flex items-start gap-3">
                <div className="w-9 h-9 rounded-xl bg-[#EFF6FF] flex items-center justify-center text-[#1D4ED8] font-800 text-sm shrink-0">
                  2
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-base font-700 text-[#0C1A3A]">{currentActivity.label}</p>
                  <p className="text-xs text-[#64748B] mt-0.5">{currentActivity.addr}</p>
                  <div className="grid grid-cols-3 gap-3 mt-3">
                    <Stat label="Arrival" value="12:28 PM" />
                    <Stat label="Remaining" value="14 min" />
                    <Stat label="Distance" value="2.1 km" />
                  </div>
                </div>
              </div>

              {/* Activity controls */}
              <div className="mt-4 flex gap-2">
                <button
                  onClick={() => setActivityControls("started")}
                  className={`flex-1 text-xs font-600 py-2 rounded-lg transition-colors ${activityControls === "started" ? "bg-[#DCFCE7] text-[#16A34A] border border-[rgba(22,163,74,0.25)]" : "bg-[#1D4ED8] text-white hover:bg-[#1E40AF]"}`}
                >
                  {activityControls === "started" ? "In progress" : "Start activity"}
                </button>
                <button
                  onClick={() => setActivityControls("completed")}
                  className="text-xs font-600 py-2 px-3 rounded-lg border border-[rgba(30,58,138,0.15)] text-[#0C1A3A] hover:bg-[#F8FAFC] transition-colors"
                >
                  Mark done
                </button>
                <button className="text-xs font-600 py-2 px-3 rounded-lg border border-[rgba(30,58,138,0.15)] text-[#64748B] hover:bg-[#F8FAFC] transition-colors">
                  Skip
                </button>
              </div>
            </div>

            {/* Proposal panel */}
            {proposalState === "pending" && (
              <div className="p-5 border-b border-[rgba(30,58,138,0.08)] bg-[#FFFBEB]">
                <p className="text-[10px] font-700 uppercase tracking-widest text-[#D97706] mb-1">
                  Suggested change
                </p>
                <p className="text-sm font-700 text-[#0C1A3A] mb-2">Review the proposed plan</p>
                <p className="text-xs text-[#64748B] leading-relaxed mb-2">
                  Heavier traffic near Riverside Park. This suggestion keeps 2 activities in place
                  and adjusts the route to Stop 3.
                </p>
                <p className="text-[10px] text-[#94A3B8] italic mb-3">
                  Your current plan remains unchanged until you accept this suggestion.
                </p>
                <div className="flex gap-2">
                  <button
                    onClick={() => setProposalState("accepted")}
                    className="flex-1 bg-[#1D4ED8] text-white text-xs font-600 py-2 rounded-lg hover:bg-[#1E40AF] transition-colors"
                  >
                    Accept suggestion
                  </button>
                  <button
                    onClick={() => setProposalState("rejected")}
                    className="flex-1 border border-[rgba(30,58,138,0.20)] text-[#0C1A3A] text-xs font-600 py-2 rounded-lg hover:bg-white transition-colors"
                  >
                    Keep current plan
                  </button>
                </div>
              </div>
            )}

            {proposalState === "accepted" && (
              <div className="p-4 border-b border-[rgba(30,58,138,0.08)] bg-[#F0FDF4]">
                <div className="flex items-center gap-2">
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
                    <path d="M5 12l4.5 4.5L19 7" stroke="#16A34A" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
                  </svg>
                  <p className="text-sm font-600 text-[#16A34A]">Suggestion accepted</p>
                </div>
                <p className="text-xs text-[#64748B] mt-1 ml-6">Route updated to reflect the new plan.</p>
              </div>
            )}

            {proposalState === "rejected" && (
              <div className="p-4 border-b border-[rgba(30,58,138,0.08)] bg-[#F8FAFC]">
                <p className="text-sm font-600 text-[#0C1A3A]">Keeping current plan</p>
                <p className="text-xs text-[#64748B] mt-1">Your original itinerary remains active.</p>
              </div>
            )}

            {proposalState === "stale" && (
              <div className="p-4 border-b border-[rgba(30,58,138,0.08)] bg-[#F1F5F9]">
                <p className="text-sm font-600 text-[#64748B]">Suggestion no longer current</p>
                <p className="text-xs text-[#94A3B8] mt-1">Conditions have changed since this was proposed. Your current plan is still active.</p>
              </div>
            )}

            {/* Remaining itinerary */}
            <div className="p-5 flex-1">
              <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-3">
                Remaining stops
              </p>
              <div className="space-y-2">
                {activities.map((act) => (
                  <div
                    key={act.id}
                    className={`flex items-center gap-3 p-3 rounded-lg ${
                      act.status === "active"
                        ? "border border-[rgba(29,78,216,0.25)] bg-[#EFF6FF]"
                        : act.status === "completed"
                        ? "opacity-50"
                        : "border border-[rgba(30,58,138,0.08)] bg-white"
                    }`}
                  >
                    <span
                      className={`w-6 h-6 rounded-full text-xs font-700 flex items-center justify-center shrink-0 ${
                        act.status === "completed"
                          ? "bg-[#F1F5F9] text-[#64748B]"
                          : act.status === "active"
                          ? "bg-[#1D4ED8] text-white"
                          : "bg-[#EFF6FF] text-[#1D4ED8]"
                      }`}
                    >
                      {act.status === "completed" ? (
                        <svg width="10" height="10" viewBox="0 0 24 24" fill="none">
                          <path d="M5 12l4 4L19 8" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
                        </svg>
                      ) : (
                        act.id
                      )}
                    </span>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-600 text-[#0C1A3A] truncate">{act.label}</p>
                      <p className="text-xs text-[#64748B]">{act.time}</p>
                    </div>
                    {act.status === "active" && (
                      <span className="pulse-dot shrink-0" style={{ background: "#1D4ED8" }} />
                    )}
                  </div>
                ))}
              </div>
            </div>

            {/* Stop trip */}
            <div className="p-4 border-t border-[rgba(30,58,138,0.08)]">
              <button
                onClick={() => setStopDialog(true)}
                className="w-full border border-[rgba(220,38,38,0.25)] text-[#DC2626] hover:bg-[#FEF2F2] text-sm font-600 py-2.5 rounded-xl transition-colors"
              >
                Stop trip
              </button>
            </div>
          </div>

          {/* Map */}
          <div className="hidden lg:flex flex-col bg-[#EFF6FF] relative">
            <MapPlaceholder
              stops={mapStops}
              showCurrentLocation
              proposed={proposalState === "pending"}
              className="flex-1 rounded-none border-0"
            />

            {/* GPS status overlay */}
            {liveState !== "connected" && (
              <div className="absolute top-4 left-4 right-4">
                <GpsStatusBanner state={liveState} />
              </div>
            )}

            {/* Route comparison legend */}
            {proposalState === "pending" && (
              <div className="absolute bottom-4 left-4 bg-white/95 rounded-xl p-3 border border-[rgba(30,58,138,0.12)] shadow-[0_4px_24px_rgba(30,58,138,0.07)]">
                <p className="text-xs font-700 text-[#0C1A3A] mb-2">Route comparison</p>
                <div className="flex items-center gap-2 mb-1">
                  <div className="h-0.5 w-8 bg-[#1D4ED8] rounded" />
                  <p className="text-xs text-[#64748B]">Current plan</p>
                </div>
                <div className="flex items-center gap-2">
                  <div className="h-0.5 w-8 bg-[#94A3B8] rounded" style={{ borderTop: "2px dashed #94A3B8", background: "none" }} />
                  <p className="text-xs text-[#64748B]">Proposed route</p>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Stop dialog */}
      {stopDialog && (
        <div className="fixed inset-0 bg-black/30 backdrop-blur-sm z-50 flex items-center justify-center p-4">
          <div className="bg-white rounded-2xl p-6 max-w-sm w-full shadow-[0_8px_40px_rgba(30,58,138,0.15)] border border-[rgba(30,58,138,0.10)]">
            <h2 className="text-lg font-800 text-[#0C1A3A] mb-2">Stop this trip?</h2>
            <p className="text-sm text-[#64748B] leading-relaxed mb-5">
              Your saved itinerary will remain intact. The live session will end and
              execution state will be cleared.
            </p>
            <div className="flex gap-2">
              <button
                onClick={() => setStopDialog(false)}
                className="flex-1 border border-[rgba(30,58,138,0.15)] text-[#0C1A3A] font-600 py-2.5 rounded-xl text-sm hover:bg-[#F8FAFC] transition-colors"
              >
                Keep going
              </button>
              <button
                onClick={() => setStopDialog(false)}
                className="flex-1 bg-[#DC2626] hover:bg-[#B91C1C] text-white font-600 py-2.5 rounded-xl text-sm transition-colors"
              >
                Stop trip
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="text-[10px] text-[#94A3B8] font-500 uppercase tracking-wide">{label}</p>
      <p className="text-sm font-700 text-[#0C1A3A] mt-0.5">{value}</p>
    </div>
  )
}

function ConnectionStatus({ state }: { state: LiveState }) {
  const configs: Record<LiveState, { label: string; color: string }> = {
    connected: { label: "Live trip connected", color: "#16A34A" },
    connecting: { label: "Preparing live trip connection…", color: "#D97706" },
    reconnecting: { label: "Reconnecting…", color: "#D97706" },
    "gps-denied": { label: "Location permission is required", color: "#DC2626" },
    "gps-stale": { label: "Live location is stale", color: "#D97706" },
  }
  const c = configs[state]
  return (
    <div className="flex items-center gap-2 mt-1">
      <span className="pulse-dot" style={{ background: c.color, width: 6, height: 6 }} />
      <p className="text-xs font-500" style={{ color: c.color }}>{c.label}</p>
    </div>
  )
}

function GpsStatusBanner({ state }: { state: LiveState }) {
  const configs: Record<string, { title: string; body: string; color: string; bg: string }> = {
    connecting: {
      title: "Preparing live trip connection…",
      body: "Establishing a live connection. This should take just a moment.",
      color: "#D97706", bg: "#FFFBEB",
    },
    reconnecting: {
      title: "Reconnecting",
      body: "Live rerouting is temporarily unavailable; the previous route remains visible.",
      color: "#D97706", bg: "#FFFBEB",
    },
    "gps-denied": {
      title: "Location permission required",
      body: "LiveRoute needs your location to show your position and navigate. Please allow location access.",
      color: "#DC2626", bg: "#FEF2F2",
    },
    "gps-stale": {
      title: "Waiting for a live location…",
      body: "Your last known location is shown. The map will update when a fresh signal is available.",
      color: "#D97706", bg: "#FFFBEB",
    },
  }
  const c = configs[state]
  if (!c) return null
  return (
    <div
      className="rounded-xl p-3 border shadow-sm"
      style={{ background: c.bg, borderColor: `${c.color}40` }}
    >
      <p className="text-sm font-600" style={{ color: c.color }}>{c.title}</p>
      <p className="text-xs mt-0.5" style={{ color: c.color, opacity: 0.8 }}>{c.body}</p>
    </div>
  )
}

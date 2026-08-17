import { useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import Nav from "../components/Nav"
import StatusPill from "../components/StatusPill"

type TripState = "loading" | "empty" | "list" | "active-and-list" | "error"

const savedTrips = [
  { id: 1, name: "Weekend in Vienna", stops: 5, updated: "Aug 12, 2026" },
  { id: 2, name: "Paris Museum Day", stops: 4, updated: "Aug 8, 2026" },
  { id: 3, name: "Berlin Street Art", stops: 6, updated: "Jul 30, 2026" },
]

const activeTrip = {
  name: "Amsterdam Canal Tour",
  stops: 4,
  started: "Today at 10:14 AM",
}

export default function Trips() {
  const [tripState, setTripState] = useState<TripState>("active-and-list")
  const navigate = useNavigate()

  return (
    <div className="min-h-screen bg-white">
      <Nav hasActiveTrip={tripState === "active-and-list"} />

      <main className="max-w-3xl mx-auto px-6 py-12">
        {/* Header */}
        <div className="flex items-start justify-between mb-10">
          <div>
            <p className="text-xs font-700 uppercase tracking-widest text-[#64748B] mb-1">
              Your itineraries
            </p>
            <h1 className="text-3xl font-800 text-[#0C1A3A] tracking-tight">Trips</h1>
            <p className="text-sm text-[#64748B] mt-1">
              Open a saved plan, or start shaping a new day.
            </p>
          </div>
          <Link
            to="/trips/new"
            className="bg-[#1D4ED8] hover:bg-[#1E40AF] text-white text-sm font-600 px-5 py-2.5 rounded-xl transition-colors"
          >
            New trip
          </Link>
        </div>

        {/* State picker (dev only) */}
        <div className="flex flex-wrap gap-2 mb-8 p-4 bg-[#F8FAFC] rounded-xl border border-[rgba(30,58,138,0.08)]">
          <p className="w-full text-xs font-600 text-[#64748B] mb-1">Preview state:</p>
          {(["loading", "empty", "list", "active-and-list", "error"] as TripState[]).map((s) => (
            <button
              key={s}
              onClick={() => setTripState(s)}
              className={`text-xs px-3 py-1.5 rounded-lg font-500 transition-colors ${
                tripState === s
                  ? "bg-[#1D4ED8] text-white"
                  : "bg-white border border-[rgba(30,58,138,0.12)] text-[#64748B] hover:text-[#0C1A3A]"
              }`}
            >
              {s}
            </button>
          ))}
        </div>

        {tripState === "loading" && <LoadingState />}
        {tripState === "empty" && <EmptyState />}
        {tripState === "list" && <TripList trips={savedTrips} onOpen={(id) => navigate(`/trips/planner?id=${id}`)} />}
        {tripState === "active-and-list" && (
          <>
            <ActiveTripCard trip={activeTrip} />
            <TripList trips={savedTrips} onOpen={(id) => navigate(`/trips/planner?id=${id}`)} />
          </>
        )}
        {tripState === "error" && <ErrorState onRetry={() => setTripState("active-and-list")} />}
      </main>
    </div>
  )
}

function ActiveTripCard({ trip }: { trip: typeof activeTrip }) {
  return (
    <div className="mb-10">
      <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-3">
        In progress
      </p>
      <div className="rounded-xl border border-[rgba(22,163,74,0.25)] bg-[#F0FDF4] p-5 shadow-[0_4px_24px_rgba(30,58,138,0.07)]">
        <div className="flex items-start justify-between gap-4 flex-wrap">
          <div className="flex items-start gap-4">
            <div className="w-10 h-10 rounded-xl bg-[#DCFCE7] flex items-center justify-center shrink-0">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none">
                <circle cx="12" cy="12" r="9" stroke="#16A34A" strokeWidth="1.5" />
                <path d="M8 12l2.5 2.5L16 9" stroke="#16A34A" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
            </div>
            <div>
              <h2 className="text-lg font-700 text-[#0C1A3A]">{trip.name}</h2>
              <p className="text-sm text-[#64748B] mt-0.5">Started {trip.started} · {trip.stops} stops</p>
              <div className="mt-2">
                <StatusPill status="active" />
              </div>
            </div>
          </div>
          <Link
            to="/live"
            className="bg-[#16A34A] hover:bg-[#15803D] text-white text-sm font-600 px-4 py-2.5 rounded-lg transition-colors"
          >
            Open live view
          </Link>
        </div>
      </div>
    </div>
  )
}

function TripList({ trips, onOpen }: { trips: typeof savedTrips; onOpen: (id: number) => void }) {
  return (
    <div>
      <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-3">
        Saved trips
      </p>
      <div className="space-y-2">
        {trips.map((trip) => (
          <button
            key={trip.id}
            onClick={() => onOpen(trip.id)}
            className="w-full text-left flex items-center gap-4 p-4 rounded-xl border border-[rgba(30,58,138,0.10)] bg-white hover:border-[rgba(30,58,138,0.25)] hover:shadow-[0_4px_24px_rgba(30,58,138,0.07)] transition-all group"
          >
            <div className="w-9 h-9 rounded-lg bg-[#EFF6FF] flex items-center justify-center shrink-0">
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
                <path d="M12 2C8.13 2 5 5.13 5 9c0 5.25 7 13 7 13s7-7.75 7-13c0-3.87-3.13-7-7-7zm0 9.5c-1.38 0-2.5-1.12-2.5-2.5s1.12-2.5 2.5-2.5 2.5 1.12 2.5 2.5-1.12 2.5-2.5 2.5z" fill="#1D4ED8" />
              </svg>
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-sm font-600 text-[#0C1A3A]">{trip.name}</p>
              <p className="text-xs text-[#64748B] mt-0.5">{trip.stops} stops · Updated {trip.updated}</p>
            </div>
            <StatusPill status="saved" />
            <svg
              width="16"
              height="16"
              viewBox="0 0 24 24"
              fill="none"
              className="text-[#CBD5E1] group-hover:text-[#1D4ED8] transition-colors shrink-0"
            >
              <path d="M9 18l6-6-6-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </button>
        ))}
      </div>
    </div>
  )
}

function EmptyState() {
  return (
    <div className="text-center py-16">
      <div className="w-20 h-20 mx-auto mb-6 rounded-2xl bg-[#EFF6FF] flex items-center justify-center">
        <svg width="36" height="36" viewBox="0 0 48 48" fill="none">
          <circle cx="24" cy="20" r="12" stroke="#1D4ED8" strokeWidth="2" fill="none" />
          <circle cx="24" cy="20" r="4" fill="#DBEAFE" stroke="#1D4ED8" strokeWidth="1.5" />
          <path d="M14 36 Q24 46 34 36" stroke="#1D4ED8" strokeWidth="2" strokeLinecap="round" fill="none" />
          <path d="M10 36 Q24 48 38 36" stroke="#BFDBFE" strokeWidth="1" strokeLinecap="round" fill="none" />
        </svg>
      </div>
      <h2 className="text-xl font-700 text-[#0C1A3A] mb-2">No saved trips yet</h2>
      <p className="text-sm text-[#64748B] max-w-xs mx-auto mb-6">
        Create your first itinerary — add stops, set times, and save your plan.
      </p>
      <Link
        to="/trips/new"
        className="inline-flex items-center gap-2 bg-[#1D4ED8] hover:bg-[#1E40AF] text-white text-sm font-600 px-5 py-2.5 rounded-xl transition-colors"
      >
        Plan your first trip
      </Link>
    </div>
  )
}

function LoadingState() {
  return (
    <div className="space-y-3">
      <div className="skeleton h-3 w-28 mb-4" />
      <div className="skeleton h-24 rounded-xl" />
      <div className="skeleton h-3 w-24 mt-8 mb-4" />
      {[1, 2, 3].map((i) => (
        <div key={i} className="skeleton h-16 rounded-xl" />
      ))}
    </div>
  )
}

function ErrorState({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="text-center py-16">
      <div className="w-16 h-16 mx-auto mb-5 rounded-2xl bg-[#FEF2F2] flex items-center justify-center">
        <svg width="28" height="28" viewBox="0 0 24 24" fill="none">
          <circle cx="12" cy="12" r="9" stroke="#DC2626" strokeWidth="1.5" />
          <path d="M12 8v4M12 16v.5" stroke="#DC2626" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      </div>
      <h2 className="text-lg font-700 text-[#0C1A3A] mb-2">We couldn't load your trips.</h2>
      <p className="text-sm text-[#64748B] mb-6">Connection interrupted. Your saved trips are still there.</p>
      <button
        onClick={onRetry}
        className="bg-[#1D4ED8] hover:bg-[#1E40AF] text-white text-sm font-600 px-5 py-2.5 rounded-xl transition-colors"
      >
        Try again
      </button>
    </div>
  )
}

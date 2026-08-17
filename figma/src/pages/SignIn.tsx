import { useState } from "react"
import { useNavigate } from "react-router-dom"
import Logo from "../components/Logo"
import MapPlaceholder from "../components/MapPlaceholder"

type SignInState = "idle" | "loading" | "error" | "unavailable" | "success"

export default function SignIn() {
  const [state, setState] = useState<SignInState>("idle")
  const navigate = useNavigate()

  function handleSignIn() {
    setState("loading")
    setTimeout(() => {
      setState("success")
      setTimeout(() => navigate("/trips"), 800)
    }, 1600)
  }

  return (
    <div className="min-h-screen grid lg:grid-cols-2">
      {/* Left panel */}
      <div className="flex flex-col justify-center px-8 py-16 lg:px-16">
        <div className="max-w-sm mx-auto w-full">
          <div className="mb-10">
            <Logo size="md" />
          </div>

          <h1 className="text-3xl font-800 text-[#0C1A3A] tracking-tight leading-tight mb-3">
            Build the plan.<br />Keep the flexibility.
          </h1>
          <p className="text-sm text-[#64748B] leading-relaxed mb-10">
            Sign in to create reusable itineraries and adapt them when travel conditions change.
            Your plan stays authoritative — you decide every change.
          </p>

          {state === "idle" && (
            <button
              onClick={handleSignIn}
              className="w-full flex items-center justify-center gap-3 bg-[#1D4ED8] hover:bg-[#1E40AF] text-white font-600 py-3 rounded-xl transition-colors text-sm"
            >
              <GoogleIcon />
              Sign in with Google
            </button>
          )}

          {state === "loading" && (
            <div className="w-full flex items-center justify-center gap-3 bg-[#1D4ED8] text-white font-600 py-3 rounded-xl text-sm opacity-80">
              <Spinner />
              Signing in…
            </div>
          )}

          {state === "success" && (
            <div className="w-full flex items-center justify-center gap-2 bg-[#16A34A] text-white font-600 py-3 rounded-xl text-sm">
              <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                <path d="M3 8l3.5 3.5L13 5" stroke="white" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
              Signed in — loading your trips…
            </div>
          )}

          {state === "error" && (
            <div className="space-y-3">
              <div className="bg-[#FEF2F2] border border-[#FECACA] rounded-xl p-4 text-sm text-[#DC2626]">
                Sign-in failed. Please try again.
              </div>
              <button
                onClick={() => setState("idle")}
                className="w-full border border-[rgba(30,58,138,0.15)] text-[#0C1A3A] font-600 py-3 rounded-xl hover:bg-[#F8FAFC] transition-colors text-sm"
              >
                Try again
              </button>
            </div>
          )}

          {state === "unavailable" && (
            <div className="space-y-3">
              <div className="bg-[#FFFBEB] border border-[#FDE68A] rounded-xl p-4 text-sm text-[#D97706]">
                Sign-in is temporarily unavailable. Please try again in a moment.
              </div>
              <button
                onClick={() => setState("idle")}
                className="w-full border border-[rgba(30,58,138,0.15)] text-[#0C1A3A] font-600 py-3 rounded-xl hover:bg-[#F8FAFC] transition-colors text-sm"
              >
                Retry
              </button>
            </div>
          )}

          {state === "idle" && (
            <div className="mt-4 flex gap-3">
              <button
                onClick={() => setState("error")}
                className="text-xs text-[#94A3B8] hover:text-[#64748B]"
              >
                Simulate error
              </button>
              <button
                onClick={() => setState("unavailable")}
                className="text-xs text-[#94A3B8] hover:text-[#64748B]"
              >
                Simulate unavailable
              </button>
            </div>
          )}
        </div>
      </div>

      {/* Right panel — map preview */}
      <div className="hidden lg:flex flex-col bg-[#EFF6FF] p-10 relative overflow-hidden">
        <div className="absolute inset-0 opacity-20">
          <div className="absolute inset-0" style={{
            backgroundImage: "radial-gradient(circle at center, rgba(30,58,138,0.3) 1px, transparent 1.5px)",
            backgroundSize: "24px 24px"
          }} />
        </div>
        <div className="relative flex-1 flex flex-col gap-6 items-center justify-center">
          <MapPlaceholder className="w-full max-w-md aspect-[4/3]" />
          <div className="bg-white rounded-xl p-4 border border-[rgba(30,58,138,0.10)] shadow-[0_4px_24px_rgba(30,58,138,0.07)] w-full max-w-md">
            <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-2">
              Example itinerary · Saturday
            </p>
            {[
              { n: 1, label: "City Museum", sub: "10:00 AM · 2 hr" },
              { n: 2, label: "Garden Café", sub: "12:30 PM · 1 hr" },
              { n: 3, label: "Riverside Park", sub: "2:00 PM · 1.5 hr" },
              { n: 4, label: "Old Town Hall", sub: "4:00 PM · 45 min" },
            ].map((a) => (
              <div key={a.n} className="flex items-center gap-3 py-2.5 border-b border-[rgba(30,58,138,0.07)] last:border-0">
                <span className="w-6 h-6 rounded-full bg-[#EFF6FF] text-[#1D4ED8] text-xs font-700 flex items-center justify-center shrink-0">
                  {a.n}
                </span>
                <div>
                  <p className="text-sm font-600 text-[#0C1A3A]">{a.label}</p>
                  <p className="text-xs text-[#64748B]">{a.sub}</p>
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

function GoogleIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
      <circle cx="12" cy="12" r="11" fill="white" opacity="0.15" />
      <path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="white" />
      <path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l3.66-2.84z" fill="white" opacity="0.85" />
      <path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="white" opacity="0.85" />
      <path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z" fill="white" />
    </svg>
  )
}

function Spinner() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" className="animate-spin">
      <circle cx="12" cy="12" r="9" stroke="white" strokeWidth="2.5" strokeOpacity="0.25" />
      <path d="M12 3a9 9 0 0 1 9 9" stroke="white" strokeWidth="2.5" strokeLinecap="round" />
    </svg>
  )
}

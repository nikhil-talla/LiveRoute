import { Link } from "react-router-dom"
import Logo from "../components/Logo"
import MapPlaceholder from "../components/MapPlaceholder"

const steps = [
  {
    n: "01",
    title: "Plan your day",
    body: "Add your stops, set times, and arrange activities in the order that makes sense to you.",
  },
  {
    n: "02",
    title: "Start when you're ready",
    body: "Press Go when the day begins. LiveRoute activates only when you decide to.",
  },
  {
    n: "03",
    title: "Get suggestions when conditions change",
    body: "If your schedule shifts, LiveRoute may suggest adjustments — but never changes anything on its own.",
  },
  {
    n: "04",
    title: "Decide whether to accept them",
    body: "You review every proposed change and choose to accept it or keep your original plan.",
  },
]

export default function Landing() {
  return (
    <div className="min-h-screen bg-white">
      {/* Header */}
      <header className="sticky top-0 z-50 bg-white/90 backdrop-blur-sm border-b border-[rgba(30,58,138,0.10)]">
        <div className="max-w-6xl mx-auto px-6 h-14 flex items-center justify-between">
          <Logo />
          <Link
            to="/signin"
            className="text-sm font-600 text-[#1D4ED8] hover:text-[#1E40AF] transition-colors"
          >
            Sign in
          </Link>
        </div>
      </header>

      {/* Hero */}
      <section className="relative overflow-hidden bg-gradient-to-b from-[#EFF6FF] to-white pt-24 pb-20 px-6">
        <div className="max-w-6xl mx-auto grid lg:grid-cols-2 gap-16 items-center">
          <div>
            <div className="inline-block text-xs font-700 uppercase tracking-widest text-[#1D4ED8] bg-[#DBEAFE] px-3 py-1 rounded-full mb-6">
              Trip planning, reimagined
            </div>
            <h1 className="text-5xl lg:text-6xl font-800 text-[#0C1A3A] leading-[1.08] tracking-tight mb-6">
              Plans that move<br />
              <span className="text-[#1D4ED8]">with you.</span>
            </h1>
            <p className="text-lg text-[#64748B] font-400 leading-relaxed max-w-[480px] mb-8">
              Build a reusable day itinerary. When travel conditions change,
              LiveRoute suggests adjustments — and you decide whether to accept them.
              Your plan stays yours.
            </p>
            <div className="flex flex-col sm:flex-row gap-3">
              <Link
                to="/signin"
                className="inline-flex items-center justify-center gap-2.5 bg-[#1D4ED8] hover:bg-[#1E40AF] text-white font-600 px-6 py-3 rounded-xl transition-colors text-sm"
              >
                <GoogleIcon />
                Sign in with Google
              </Link>
              <a
                href="#how-it-works"
                className="inline-flex items-center justify-center gap-2 text-[#64748B] hover:text-[#0C1A3A] font-500 px-6 py-3 rounded-xl border border-[rgba(30,58,138,0.15)] transition-colors text-sm"
              >
                See how it works
              </a>
            </div>
          </div>

          <div className="hidden lg:block">
            <div className="relative">
              <MapPlaceholder className="w-full aspect-[4/3]" />
              {/* Floating itinerary card */}
              <div
                className="absolute -bottom-4 -left-4 bg-white rounded-xl p-4 shadow-[0_4px_24px_rgba(30,58,138,0.10)] border border-[rgba(30,58,138,0.10)] w-56"
              >
                <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-2.5">
                  Today's itinerary
                </p>
                {[
                  { n: 1, label: "City Museum", time: "10:00 AM" },
                  { n: 2, label: "Garden Café", time: "12:30 PM" },
                  { n: 3, label: "Riverside Park", time: "2:00 PM" },
                ].map((a) => (
                  <div key={a.n} className="flex items-center gap-2.5 py-1.5 border-b border-[rgba(30,58,138,0.07)] last:border-0">
                    <span className="w-5 h-5 rounded-full bg-[#EFF6FF] text-[#1D4ED8] text-[10px] font-700 flex items-center justify-center shrink-0">
                      {a.n}
                    </span>
                    <div className="min-w-0">
                      <p className="text-xs font-600 text-[#0C1A3A] truncate">{a.label}</p>
                      <p className="text-[10px] text-[#64748B]">{a.time}</p>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* How it works */}
      <section id="how-it-works" className="py-24 px-6">
        <div className="max-w-6xl mx-auto">
          <div className="max-w-lg mb-16">
            <p className="text-xs font-700 uppercase tracking-widest text-[#1D4ED8] mb-4">
              How LiveRoute works
            </p>
            <h2 className="text-4xl font-800 text-[#0C1A3A] tracking-tight leading-[1.15]">
              Build the plan.<br />Keep the flexibility.
            </h2>
          </div>

          <div className="grid md:grid-cols-2 lg:grid-cols-4 gap-6">
            {steps.map((s) => (
              <div
                key={s.n}
                className="p-6 rounded-xl border border-[rgba(30,58,138,0.10)] bg-white hover:shadow-[0_4px_24px_rgba(30,58,138,0.07)] transition-shadow"
              >
                <p className="text-3xl font-800 text-[#DBEAFE] mb-4">{s.n}</p>
                <h3 className="text-base font-700 text-[#0C1A3A] mb-2">{s.title}</h3>
                <p className="text-sm text-[#64748B] leading-relaxed">{s.body}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Control section */}
      <section className="py-24 px-6 bg-[#F8FAFF]">
        <div className="max-w-6xl mx-auto grid lg:grid-cols-2 gap-16 items-center">
          <div>
            <p className="text-xs font-700 uppercase tracking-widest text-[#1D4ED8] mb-4">
              Always your call
            </p>
            <h2 className="text-4xl font-800 text-[#0C1A3A] tracking-tight leading-[1.15] mb-6">
              You're in control,<br />every step of the way.
            </h2>
            <p className="text-base text-[#64748B] leading-relaxed mb-6">
              LiveRoute never silently changes your itinerary. When conditions shift and a
              reroute might help, you'll see a clear proposal — with exactly what would change
              and what would stay the same.
            </p>
            <ul className="space-y-3">
              {[
                "Your current plan stays active until you accept a change",
                "See exactly which activities would shift before deciding",
                "One tap to keep your original plan",
              ].map((item) => (
                <li key={item} className="flex items-start gap-3 text-sm text-[#0C1A3A]">
                  <span className="w-5 h-5 rounded-full bg-[#DBEAFE] text-[#1D4ED8] flex items-center justify-center shrink-0 mt-0.5">
                    <svg width="10" height="10" viewBox="0 0 10 10" fill="none">
                      <path d="M2 5l2.5 2.5L8 3" stroke="#1D4ED8" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
                    </svg>
                  </span>
                  {item}
                </li>
              ))}
            </ul>
          </div>
          <div className="space-y-4">
            {/* Proposal card mock */}
            <div className="bg-white rounded-xl border border-[rgba(30,58,138,0.12)] shadow-[0_4px_24px_rgba(30,58,138,0.07)] p-5">
              <p className="text-[10px] font-700 uppercase tracking-widest text-[#64748B] mb-1">
                Suggested change
              </p>
              <p className="text-base font-700 text-[#0C1A3A] mb-3">Review the proposed plan</p>
              <p className="text-sm text-[#64748B] mb-4">
                Traffic near Riverside Park is heavier than expected. This suggestion keeps
                2 activities in place and adjusts the remaining route.
              </p>
              <p className="text-xs text-[#64748B] italic mb-4">
                Your current plan remains unchanged until you accept this suggestion.
              </p>
              <div className="flex gap-2">
                <button className="flex-1 bg-[#1D4ED8] text-white text-sm font-600 py-2 rounded-lg hover:bg-[#1E40AF] transition-colors">
                  Accept suggestion
                </button>
                <button className="flex-1 border border-[rgba(30,58,138,0.15)] text-[#0C1A3A] text-sm font-600 py-2 rounded-lg hover:bg-[#F8FAFC] transition-colors">
                  Keep current plan
                </button>
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* Secondary CTA */}
      <section className="py-24 px-6 text-center">
        <div className="max-w-xl mx-auto">
          <h2 className="text-4xl font-800 text-[#0C1A3A] tracking-tight mb-4">
            Ready to plan your next trip?
          </h2>
          <p className="text-base text-[#64748B] mb-8">
            Sign in with Google to get started. Free to use, no credit card required.
          </p>
          <Link
            to="/signin"
            className="inline-flex items-center justify-center gap-2.5 bg-[#1D4ED8] hover:bg-[#1E40AF] text-white font-600 px-8 py-3.5 rounded-xl transition-colors"
          >
            <GoogleIcon />
            Sign in with Google
          </Link>
        </div>
      </section>

      {/* Footer */}
      <footer className="border-t border-[rgba(30,58,138,0.10)] py-8 px-6">
        <div className="max-w-6xl mx-auto flex flex-col sm:flex-row items-center justify-between gap-4">
          <Logo size="sm" />
          <div className="flex items-center gap-6 text-xs text-[#64748B]">
            <a href="#" className="hover:text-[#0C1A3A] transition-colors">Privacy</a>
            <a href="#" className="hover:text-[#0C1A3A] transition-colors">Terms</a>
            <a href="#" className="hover:text-[#0C1A3A] transition-colors">Contact</a>
          </div>
          <p className="text-xs text-[#94A3B8]">© 2026 LiveRoute</p>
        </div>
      </footer>
    </div>
  )
}

function GoogleIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
      <path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z" fill="#ffffff" opacity="0.9" />
      <path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#ffffff" opacity="0.9" />
      <path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z" fill="#ffffff" opacity="0.9" />
      <path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="#ffffff" opacity="0.9" />
    </svg>
  )
}

import { Logo } from '../components/TopBar'

interface LandingPageProps {
  onSignIn: () => void
}

const steps = [
  {
    n: '01',
    title: 'Plan your day',
    body: 'Add stops, set travel preferences, and shape your itinerary exactly the way you want it.',
  },
  {
    n: '02',
    title: 'Start when you\'re ready',
    body: 'Press Go when you\'re set. LiveRoute activates your plan and follows your route in real time.',
  },
  {
    n: '03',
    title: 'Get suggestions when conditions change',
    body: 'If something shifts — traffic, timing, a closed venue — LiveRoute may suggest an adjustment.',
  },
  {
    n: '04',
    title: 'Decide whether to accept',
    body: 'Every suggested change needs your approval. Your current plan stays exactly as-is until you say so.',
  },
]

const mockStops = [
  { label: 'Ferry Building', time: '9:00 AM', done: true },
  { label: 'SFMOMA', time: '10:30 AM', done: false, active: true },
  { label: 'Tartine Bakery', time: '12:30 PM', done: false },
  { label: 'Dolores Park', time: '2:00 PM', done: false },
]

export default function LandingPage({ onSignIn }: LandingPageProps) {
  return (
    <div className="min-h-screen bg-white" style={{ fontFamily: "'Plus Jakarta Sans', sans-serif" }}>
      {/* Nav */}
      <header
        className="flex items-center justify-between px-6 lg:px-10 border-b sticky top-0 bg-white z-10"
        style={{
          borderColor: 'rgba(30,58,138,0.10)',
          paddingTop: 'max(16px, env(safe-area-inset-top))',
          paddingBottom: '16px',
        }}
      >
        <Logo />
        <button
          onClick={onSignIn}
          className="flex items-center gap-2 text-sm font-600 px-4 py-2 rounded-xl text-white transition-opacity hover:opacity-90 cursor-pointer"
          style={{ background: '#1D4ED8' }}
        >
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
            <rect x="2" y="4" width="12" height="9" rx="1.5" stroke="white" strokeWidth="1.4" />
            <path d="M2 6.5l6 4 6-4" stroke="white" strokeWidth="1.4" />
          </svg>
          Sign in with Google
        </button>
      </header>

      {/* Hero */}
      <section
        className="px-6 lg:px-10 pt-20 pb-16"
        style={{ background: 'linear-gradient(180deg, #EFF6FF 0%, #FFFFFF 100%)' }}
      >
        <div className="max-w-5xl mx-auto grid lg:grid-cols-2 gap-12 items-center">
          <div>
            <div
              className="inline-flex items-center gap-2 px-3 py-1.5 rounded-full text-xs font-600 mb-6"
              style={{ background: '#DBEAFE', color: '#1D4ED8' }}
            >
              <span className="w-1.5 h-1.5 rounded-full" style={{ background: '#1D4ED8' }} />
              Now in early access
            </div>
            <h1
              className="text-4xl lg:text-5xl font-800 leading-tight mb-5"
              style={{ color: '#0C1A3A' }}
            >
              Plans that move<br />with you.
            </h1>
            <p className="text-lg mb-8 leading-relaxed" style={{ color: '#64748B' }}>
              Save an itinerary, start it when you're ready, and adapt when travel conditions change — without losing control of your plan.
            </p>
            <button
              onClick={onSignIn}
              className="flex items-center gap-3 px-6 py-3.5 rounded-xl text-base font-600 text-white transition-opacity hover:opacity-90 cursor-pointer"
              style={{ background: '#1D4ED8' }}
            >
              <svg width="18" height="18" viewBox="0 0 18 18" fill="none">
                <path d="M9 2a5 5 0 100 10A5 5 0 009 2zM3.5 16c0-2 2.5-4 5.5-4s5.5 2 5.5 4" stroke="white" strokeWidth="1.5" strokeLinecap="round" />
              </svg>
              Get started with Google
            </button>
            <p className="mt-3 text-xs" style={{ color: '#94A3B8' }}>
              No credit card required. Free to use during early access.
            </p>
          </div>

          {/* Itinerary card mockup */}
          <div className="relative">
            <div
              className="rounded-2xl border bg-white p-5 max-w-sm mx-auto lg:ml-auto"
              style={{
                boxShadow: '0 8px 40px rgba(30,58,138,0.10)',
                borderColor: 'rgba(30,58,138,0.12)',
              }}
            >
              <div className="flex items-center justify-between mb-4">
                <div>
                  <div className="text-xs font-600 uppercase tracking-wide mb-0.5" style={{ color: '#94A3B8' }}>
                    Saturday itinerary
                  </div>
                  <div className="text-sm font-700" style={{ color: '#0C1A3A' }}>SF Arts Day</div>
                </div>
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
              </div>

              {/* Route line */}
              <div className="space-y-0">
                {mockStops.map((stop, i) => (
                  <div key={i} className="flex gap-3">
                    <div className="flex flex-col items-center">
                      <div
                        className="w-5 h-5 rounded-full flex items-center justify-center text-xs font-700 shrink-0 mt-0.5"
                        style={{
                          background: stop.done ? '#DBEAFE' : stop.active ? '#1D4ED8' : '#F1F5F9',
                          color: stop.done ? '#1D4ED8' : stop.active ? '#FFFFFF' : '#94A3B8',
                        }}
                      >
                        {stop.done ? (
                          <svg width="10" height="10" viewBox="0 0 10 10" fill="none">
                            <path d="M2 5l2.5 2.5L8 3" stroke="#1D4ED8" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
                          </svg>
                        ) : (
                          i + 1
                        )}
                      </div>
                      {i < mockStops.length - 1 && (
                        <div className="w-px flex-1 my-1" style={{ background: stop.done ? '#DBEAFE' : 'rgba(30,58,138,0.12)', minHeight: 20 }} />
                      )}
                    </div>
                    <div className={`pb-4 ${i === mockStops.length - 1 ? 'pb-0' : ''}`}>
                      <div
                        className="text-sm font-500 leading-tight"
                        style={{ color: stop.active ? '#0C1A3A' : stop.done ? '#94A3B8' : '#64748B', fontWeight: stop.active ? 600 : 500 }}
                      >
                        {stop.label}
                      </div>
                      <div className="text-xs mt-0.5" style={{ color: '#94A3B8' }}>{stop.time}</div>
                    </div>
                  </div>
                ))}
              </div>

              {/* Map placeholder */}
              <div
                className="mt-4 rounded-xl overflow-hidden h-28 flex items-center justify-center relative"
                style={{ background: '#EFF6FF' }}
              >
                <svg width="100%" height="100%" viewBox="0 0 320 112" preserveAspectRatio="none" fill="none">
                  <rect width="320" height="112" fill="#EFF6FF" />
                  <path d="M20 80 Q80 40 140 60 Q200 80 260 30 Q290 15 300 20" stroke="#DBEAFE" strokeWidth="8" strokeLinecap="round" fill="none" />
                  <path d="M20 80 Q80 40 140 60 Q200 80 260 30" stroke="#1D4ED8" strokeWidth="2.5" strokeLinecap="round" strokeDasharray="6 4" fill="none" />
                  <circle cx="20" cy="80" r="5" fill="#16A34A" />
                  <circle cx="140" cy="60" r="5" fill="#1D4ED8" />
                  <circle cx="260" cy="30" r="5" fill="#64748B" />
                </svg>
                <div className="absolute bottom-2 right-2">
                  <span
                    className="text-xs font-600 px-2 py-1 rounded-lg"
                    style={{ background: 'white', color: '#1D4ED8', boxShadow: '0 2px 8px rgba(30,58,138,0.10)' }}
                  >
                    4 stops · 8.2 mi
                  </span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* How it works */}
      <section className="px-6 lg:px-10 py-20">
        <div className="max-w-5xl mx-auto">
          <div className="text-center mb-14">
            <div
              className="text-xs font-600 uppercase tracking-widest mb-3"
              style={{ color: '#1D4ED8' }}
            >
              How it works
            </div>
            <h2 className="text-3xl font-800" style={{ color: '#0C1A3A' }}>
              Build the plan. Keep the flexibility.
            </h2>
          </div>

          <div className="grid sm:grid-cols-2 lg:grid-cols-4 gap-6">
            {steps.map((step) => (
              <div
                key={step.n}
                className="rounded-2xl border p-6"
                style={{
                  borderColor: 'rgba(30,58,138,0.12)',
                  boxShadow: '0 4px 24px rgba(30,58,138,0.05)',
                }}
              >
                <div
                  className="text-2xl font-800 mb-4"
                  style={{ color: '#DBEAFE', letterSpacing: '-0.02em' }}
                >
                  {step.n}
                </div>
                <h3 className="text-sm font-700 mb-2" style={{ color: '#0C1A3A' }}>
                  {step.title}
                </h3>
                <p className="text-sm leading-relaxed" style={{ color: '#64748B' }}>
                  {step.body}
                </p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Control section */}
      <section
        className="px-6 lg:px-10 py-16 mx-6 lg:mx-10 rounded-2xl mb-20"
        style={{ background: '#EFF6FF', border: '1px solid rgba(30,58,138,0.10)' }}
      >
        <div className="max-w-2xl mx-auto text-center">
          <div
            className="w-12 h-12 rounded-2xl flex items-center justify-center mx-auto mb-5"
            style={{ background: '#DBEAFE' }}
          >
            <svg width="22" height="22" viewBox="0 0 22 22" fill="none">
              <path d="M11 3l7 4v7l-7 4-7-4V7l7-4Z" stroke="#1D4ED8" strokeWidth="1.6" strokeLinejoin="round" />
              <circle cx="11" cy="11" r="2.5" fill="#1D4ED8" />
            </svg>
          </div>
          <h2 className="text-2xl font-800 mb-3" style={{ color: '#0C1A3A' }}>
            You stay in control
          </h2>
          <p className="text-base leading-relaxed mb-8" style={{ color: '#64748B' }}>
            LiveRoute never silently changes your itinerary. If conditions shift and a new route might work better, we'll show you the suggestion — and wait for you to decide. Your current plan stays exactly as-is until you accept a change.
          </p>
          <button
            onClick={onSignIn}
            className="inline-flex items-center gap-2 px-6 py-3 rounded-xl text-sm font-600 text-white cursor-pointer hover:opacity-90 transition-opacity"
            style={{ background: '#1D4ED8' }}
          >
            Sign in with Google to get started
          </button>
        </div>
      </section>

      {/* Footer */}
      <footer
        className="px-6 lg:px-10 py-8 border-t"
        style={{ borderColor: 'rgba(30,58,138,0.10)' }}
      >
        <div className="max-w-5xl mx-auto flex flex-col sm:flex-row items-center justify-between gap-4">
          <Logo />
          <div className="flex items-center gap-6 text-xs" style={{ color: '#94A3B8' }}>
            <a href="#" className="hover:text-slate-600 transition-colors">Privacy</a>
            <a href="#" className="hover:text-slate-600 transition-colors">Terms</a>
            <a href="#" className="hover:text-slate-600 transition-colors">Help</a>
            <span>© 2026 LiveRoute</span>
          </div>
        </div>
      </footer>
    </div>
  )
}

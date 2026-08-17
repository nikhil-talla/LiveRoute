import { useState } from 'react'
import { Logo } from '../components/TopBar'

type SignInState = 'idle' | 'loading' | 'error' | 'unavailable'

interface SignInPageProps {
  onSignIn: () => void
  onBack: () => void
}

const mockStops = [
  { label: 'Golden Gate Park', time: '10:00 AM', active: false, done: true },
  { label: 'Haight-Ashbury', time: '11:30 AM', active: true, done: false },
  { label: 'Mission Dolores', time: '1:00 PM', active: false, done: false },
  { label: 'The Castro', time: '2:30 PM', active: false, done: false },
]

export default function SignInPage({ onSignIn, onBack }: SignInPageProps) {
  const [state, setState] = useState<SignInState>('idle')

  const handleSignIn = () => {
    setState('loading')
    setTimeout(() => {
      onSignIn()
    }, 1200)
  }

  const errorMessage =
    state === 'error'
      ? "We couldn't sign you in. Please try again."
      : state === 'unavailable'
      ? 'Sign-in is temporarily unavailable. Try again in a moment.'
      : null

  return (
    <div className="min-h-screen flex" style={{ fontFamily: "'Plus Jakarta Sans', sans-serif" }}>
      {/* Left panel */}
      <div className="flex-1 flex flex-col px-8 lg:px-16 py-10">
        <button
          onClick={onBack}
          className="flex items-center gap-1.5 text-sm mb-10 cursor-pointer transition-colors w-fit"
          style={{ color: '#64748B' }}
          onMouseEnter={(e) => (e.currentTarget.style.color = '#0C1A3A')}
          onMouseLeave={(e) => (e.currentTarget.style.color = '#64748B')}
        >
          <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
            <path d="M9 2L4 7l5 5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          Back
        </button>

        <div className="flex-1 flex flex-col justify-center max-w-sm">
          <div className="mb-8">
            <Logo />
          </div>

          <h1 className="text-3xl font-800 mb-3 leading-tight" style={{ color: '#0C1A3A' }}>
            Build the plan.<br />Keep the flexibility.
          </h1>
          <p className="text-sm leading-relaxed mb-8" style={{ color: '#64748B' }}>
            Sign in to save itineraries, start live trips, and adapt when conditions change — without losing control of your plan.
          </p>

          {errorMessage && (
            <div
              className="flex items-start gap-2.5 px-4 py-3 rounded-xl mb-5 text-sm"
              style={{ background: '#FEE2E2', color: '#DC2626', border: '1px solid rgba(220,38,38,0.15)' }}
            >
              <svg width="15" height="15" viewBox="0 0 15 15" fill="none" className="mt-0.5 shrink-0">
                <circle cx="7.5" cy="7.5" r="6.5" stroke="currentColor" strokeWidth="1.4" />
                <line x1="7.5" y1="4" x2="7.5" y2="8.5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
                <circle cx="7.5" cy="10.5" r="0.75" fill="currentColor" />
              </svg>
              {errorMessage}
            </div>
          )}

          <button
            onClick={handleSignIn}
            disabled={state === 'loading'}
            className="w-full flex items-center justify-center gap-3 px-5 py-3.5 rounded-xl text-sm font-600 border transition-all cursor-pointer"
            style={{
              background: state === 'loading' ? '#EFF6FF' : 'white',
              color: state === 'loading' ? '#94A3B8' : '#0C1A3A',
              borderColor: 'rgba(30,58,138,0.18)',
              boxShadow: '0 2px 8px rgba(30,58,138,0.06)',
              cursor: state === 'loading' ? 'not-allowed' : 'pointer',
            }}
          >
            {state === 'loading' ? (
              <>
                <svg className="animate-spin" width="16" height="16" viewBox="0 0 16 16" fill="none">
                  <circle cx="8" cy="8" r="6" stroke="#DBEAFE" strokeWidth="2" />
                  <path d="M8 2a6 6 0 016 6" stroke="#1D4ED8" strokeWidth="2" strokeLinecap="round" />
                </svg>
                Signing in…
              </>
            ) : (
              <>
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                  <path d="M15.5 8.17c0-.57-.05-1.12-.14-1.65H8v3.12h4.2a3.6 3.6 0 01-1.56 2.36v1.96h2.53C14.78 12.4 15.5 10.43 15.5 8.17z" fill="#4285F4" />
                  <path d="M8 16c2.1 0 3.87-.7 5.16-1.89l-2.52-1.96c-.7.47-1.6.75-2.64.75-2.03 0-3.75-1.37-4.36-3.22H1.05v2.02A7.99 7.99 0 008 16z" fill="#34A853" />
                  <path d="M3.64 9.68A4.8 4.8 0 013.38 8c0-.59.1-1.16.26-1.7V4.28H1.05A8 8 0 000 8c0 1.3.3 2.52.83 3.62l2.8-1.94z" fill="#FBBC05" />
                  <path d="M8 3.2c1.14 0 2.17.39 2.97 1.16l2.23-2.23A7.98 7.98 0 008 0a8 8 0 00-6.95 4.03l2.6 2.02C4.25 4.48 5.97 3.2 8 3.2z" fill="#EA4335" />
                </svg>
                Continue with Google
              </>
            )}
          </button>

          {(state === 'error' || state === 'unavailable') && (
            <button
              onClick={() => setState('idle')}
              className="mt-3 w-full text-sm font-500 py-2 rounded-xl text-center cursor-pointer transition-colors"
              style={{ color: '#1D4ED8' }}
            >
              Try again
            </button>
          )}

          <p className="mt-6 text-xs text-center leading-relaxed" style={{ color: '#94A3B8' }}>
            By signing in you agree to our{' '}
            <a href="#" style={{ color: '#64748B' }}>Terms</a> and{' '}
            <a href="#" style={{ color: '#64748B' }}>Privacy Policy</a>.
          </p>
        </div>
      </div>

      {/* Right panel — itinerary preview, desktop only */}
      <div
        className="hidden lg:flex flex-col w-96 xl:w-[480px] border-l"
        style={{
          background: 'linear-gradient(160deg, #EFF6FF 0%, #FFFFFF 60%)',
          borderColor: 'rgba(30,58,138,0.12)',
        }}
      >
        <div className="flex-1 flex items-center justify-center p-10">
          <div
            className="w-full rounded-2xl border bg-white p-6"
            style={{
              boxShadow: '0 8px 40px rgba(30,58,138,0.09)',
              borderColor: 'rgba(30,58,138,0.12)',
            }}
          >
            <div className="flex items-center justify-between mb-5">
              <div>
                <div className="text-xs font-600 uppercase tracking-wide mb-0.5" style={{ color: '#94A3B8' }}>
                  Your itinerary
                </div>
                <div className="text-sm font-700" style={{ color: '#0C1A3A' }}>SF Afternoon Loop</div>
              </div>
              <span
                className="text-xs font-600 px-2.5 py-1 rounded-lg"
                style={{ background: '#F1F5F9', color: '#64748B' }}
              >
                Saved
              </span>
            </div>

            {/* Map mini */}
            <div
              className="rounded-xl mb-5 overflow-hidden"
              style={{ height: 130, background: '#EFF6FF' }}
            >
              <svg width="100%" height="130" viewBox="0 0 340 130" preserveAspectRatio="none" fill="none">
                <rect width="340" height="130" fill="#EFF6FF" />
                <path d="M30 100 Q100 50 180 70 Q240 85 310 25" stroke="#DBEAFE" strokeWidth="10" strokeLinecap="round" fill="none" />
                <path d="M30 100 Q100 50 180 70 Q240 85 310 25" stroke="#1D4ED8" strokeWidth="2" strokeLinecap="round" fill="none" />
                {[{cx:30,cy:100},{cx:180,cy:70},{cx:250,cy:55},{cx:310,cy:25}].map((pt,i)=>(
                  <circle key={i} cx={pt.cx} cy={pt.cy} r="5" fill={i===1?'#1D4ED8':'#94A3B8'} />
                ))}
              </svg>
            </div>

            {/* Stop list */}
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
                        <svg width="9" height="9" viewBox="0 0 9 9" fill="none">
                          <path d="M1.5 4.5l2 2L7.5 2" stroke="#1D4ED8" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
                        </svg>
                      ) : i + 1}
                    </div>
                    {i < mockStops.length - 1 && (
                      <div className="w-px flex-1 my-1" style={{ background: 'rgba(30,58,138,0.10)', minHeight: 18 }} />
                    )}
                  </div>
                  <div className={`pb-3.5 ${i === mockStops.length - 1 ? 'pb-0' : ''}`}>
                    <div
                      className="text-sm leading-tight"
                      style={{
                        color: stop.done ? '#94A3B8' : '#0C1A3A',
                        fontWeight: stop.active ? 600 : 500,
                      }}
                    >
                      {stop.label}
                    </div>
                    <div className="text-xs mt-0.5" style={{ color: '#94A3B8' }}>{stop.time}</div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
        <div className="px-10 pb-10 text-center text-xs leading-relaxed" style={{ color: '#94A3B8' }}>
          Your saved itineraries and live trips<br />appear here once you sign in.
        </div>
      </div>
    </div>
  )
}

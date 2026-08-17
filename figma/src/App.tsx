import { useState } from 'react'
import LandingPage from './pages/LandingPage'
import SignInPage from './pages/SignInPage'
import TripsPage from './pages/TripsPage'
import PlannerPage from './pages/PlannerPage'
import LiveTripPage from './pages/LiveTripPage'

export type View = 'landing' | 'signin' | 'trips' | 'planner' | 'live'

export interface TripData {
  id: string
  name: string
  status: 'saved' | 'active'
  lastUpdated: string
  activityCount: number
}

export default function App() {
  const [view, setView] = useState<View>('landing')
  const [editingTrip, setEditingTrip] = useState<TripData | null>(null)
  const [isSignedIn, setIsSignedIn] = useState(false)

  const navigate = (v: View, trip?: TripData) => {
    setView(v)
    if (trip) setEditingTrip(trip)
  }

  const signIn = () => {
    setIsSignedIn(true)
    setView('trips')
  }

  const signOut = () => {
    setIsSignedIn(false)
    setView('landing')
    setEditingTrip(null)
  }

  return (
    <>
      {view === 'landing' && <LandingPage onSignIn={() => setView('signin')} />}
      {view === 'signin' && <SignInPage onSignIn={signIn} onBack={() => setView('landing')} />}
      {view === 'trips' && (
        <TripsPage
          onNavigate={navigate}
          onSignOut={signOut}
        />
      )}
      {view === 'planner' && (
        <PlannerPage
          trip={editingTrip}
          onNavigate={navigate}
          onSignOut={signOut}
        />
      )}
      {view === 'live' && (
        <LiveTripPage
          onNavigate={navigate}
          onSignOut={signOut}
        />
      )}
    </>
  )
}

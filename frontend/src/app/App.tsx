import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import {
  BrowserRouter,
  Link,
  MemoryRouter,
  Navigate,
  NavLink,
  Route,
  Routes,
  useNavigate,
  useParams,
} from "react-router-dom";

import { ApiError, liveRouteApi } from "../api/client";
import type { LiveRouteApi } from "../api/client";
import type {
  ActivityInput,
  CreateTripRequest,
  Place,
  Session,
  Trip,
  TripList,
  TripSummary,
} from "../api/types";
import { MapPreview } from "../map/MapPreview";
import type { RouteGeometry } from "../map/directions";
import { PlaceSearch } from "../map/PlaceSearch";
import { GoogleSignIn } from "../auth/GoogleSignIn";
import { ActivityEditor } from "../planner/ActivityEditor";
import { createDefaultActivity } from "../planner/activityDefaults";
import {
  browserGpsPlatform,
  GpsTelemetryController,
  type GpsLocationSample,
} from "../live/gpsTelemetry";
import {
  buildNavigationExtension,
  RouteDeviationController,
  type NavigationRoute,
} from "../live/routeDeviation";
import {
  LiveTripSocket,
  type ServerEnvelope,
  type TripResumeState,
} from "../live/liveTripSocket";

type AppState =
  | { phase: "loading" }
  | { phase: "anonymous" }
  | { phase: "error"; message: string }
  | { phase: "ready"; session: Session; trips: TripList };

interface AppRoutesProps {
  api?: LiveRouteApi;
}

interface ProposalIdentity {
  proposal_id: string;
  source_runtime_epoch: string;
  source_planner_state_version: string;
  base_current_plan_id: string;
}

interface PlannerProposal {
  identity: ProposalIdentity;
  notification: string;
  reasons: string[];
  preservedCount: number;
  revisedCount: number;
}

interface ProposalDecision {
  messageId: string;
  phase: string;
}

interface ActiveNavigationState {
  nextActivityId: string;
  scheduledStartUnixMs?: number;
}

function recordValue(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function parsePlannerProposal(value: unknown): PlannerProposal | null {
  const payload = recordValue(value);
  const proposal = recordValue(payload?.proposal);
  if (!payload || !proposal) return null;
  const identityKeys = [
    "proposal_id",
    "source_runtime_epoch",
    "source_planner_state_version",
    "base_current_plan_id",
  ] as const;
  if (
    !identityKeys.every((key) => typeof proposal[key] === "string") ||
    typeof payload.notification !== "string" ||
    !Array.isArray(payload.reasons) ||
    !payload.reasons.every((reason) => typeof reason === "string") ||
    !Array.isArray(proposal.preserved_prefix) ||
    !Array.isArray(proposal.revised_suffix)
  ) {
    return null;
  }
  return {
    identity: {
      proposal_id: proposal.proposal_id as string,
      source_runtime_epoch: proposal.source_runtime_epoch as string,
      source_planner_state_version:
        proposal.source_planner_state_version as string,
      base_current_plan_id: proposal.base_current_plan_id as string,
    },
    notification: payload.notification,
    reasons: payload.reasons as string[],
    preservedCount: proposal.preserved_prefix.length,
    revisedCount: proposal.revised_suffix.length,
  };
}

function parseActiveNavigation(value: unknown): ActiveNavigationState | null {
  const payload = recordValue(value);
  const trip = recordValue(payload?.trip);
  const currentPlan = recordValue(payload?.current_plan);
  const activities = Array.isArray(trip?.activities) ? trip.activities : [];
  const segments = Array.isArray(currentPlan?.segments)
    ? currentPlan.segments
    : [];
  const activityStates = new Map<string, string>();
  let currentActivityId: string | undefined;
  for (const value of activities) {
    const activity = recordValue(value);
    if (typeof activity?.activity_id !== "string") continue;
    if (typeof activity.activity_state === "string") {
      activityStates.set(activity.activity_id, activity.activity_state);
    }
    if (activity.activity_state === "started") {
      currentActivityId = activity.activity_id;
    }
  }
  const scheduled = segments.filter(
    (value): value is Record<string, unknown> => {
      const segment = recordValue(value);
      return (
        segment?.state === "scheduled" &&
        typeof segment.activity_id === "string"
      );
    },
  );
  if (scheduled.length === 0) return null;
  let nextIndex = 0;
  if (currentActivityId) {
    const currentIndex = scheduled.findIndex(
      (segment) => segment.activity_id === currentActivityId,
    );
    if (currentIndex >= 0) nextIndex = currentIndex + 1;
  }
  while (
    nextIndex < scheduled.length &&
    (activityStates.get(scheduled[nextIndex]!.activity_id as string) ===
      "completed" ||
      activityStates.get(scheduled[nextIndex]!.activity_id as string) ===
        "skipped")
  ) {
    nextIndex += 1;
  }
  const next = scheduled[nextIndex];
  if (!next || typeof next.activity_id !== "string") return null;
  return {
    nextActivityId: next.activity_id,
    ...(typeof next.scheduled_start_unix_ms === "number"
      ? { scheduledStartUnixMs: next.scheduled_start_unix_ms }
      : {}),
  };
}

function Brand(): ReactNode {
  return (
    <Link className="brand" to="/trips" aria-label="LiveRoute trips">
      <span className="brand-mark" aria-hidden="true">
        LR
      </span>
      <span>
        <strong>LiveRoute</strong>
        <small>Plans that move with you</small>
      </span>
    </Link>
  );
}

function LoadingScreen(): ReactNode {
  return (
    <main
      className="centered-page"
      aria-busy="true"
      aria-label="Restoring session"
    >
      <div className="loading-mark" aria-hidden="true" />
      <p className="eyebrow">Finding your route</p>
      <h1>Loading your trips…</h1>
    </main>
  );
}

function SignedOutScreen({
  api,
  onAuthenticated,
}: {
  api: LiveRouteApi;
  onAuthenticated(session: Session): void | Promise<void>;
}): ReactNode {
  return (
    <main className="signed-out-page">
      <section className="signed-out-card">
        <Brand />
        <p className="eyebrow">Your day, still on track</p>
        <h1>Build the plan. Keep the flexibility.</h1>
        <p className="lede">
          Save an itinerary once, then let LiveRoute suggest the smallest useful
          changes when travel takes longer than expected.
        </p>
        <GoogleSignIn api={api} onAuthenticated={onAuthenticated} />
      </section>
      <aside className="route-preview" aria-label="Example itinerary">
        <p className="route-preview-label">Saturday · Providence</p>
        <ol>
          <li>
            <span>09:30</span>
            <strong>Coffee &amp; breakfast</strong>
          </li>
          <li>
            <span>11:15</span>
            <strong>RISD Museum</strong>
          </li>
          <li>
            <span>14:00</span>
            <strong>India Point Park</strong>
          </li>
        </ol>
      </aside>
    </main>
  );
}

function ErrorScreen({
  message,
  retry,
}: {
  message: string;
  retry: () => void;
}): ReactNode {
  return (
    <main className="centered-page">
      <p className="eyebrow error-eyebrow">Connection interrupted</p>
      <h1>We couldn’t load your trips.</h1>
      <p className="lede compact">{message}</p>
      <button className="primary-button" type="button" onClick={retry}>
        Try again
      </button>
    </main>
  );
}

function StatePill({
  state,
}: {
  state: TripSummary["execution_state"];
}): ReactNode {
  const label =
    state === "inactive"
      ? "Saved"
      : state.replace(/^./, (value) => value.toUpperCase());
  return <span className={`state-pill state-${state}`}>{label}</span>;
}

function TripRow({ trip }: { trip: TripSummary }): ReactNode {
  return (
    <Link className="trip-row" to={`/planner/${trip.trip_id}`}>
      <span className="trip-icon" aria-hidden="true">
        ↗
      </span>
      <span className="trip-row-copy">
        <strong>{trip.trip_name}</strong>
        <small>Revision {trip.trip_revision}</small>
      </span>
      <StatePill state={trip.execution_state} />
      <span className="row-arrow" aria-hidden="true">
        →
      </span>
    </Link>
  );
}

function TripsPage({ trips }: { trips: TripList }): ReactNode {
  return (
    <main className="content-page">
      <header className="page-header">
        <div>
          <p className="eyebrow">Your itineraries</p>
          <h1>Trips</h1>
          <p>Open a saved plan, or start shaping a new day.</p>
        </div>
        <Link className="primary-button button-link" to="/planner/new">
          <span aria-hidden="true">＋</span> New trip
        </Link>
      </header>

      {trips.current_execution_trip ? (
        <section
          className="trip-section active-trip-section"
          aria-labelledby="active-trip-title"
        >
          <div className="section-heading">
            <div>
              <p className="eyebrow">In progress</p>
              <h2 id="active-trip-title">Active trip</h2>
            </div>
            <Link to="/live">Open live view</Link>
          </div>
          <TripRow trip={trips.current_execution_trip} />
        </section>
      ) : null}

      <section className="trip-section" aria-labelledby="saved-trips-title">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Ready when you are</p>
            <h2 id="saved-trips-title">Saved trips</h2>
          </div>
          <span>{trips.inactive_trips.length}</span>
        </div>
        {trips.inactive_trips.length > 0 ? (
          <div className="trip-list">
            {trips.inactive_trips.map((trip) => (
              <TripRow key={trip.trip_id} trip={trip} />
            ))}
          </div>
        ) : (
          <div className="empty-state">
            <span aria-hidden="true">⌁</span>
            <h3>No saved trips yet</h3>
            <p>
              Create a trip locally, add at least one stop, then save it here.
            </p>
            <Link to="/planner/new">Plan your first trip</Link>
          </div>
        )}
      </section>
    </main>
  );
}

function PlaceholderPage({
  title,
  detail,
}: {
  title: string;
  detail: string;
}): ReactNode {
  return (
    <main className="content-page placeholder-page">
      <p className="eyebrow">Next implementation slice</p>
      <h1>{title}</h1>
      <p className="lede compact">{detail}</p>
      <Link to="/trips">Back to trips</Link>
    </main>
  );
}

function PlannerPage({
  tripId,
  api,
  csrfToken,
}: {
  tripId: string;
  api: LiveRouteApi;
  csrfToken: string;
}): ReactNode {
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<
    | { kind: "loading" }
    | { kind: "ready"; trip: Trip }
    | { kind: "error"; message: string }
  >({ kind: "loading" });
  const [liveConnection, setLiveConnection] = useState<
    | { kind: "idle" }
    | { kind: "connecting" }
    | { kind: "subscribed" }
    | { kind: "error"; message: string }
  >({ kind: "idle" });
  const [activationState, setActivationState] = useState<
    | { kind: "idle" }
    | { kind: "locating" }
    | { kind: "submitting" }
    | { kind: "error"; message: string }
  >({ kind: "idle" });
  const [locationState, setLocationState] = useState<{
    sample: GpsLocationSample | null;
    stale: boolean;
    permissionDenied: boolean;
  }>({ sample: null, stale: false, permissionDenied: false });
  const [canonicalRoutes, setCanonicalRoutes] = useState<RouteGeometry[]>([]);
  const [routeOverride, setRouteOverride] = useState<RouteGeometry | null>(
    null,
  );
  const [routeDegraded, setRouteDegraded] = useState(false);
  const routeIdsRef = useRef(new Map<string, string>());
  const routeDeviationRef = useRef<RouteDeviationController | null>(null);
  const activeSocketRef = useRef<LiveTripSocket | null>(null);
  const [plannerNotification, setPlannerNotification] = useState<{
    notification: string;
    reasons: string[];
  } | null>(null);
  const [plannerProposal, setPlannerProposal] =
    useState<PlannerProposal | null>(null);
  const [proposalDecision, setProposalDecision] =
    useState<ProposalDecision | null>(null);
  const [activeNavigation, setActiveNavigation] =
    useState<ActiveNavigationState | null>(null);
  const proposalDecisionRef = useRef<ProposalDecision | null>(null);
  proposalDecisionRef.current = proposalDecision;

  const navigationRoute = useMemo<NavigationRoute | null>(() => {
    if (
      state.kind !== "ready" ||
      state.trip.execution_state !== "active" ||
      canonicalRoutes.length === 0 ||
      !activeNavigation
    ) {
      return null;
    }
    const destination = state.trip.saved_plan.activities.find(
      (activity) => activity.activity_id === activeNavigation.nextActivityId,
    );
    if (!destination) return null;
    const geometry = canonicalRoutes.find(
      (candidate) =>
        candidate.durationSeconds !== undefined &&
        candidate.distanceMeters !== undefined &&
        candidate.coordinates.length >= 2 &&
        candidate.coordinates.at(-1)?.[0] === destination.place.longitude &&
        candidate.coordinates.at(-1)?.[1] === destination.place.latitude,
    );
    if (
      !geometry ||
      geometry.durationSeconds === undefined ||
      geometry.distanceMeters === undefined
    ) {
      return null;
    }
    const routeIdentity = [
      state.trip.saved_plan.saved_plan_id,
      state.trip.saved_plan.saved_plan_revision,
      destination.activity_id,
      geometry.profile,
      geometry.coordinates.length,
    ].join(":");
    let navigationRouteId = routeIdsRef.current.get(routeIdentity);
    if (!navigationRouteId) {
      navigationRouteId = crypto.randomUUID();
      routeIdsRef.current.set(routeIdentity, navigationRouteId);
    }
    const relativeStart =
      destination.schedule.state === "scheduled"
        ? destination.schedule.start_offset_ms
        : 0;
    const activatedAt = state.trip.active_execution?.activated_at_unix_ms;
    const previousEtaUnixMs =
      activeNavigation.scheduledStartUnixMs !== undefined
        ? activeNavigation.scheduledStartUnixMs
        : activatedAt !== undefined
          ? activatedAt + relativeStart
          : Date.now() + Math.ceil(geometry.durationSeconds * 1000);
    if (!Number.isSafeInteger(previousEtaUnixMs)) return null;
    return {
      routeIdentity,
      navigationRouteId,
      nextActivityId: destination.activity_id,
      profile: geometry.profile,
      coordinates: geometry.coordinates,
      previousEtaUnixMs,
      remainingDurationSeconds: Math.ceil(geometry.durationSeconds),
      remainingDistanceMeters: Math.ceil(geometry.distanceMeters),
    };
  }, [activeNavigation, canonicalRoutes, state]);
  const navigationRouteRef = useRef<NavigationRoute | null>(null);
  navigationRouteRef.current = navigationRoute;

  useEffect(() => {
    const controller = new AbortController();
    void api
      .getTrip(tripId, controller.signal)
      .then((trip) => setState({ kind: "ready", trip }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState({
          kind: "error",
          message:
            error instanceof Error
              ? error.message
              : "This trip could not be loaded.",
        });
      });
    return () => controller.abort();
  }, [api, tripId, attempt]);

  useEffect(() => {
    if (
      state.kind !== "ready" ||
      (state.trip.execution_state !== "activating" &&
        state.trip.execution_state !== "deactivating")
    ) {
      return;
    }
    const timer = window.setTimeout(() => {
      setAttempt((value) => value + 1);
    }, 1000);
    return () => window.clearTimeout(timer);
  }, [state]);

  const executionState =
    state.kind === "ready" ? state.trip.execution_state : "loading";

  useEffect(() => {
    if (executionState !== "active") {
      setLiveConnection({ kind: "idle" });
      setLocationState({ sample: null, stale: false, permissionDenied: false });
      setPlannerNotification(null);
      setPlannerProposal(null);
      setProposalDecision(null);
      setActiveNavigation(null);
      return;
    }
    let mounted = true;
    let intentionallyClosed = false;
    let connecting = false;
    let reconnectTimer: number | null = null;
    let reconnectFailures = 0;
    let hasConnected = false;
    let resume: TripResumeState = {};
    let gps: GpsTelemetryController | null = null;
    let deviation: RouteDeviationController | null = null;
    let activeSocket: LiveTripSocket | null = null;

    const recordVersions = (message: ServerEnvelope): void => {
      if (message.trip_id !== tripId) return;
      resume = {
        ...resume,
        ...(message.runtime_epoch
          ? { last_runtime_epoch: message.runtime_epoch }
          : {}),
        ...(message.planner_state_version
          ? { last_planner_state_version: message.planner_state_version }
          : {}),
        ...(message.trip_revision
          ? { last_trip_revision: message.trip_revision }
          : {}),
      };
    };

    const scheduleReconnect = (message: string): void => {
      if (!mounted || intentionallyClosed || reconnectTimer !== null) return;
      reconnectFailures += 1;
      const ceiling = Math.min(
        100 * 2 ** Math.min(reconnectFailures - 1, 7),
        10_000,
      );
      const delay = Math.floor(Math.random() * (ceiling + 1));
      setLiveConnection({ kind: "error", message });
      reconnectTimer = window.setTimeout(() => {
        reconnectTimer = null;
        connectOnce();
      }, delay);
    };

    function connectOnce(): void {
      if (!mounted || intentionallyClosed || connecting) return;
      connecting = true;
      setLiveConnection({ kind: "connecting" });
      const socket = new LiveTripSocket({
        api,
        csrfToken,
        tripId,
        resume,
        resynchronize: hasConnected,
        onMessage: (message) => {
          recordVersions(message);
          if (
            message.kind === "subscription_state" ||
            message.kind === "resynchronization_state"
          ) {
            setActiveNavigation(parseActiveNavigation(message.payload));
          }
          if (
            message.kind === "planner_notification" &&
            typeof message.payload.notification === "string" &&
            Array.isArray(message.payload.reasons) &&
            message.payload.reasons.every(
              (reason) => typeof reason === "string",
            )
          ) {
            setPlannerNotification({
              notification: message.payload.notification,
              reasons: message.payload.reasons as string[],
            });
          }
          if (
            message.kind === "plan_proposal" ||
            (message.kind === "subscription_state" &&
              message.payload.latest_pending_proposal !== undefined) ||
            (message.kind === "resynchronization_state" &&
              message.payload.latest_pending_proposal !== undefined)
          ) {
            const proposal = parsePlannerProposal(
              message.kind === "plan_proposal"
                ? message.payload
                : message.payload.latest_pending_proposal,
            );
            if (proposal) setPlannerProposal(proposal);
          }
          if (
            (message.kind === "subscription_state" ||
              message.kind === "resynchronization_state") &&
            message.payload.latest_pending_proposal === undefined &&
            proposalDecisionRef.current?.phase === "planner_applied"
          ) {
            setPlannerProposal(null);
          }
          if (message.kind === "command_acknowledgement") {
            const messageId = message.payload.message_id;
            const phase = message.payload.phase;
            if (typeof messageId === "string" && typeof phase === "string") {
              setProposalDecision((current) =>
                current?.messageId === messageId
                  ? { messageId, phase }
                  : current,
              );
            }
          }
          if (message.kind === "telemetry_status") {
            gps?.handleTelemetryStatus(message);
          }
        },
        onClose: () => {
          if (activeSocket === socket) activeSocket = null;
          gps?.stop();
          gps = null;
          deviation?.stop();
          deviation = null;
          routeDeviationRef.current = null;
          scheduleReconnect(
            "The live trip connection was interrupted; reconnecting…",
          );
        },
      });
      activeSocket = socket;
      activeSocketRef.current = socket;
      void socket
        .connect()
        .then(() => {
          if (!mounted || activeSocket !== socket) return;
          hasConnected = true;
          reconnectFailures = 0;
          setLiveConnection({ kind: "subscribed" });
          if (!navigator.geolocation) {
            setLocationState((current) => ({
              ...current,
              permissionDenied: true,
            }));
            return;
          }
          deviation = new RouteDeviationController({
            accessToken:
              import.meta.env.VITE_MAPBOX_PUBLIC_ACCESS_TOKEN?.trim() ?? "",
            onRerouted: (route, observation) => {
              const updatedEtaUnixMs =
                observation.sample.observedAtUnixMs +
                route.remainingDurationSeconds * 1000;
              if (!Number.isSafeInteger(updatedEtaUnixMs)) return;
              setRouteOverride({
                profile: route.profile,
                coordinates: route.coordinates,
                durationSeconds: route.remainingDurationSeconds,
                distanceMeters: route.remainingDistanceMeters,
              });
              setRouteDegraded(false);
              socket.sendRouteDeviationTelemetry({
                latitude: observation.sample.latitude,
                longitude: observation.sample.longitude,
                observedAtUnixMs: observation.sample.observedAtUnixMs,
                distanceFromRouteMeters: Math.max(
                  0,
                  Math.round(observation.distanceFromRouteMeters),
                ),
                extensions: buildNavigationExtension({
                  nextActivityId: route.nextActivityId,
                  navigationRouteId: route.navigationRouteId,
                  previousEtaUnixMs: route.previousEtaUnixMs,
                  updatedEtaUnixMs,
                  remainingDurationSeconds: route.remainingDurationSeconds,
                  remainingDistanceMeters: route.remainingDistanceMeters,
                }),
              });
            },
            onFailure: () => setRouteDegraded(true),
          });
          routeDeviationRef.current = deviation;
          deviation.setRoute(navigationRouteRef.current);
          gps = new GpsTelemetryController({
            platform: browserGpsPlatform(),
            sender: socket,
            onLocation: (sample) => {
              deviation?.observe(sample);
              setLocationState((current) => ({
                ...current,
                sample,
                stale: false,
              }));
            },
            onStale: (stale) =>
              setLocationState((current) => ({ ...current, stale })),
            onPermissionDenied: () =>
              setLocationState((current) => ({
                ...current,
                permissionDenied: true,
              })),
            onAcknowledgementTimeout: () => {
              socket.close(undefined, "telemetry acknowledgement timeout");
            },
          });
          gps.start();
        })
        .catch((error: unknown) => {
          if (activeSocket === socket) activeSocket = null;
          if (activeSocketRef.current === socket)
            activeSocketRef.current = null;
          scheduleReconnect(
            error instanceof Error
              ? `${error.message} Reconnecting…`
              : "The live trip connection could not be established. Reconnecting…",
          );
        })
        .finally(() => {
          connecting = false;
        });
    }

    connectOnce();
    return () => {
      mounted = false;
      intentionallyClosed = true;
      if (reconnectTimer !== null) window.clearTimeout(reconnectTimer);
      reconnectTimer = null;
      gps?.stop();
      deviation?.stop();
      routeDeviationRef.current = null;
      activeSocket?.close();
      activeSocket = null;
      activeSocketRef.current = null;
    };
  }, [api, csrfToken, executionState, tripId]);

  useEffect(() => {
    if (executionState !== "active") {
      routeDeviationRef.current = null;
      setRouteOverride(null);
      setRouteDegraded(false);
      return;
    }
    routeDeviationRef.current?.setRoute(navigationRoute);
    if (navigationRoute) {
      setRouteOverride(null);
      setRouteDegraded(false);
    }
  }, [executionState, navigationRoute]);

  const decideProposal = (
    decision: "accept_proposal" | "reject_proposal",
  ): void => {
    if (!plannerProposal) return;
    const messageId = activeSocketRef.current?.sendProposalDecision(
      decision,
      plannerProposal.identity,
    );
    if (messageId) setProposalDecision({ messageId, phase: "sending" });
  };

  const startTrip = (): void => {
    if (
      state.kind !== "ready" ||
      state.trip.execution_state !== "inactive" ||
      state.trip.saved_plan.activities.some(
        (activity) => activity.schedule.state !== "scheduled",
      )
    ) {
      return;
    }
    if (!navigator.geolocation) {
      setActivationState({
        kind: "error",
        message: "This browser does not provide a starting location.",
      });
      return;
    }
    setActivationState({ kind: "locating" });
    navigator.geolocation.getCurrentPosition(
      (position) => {
        setActivationState({ kind: "submitting" });
        void api
          .activateTrip(
            tripId,
            {
              starting_location: {
                latitude: position.coords.latitude,
                longitude: position.coords.longitude,
              },
            },
            state.trip.trip_revision,
            csrfToken,
          )
          .then((transition) => {
            setState({ kind: "ready", trip: transition.trip });
            setActivationState({ kind: "idle" });
          })
          .catch((error: unknown) => {
            setActivationState({
              kind: "error",
              message:
                error instanceof Error
                  ? error.message
                  : "The trip could not be started.",
            });
          });
      },
      (error) => {
        setActivationState({
          kind: "error",
          message:
            error.code === error.PERMISSION_DENIED
              ? "Location permission is required to start this trip."
              : "The browser could not determine a starting location.",
        });
      },
      { enableHighAccuracy: true, maximumAge: 0, timeout: 10_000 },
    );
  };

  if (state.kind === "loading") {
    return (
      <main className="content-page" aria-busy="true">
        <p className="eyebrow">Trip planner</p>
        <h1>Loading your plan…</h1>
      </main>
    );
  }

  if (state.kind === "error") {
    return (
      <ErrorScreen
        message={state.message}
        retry={() => {
          setState({ kind: "loading" });
          setAttempt((value) => value + 1);
        }}
      />
    );
  }

  const { trip } = state;
  return (
    <main className="content-page planner-page">
      <header className="page-header">
        <div>
          <p className="eyebrow">Saved itinerary</p>
          <h1>{trip.trip_name}</h1>
          <p>
            Revision {trip.trip_revision} · {trip.default_time_zone_name}
          </p>
        </div>
        <div className="planner-actions">
          {trip.execution_state === "inactive" ? (
            <button
              className="primary-button"
              type="button"
              onClick={startTrip}
              disabled={
                activationState.kind === "locating" ||
                activationState.kind === "submitting" ||
                trip.saved_plan.activities.some(
                  (activity) => activity.schedule.state !== "scheduled",
                )
              }
            >
              {activationState.kind === "locating"
                ? "Finding you…"
                : activationState.kind === "submitting"
                  ? "Starting…"
                  : "Go"}
            </button>
          ) : (
            <span className={`state-pill state-${trip.execution_state}`}>
              {trip.execution_state}
            </span>
          )}
          <Link className="button-link" to="/trips">
            Back to trips
          </Link>
        </div>
      </header>
      {trip.execution_state === "inactive" &&
      trip.saved_plan.activities.some(
        (activity) => activity.schedule.state !== "scheduled",
      ) ? (
        <p className="configuration-note">
          Schedule every activity before starting this trip.
        </p>
      ) : null}
      {activationState.kind === "error" ? (
        <p className="form-error" role="alert">
          {activationState.message}
        </p>
      ) : null}
      {trip.execution_state === "active" ? (
        <>
          <p
            className={
              liveConnection.kind === "error"
                ? "map-preview-error"
                : "configuration-note"
            }
            role={liveConnection.kind === "error" ? "alert" : "status"}
          >
            {liveConnection.kind === "connecting"
              ? "Connecting to the active trip…"
              : liveConnection.kind === "subscribed"
                ? "Live trip connected."
                : liveConnection.kind === "error"
                  ? liveConnection.message
                  : "Preparing live trip connection…"}
          </p>
          {liveConnection.kind === "subscribed" ? (
            <>
              <p
                className={
                  locationState.stale || locationState.permissionDenied
                    ? "map-preview-error"
                    : "configuration-note"
                }
                role={
                  locationState.stale || locationState.permissionDenied
                    ? "alert"
                    : "status"
                }
              >
                {locationState.permissionDenied
                  ? "Location permission is required for live location."
                  : locationState.sample
                    ? locationState.stale
                      ? "Live location is stale."
                      : `Live location · ±${Math.round(locationState.sample.accuracy)} m`
                    : "Waiting for a live location…"}
              </p>
              {routeDegraded ? (
                <p className="map-preview-error" role="alert">
                  Live rerouting is temporarily unavailable; the previous route
                  remains visible.
                </p>
              ) : null}
            </>
          ) : null}
          {plannerNotification &&
          plannerNotification.notification !== "none" ? (
            <p className="configuration-note" role="status">
              {plannerNotification.notification.replaceAll("_", " ")} ·{" "}
              {plannerNotification.reasons.join(", ")}
            </p>
          ) : null}
          {plannerProposal ? (
            <section
              className="trip-section proposal-card"
              aria-labelledby="proposal-title"
            >
              <div className="section-heading">
                <div>
                  <p className="eyebrow">Suggested change</p>
                  <h2 id="proposal-title">Review the proposed plan</h2>
                </div>
                <span className="state-pill">Not yet accepted</span>
              </div>
              <p>
                {plannerProposal.notification.replaceAll("_", " ")} ·{" "}
                {plannerProposal.reasons.join(", ")}
              </p>
              <p className="configuration-note">
                {plannerProposal.preservedCount} activities stay in place;{" "}
                {plannerProposal.revisedCount} are in the suggested suffix. The
                current route remains canonical until you accept.
              </p>
              {proposalDecision ? (
                <p className="configuration-note" role="status">
                  Proposal decision:{" "}
                  {proposalDecision.phase.replaceAll("_", " ")}.
                </p>
              ) : null}
              <div className="planner-actions">
                <button
                  className="primary-button"
                  type="button"
                  onClick={() => decideProposal("accept_proposal")}
                  disabled={proposalDecision !== null}
                >
                  Accept suggestion
                </button>
                <button
                  className="button-link"
                  type="button"
                  onClick={() => decideProposal("reject_proposal")}
                  disabled={proposalDecision !== null}
                >
                  Keep current plan
                </button>
              </div>
            </section>
          ) : null}
        </>
      ) : null}
      <section className="trip-section" aria-labelledby="activities-title">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Your stops</p>
            <h2 id="activities-title">Activities</h2>
          </div>
          <span>{trip.saved_plan.activities.length}</span>
        </div>
        <ol className="planner-activity-list">
          {trip.saved_plan.activities.map((activity) => (
            <li key={activity.activity_id} className="planner-activity">
              <span className="trip-icon" aria-hidden="true">
                {activity.ordinal + 1}
              </span>
              <span>
                <strong>{activity.place.display_name}</strong>
                <small>
                  {activity.schedule.state === "scheduled"
                    ? `${formatOffset(activity.schedule.start_offset_ms)} – ${formatOffset(activity.schedule.end_offset_ms)}`
                    : "Unscheduled"}
                </small>
              </span>
              <span className="state-pill">{activity.inbound_travel_mode}</span>
            </li>
          ))}
        </ol>
      </section>
      <MapPreview
        trip={trip}
        currentLocation={locationState.sample}
        onCanonicalRoutes={setCanonicalRoutes}
        routeOverride={routeOverride}
      />
      <div className="empty-state planner-coming-soon">
        <h3>Map and editing are next</h3>
        <p>
          Place search, route preview, and saved-plan editing will use this
          canonical trip data.
        </p>
      </div>
    </main>
  );
}

function formatOffset(offsetMs: number): string {
  const totalMinutes = Math.floor(offsetMs / 60000);
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return `${String(hours).padStart(2, "0")}:${String(minutes).padStart(2, "0")}`;
}

function formatActivityDuration(seconds: number): string {
  if (seconds % 3600 === 0) return String(seconds / 3600) + " hour(s)";
  if (seconds % 60 === 0) return String(seconds / 60) + " minutes";
  return String(seconds) + " seconds";
}

export function NewTripPage({
  api,
  csrfToken,
  defaultTimeZoneName,
}: {
  api: LiveRouteApi;
  csrfToken: string;
  defaultTimeZoneName: string;
}): ReactNode {
  const navigate = useNavigate();
  const [tripName, setTripName] = useState("");
  const [tripTimeZoneName, setTripTimeZoneName] = useState(defaultTimeZoneName);
  const [draftActivity, setDraftActivity] = useState<{
    place: Place;
    input: ActivityInput;
  } | null>(null);
  const [saveState, setSaveState] = useState<
    { kind: "idle" } | { kind: "saving" } | { kind: "error"; message: string }
  >({ kind: "idle" });

  const saveTrip = async (): Promise<void> => {
    if (!tripName.trim() || !draftActivity || !tripTimeZoneName.trim()) {
      return;
    }
    setSaveState({ kind: "saving" });
    const request: CreateTripRequest = {
      trip_name: tripName.trim(),
      default_time_zone_name: tripTimeZoneName.trim(),
      activities: [draftActivity.input],
    };
    try {
      const createdTrip = await api.createTrip(request, csrfToken);
      void navigate("/planner/" + createdTrip.trip_id);
    } catch (error: unknown) {
      setSaveState({
        kind: "error",
        message:
          error instanceof Error
            ? error.message
            : "This trip could not be saved.",
      });
    }
  };

  return (
    <main className="content-page planner-page">
      <header className="page-header">
        <div>
          <p className="eyebrow">New itinerary</p>
          <h1>Plan a new trip</h1>
          <p>Start with a name and a confirmed destination.</p>
        </div>
        <Link className="button-link" to="/trips">
          Back to trips
        </Link>
      </header>
      <section className="trip-section" aria-labelledby="trip-details-title">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Trip details</p>
            <h2 id="trip-details-title">Name your trip</h2>
          </div>
        </div>
        <label className="field-label" htmlFor="new-trip-name">
          Trip name
        </label>
        <input
          id="new-trip-name"
          className="text-input"
          value={tripName}
          maxLength={120}
          onChange={(event) => setTripName(event.target.value)}
          placeholder="Saturday in Providence"
        />
        <label className="field-label" htmlFor="new-trip-time-zone">
          Trip timezone
        </label>
        <input
          id="new-trip-time-zone"
          className="text-input"
          value={tripTimeZoneName}
          onChange={(event) => setTripTimeZoneName(event.target.value)}
          placeholder="America/New_York"
          autoComplete="off"
        />
        <p className="configuration-note">
          Initialized from your stored default; you can change it for this trip.
        </p>
        <p className="configuration-note">Trip timezone: {tripTimeZoneName}</p>
        {draftActivity ? (
          <div className="place-confirmation" role="status">
            <p className="eyebrow">Confirmed destination</p>
            <strong>{draftActivity.place.display_name}</strong>
            <small>
              {draftActivity.input.inbound_travel_mode} ·{" "}
              {formatActivityDuration(
                draftActivity.input.timing.preferred_duration_seconds,
              )}{" "}
              · {draftActivity.input.activity_class} ·{" "}
              {draftActivity.input.timing.can_skip
                ? "skippable"
                : "not skippable"}{" "}
              · {draftActivity.place.time_zone_name}
            </small>
          </div>
        ) : null}
      </section>
      <PlaceSearch
        api={api}
        csrfToken={csrfToken}
        onPlaceConfirmed={(place) =>
          setDraftActivity({
            place,
            input: createDefaultActivity(place.place_id, 0),
          })
        }
      />
      {draftActivity ? (
        <ActivityEditor
          value={draftActivity.input}
          onChange={(input) => setDraftActivity({ ...draftActivity, input })}
        />
      ) : null}
      <div className="empty-state planner-coming-soon">
        <h3>Save your inactive trip</h3>
        <p>
          A trip is saved only after it has a name and at least one complete
          activity with schedule and travel settings. The selected timezone is
          sent with that first canonical inactive revision.
        </p>
        {saveState.kind === "error" ? (
          <p className="form-error" role="alert">
            {saveState.message}
          </p>
        ) : null}
        <button
          className="primary-button"
          type="button"
          disabled={
            !tripName.trim() ||
            !tripTimeZoneName.trim() ||
            !draftActivity ||
            saveState.kind === "saving"
          }
          onClick={() => void saveTrip()}
        >
          {saveState.kind === "saving" ? "Saving…" : "Save trip"}
        </button>
      </div>
    </main>
  );
}

function AppShell({
  session,
  trips,
  api,
}: {
  session: Session;
  trips: TripList;
  api: LiveRouteApi;
}): ReactNode {
  const active = trips.current_execution_trip !== undefined;
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Brand />
        <nav aria-label="Primary navigation">
          <NavLink to="/trips">Trips</NavLink>
          <NavLink to="/planner/new">Planner</NavLink>
          <NavLink
            className={active ? "" : "nav-disabled"}
            to="/live"
            aria-disabled={!active}
          >
            Live trip
          </NavLink>
        </nav>
        <div className="user-card">
          <span aria-hidden="true">
            {session.user.display_name.slice(0, 1).toUpperCase()}
          </span>
          <div>
            <strong>{session.user.display_name}</strong>
            <small>Signed in</small>
          </div>
        </div>
      </aside>
      <div className="app-content">
        <Routes>
          <Route path="/trips" element={<TripsPage trips={trips} />} />
          <Route
            path="/planner/:tripId"
            element={
              <PlannerRoute
                api={api}
                csrfToken={session.csrf_token}
                defaultTimeZoneName={session.user.default_time_zone_name}
              />
            }
          />
          <Route
            path="/live"
            element={
              active ? (
                <PlaceholderPage
                  title="Live trip"
                  detail="Live execution will connect after activation and WebSocket-ticket handling are implemented."
                />
              ) : (
                <Navigate to="/trips" replace />
              )
            }
          />
          <Route path="*" element={<Navigate to="/trips" replace />} />
        </Routes>
      </div>
    </div>
  );
}

function PlannerRoute({
  api,
  csrfToken,
  defaultTimeZoneName,
}: {
  api: LiveRouteApi;
  csrfToken: string;
  defaultTimeZoneName: string;
}): ReactNode {
  const { tripId } = useParams();
  if (tripId === "new") {
    return (
      <NewTripPage
        api={api}
        csrfToken={csrfToken}
        defaultTimeZoneName={defaultTimeZoneName}
      />
    );
  }
  return tripId ? (
    <PlannerPage api={api} tripId={tripId} csrfToken={csrfToken} />
  ) : (
    <Navigate to="/trips" replace />
  );
}

export function AppRoutes({ api = liveRouteApi }: AppRoutesProps): ReactNode {
  const [state, setState] = useState<AppState>({ phase: "loading" });
  const [attempt, setAttempt] = useState(0);

  const retry = useCallback(() => {
    setState({ phase: "loading" });
    setAttempt((value) => value + 1);
  }, []);

  const authenticated = useCallback(
    async (session: Session): Promise<void> => {
      setState({ phase: "loading" });
      try {
        setState({
          phase: "ready",
          session,
          trips: await api.listTrips(),
        });
      } catch (error) {
        setState({
          phase: "error",
          message:
            error instanceof Error
              ? error.message
              : "Your session was created, but trips could not be loaded.",
        });
      }
    },
    [api],
  );

  useEffect(() => {
    const controller = new AbortController();
    const restore = async (): Promise<void> => {
      try {
        const session = await api.getSession(controller.signal);
        const trips = await api.listTrips(controller.signal);
        setState({ phase: "ready", session, trips });
      } catch (error) {
        if (controller.signal.aborted) return;
        if (error instanceof ApiError && error.status === 401) {
          setState({ phase: "anonymous" });
          return;
        }
        setState({
          phase: "error",
          message:
            error instanceof Error
              ? error.message
              : "An unexpected error occurred.",
        });
      }
    };
    void restore();
    return () => controller.abort();
  }, [api, attempt]);

  if (state.phase === "loading") return <LoadingScreen />;
  if (state.phase === "anonymous")
    return <SignedOutScreen api={api} onAuthenticated={authenticated} />;
  if (state.phase === "error")
    return <ErrorScreen message={state.message} retry={retry} />;
  return <AppShell api={api} session={state.session} trips={state.trips} />;
}

export function App(): ReactNode {
  return (
    <BrowserRouter>
      <AppRoutes />
    </BrowserRouter>
  );
}

export function TestApp({
  api,
  initialEntries = ["/trips"],
}: AppRoutesProps & { initialEntries?: string[] }): ReactNode {
  return (
    <MemoryRouter initialEntries={initialEntries}>
      <AppRoutes api={api} />
    </MemoryRouter>
  );
}

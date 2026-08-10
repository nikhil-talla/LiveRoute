import routePolicy from "./route-deviation-policy.json";
import {
  DirectionsHttpError,
  requestDirectionsRoute,
  type DirectionsRouteResult,
  type RouteCoordinate,
  type RouteProfile,
} from "../map/directions";
import type { GpsLocationSample } from "./gpsTelemetry";

const EARTH_RADIUS_METERS = routePolicy.distance_earth_radius_meters;

export interface NavigationRoute {
  routeIdentity: string;
  navigationRouteId: string;
  nextActivityId: string;
  profile: RouteProfile;
  coordinates: RouteCoordinate[];
  previousEtaUnixMs: number;
  remainingDurationSeconds: number;
  remainingDistanceMeters: number;
}

export interface RouteDeviationObservation {
  sample: GpsLocationSample;
  distanceFromRouteMeters: number;
  effectiveDistanceMeters: number;
}

export interface RouteDeviationState {
  offRoute: boolean;
  entrySamples: number;
  recoverySamples: number;
}

export interface RouteDeviationTrackerOptions {
  onConfirmedDeviation: (observation: RouteDeviationObservation) => void;
  onRecovered?: () => void;
  onStateChange?: (state: RouteDeviationState) => void;
}

function radians(degrees: number): number {
  return (degrees * Math.PI) / 180;
}

function normalizeLongitudeDelta(delta: number): number {
  return ((delta + Math.PI) % (2 * Math.PI)) - Math.PI;
}

export function distanceToPolylineMeters(
  sample: GpsLocationSample,
  coordinates: RouteCoordinate[],
): number {
  if (coordinates.length === 0) return Number.POSITIVE_INFINITY;
  const sampleLatitude = radians(sample.latitude);
  const project = (coordinate: RouteCoordinate): [number, number] => [
    normalizeLongitudeDelta(
      radians(coordinate[0]) - radians(sample.longitude),
    ) *
      Math.cos(sampleLatitude) *
      EARTH_RADIUS_METERS,
    (radians(coordinate[1]) - sampleLatitude) * EARTH_RADIUS_METERS,
  ];
  const first = project(coordinates[0]!);
  let nearest = Math.hypot(first[0], first[1]);
  for (let index = 1; index < coordinates.length; index += 1) {
    const second = project(coordinates[index]!);
    const dx = second[0] - first[0];
    const dy = second[1] - first[1];
    const lengthSquared = dx * dx + dy * dy;
    const projection =
      lengthSquared === 0
        ? 0
        : Math.max(
            0,
            Math.min(1, (-first[0] * dx - first[1] * dy) / lengthSquared),
          );
    nearest = Math.min(
      nearest,
      Math.hypot(first[0] + projection * dx, first[1] + projection * dy),
    );
    first[0] = second[0];
    first[1] = second[1];
  }
  return nearest;
}

function thresholds(profile: RouteProfile): {
  enter: number;
  exit: number;
} {
  return profile === "mapbox/walking"
    ? {
        enter: routePolicy.walking_enter_effective_distance_meters,
        exit: routePolicy.walking_exit_effective_distance_meters,
      }
    : {
        enter: routePolicy.driving_enter_effective_distance_meters,
        exit: routePolicy.driving_exit_effective_distance_meters,
      };
}

export class RouteDeviationTracker {
  readonly #options: RouteDeviationTrackerOptions;
  #routeIdentity: string | null = null;
  #offRoute = false;
  #entrySamples: GpsLocationSample[] = [];
  #recoverySamples: GpsLocationSample[] = [];

  constructor(options: RouteDeviationTrackerOptions) {
    this.#options = options;
  }

  reset(routeIdentity: string | null = null): void {
    this.#routeIdentity = routeIdentity;
    this.#offRoute = false;
    this.#entrySamples = [];
    this.#recoverySamples = [];
    this.#publishState();
  }

  observe(sample: GpsLocationSample, route: NavigationRoute | null): void {
    if (!route || route.routeIdentity !== this.#routeIdentity) {
      this.reset(route?.routeIdentity ?? null);
      return;
    }
    if (sample.accuracy > routePolicy.maximum_accuracy_meters) return;
    const rawDistance = distanceToPolylineMeters(sample, route.coordinates);
    const effectiveDistance = Math.max(0, rawDistance - sample.accuracy);
    const { enter, exit } = thresholds(route.profile);
    if (!this.#offRoute) {
      if (effectiveDistance < enter) {
        this.#entrySamples = [];
        this.#publishState();
        return;
      }
      if (!this.#countSpacedSample(this.#entrySamples, sample)) return;
      if (this.#entrySamples.length >= routePolicy.confirmation_sample_count) {
        this.#offRoute = true;
        this.#recoverySamples = [];
        this.#publishState();
        this.#options.onConfirmedDeviation({
          sample,
          distanceFromRouteMeters: rawDistance,
          effectiveDistanceMeters: effectiveDistance,
        });
      } else {
        this.#publishState();
      }
      return;
    }

    if (effectiveDistance > exit) {
      this.#recoverySamples = [];
      this.#publishState();
      return;
    }
    if (!this.#countSpacedSample(this.#recoverySamples, sample)) return;
    if (this.#recoverySamples.length >= routePolicy.recovery_sample_count) {
      this.#offRoute = false;
      this.#entrySamples = [];
      this.#recoverySamples = [];
      this.#publishState();
      this.#options.onRecovered?.();
    } else {
      this.#publishState();
    }
  }

  #countSpacedSample(
    samples: GpsLocationSample[],
    sample: GpsLocationSample,
  ): boolean {
    const previous = samples.at(-1);
    if (previous) {
      const spacing = sample.observedAtUnixMs - previous.observedAtUnixMs;
      if (spacing < routePolicy.sample_minimum_spacing_ms) return false;
      if (
        sample.observedAtUnixMs - samples[0]!.observedAtUnixMs >
        routePolicy.sample_sequence_window_ms
      ) {
        samples.splice(0, samples.length, sample);
        return true;
      }
    }
    samples.push(sample);
    return true;
  }

  #publishState(): void {
    this.#options.onStateChange?.({
      offRoute: this.#offRoute,
      entrySamples: this.#entrySamples.length,
      recoverySamples: this.#recoverySamples.length,
    });
  }
}

export interface EtaBoundarySet {
  boundariesUnixMs: number[];
}

export function isMaterialEtaChange(
  previousEtaUnixMs: number,
  updatedEtaUnixMs: number,
  boundaries: EtaBoundarySet,
): boolean {
  if (
    !Number.isSafeInteger(previousEtaUnixMs) ||
    !Number.isSafeInteger(updatedEtaUnixMs) ||
    updatedEtaUnixMs < previousEtaUnixMs
  ) {
    return false;
  }
  if (
    updatedEtaUnixMs - previousEtaUnixMs >=
    routePolicy.material_eta_delay_ms
  ) {
    return true;
  }
  return boundaries.boundariesUnixMs.some(
    (boundary) =>
      Number.isSafeInteger(boundary) &&
      previousEtaUnixMs <= boundary &&
      boundary < updatedEtaUnixMs,
  );
}

export interface NavigationExtensionInput {
  nextActivityId: string;
  navigationRouteId: string;
  previousEtaUnixMs: number;
  updatedEtaUnixMs: number;
  remainingDurationSeconds: number;
  remainingDistanceMeters: number;
}

export function buildNavigationExtension(
  input: NavigationExtensionInput,
): Record<string, unknown> {
  return {
    "liveroute.navigation_v1": {
      policy_version: routePolicy.policy_version,
      next_activity_id: input.nextActivityId,
      navigation_route_id: input.navigationRouteId,
      previous_eta_unix_ms: input.previousEtaUnixMs,
      updated_eta_unix_ms: input.updatedEtaUnixMs,
      remaining_duration_seconds: input.remainingDurationSeconds,
      remaining_distance_meters: input.remainingDistanceMeters,
      off_route: true,
    },
  };
}

type DirectionsFailure =
  | "network-error"
  | "timeout"
  | "http-429"
  | "http-5xx"
  | "terminal"
  | "aborted";

function classifyDirectionsFailure(error: unknown): DirectionsFailure {
  if (error instanceof DirectionsTimeoutError) return "timeout";
  if (error instanceof DOMException && error.name === "AbortError") {
    return "aborted";
  }
  if (error instanceof DirectionsHttpError) {
    if (error.status === 429) return "http-429";
    if (error.status >= 500 && error.status <= 599) return "http-5xx";
    return "terminal";
  }
  if (error instanceof TypeError) return "network-error";
  return "terminal";
}

interface RerouterOptions {
  accessToken: string;
  now?: () => number;
  onSuccess: (
    route: NavigationRoute,
    observation: RouteDeviationObservation,
  ) => void;
  onFailure: (error: unknown) => void;
}

export class SameDestinationRerouter {
  readonly #options: RerouterOptions;
  readonly #now: () => number;
  readonly #routeIds = new Set<string>();
  #route: NavigationRoute | null = null;
  #inFlight: AbortController | null = null;
  #cooldownUntil = 0;

  constructor(options: RerouterOptions) {
    this.#options = options;
    this.#now = options.now ?? Date.now;
  }

  setRoute(route: NavigationRoute | null): void {
    if (route?.routeIdentity === this.#route?.routeIdentity) return;
    this.cancel();
    this.#route = route;
    this.#cooldownUntil = 0;
  }

  adoptSuccessfulRoute(route: NavigationRoute): void {
    this.#route = route;
  }

  request(
    observation: RouteDeviationObservation,
    route: NavigationRoute,
  ): void {
    if (
      this.#inFlight ||
      this.#route?.routeIdentity !== route.routeIdentity ||
      this.#now() < this.#cooldownUntil
    ) {
      return;
    }
    const controller = new AbortController();
    this.#inFlight = controller;
    void this.#runRequest(observation, route, controller)
      .then((result) => {
        if (!result || controller.signal.aborted) return;
        this.#cooldownUntil =
          this.#now() + routePolicy.directions_success_cooldown_ms;
        this.#options.onSuccess(result.route, observation);
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        this.#cooldownUntil =
          this.#now() + routePolicy.directions_failure_cooldown_ms;
        this.#options.onFailure(error);
      })
      .finally(() => {
        if (this.#inFlight === controller) this.#inFlight = null;
      });
  }

  cancel(): void {
    this.#inFlight?.abort();
    this.#inFlight = null;
  }

  #runRequest(
    observation: RouteDeviationObservation,
    route: NavigationRoute,
    controller: AbortController,
  ): Promise<{ route: NavigationRoute } | null> {
    return this.#attempt(observation, route, controller, false);
  }

  async #attempt(
    observation: RouteDeviationObservation,
    route: NavigationRoute,
    controller: AbortController,
    retried: boolean,
  ): Promise<{ route: NavigationRoute } | null> {
    try {
      const result = await this.#requestWithTimeout(
        observation,
        route,
        controller.signal,
      );
      const navigationRouteId = this.#newRouteId();
      const durationSeconds = Math.ceil(result.durationSeconds);
      const distanceMeters = Math.ceil(result.distanceMeters);
      const updatedEtaUnixMs =
        observation.sample.observedAtUnixMs + durationSeconds * 1000;
      if (
        !Number.isSafeInteger(updatedEtaUnixMs) ||
        durationSeconds < 0 ||
        distanceMeters < 0
      ) {
        throw new Error("Mapbox Directions returned unsafe route metrics.");
      }
      return {
        route: {
          routeIdentity: route.routeIdentity + ":" + navigationRouteId,
          navigationRouteId,
          nextActivityId: route.nextActivityId,
          profile: route.profile,
          coordinates: result.coordinates,
          previousEtaUnixMs: route.previousEtaUnixMs,
          remainingDurationSeconds: durationSeconds,
          remainingDistanceMeters: distanceMeters,
        },
      };
    } catch (error: unknown) {
      const failure = classifyDirectionsFailure(error);
      if (
        !retried &&
        (failure === "network-error" ||
          failure === "timeout" ||
          failure === "http-429" ||
          failure === "http-5xx")
      ) {
        await abortableDelay(
          routePolicy.directions_retry_delay_ms,
          controller.signal,
        );
        return this.#attempt(observation, route, controller, true);
      }
      throw error;
    }
  }

  async #requestWithTimeout(
    observation: RouteDeviationObservation,
    route: NavigationRoute,
    parentSignal: AbortSignal,
  ): Promise<DirectionsRouteResult> {
    const timeoutController = new AbortController();
    const abort = (): void => timeoutController.abort();
    parentSignal.addEventListener("abort", abort, { once: true });
    const timer = setTimeout(abort, routePolicy.directions_request_timeout_ms);
    try {
      return await requestDirectionsRoute(
        [
          [observation.sample.longitude, observation.sample.latitude],
          route.coordinates.at(-1)!,
        ],
        route.profile,
        this.#options.accessToken,
        timeoutController.signal,
      );
    } catch (error: unknown) {
      if (parentSignal.aborted) throw new DOMException("Aborted", "AbortError");
      if (timeoutController.signal.aborted) {
        throw new DirectionsTimeoutError();
      }
      throw error;
    } finally {
      clearTimeout(timer);
      parentSignal.removeEventListener("abort", abort);
    }
  }

  #newRouteId(): string {
    let id = crypto.randomUUID();
    while (this.#routeIds.has(id)) id = crypto.randomUUID();
    this.#routeIds.add(id);
    return id;
  }
}

export class DirectionsTimeoutError extends Error {
  constructor() {
    super("Mapbox Directions request timed out.");
    this.name = "DirectionsTimeoutError";
  }
}

async function abortableDelay(
  delayMs: number,
  signal: AbortSignal,
): Promise<void> {
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(resolve, delayMs);
    const abort = (): void => {
      clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal.addEventListener("abort", abort, { once: true });
  });
}

export class RouteDeviationController {
  readonly #tracker: RouteDeviationTracker;
  readonly #rerouter: SameDestinationRerouter;
  #route: NavigationRoute | null = null;
  #onRerouted: (
    route: NavigationRoute,
    observation: RouteDeviationObservation,
  ) => void;

  constructor(options: {
    accessToken: string;
    now?: () => number;
    onRerouted: (
      route: NavigationRoute,
      observation: RouteDeviationObservation,
    ) => void;
    onFailure: (error: unknown) => void;
    onRecovered?: () => void;
    onStateChange?: (state: RouteDeviationState) => void;
  }) {
    this.#onRerouted = options.onRerouted;
    this.#rerouter = new SameDestinationRerouter({
      accessToken: options.accessToken,
      now: options.now,
      onSuccess: (route, observation) => {
        this.#route = route;
        this.#rerouter.adoptSuccessfulRoute(route);
        this.#tracker.reset(route.routeIdentity);
        this.#onRerouted(route, observation);
      },
      onFailure: options.onFailure,
    });
    this.#tracker = new RouteDeviationTracker({
      onConfirmedDeviation: (observation) => {
        if (this.#route) this.#rerouter.request(observation, this.#route);
      },
      onRecovered: options.onRecovered,
      onStateChange: options.onStateChange,
    });
  }

  setRoute(route: NavigationRoute | null): void {
    this.#route = route;
    this.#rerouter.setRoute(route);
    this.#tracker.reset(route?.routeIdentity ?? null);
  }

  observe(sample: GpsLocationSample): void {
    this.#tracker.observe(sample, this.#route);
  }

  stop(): void {
    this.#rerouter.cancel();
    this.#route = null;
    this.#tracker.reset();
  }
}

import type {
  Coordinate,
  CreateTripRequest,
  ActivateTripRequest,
  ExecutionTransition,
  GoogleLoginRequest,
  GoogleNonceResponse,
  Place,
  PlaceResolution,
  Problem,
  Session,
  Trip,
  TripList,
  ActivityInput,
  UpdateTripRequest,
  WebSocketTicket,
} from "./types";

export interface LiveRouteApi {
  createGoogleLoginNonce(signal?: AbortSignal): Promise<GoogleNonceResponse>;
  authenticateWithGoogle(
    request: GoogleLoginRequest,
    signal?: AbortSignal,
  ): Promise<Session>;
  getSession(signal?: AbortSignal): Promise<Session>;
  logout(csrfToken: string, signal?: AbortSignal): Promise<void>;
  listTrips(signal?: AbortSignal): Promise<TripList>;
  getTrip(tripId: string, signal?: AbortSignal): Promise<Trip>;
  createTrip(
    request: CreateTripRequest,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip>;
  updateTrip(
    tripId: string,
    request: UpdateTripRequest,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip>;
  deleteTrip(
    tripId: string,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<void>;
  addTripActivity(
    tripId: string,
    activity: ActivityInput,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip>;
  replaceTripActivity(
    tripId: string,
    activityId: string,
    activity: ActivityInput,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip>;
  deleteTripActivity(
    tripId: string,
    activityId: string,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip>;
  activateTrip(
    tripId: string,
    request: ActivateTripRequest,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<ExecutionTransition>;
  deactivateTrip(
    tripId: string,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<ExecutionTransition>;
  createWebSocketTicket(
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<WebSocketTicket>;
  resolvePlace(
    coordinate: Coordinate,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<PlaceResolution>;
  createPlace(
    resolutionToken: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Place>;
}

export class ApiError extends Error {
  readonly status: number;
  readonly problem?: Problem;

  constructor(status: number, problem?: Problem) {
    super(problem?.title ?? `LiveRoute request failed (${status})`);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
  }
}

function configuredOrigin(): string {
  return (import.meta.env.VITE_LIVEROUTE_API_ORIGIN ?? "").replace(/\/$/, "");
}

async function readProblem(response: Response): Promise<Problem | undefined> {
  if (
    !response.headers.get("content-type")?.includes("application/problem+json")
  ) {
    return undefined;
  }
  return (await response.json()) as Problem;
}

export class HttpLiveRouteApi implements LiveRouteApi {
  readonly #origin: string;

  constructor(origin = configuredOrigin()) {
    this.#origin = origin.replace(/\/$/, "");
  }

  async #get<T>(path: string, signal?: AbortSignal): Promise<T> {
    const response = await fetch(`${this.#origin}/api/v1${path}`, {
      method: "GET",
      credentials: "include",
      headers: { Accept: "application/json" },
      signal,
    });
    if (!response.ok) {
      throw new ApiError(response.status, await readProblem(response));
    }
    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  }

  async #postUnauthenticated<T>(
    path: string,
    body?: unknown,
    signal?: AbortSignal,
  ): Promise<T> {
    const headers: Record<string, string> = { Accept: "application/json" };
    const request: RequestInit = {
      method: "POST",
      credentials: "include",
      headers,
      signal,
    };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      request.body = JSON.stringify(body);
    }
    const response = await fetch(`${this.#origin}/api/v1${path}`, request);
    if (!response.ok) {
      throw new ApiError(response.status, await readProblem(response));
    }
    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  }

  async #post<T>(
    path: string,
    body: unknown,
    csrfToken: string,
    signal?: AbortSignal,
    ifMatch?: string,
    includeIdempotency = true,
  ): Promise<T> {
    const headers: Record<string, string> = {
      Accept: "application/json",
      "X-CSRF-Token": csrfToken,
    };
    if (includeIdempotency) headers["Idempotency-Key"] = crypto.randomUUID();
    if (ifMatch) headers["If-Match"] = ifMatch;
    const request: RequestInit = {
      method: "POST",
      credentials: "include",
      headers,
      signal,
    };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      request.body = JSON.stringify(body);
    }
    const response = await fetch(this.#origin + "/api/v1" + path, request);
    if (!response.ok) {
      throw new ApiError(response.status, await readProblem(response));
    }
    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  }

  async #mutation<T>(
    method: "PATCH" | "DELETE",
    path: string,
    body: unknown,
    csrfToken: string,
    signal: AbortSignal | undefined,
    ifMatch: string,
  ): Promise<T> {
    const headers: Record<string, string> = {
      Accept: "application/json",
      "X-CSRF-Token": csrfToken,
      "Idempotency-Key": crypto.randomUUID(),
      "If-Match": ifMatch,
    };
    const request: RequestInit = {
      method,
      credentials: "include",
      headers,
      signal,
    };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      request.body = JSON.stringify(body);
    }
    const response = await fetch(this.#origin + "/api/v1" + path, request);
    if (!response.ok) {
      throw new ApiError(response.status, await readProblem(response));
    }
    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  }

  getSession(signal?: AbortSignal): Promise<Session> {
    return this.#get<Session>("/session", signal);
  }

  logout(csrfToken: string, signal?: AbortSignal): Promise<void> {
    return this.#post<void>(
      "/auth/logout",
      undefined,
      csrfToken,
      signal,
      undefined,
      false,
    );
  }

  createGoogleLoginNonce(signal?: AbortSignal): Promise<GoogleNonceResponse> {
    return this.#postUnauthenticated<GoogleNonceResponse>(
      "/auth/google/nonce",
      undefined,
      signal,
    );
  }

  authenticateWithGoogle(
    request: GoogleLoginRequest,
    signal?: AbortSignal,
  ): Promise<Session> {
    return this.#postUnauthenticated<Session>("/auth/google", request, signal);
  }

  listTrips(signal?: AbortSignal): Promise<TripList> {
    return this.#get<TripList>("/trips", signal);
  }

  getTrip(tripId: string, signal?: AbortSignal): Promise<Trip> {
    return this.#get<Trip>(`/trips/${encodeURIComponent(tripId)}`, signal);
  }

  createTrip(
    request: CreateTripRequest,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip> {
    return this.#post<Trip>("/trips", request, csrfToken, signal);
  }

  updateTrip(
    tripId: string,
    request: UpdateTripRequest,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip> {
    return this.#mutation<Trip>(
      "PATCH",
      "/trips/" + encodeURIComponent(tripId),
      request,
      csrfToken,
      signal,
      '"trip-revision-' + tripRevision + '"',
    );
  }

  deleteTrip(
    tripId: string,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<void> {
    return this.#mutation<void>(
      "DELETE",
      "/trips/" + encodeURIComponent(tripId),
      undefined,
      csrfToken,
      signal,
      '"trip-revision-' + tripRevision + '"',
    );
  }

  addTripActivity(
    tripId: string,
    activity: ActivityInput,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip> {
    return this.#post<Trip>(
      "/trips/" + encodeURIComponent(tripId) + "/activities",
      activity,
      csrfToken,
      signal,
      '"trip-revision-' + tripRevision + '"',
    );
  }

  replaceTripActivity(
    tripId: string,
    activityId: string,
    activity: ActivityInput,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip> {
    return this.#mutation<Trip>(
      "PATCH",
      "/trips/" +
        encodeURIComponent(tripId) +
        "/activities/" +
        encodeURIComponent(activityId),
      activity,
      csrfToken,
      signal,
      '"trip-revision-' + tripRevision + '"',
    );
  }

  deleteTripActivity(
    tripId: string,
    activityId: string,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Trip> {
    return this.#mutation<Trip>(
      "DELETE",
      "/trips/" +
        encodeURIComponent(tripId) +
        "/activities/" +
        encodeURIComponent(activityId),
      undefined,
      csrfToken,
      signal,
      '"trip-revision-' + tripRevision + '"',
    );
  }

  activateTrip(
    tripId: string,
    request: ActivateTripRequest,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<ExecutionTransition> {
    return this.#post<ExecutionTransition>(
      "/trips/" + encodeURIComponent(tripId) + "/activate",
      request,
      csrfToken,
      signal,
      '"trip-revision-' + tripRevision + '"',
    );
  }

  deactivateTrip(
    tripId: string,
    tripRevision: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<ExecutionTransition> {
    return this.#post<ExecutionTransition>(
      "/trips/" + encodeURIComponent(tripId) + "/deactivate",
      undefined,
      csrfToken,
      signal,
      '"trip-revision-' + tripRevision + '"',
    );
  }

  createWebSocketTicket(
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<WebSocketTicket> {
    return this.#post<WebSocketTicket>(
      "/auth/ws-ticket",
      undefined,
      csrfToken,
      signal,
      undefined,
      false,
    );
  }

  resolvePlace(
    coordinate: Coordinate,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<PlaceResolution> {
    return this.#post<PlaceResolution>(
      "/places/resolve",
      coordinate,
      csrfToken,
      signal,
    );
  }

  createPlace(
    resolutionToken: string,
    csrfToken: string,
    signal?: AbortSignal,
  ): Promise<Place> {
    return this.#post<Place>(
      "/places",
      { resolution_token: resolutionToken },
      csrfToken,
      signal,
    );
  }
}

export const liveRouteApi = new HttpLiveRouteApi();

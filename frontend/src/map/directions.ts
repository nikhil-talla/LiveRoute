import type { Trip } from "../api/types";

export const MAX_DIRECTIONS_COORDINATES = 25;

export type RouteProfile = "mapbox/driving" | "mapbox/walking";
export type RouteCoordinate = [number, number];

export interface DirectionsRequest {
  planId: string;
  planRevision: string;
  profile: RouteProfile;
  coordinates: RouteCoordinate[];
}

export interface RouteGeometry {
  profile: RouteProfile;
  coordinates: RouteCoordinate[];
  durationSeconds?: number;
  distanceMeters?: number;
}

export interface DirectionsRouteResult extends RouteGeometry {
  durationSeconds: number;
  distanceMeters: number;
}

function profileForMode(mode: "walking" | "driving"): RouteProfile {
  return mode === "walking" ? "mapbox/walking" : "mapbox/driving";
}

function coordinateKey(coordinates: RouteCoordinate[]): string {
  return coordinates
    .map(([longitude, latitude]) => String(longitude) + "," + String(latitude))
    .join(";");
}

function requestKey(request: DirectionsRequest): string {
  return [
    request.planId,
    request.planRevision,
    request.profile,
    coordinateKey(request.coordinates),
  ].join("|");
}

export function buildDirectionsRequests(trip: Trip): DirectionsRequest[] {
  const activities = trip.saved_plan.activities;
  if (activities.length < 2) return [];

  const requests: DirectionsRequest[] = [];
  let groupStart = 0;
  let destinationIndex = 1;

  while (destinationIndex < activities.length) {
    const mode = activities[destinationIndex]!.inbound_travel_mode;
    let groupEnd = destinationIndex;
    while (
      groupEnd + 1 < activities.length &&
      activities[groupEnd + 1]!.inbound_travel_mode === mode
    ) {
      groupEnd += 1;
    }

    const groupCoordinates = activities
      .slice(groupStart, groupEnd + 1)
      .map((activity): RouteCoordinate => [
        activity.place.longitude,
        activity.place.latitude,
      ]);
    let chunkStart = 0;
    while (chunkStart < groupCoordinates.length - 1) {
      const chunkEnd = Math.min(
        chunkStart + MAX_DIRECTIONS_COORDINATES - 1,
        groupCoordinates.length - 1,
      );
      requests.push({
        planId: trip.saved_plan.saved_plan_id,
        planRevision: trip.saved_plan.saved_plan_revision,
        profile: profileForMode(mode),
        coordinates: groupCoordinates.slice(chunkStart, chunkEnd + 1),
      });
      if (chunkEnd === groupCoordinates.length - 1) break;
      chunkStart = chunkEnd;
    }

    groupStart = groupEnd;
    destinationIndex = groupEnd + 1;
  }

  return requests;
}

async function fetchDirections(
  request: DirectionsRequest,
  accessToken: string,
  signal?: AbortSignal,
): Promise<RouteGeometry> {
  const coordinates = coordinateKey(request.coordinates);
  const response = await fetch(
    "https://api.mapbox.com/directions/v5/" +
      request.profile +
      "/" +
      coordinates +
      "?alternatives=false&geometries=geojson&overview=full&access_token=" +
      encodeURIComponent(accessToken),
    { headers: { Accept: "application/json" }, signal },
  );
  if (!response.ok) {
    throw new Error("Mapbox Directions failed (" + response.status + ").");
  }
  const body: unknown = await response.json();
  if (
    !body ||
    typeof body !== "object" ||
    !("routes" in body) ||
    !Array.isArray(body.routes)
  ) {
    throw new Error("Mapbox Directions returned an invalid route.");
  }
  const geometry = body.routes[0]?.geometry;
  if (
    !geometry ||
    typeof geometry !== "object" ||
    !("type" in geometry) ||
    geometry.type !== "LineString" ||
    !("coordinates" in geometry) ||
    !Array.isArray(geometry.coordinates)
  ) {
    throw new Error("Mapbox Directions returned no route geometry.");
  }
  const coordinatesFromResponse: unknown[] = geometry.coordinates;
  const isRouteCoordinate = (
    coordinate: unknown,
  ): coordinate is RouteCoordinate =>
    Array.isArray(coordinate) &&
    coordinate.length >= 2 &&
    typeof coordinate[0] === "number" &&
    typeof coordinate[1] === "number";
  if (!coordinatesFromResponse.every(isRouteCoordinate)) {
    throw new Error("Mapbox Directions returned invalid route coordinates.");
  }
  const validCoordinates = coordinatesFromResponse as RouteCoordinate[];
  return {
    profile: request.profile,
    coordinates: validCoordinates.map(([longitude, latitude]) => [
      longitude,
      latitude,
    ]),
    ...(typeof body.routes[0]?.duration === "number" &&
    Number.isFinite(body.routes[0].duration) &&
    body.routes[0].duration >= 0
      ? { durationSeconds: body.routes[0].duration }
      : {}),
    ...(typeof body.routes[0]?.distance === "number" &&
    Number.isFinite(body.routes[0].distance) &&
    body.routes[0].distance >= 0
      ? { distanceMeters: body.routes[0].distance }
      : {}),
  };
}

export async function requestDirectionsRoute(
  coordinates: RouteCoordinate[],
  profile: RouteProfile,
  accessToken: string,
  signal?: AbortSignal,
): Promise<DirectionsRouteResult> {
  const coordinatePath = coordinateKey(coordinates);
  const response = await fetch(
    "https://api.mapbox.com/directions/v5/" +
      profile +
      "/" +
      coordinatePath +
      "?alternatives=false&geometries=geojson&overview=full&access_token=" +
      encodeURIComponent(accessToken),
    { headers: { Accept: "application/json" }, signal },
  );
  if (!response.ok) {
    throw new DirectionsHttpError(response.status);
  }
  const body: unknown = await response.json();
  if (
    !body ||
    typeof body !== "object" ||
    !("routes" in body) ||
    !Array.isArray(body.routes)
  ) {
    throw new Error("Mapbox Directions returned an invalid route.");
  }
  const route = body.routes[0];
  const geometry = route?.geometry;
  if (
    !geometry ||
    typeof geometry !== "object" ||
    !("type" in geometry) ||
    geometry.type !== "LineString" ||
    !("coordinates" in geometry) ||
    !Array.isArray(geometry.coordinates) ||
    typeof route.duration !== "number" ||
    !Number.isFinite(route.duration) ||
    route.duration < 0 ||
    typeof route.distance !== "number" ||
    !Number.isFinite(route.distance) ||
    route.distance < 0
  ) {
    throw new Error("Mapbox Directions returned no complete route.");
  }
  const routeCoordinates = geometry.coordinates;
  if (
    routeCoordinates.length < 2 ||
    !routeCoordinates.every(
      (coordinate: unknown): coordinate is RouteCoordinate =>
        Array.isArray(coordinate) &&
        coordinate.length >= 2 &&
        typeof coordinate[0] === "number" &&
        Number.isFinite(coordinate[0]) &&
        typeof coordinate[1] === "number" &&
        Number.isFinite(coordinate[1]),
    )
  ) {
    throw new Error("Mapbox Directions returned invalid route coordinates.");
  }
  const validCoordinates = routeCoordinates as RouteCoordinate[];
  return {
    profile,
    coordinates: validCoordinates.map(([longitude, latitude]) => [
      longitude,
      latitude,
    ]),
    durationSeconds: route.duration,
    distanceMeters: route.distance,
  };
}

export class DirectionsHttpError extends Error {
  readonly status: number;

  constructor(status: number) {
    super("Mapbox Directions failed (" + status + ").");
    this.name = "DirectionsHttpError";
    this.status = status;
  }
}

export class RouteCache {
  readonly #entries = new Map<string, Promise<RouteGeometry>>();

  get(
    request: DirectionsRequest,
    accessToken: string,
    signal?: AbortSignal,
  ): Promise<RouteGeometry> {
    const key = requestKey(request);
    const existing = this.#entries.get(key);
    if (existing) return existing;

    const pending = fetchDirections(request, accessToken, signal).catch(
      (error: unknown) => {
        if (this.#entries.get(key) === pending) this.#entries.delete(key);
        throw error;
      },
    );
    this.#entries.set(key, pending);
    return pending;
  }

  retain(planId: string, keys: Set<string>): void {
    for (const key of this.#entries.keys()) {
      if (key.startsWith(planId + "|") && !keys.has(key)) {
        this.#entries.delete(key);
      }
    }
  }
}

export function directionsRequestKey(request: DirectionsRequest): string {
  return requestKey(request);
}

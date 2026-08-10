import {
  buildNavigationExtension,
  distanceToPolylineMeters,
  isMaterialEtaChange,
  RouteDeviationTracker,
  SameDestinationRerouter,
  type NavigationRoute,
} from "./routeDeviation";
import type { GpsLocationSample } from "./gpsTelemetry";

const route: NavigationRoute = {
  routeIdentity: "canonical-route-1",
  navigationRouteId: "00000000-0000-4000-8000-000000000001",
  nextActivityId: "11111111-1111-4111-8111-111111111111",
  profile: "mapbox/walking",
  coordinates: [
    [-71.412, 41.824],
    [-71.412, 41.825],
  ],
  previousEtaUnixMs: 1_000_000,
  remainingDurationSeconds: 300,
  remainingDistanceMeters: 500,
};

function sample(
  timestamp: number,
  longitude = -71.4116,
  accuracy = 5,
): GpsLocationSample {
  return {
    latitude: 41.8245,
    longitude,
    accuracy,
    observedAtUnixMs: timestamp,
  };
}

function directionsResponse(): Response {
  return new Response(
    JSON.stringify({
      routes: [
        {
          duration: 301.2,
          distance: 501.1,
          geometry: {
            type: "LineString",
            coordinates: [
              [-71.4116, 41.8245],
              [-71.412, 41.825],
            ],
          },
        },
      ],
    }),
    { status: 200, headers: { "content-type": "application/json" } },
  );
}

describe("same-destination route deviation baseline", () => {
  it("uses the accuracy-adjusted polyline distance and confirms after three spaced samples", () => {
    const confirmed = vi.fn();
    const tracker = new RouteDeviationTracker({
      onConfirmedDeviation: confirmed,
    });
    tracker.reset(route.routeIdentity);
    const measured = distanceToPolylineMeters(sample(0), route.coordinates);
    expect(measured).toBeGreaterThan(20);

    tracker.observe(sample(0), route);
    tracker.observe(sample(500), route);
    tracker.observe(sample(1_000), route);
    tracker.observe(sample(2_000), route);
    expect(confirmed).toHaveBeenCalledOnce();
    expect(confirmed).toHaveBeenCalledWith(
      expect.objectContaining({
        distanceFromRouteMeters: expect.any(Number),
        effectiveDistanceMeters: expect.any(Number),
      }),
    );
  });

  it("requires two spaced recovery samples and resets on a changed route", () => {
    const recovered = vi.fn();
    const states: Array<{
      offRoute: boolean;
      entrySamples: number;
      recoverySamples: number;
    }> = [];
    const tracker = new RouteDeviationTracker({
      onConfirmedDeviation: vi.fn(),
      onRecovered: recovered,
      onStateChange: (state) => states.push(state),
    });
    tracker.reset(route.routeIdentity);
    tracker.observe(sample(0), route);
    tracker.observe(sample(1_000), route);
    tracker.observe(sample(2_000), route);
    tracker.observe(sample(3_000, -71.412, 5), route);
    tracker.observe(sample(4_000, -71.412, 5), route);
    expect(recovered).toHaveBeenCalledOnce();
    tracker.observe(sample(5_000), { ...route, routeIdentity: "new-route" });
    expect(states.at(-1)?.offRoute).toBe(false);
  });

  it("marks five-minute delays and crossed boundaries as material", () => {
    expect(
      isMaterialEtaChange(1_000_000, 1_300_000, { boundariesUnixMs: [] }),
    ).toBe(true);
    expect(
      isMaterialEtaChange(1_000_000, 1_000_100, {
        boundariesUnixMs: [1_000_050],
      }),
    ).toBe(true);
    expect(
      isMaterialEtaChange(1_000_000, 999_999, { boundariesUnixMs: [] }),
    ).toBe(false);
  });

  it("retries a network failure once and replaces only the same destination route", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi
        .spyOn(globalThis, "fetch")
        .mockRejectedValueOnce(new TypeError("network down"))
        .mockResolvedValueOnce(directionsResponse());
      const success = vi.fn();
      const failure = vi.fn();
      const rerouter = new SameDestinationRerouter({
        accessToken: "public-token",
        now: () => 2_000_000,
        onSuccess: success,
        onFailure: failure,
      });
      rerouter.setRoute(route);
      rerouter.request(
        {
          sample: sample(2_000_000),
          distanceFromRouteMeters: 30,
          effectiveDistanceMeters: 25,
        },
        route,
      );
      await Promise.resolve();
      await Promise.resolve();
      expect(fetchMock).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(2_000);
      await Promise.resolve();
      await Promise.resolve();
      expect(fetchMock).toHaveBeenCalledTimes(2);
      await vi.waitFor(() => expect(success).toHaveBeenCalledOnce());
      expect(failure).not.toHaveBeenCalled();
      expect(success.mock.calls[0]?.[0]).toMatchObject({
        nextActivityId: route.nextActivityId,
        remainingDurationSeconds: 302,
        remainingDistanceMeters: 502,
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("builds the strict namespaced extension", () => {
    expect(
      buildNavigationExtension({
        nextActivityId: route.nextActivityId,
        navigationRouteId: "22222222-2222-4222-8222-222222222222",
        previousEtaUnixMs: 1_000_000,
        updatedEtaUnixMs: 1_000_001,
        remainingDurationSeconds: 1,
        remainingDistanceMeters: 2,
      }),
    ).toEqual({
      "liveroute.navigation_v1": {
        policy_version: "liveroute-navigation-v1-baseline-1",
        next_activity_id: route.nextActivityId,
        navigation_route_id: "22222222-2222-4222-8222-222222222222",
        previous_eta_unix_ms: 1_000_000,
        updated_eta_unix_ms: 1_000_001,
        remaining_duration_seconds: 1,
        remaining_distance_meters: 2,
        off_route: true,
      },
    });
  });
});

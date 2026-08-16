import { HttpLiveRouteApi } from "./client";

describe("HttpLiveRouteApi", () => {
  it("creates a nonce with only the OIDC binding cookie", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({ nonce: "nonce", expires_at_unix_ms: 1000 }),
          { status: 201, headers: { "content-type": "application/json" } },
        ),
      );

    await new HttpLiveRouteApi(
      "http://localhost:8080",
    ).createGoogleLoginNonce();

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/auth/google/nonce",
      {
        method: "POST",
        credentials: "include",
        headers: { Accept: "application/json" },
        signal: undefined,
      },
    );
  });

  it("exchanges a nonce-bound Google credential for a LiveRoute session", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ user: {}, csrf_token: "csrf" }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );
    const request = {
      credential: "header.payload.signature",
      default_time_zone_name: "America/New_York",
    };

    await new HttpLiveRouteApi("http://localhost:8080").authenticateWithGoogle(
      request,
    );

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/auth/google",
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        body: JSON.stringify(request),
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
      }),
    );
  });

  it("restores a session with credentials and the fixed API path", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          user: {
            user_id: "11111111-1111-4111-8111-111111111111",
            display_name: "Nikhil",
            default_time_zone_name: "America/New_York",
          },
          csrf_token: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
          idle_expires_at_unix_ms: 1,
          absolute_expires_at_unix_ms: 2,
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await new HttpLiveRouteApi("http://localhost:8080/").getSession();

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/session",
      expect.objectContaining({ method: "GET", credentials: "include" }),
    );
  });

  it("loads one full trip with credentials", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          trip_id: "22222222-2222-4222-8222-222222222222",
          trip_name: "Saturday in Providence",
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    await new HttpLiveRouteApi("http://localhost:8080/").getTrip(
      "22222222-2222-4222-8222-222222222222",
    );

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/trips/22222222-2222-4222-8222-222222222222",
      expect.objectContaining({ method: "GET", credentials: "include" }),
    );
  });

  it("creates a complete inactive trip with CSRF and idempotency headers", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ trip_id: "trip-id" }), {
        status: 201,
        headers: { "content-type": "application/json" },
      }),
    );
    const request = {
      trip_name: "Saturday in Providence",
      default_time_zone_name: "America/New_York",
      activities: [
        {
          place_id: "66666666-6666-4666-8666-666666666666",
          ordinal: 0,
          schedule: { state: "unscheduled" as const },
          inbound_travel_mode: "driving" as const,
          activity_class: "flexible" as const,
          priority_rank: 0,
          utility_score: 0,
          timing: {
            open_windows: [
              { opens_offset_ms: 0, closes_offset_ms: 86_400_000 },
            ],
            reservation_grace_seconds: 0,
            min_duration_seconds: 3_600,
            preferred_duration_seconds: 3_600,
            max_duration_seconds: 3_600,
            mandatory: false,
            can_shorten: false as const,
            can_move: true,
            can_skip: true,
          },
        },
      ],
    };

    await new HttpLiveRouteApi("http://localhost:8080").createTrip(
      request,
      "csrf-token",
    );

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/trips",
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        body: JSON.stringify(request),
        headers: expect.objectContaining({
          "X-CSRF-Token": "csrf-token",
          "Idempotency-Key": expect.any(String),
        }),
      }),
    );
  });

  it("resolves a selected coordinate with CSRF and a fresh idempotency key", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ resolution_token: "token" }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );

    await new HttpLiveRouteApi("http://localhost:8080").resolvePlace(
      { latitude: 41.824, longitude: -71.412 },
      "csrf-token",
    );

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/places/resolve",
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        body: JSON.stringify({ latitude: 41.824, longitude: -71.412 }),
        headers: expect.objectContaining({
          "X-CSRF-Token": "csrf-token",
          "Idempotency-Key": expect.any(String),
        }),
      }),
    );
  });

  it("activates with the starting location and strong trip revision precondition", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ trip: {}, operation: {} }), {
        status: 202,
        headers: { "content-type": "application/json" },
      }),
    );

    await new HttpLiveRouteApi("http://localhost:8080").activateTrip(
      "22222222-2222-4222-8222-222222222222",
      { starting_location: { latitude: 41.824, longitude: -71.412 } },
      "4",
      "csrf-token",
    );

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/trips/22222222-2222-4222-8222-222222222222/activate",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          starting_location: { latitude: 41.824, longitude: -71.412 },
        }),
        headers: expect.objectContaining({
          "If-Match": '"trip-revision-4"',
          "X-CSRF-Token": "csrf-token",
          "Idempotency-Key": expect.any(String),
        }),
      }),
    );
  });

  it("deactivates with CSRF, idempotency, and a strong trip revision precondition", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ trip: {}, operation: {} }), {
        status: 202,
        headers: { "content-type": "application/json" },
      }),
    );

    await new HttpLiveRouteApi("http://localhost:8080").deactivateTrip(
      "22222222-2222-4222-8222-222222222222",
      "7",
      "csrf-token",
    );

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/trips/22222222-2222-4222-8222-222222222222/deactivate",
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        headers: expect.objectContaining({
          "If-Match": '"trip-revision-7"',
          "X-CSRF-Token": "csrf-token",
          "Idempotency-Key": expect.any(String),
        }),
      }),
    );
  });

  it("uses revision-checked HTTP mutations for saved-plan editing", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(
      async () =>
        new Response(JSON.stringify({ trip: "updated" }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
    );
    const activity = {
      place_id: "66666666-6666-4666-8666-666666666666",
      ordinal: 0,
      schedule: { state: "unscheduled" as const },
      inbound_travel_mode: "driving" as const,
      activity_class: "flexible" as const,
      priority_rank: 0,
      utility_score: 0,
      timing: {
        open_windows: [{ opens_offset_ms: 0, closes_offset_ms: 86_400_000 }],
        reservation_grace_seconds: 0,
        min_duration_seconds: 3_600,
        preferred_duration_seconds: 3_600,
        max_duration_seconds: 3_600,
        mandatory: false,
        can_shorten: false as const,
        can_move: true,
        can_skip: true,
      },
    };
    const client = new HttpLiveRouteApi("http://localhost:8080");

    await client.updateTrip(
      "trip-id",
      { trip_name: "Updated" },
      "4",
      "csrf-token",
    );
    await client.addTripActivity("trip-id", activity, "5", "csrf-token");
    await client.replaceTripActivity(
      "trip-id",
      "activity-id",
      activity,
      "6",
      "csrf-token",
    );
    await client.deleteTripActivity(
      "trip-id",
      "activity-id",
      "7",
      "csrf-token",
    );

    expect(fetchMock.mock.calls.map(([, init]) => init?.method)).toEqual([
      "PATCH",
      "POST",
      "PATCH",
      "DELETE",
    ]);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "http://localhost:8080/api/v1/trips/trip-id",
    );
    expect(fetchMock.mock.calls[1]?.[0]).toBe(
      "http://localhost:8080/api/v1/trips/trip-id/activities",
    );
    expect(fetchMock.mock.calls[2]?.[0]).toBe(
      "http://localhost:8080/api/v1/trips/trip-id/activities/activity-id",
    );
    expect(fetchMock.mock.calls[3]?.[1]).toEqual(
      expect.objectContaining({
        headers: expect.objectContaining({
          "If-Match": '"trip-revision-7"',
          "X-CSRF-Token": "csrf-token",
          "Idempotency-Key": expect.any(String),
        }),
      }),
    );
  });

  it("deletes a saved trip and accepts the contract's empty response", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response(null, { status: 204 }));

    await new HttpLiveRouteApi("http://localhost:8080").deleteTrip(
      "trip-id",
      "8",
      "csrf-token",
    );

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/trips/trip-id",
      expect.objectContaining({
        method: "DELETE",
        headers: expect.objectContaining({
          "If-Match": '"trip-revision-8"',
          "X-CSRF-Token": "csrf-token",
          "Idempotency-Key": expect.any(String),
        }),
      }),
    );
  });

  it("logs out with CSRF and no idempotency body", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response(null, { status: 204 }));

    await new HttpLiveRouteApi("http://localhost:8080").logout("csrf-token");

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/auth/logout",
      expect.objectContaining({
        method: "POST",
        headers: {
          Accept: "application/json",
          "X-CSRF-Token": "csrf-token",
        },
      }),
    );
    expect(fetchMock.mock.calls[0]?.[1]).not.toHaveProperty("body");
  });

  it("requests a WebSocket ticket without an idempotency body", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({ ticket: "ticket", expires_at_unix_ms: 1 }),
        {
          status: 201,
          headers: { "content-type": "application/json" },
        },
      ),
    );

    await new HttpLiveRouteApi("http://localhost:8080").createWebSocketTicket(
      "csrf-token",
    );

    expect(fetchMock).toHaveBeenCalledWith(
      "http://localhost:8080/api/v1/auth/ws-ticket",
      expect.objectContaining({
        method: "POST",
        headers: {
          Accept: "application/json",
          "X-CSRF-Token": "csrf-token",
        },
      }),
    );
    expect(fetchMock.mock.calls[0]?.[1]).not.toHaveProperty("body");
  });
});

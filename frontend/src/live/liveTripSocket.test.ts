import type { LiveRouteApi } from "../api/client";
import { LiveTripSocket } from "./liveTripSocket";

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  readonly url: string;
  readonly sent: Record<string, unknown>[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  send(data: string): void {
    this.sent.push(JSON.parse(data) as Record<string, unknown>);
  }

  close(): void {
    this.onclose?.();
  }

  open(): void {
    this.onopen?.();
  }

  receive(message: Record<string, unknown>): void {
    this.onmessage?.({
      data: JSON.stringify(message),
    } as MessageEvent<string>);
  }
}

function connectionReady(): Record<string, unknown> {
  return {
    protocol_version: "liveroute.v1",
    server_message_id: "11111111-1111-4111-8111-111111111111",
    kind: "connection_ready",
    status: "OK",
    retryable: false,
    payload: {
      user_id: "22222222-2222-4222-8222-222222222222",
      backend_instance_id: "33333333-3333-4333-8333-333333333333",
      heartbeat_interval_ms: 10_000,
      idle_timeout_ms: 30_000,
      max_frame_bytes: 262_144,
      max_outstanding_resync_ids: 128,
    },
  };
}

function subscriptionState(): Record<string, unknown> {
  return {
    protocol_version: "liveroute.v1",
    server_message_id: "44444444-4444-4444-8444-444444444444",
    kind: "subscription_state",
    status: "OK",
    retryable: false,
    trip_id: "55555555-5555-4555-8555-555555555555",
    trip_revision: "4",
    runtime_epoch: "1",
    planner_state_version: "2",
    accepted_mutation_sequence: "3",
    accepted_observation_sequence: "4",
    payload: {
      subscribed: true,
      trip: {},
      current_plan: {},
      runtime_sync_state: "synced",
    },
  };
}

describe("LiveTripSocket", () => {
  beforeEach(() => {
    FakeWebSocket.instances = [];
    vi.stubGlobal("WebSocket", FakeWebSocket);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("gets a ticket, authenticates first, then gates subscription on connection_ready", async () => {
    const createWebSocketTicket = vi.fn(async () => ({
      ticket: "A".repeat(43),
      expires_at_unix_ms: 1,
    }));
    const api = { createWebSocketTicket } as unknown as LiveRouteApi;
    const client = new LiveTripSocket({
      api,
      csrfToken: "csrf-token",
      tripId: "55555555-5555-4555-8555-555555555555",
      resume: { last_trip_revision: "3" },
    });

    const connected = client.connect();
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const socket = FakeWebSocket.instances[0]!;
    expect(createWebSocketTicket).toHaveBeenCalledWith("csrf-token");
    expect(socket.url).toMatch(/\/ws$/);

    socket.open();
    expect(socket.sent).toHaveLength(1);
    expect(socket.sent[0]).toMatchObject({
      protocol_version: "liveroute.v1",
      kind: "authenticate",
      payload: { token: "A".repeat(43) },
    });

    socket.receive(connectionReady());
    expect(socket.sent).toHaveLength(2);
    expect(socket.sent[1]).toMatchObject({
      protocol_version: "liveroute.v1",
      kind: "subscribe_trip",
      trip_id: "55555555-5555-4555-8555-555555555555",
      payload: { last_trip_revision: "3" },
    });

    socket.receive(subscriptionState());
    await expect(connected).resolves.toMatchObject({
      kind: "subscription_state",
      payload: { subscribed: true },
    });
    client.close();
  });

  it("answers server pings with a protocol pong", async () => {
    const api = {
      createWebSocketTicket: async () => ({
        ticket: "A".repeat(43),
        expires_at_unix_ms: 1,
      }),
    } as unknown as LiveRouteApi;
    const client = new LiveTripSocket({
      api,
      csrfToken: "csrf-token",
      tripId: "55555555-5555-4555-8555-555555555555",
    });
    const connected = client.connect();
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const socket = FakeWebSocket.instances[0]!;
    socket.open();
    socket.receive(connectionReady());
    socket.receive({
      protocol_version: "liveroute.v1",
      server_message_id: "66666666-6666-4666-8666-666666666666",
      kind: "ping",
      status: "OK",
      retryable: false,
      payload: { nonce: "heartbeat" },
    });

    expect(socket.sent[2]).toMatchObject({
      protocol_version: "liveroute.v1",
      kind: "pong",
      payload: { nonce: "heartbeat" },
    });
    socket.receive(subscriptionState());
    await connected;
    client.close();
  });

  it("sends location telemetry only after subscription with the geolocation timestamp", async () => {
    const api = {
      createWebSocketTicket: async () => ({
        ticket: "A".repeat(43),
        expires_at_unix_ms: 1,
      }),
    } as unknown as LiveRouteApi;
    const client = new LiveTripSocket({
      api,
      csrfToken: "csrf-token",
      tripId: "55555555-5555-4555-8555-555555555555",
    });
    expect(
      client.sendLocationTelemetry({
        latitude: 41.824,
        longitude: -71.412,
        observedAtUnixMs: 1,
      }),
    ).toBeNull();

    const connected = client.connect();
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const socket = FakeWebSocket.instances[0]!;
    socket.open();
    socket.receive(connectionReady());
    socket.receive(subscriptionState());
    await connected;

    const messageId = client.sendLocationTelemetry({
      latitude: 41.824,
      longitude: -71.412,
      observedAtUnixMs: 1_786_291_200_123,
    });
    expect(messageId).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    );
    expect(socket.sent[2]).toMatchObject({
      protocol_version: "liveroute.v1",
      kind: "telemetry_update",
      trip_id: "55555555-5555-4555-8555-555555555555",
      payload: {
        observation_kind: "location",
        observed_at_unix_ms: 1_786_291_200_123,
        observation: { latitude: 41.824, longitude: -71.412 },
      },
    });
    client.close();
  });

  it("uses bounded resynchronization after a reconnect", async () => {
    const api = {
      createWebSocketTicket: async () => ({
        ticket: "A".repeat(43),
        expires_at_unix_ms: 1,
      }),
    } as unknown as LiveRouteApi;
    const client = new LiveTripSocket({
      api,
      csrfToken: "csrf-token",
      tripId: "55555555-5555-4555-8555-555555555555",
      resynchronize: true,
      resume: {
        last_runtime_epoch: "7",
        last_planner_state_version: "8",
        last_trip_revision: "9",
      },
    });
    const connected = client.connect();
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const socket = FakeWebSocket.instances[0]!;
    socket.open();
    socket.receive(connectionReady());
    expect(socket.sent[1]).toMatchObject({
      kind: "resynchronize_trip",
      payload: {
        last_runtime_epoch: "7",
        last_planner_state_version: "8",
        last_trip_revision: "9",
        outstanding_message_ids: [],
      },
    });
    socket.receive({
      protocol_version: "liveroute.v1",
      server_message_id: "77777777-7777-4777-8777-777777777777",
      kind: "resynchronization_state",
      status: "OK",
      retryable: false,
      trip_id: "55555555-5555-4555-8555-555555555555",
      trip_revision: "9",
      runtime_epoch: "7",
      planner_state_version: "8",
      accepted_mutation_sequence: "10",
      accepted_observation_sequence: "11",
      payload: {
        trip: {},
        current_plan: {},
        runtime_sync_state: "synced",
        outcomes: [],
      },
    });
    await expect(connected).resolves.toMatchObject({
      kind: "resynchronization_state",
    });
    client.close();
  });

  it("keeps navigation metadata in the namespaced telemetry extension", async () => {
    const api = {
      createWebSocketTicket: async () => ({
        ticket: "A".repeat(43),
        expires_at_unix_ms: 1,
      }),
    } as unknown as LiveRouteApi;
    const client = new LiveTripSocket({
      api,
      csrfToken: "csrf-token",
      tripId: "55555555-5555-4555-8555-555555555555",
    });
    const connected = client.connect();
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const socket = FakeWebSocket.instances[0]!;
    socket.open();
    socket.receive(connectionReady());
    socket.receive(subscriptionState());
    await connected;

    client.sendRouteDeviationTelemetry({
      latitude: 41.824,
      longitude: -71.412,
      observedAtUnixMs: 1_786_291_200_123,
      distanceFromRouteMeters: 31,
      extensions: {
        "liveroute.navigation_v1": {
          policy_version: "liveroute-navigation-v1-baseline-1",
          next_activity_id: "66666666-6666-4666-8666-666666666666",
          navigation_route_id: "77777777-7777-4777-8777-777777777777",
          previous_eta_unix_ms: 1,
          updated_eta_unix_ms: 2,
          remaining_duration_seconds: 1,
          remaining_distance_meters: 2,
          off_route: true,
        },
      },
    });
    expect(socket.sent[2]).toMatchObject({
      kind: "telemetry_update",
      payload: {
        observation_kind: "route_deviation",
        observation: {
          location: { latitude: 41.824, longitude: -71.412 },
          distance_from_route_meters: 31,
        },
      },
      extensions: {
        "liveroute.navigation_v1": {
          policy_version: "liveroute-navigation-v1-baseline-1",
          off_route: true,
        },
      },
    });
    client.close();
  });

  it("sends exact proposal identity decisions as trip commands", async () => {
    const api = {
      createWebSocketTicket: async () => ({
        ticket: "A".repeat(43),
        expires_at_unix_ms: 1,
      }),
    } as unknown as LiveRouteApi;
    const client = new LiveTripSocket({
      api,
      csrfToken: "csrf-token",
      tripId: "55555555-5555-4555-8555-555555555555",
    });
    const connected = client.connect();
    await vi.waitFor(() => expect(FakeWebSocket.instances).toHaveLength(1));
    const socket = FakeWebSocket.instances[0]!;
    socket.open();
    socket.receive(connectionReady());
    socket.receive(subscriptionState());
    await connected;

    const messageId = client.sendProposalDecision("accept_proposal", {
      proposal_id: "66666666-6666-4666-8666-666666666666",
      source_runtime_epoch: "7",
      source_planner_state_version: "8",
      base_current_plan_id: "77777777-7777-4777-8777-777777777777",
    });
    expect(messageId).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    );
    expect(socket.sent[2]).toMatchObject({
      kind: "trip_command",
      trip_id: "55555555-5555-4555-8555-555555555555",
      payload: {
        command_kind: "accept_proposal",
        command: {
          proposal_id: "66666666-6666-4666-8666-666666666666",
          source_runtime_epoch: "7",
          source_planner_state_version: "8",
          base_current_plan_id: "77777777-7777-4777-8777-777777777777",
        },
      },
    });
    client.close();
  });
});

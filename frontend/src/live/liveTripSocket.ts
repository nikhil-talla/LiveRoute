import type { LiveRouteApi } from "../api/client";

export interface TripResumeState {
  last_runtime_epoch?: string;
  last_planner_state_version?: string;
  last_trip_revision?: string;
  outstanding_message_ids?: string[];
}

export interface ServerEnvelope {
  protocol_version: "liveroute.v1";
  server_message_id: string;
  kind: string;
  status: string;
  retryable: boolean;
  trip_id?: string;
  trip_revision?: string;
  runtime_epoch?: string;
  planner_state_version?: string;
  accepted_mutation_sequence?: string;
  accepted_observation_sequence?: string;
  payload: Record<string, unknown>;
  in_reply_to_message_id?: string;
  extensions?: Record<string, unknown>;
}

const serverStatuses = new Set([
  "OK",
  "DUPLICATE",
  "STALE",
  "INVALID_ARGUMENT",
  "UNAUTHENTICATED",
  "PERMISSION_DENIED",
  "NOT_FOUND",
  "IDEMPOTENCY_KEY_REUSED",
  "INACTIVE_TRIP",
  "RESOURCE_EXHAUSTED",
  "DEADLINE_EXCEEDED",
  "COMMAND_EXPIRED",
  "CANCELLED",
  "INFEASIBLE",
  "PROVIDER_UNAVAILABLE",
  "DURABILITY_UNAVAILABLE",
  "UNAVAILABLE",
  "UNSUPPORTED_VERSION",
  "SNAPSHOT_NOT_READY",
  "SNAPSHOT_INCOMPATIBLE",
  "INTERNAL",
  "MATRIX_TOO_LARGE",
]);

const connectionKinds = new Set(["connection_ready", "error", "ping", "pong"]);
const tripKinds = new Set([
  "subscription_state",
  "command_acknowledgement",
  "telemetry_status",
  "planner_notification",
  "plan_proposal",
  "resynchronization_state",
  "error",
]);
const envelopeKeys = new Set([
  "protocol_version",
  "server_message_id",
  "kind",
  "status",
  "retryable",
  "trip_id",
  "trip_revision",
  "runtime_epoch",
  "planner_state_version",
  "accepted_mutation_sequence",
  "accepted_observation_sequence",
  "payload",
  "in_reply_to_message_id",
  "extensions",
]);
const uint64Pattern = /^(0|[1-9][0-9]{0,19})$/;
const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requireString(
  value: Record<string, unknown>,
  key: string,
  pattern?: RegExp,
): string {
  const result = value[key];
  if (typeof result !== "string" || (pattern && !pattern.test(result))) {
    throw new Error("Invalid WebSocket server envelope field: " + key);
  }
  return result;
}

function parseServerEnvelope(data: string): ServerEnvelope {
  let value: unknown;
  try {
    value = JSON.parse(data);
  } catch {
    throw new Error("WebSocket server message was not valid JSON.");
  }
  if (!isRecord(value)) {
    throw new Error("WebSocket server message was not an object.");
  }
  for (const key of Object.keys(value)) {
    if (!envelopeKeys.has(key)) {
      throw new Error("Unknown WebSocket server envelope field: " + key);
    }
  }

  const protocolVersion = requireString(value, "protocol_version");
  if (protocolVersion !== "liveroute.v1") {
    throw new Error("Unsupported WebSocket protocol version.");
  }
  requireString(value, "server_message_id", uuidPattern);
  const kind = requireString(value, "kind");
  const status = requireString(value, "status");
  if (!serverStatuses.has(status)) {
    throw new Error("Unknown WebSocket server status.");
  }
  if (typeof value.retryable !== "boolean") {
    throw new Error("Invalid WebSocket retryable field.");
  }
  if (!isRecord(value.payload)) {
    throw new Error("Invalid WebSocket server payload.");
  }

  const hasTripId = value.trip_id !== undefined;
  if (!connectionKinds.has(kind) && !tripKinds.has(kind)) {
    throw new Error("Unknown WebSocket server message kind.");
  }
  if (hasTripId) {
    if (!tripKinds.has(kind)) {
      throw new Error("Connection message unexpectedly has trip_id.");
    }
    requireString(value, "trip_id", uuidPattern);
    for (const key of [
      "trip_revision",
      "runtime_epoch",
      "planner_state_version",
      "accepted_mutation_sequence",
      "accepted_observation_sequence",
    ]) {
      requireString(value, key, uint64Pattern);
    }
  } else if (!connectionKinds.has(kind)) {
    throw new Error("Trip message is missing trip_id.");
  }
  if (value.in_reply_to_message_id !== undefined) {
    requireString(value, "in_reply_to_message_id", uuidPattern);
  }

  return value as unknown as ServerEnvelope;
}

function socketUrl(): string {
  const configuredOrigin =
    import.meta.env.VITE_LIVEROUTE_API_ORIGIN?.trim() || window.location.origin;
  const url = new URL("/ws", configuredOrigin);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}

function clientEnvelope(
  kind: string,
  payload: Record<string, unknown>,
  tripId?: string,
  extensions?: Record<string, unknown>,
): Record<string, unknown> {
  return {
    protocol_version: "liveroute.v1",
    message_id: crypto.randomUUID(),
    kind,
    ...(tripId ? { trip_id: tripId } : {}),
    payload,
    ...(extensions ? { extensions } : {}),
  };
}

export interface LiveTripSocketOptions {
  api: LiveRouteApi;
  csrfToken: string;
  tripId: string;
  resume?: TripResumeState;
  resynchronize?: boolean;
  onMessage?: (message: ServerEnvelope) => void;
  onClose?: () => void;
}

export class LiveTripSocket {
  readonly #options: LiveTripSocketOptions;
  #socket: WebSocket | null = null;
  #subscribed = false;

  constructor(options: LiveTripSocketOptions) {
    this.#options = options;
  }

  async connect(): Promise<ServerEnvelope> {
    if (this.#socket) throw new Error("Live trip WebSocket is already open.");
    const ticket = await this.#options.api.createWebSocketTicket(
      this.#options.csrfToken,
    );
    const socket = new WebSocket(socketUrl());
    this.#socket = socket;

    return new Promise<ServerEnvelope>((resolve, reject) => {
      let authenticated = false;
      let subscribed = false;
      let settled = false;

      const fail = (error: Error): void => {
        this.#subscribed = false;
        if (!settled) {
          settled = true;
          reject(error);
        }
        socket.close();
      };

      socket.onopen = () => {
        socket.send(
          JSON.stringify(
            clientEnvelope("authenticate", { token: ticket.ticket }),
          ),
        );
      };
      socket.onmessage = (event: MessageEvent<string>) => {
        if (typeof event.data !== "string") {
          fail(new Error("WebSocket server message was not text."));
          return;
        }
        let message: ServerEnvelope;
        try {
          message = parseServerEnvelope(event.data);
        } catch (error: unknown) {
          fail(
            error instanceof Error
              ? error
              : new Error("Invalid WebSocket message."),
          );
          return;
        }
        this.#options.onMessage?.(message);

        if (message.kind === "ping") {
          socket.send(
            JSON.stringify(
              clientEnvelope("pong", {
                nonce: message.payload.nonce,
                received_at_unix_ms: Date.now(),
              }),
            ),
          );
          return;
        }
        if (message.kind === "error") {
          fail(
            new Error("Live trip WebSocket returned " + message.status + "."),
          );
          return;
        }
        if (message.kind === "connection_ready") {
          if (authenticated) {
            fail(new Error("WebSocket sent connection_ready twice."));
            return;
          }
          authenticated = true;
          socket.send(
            JSON.stringify(
              clientEnvelope(
                this.#options.resynchronize
                  ? "resynchronize_trip"
                  : "subscribe_trip",
                this.#options.resynchronize
                  ? {
                      last_runtime_epoch:
                        this.#options.resume?.last_runtime_epoch ?? "0",
                      last_planner_state_version:
                        this.#options.resume?.last_planner_state_version ?? "0",
                      last_trip_revision:
                        this.#options.resume?.last_trip_revision ?? "0",
                      outstanding_message_ids:
                        this.#options.resume?.outstanding_message_ids ?? [],
                    }
                  : { ...(this.#options.resume ?? {}) },
                this.#options.tripId,
              ),
            ),
          );
          return;
        }
        if (
          authenticated &&
          ((message.kind === "subscription_state" &&
            message.payload.subscribed === true) ||
            (message.kind === "resynchronization_state" &&
              isRecord(message.payload.trip) &&
              isRecord(message.payload.current_plan)))
        ) {
          if (subscribed) return;
          subscribed = true;
          this.#subscribed = true;
          settled = true;
          resolve(message);
        }
      };
      socket.onerror = () => fail(new Error("Live trip WebSocket failed."));
      socket.onclose = () => {
        this.#subscribed = false;
        this.#options.onClose?.();
        if (!settled)
          reject(new Error("Live trip WebSocket closed before subscription."));
      };
    });
  }

  sendLocationTelemetry(sample: {
    latitude: number;
    longitude: number;
    observedAtUnixMs: number;
  }): string | null {
    return this.#sendTelemetry("location", sample.observedAtUnixMs, {
      latitude: sample.latitude,
      longitude: sample.longitude,
    });
  }

  sendRouteDeviationTelemetry(sample: {
    latitude: number;
    longitude: number;
    observedAtUnixMs: number;
    distanceFromRouteMeters: number;
    extensions: Record<string, unknown>;
  }): string | null {
    return this.#sendTelemetry(
      "route_deviation",
      sample.observedAtUnixMs,
      {
        location: {
          latitude: sample.latitude,
          longitude: sample.longitude,
        },
        distance_from_route_meters: sample.distanceFromRouteMeters,
      },
      sample.extensions,
    );
  }

  sendProposalDecision(
    decision: "accept_proposal" | "reject_proposal",
    identity: {
      proposal_id: string;
      source_runtime_epoch: string;
      source_planner_state_version: string;
      base_current_plan_id: string;
    },
  ): string | null {
    if (!this.#subscribed || !this.#socket) return null;
    const envelope = clientEnvelope(
      "trip_command",
      { command_kind: decision, command: identity },
      this.#options.tripId,
    );
    const messageId = envelope.message_id;
    if (typeof messageId !== "string") return null;
    this.#socket.send(JSON.stringify(envelope));
    return messageId;
  }

  sendActivityStatus(
    activityId: string,
    state: "started" | "completed" | "skipped",
    expectedTripRevision: string,
  ): string | null {
    if (!this.#subscribed || !this.#socket) return null;
    const envelope = clientEnvelope(
      "trip_command",
      {
        command_kind: "activity_status_changed",
        command: {
          expected_trip_revision: expectedTripRevision,
          activity_id: activityId,
          state,
        },
      },
      this.#options.tripId,
    );
    const messageId = envelope.message_id;
    if (typeof messageId !== "string") return null;
    this.#socket.send(JSON.stringify(envelope));
    return messageId;
  }

  #sendTelemetry(
    observationKind: "location" | "route_deviation",
    observedAtUnixMs: number,
    observation: Record<string, unknown>,
    extensions?: Record<string, unknown>,
  ): string | null {
    if (!this.#subscribed || !this.#socket) return null;
    const envelope = clientEnvelope(
      "telemetry_update",
      {
        observation_kind: observationKind,
        observed_at_unix_ms: observedAtUnixMs,
        observation,
      },
      this.#options.tripId,
      extensions,
    );
    const messageId = envelope.message_id;
    if (typeof messageId !== "string") return null;
    this.#socket.send(JSON.stringify(envelope));
    return messageId;
  }

  close(code?: number, reason?: string): void {
    this.#subscribed = false;
    this.#socket?.close(code, reason);
    this.#socket = null;
  }
}

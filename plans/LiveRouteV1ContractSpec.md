# LiveRoute V1 Contract Specification

## Authority and Scope

This document closes the exact v1 choices that are intentionally summarized in `plans/LiveRouteV1ArchitecturePlan.md`. It is normative for the backend stack, identifiers, statuses, version transitions, Protobuf payloads, WebSocket JSON shapes, PostgreSQL schema/transactions, recovery, time normalization, authentication, readiness, and compatibility gates.

The checked-in `.proto`, JSON Schema, SQL migration, and configuration files created during implementation must match this specification. Once those machine-readable artifacts exist, they become the executable form of this contract. A handler must not merge when its generated descriptors or schemas differ from this document.

V1 remains a local development/demo deployment. Internet production security, horizontal replicas, rolling upgrades, and multi-region behavior are outside this contract.

## Backend Stack

V1 uses this backend stack:

- Go 1.26, pinned to the current security patch in the builder/runtime image lock file.
- Standard `net/http` for `/ws`, `/healthz`, and `/readyz`.
- `github.com/coder/websocket` v1 for WebSocket framing, close handshakes, deadlines, and ping/pong.
- `google.golang.org/grpc` v1 and `google.golang.org/protobuf` v1 for the long-lived bidirectional planner streams.
- `github.com/jackc/pgx/v5` and `pgxpool` for PostgreSQL; use explicit SQL and transaction functions rather than an ORM.
- `github.com/pressly/goose/v3` with sequential, transactional SQL migrations. Embedded migrations may be used by a dedicated migrator command, but the serving backend never performs an implicit schema upgrade on startup.
- `github.com/santhosh-tekuri/jsonschema/v6` with JSON Schema draft 2020-12 for WebSocket validation.
- Buf CLI, pinned in the container toolchain, for Protobuf lint, code generation, descriptor images, and breaking-change checks.
- Go standard `log/slog` for structured logs. Tokens, raw location payloads, and raw provider/database bodies are always redacted.
- Go standard tests plus container-backed PostgreSQL/gRPC/WebSocket integration tests. The C++ project continues to use CMake/CTest initially.

Go is selected because the backend is an I/O and coordination service rather than the planner hot path. It has mature bidirectional gRPC, PostgreSQL, HTTP/WebSocket, cancellation, bounded-goroutine, race-test, and single-binary container support. C++ remains responsible for stateful low-latency replanning.

Every non-standard dependency is pinned by `go.mod`/`go.sum`; build images and service images are pinned by immutable digest in the Compose lock/manifest. Patch upgrades are allowed only after the full contract and recovery suite passes.

## Identifier and Canonicalization Contract

### Identifier formats

- `message_id`: client-generated lowercase RFC 4122 UUID string. For a durable command it is the trip-scoped idempotency key.
- `server_message_id`: backend-generated lowercase RFC 4122 UUID string for each WebSocket server message.
- `request_id`: backend-generated lowercase RFC 4122 UUID string for one gRPC dispatch attempt. A retry uses a new `request_id`.
- `event_id`: stable event identity. For a durable command it equals the client `message_id`; for telemetry/advisory input it is a backend-generated UUID.
- `outbox_id`, `snapshot_id`, `plan_id`, backend instance id, and C++ instance id: lowercase RFC 4122 UUID strings.
- `trip_id`, `activity_id`, and `user_id`: lowercase RFC 4122 UUID strings.
- All UUID fields are exactly 36 ASCII characters and are rejected if not in canonical lowercase form.

`request_id` is correlation only and is never used for deduplication. `event_id` plus the mutation/observation sequence is the C++ deduplication identity. A replay changes `request_id` and runtime epoch as needed but preserves `event_id`, mutation sequence, expected trip revision, and event payload.

### Durable command digest

The backend validates the WebSocket message, extracts the object containing exactly `protocol_version`, `kind`, `trip_id`, `payload`, and top-level `extensions` when present, and canonicalizes it with RFC 8785 JSON Canonicalization Scheme. `message_id` is excluded because it is the key being checked; transport metadata and server-added fields are excluded. All payload fields, including `command_expires_at_unix_ms`, are included.

The payload digest is SHA-256 over the UTF-8 RFC 8785 bytes. PostgreSQL stores:

- `digest_algorithm = 'rfc8785-sha256-v1'`
- the 32-byte digest
- the validated original command payload as `jsonb`

Reusing a `(trip_id, message_id)` with an identical algorithm and digest returns the stored pending or terminal outcome. Any digest difference returns terminal `IDEMPOTENCY_KEY_REUSED`. A future canonicalization change requires a new algorithm name and protocol package; old rows continue to use their recorded algorithm.

## Version and Sequence Contract

- `runtime_epoch` is PostgreSQL-issued and starts at 1 on the first lease acquisition. Every new lease holder/process acquisition increments it by exactly one while holding the trip row lock.
- `trip_revision` is durable and starts at 0 when the trip is created. It increments by one only when an accepted durable mutation is finalized in PostgreSQL.
- `mutation_sequence` is durable, starts at 1, and is never reused. Accepted and terminally rejected commands both consume a sequence. Only an accepted mutation advances `trip_revision`.
- `planner_state_version` is scoped to `runtime_epoch`, starts at 0 after a higher-epoch bootstrap, and increments once for each accepted state-changing durable event or telemetry observation in that epoch.
- `planning_generation` is internal, starts at 0 per runtime epoch, and increments whenever accepted input invalidates planning work.
- `observation_sequence` is owned by the backend in memory, scoped to `(trip_id, runtime_epoch)`, starts at 1, and may contain gaps. It is never stored in PostgreSQL or a durable snapshot.
- Freshness is compared lexicographically as `(runtime_epoch, planner_state_version)`, never by planner state version alone.
- A proposed plan is identified by `(runtime_epoch, plan_id, source_planner_state_version)`. Acceptance/rejection must match all three values.

When a higher runtime epoch is bootstrapped, C++ atomically:

1. fences the old epoch;
2. cancels and discards old-epoch provider/planner work and pending telemetry;
3. clears the current observation, observation watermark, ephemeral proposal, and observation-derived latest plan;
4. resets planner state version and planning generation to 0;
5. retains/rebuilds only durable trip state from the bootstrap base and finalized mutation replay.

On a same-epoch stream reconnect, observation sequencing does not reset. The backend supplies its latest in-memory observation and watermark during idempotent bootstrap. After a backend process restart a higher epoch is acquired, so old telemetry is deliberately discarded and planning waits for a new observation.

## Status and Result-Quality Contract

### Stable status codes

The same names and numeric values are used by Protobuf and WebSocket JSON:

| Number | Status | Default retryable | Meaning |
| ---: | --- | --- | --- |
| 0 | `UNSPECIFIED` | false | Invalid on a sent response. |
| 1 | `OK` | false | Operation succeeded or was admitted. |
| 2 | `DUPLICATE` | false | Previously resolved identical event/command; stored outcome is returned. |
| 3 | `STALE` | false | Epoch, sequence, revision, planner version, or proposal is older than current state. Resynchronize; do not retry unchanged. |
| 4 | `INVALID_ARGUMENT` | false | Schema-valid envelope contains invalid domain values. |
| 5 | `UNAUTHENTICATED` | false | Authentication missing or invalid. |
| 6 | `PERMISSION_DENIED` | false | User lacks trip access or origin policy fails. |
| 7 | `NOT_FOUND` | false | Requested user/trip/activity does not exist. |
| 8 | `IDEMPOTENCY_KEY_REUSED` | false | Same durable `message_id`, different canonical digest. |
| 9 | `INACTIVE_TRIP` | true internally | C++ requires bootstrap before retry; backend normally absorbs this status. |
| 10 | `RESOURCE_EXHAUSTED` | true | A bounded queue, buffer, message, trip, matrix, or memory limit was reached. |
| 11 | `DEADLINE_EXCEEDED` | context-specific | One transport/planner attempt exceeded its deadline. Durable work is retried; obsolete telemetry is not. |
| 12 | `COMMAND_EXPIRED` | false | Optional durable-command logical expiry passed before acceptance. |
| 13 | `CANCELLED` | context-specific | Work was cooperatively cancelled. Superseded telemetry/advisory work is terminal; durable work remains pending. |
| 14 | `INFEASIBLE` | false | No plan satisfies protected constraints. |
| 15 | `PROVIDER_UNAVAILABLE` | true | Required route/hours data was unavailable and no permitted data source could answer. |
| 16 | `DURABILITY_UNAVAILABLE` | true | PostgreSQL cannot safely record/finalize durability-dependent work. |
| 17 | `UNAVAILABLE` | true | Transient stream/service failure not covered by a more specific code. |
| 18 | `UNSUPPORTED_VERSION` | false | Protocol, capability, or snapshot schema is unsupported. |
| 19 | `SNAPSHOT_NOT_READY` | true | Accepted and PostgreSQL-finalized mutation watermarks do not yet match. |
| 20 | `SNAPSHOT_INCOMPATIBLE` | false | Snapshot schema/checksum/metadata cannot be used. |
| 21 | `INTERNAL` | false | Invariant violation or non-classified defect; operators investigate instead of automatically retrying forever. |

All old/late ownership and version cases use `STALE`. A structured stale reason preserves diagnostics:

`EPOCH`, `MUTATION_SEQUENCE`, `OBSERVATION_SEQUENCE`, `TRIP_REVISION`, `PLANNER_STATE_VERSION`, or `PLAN_PROPOSAL`.

`degraded` is not a status. It previously mixed successful-but-lower-quality output with actual failures. Successful results use `status = OK` plus:

- `plan_quality`: `COMPLETE`, `BEST_SO_FAR`, or `NO_NEW_PLAN`;
- `routing_quality`: `FRESH`, `STALE_CACHE`, or `UNAVAILABLE`;
- `recovery_state`: `CURRENT` or `NOT_ADVANCING`.

Examples:

- A deadline returns a valid partial plan: `OK` + `BEST_SO_FAR`.
- OSRM fails and no plan can be computed: `PROVIDER_UNAVAILABLE`.
- A future explicitly allowed stale cache produces a plan: `OK` + `STALE_CACHE`.
- PostgreSQL is down but still-valid-lease telemetry is accepted: `OK` + `NOT_ADVANCING`.

Telemetry has a separate disposition: `ACCEPTED`, `COALESCED`, `DROPPED`, or `REJECTED`. A dropped/coalesced obsolete sample is not a planner error.

For a new durable mutation, C++ consumes/advances the mutation sequence only on `ACCEPTED`, or on terminal `REJECTED` with `STALE`, `INVALID_ARGUMENT`, `NOT_FOUND`, `COMMAND_EXPIRED`, or `INFEASIBLE`. `INACTIVE_TRIP`, `RESOURCE_EXHAUSTED`, `DEADLINE_EXCEEDED`, `CANCELLED`, `PROVIDER_UNAVAILABLE`, and `UNAVAILABLE` leave the sequence pending for retry. `INTERNAL` also leaves it pending, pauses automatic dispatch for that trip, and requires an explicit operator repair/retry decision; later durable commands remain blocked rather than silently bypassing uncertain state. `DUPLICATE` returns the already-recorded resolution and does not apply anything again.

## Complete Protobuf Payload Schema

The implementation writes this as `proto/liveroute/v1/planner.proto` using proto3. All enums reserve zero as `UNSPECIFIED`; deleted field names and numbers are reserved forever. Times are signed Unix epoch milliseconds. Durations are signed seconds unless the field name states milliseconds. Coordinates are finite `double` latitude/longitude validated before conversion.

### Stream envelopes

`PlannerStreamRequest` fields:

| No. | Type | Name |
| ---: | --- | --- |
| 1 | `string` | `request_id` |
| 2 | `string` | `trip_id` |
| 3 | `uint64` | `runtime_epoch` |
| 4 | `uint64` | `mutation_sequence` |
| 5 | `uint64` | `observation_sequence` |
| 6 | `optional uint64` | `expected_planner_state_version` |
| 7 | `int64` | `expires_at_unix_ms` |
| 8 | `optional uint64` | `expected_trip_revision` |
| 20 | `OpenStream` | `open_stream` |
| 21 | `BootstrapTrip` | `bootstrap_trip` |
| 22 | `ApplyTripEvent` | `apply_event` |
| 23 | `RequestSnapshot` | `request_snapshot` |
| 24 | `DeactivateTrip` | `deactivate_trip` |
| 25 | `Ping` | `ping` |
| 26 | `ConfirmFinalizedMutations` | `confirm_finalized_mutations` |

Fields 20-26 form one `payload` oneof.

`PlannerStreamResponse` fields:

| No. | Type | Name |
| ---: | --- | --- |
| 1 | `string` | `request_id` |
| 2 | `string` | `trip_id` |
| 3 | `uint64` | `runtime_epoch` |
| 4 | `uint64` | `accepted_mutation_sequence` |
| 5 | `uint64` | `accepted_observation_sequence` |
| 6 | `uint64` | `planner_state_version` |
| 7 | `uint64` | `trip_revision` |
| 20 | `StreamReady` | `stream_ready` |
| 21 | `EventAcknowledged` | `event_acknowledged` |
| 22 | `ReplanResult` | `replan_result` |
| 23 | `TripSnapshot` | `trip_snapshot` |
| 24 | `TripDeactivated` | `trip_deactivated` |
| 25 | `PlannerError` | `error` |
| 26 | `Pong` | `pong` |
| 27 | `FinalizedMutationsAcknowledged` | `finalized_mutations_acknowledged` |

Fields 20-27 form one `payload` oneof.

### Common enums

- `StatusCode`: the numeric table above.
- `StaleReason`: 0 `UNSPECIFIED`, 1 `EPOCH`, 2 `MUTATION_SEQUENCE`, 3 `OBSERVATION_SEQUENCE`, 4 `TRIP_REVISION`, 5 `PLANNER_STATE_VERSION`, 6 `PLAN_PROPOSAL`.
- `EventDisposition`: 0 `UNSPECIFIED`, 1 `ACCEPTED`, 2 `DUPLICATE`, 3 `STALE`, 4 `REJECTED`.
- `TravelMode`: 0 `UNSPECIFIED`, 1 `WALKING`, 2 `DRIVING`.
- `ActivityClass`: 0 `UNSPECIFIED`, 1 `FIXED`, 2 `FLEXIBLE`.
- `ActivityState`: 0 `UNSPECIFIED`, 1 `PLANNED`, 2 `STARTED`, 3 `COMPLETED`, 4 `SKIPPED`.
- `PlanDecision`: 0 `UNSPECIFIED`, 1 `ACCEPT`, 2 `REJECT`.
- `SegmentDisposition`: 0 `UNSPECIFIED`, 1 `PRESERVED`, 2 `MOVED`, 3 `SHORTENED`, 4 `SKIPPED`, 5 `ADDED`.
- `NotificationType`: 0 `UNSPECIFIED`, 1 `NONE`, 2 `LOW_SLACK_WARNING`, 3 `CRITICAL_LATENESS`, 4 `PLAN_CHANGE_SUGGESTED`, 5 `INFEASIBLE_SCHEDULE`.
- `PlanReasonCode`: 0 `UNSPECIFIED`, 1 `LATE_DEPARTURE`, 2 `ACTIVITY_DELAY`, 3 `ROUTE_DEVIATION`, 4 `HOURS_CHANGED`, 5 `PLACE_CLOSED`, 6 `RESERVATION_AT_RISK`, 7 `TRAVEL_DELAY`, 8 `USER_EDIT`, 9 `DEADLINE_BUDGET`, 10 `NO_FEASIBLE_PLAN`.
- `PlanQuality`: 0 `UNSPECIFIED`, 1 `COMPLETE`, 2 `BEST_SO_FAR`, 3 `NO_NEW_PLAN`.
- `RoutingQuality`: 0 `UNSPECIFIED`, 1 `FRESH`, 2 `STALE_CACHE`, 3 `UNAVAILABLE`.
- `RecoveryState`: 0 `UNSPECIFIED`, 1 `CURRENT`, 2 `NOT_ADVANCING`.
- `SnapshotReason`: 0 `UNSPECIFIED`, 1 `PERIODIC`, 2 `DURABLE_BOUNDARY`, 3 `DEACTIVATION`, 4 `SHUTDOWN`.
- `DeactivationReason`: 0 `UNSPECIFIED`, 1 `BACKEND_REQUEST`, 2 `IDLE_EVICTION`, 3 `TRIP_DELETED`, 4 `SHUTDOWN`.
- `AdvisoryKind`: 0 `UNSPECIFIED`, 1 `RECOMMENDATION_REFRESH`, 2 `WEATHER_CHANGED`, 3 `CROWD_CHANGED`, 4 `SOCIAL_UPDATE`.

### Domain messages

`Location`: 1 `double latitude`, 2 `double longitude`.

`TimeWindow`: 1 `int64 opens_at_unix_ms`, 2 `int64 closes_at_unix_ms`.

`ActivityTiming`: 1 `repeated TimeWindow open_windows`, 2 `optional int64 reservation_start_unix_ms`, 3 `uint32 reservation_grace_seconds`, 4 `uint32 min_duration_seconds`, 5 `uint32 preferred_duration_seconds`, 6 `uint32 max_duration_seconds`, 7 `bool mandatory`, 8 `bool can_shorten`, 9 `bool can_move`, 10 `bool can_skip`, 11 `optional int64 mandatory_deadline_unix_ms`.

`Activity`: 1 `string activity_id`, 2 `string place_id`, 3 `string display_name`, 4 `Location location`, 5 `string time_zone_name`, 6 `TravelMode inbound_travel_mode`, 7 `ActivityClass activity_class`, 8 `ActivityState activity_state`, 9 `int32 priority_rank`, 10 `int32 utility_score`, 11 `ActivityTiming timing`, 12 `uint32 activity_delay_seconds`, 13 `optional int64 found_closed_at_unix_ms`.

`TravelDelayState`: 1 `string from_activity_id`, 2 `string to_activity_id`, 3 `uint32 additional_seconds`, 4 `int64 observed_at_unix_ms`.

`TripDefinition`: 1 `string trip_id`, 2 `string owner_user_id`, 3 `string default_time_zone_name`, 4 `repeated Activity activities`, 5 `uint32 completed_prefix_count`, 6 `string current_activity_id`, 7 `string accepted_plan_id`, 8 `repeated TravelDelayState travel_delays`.

`CurrentObservation`: 1 `Location location`, 2 `int64 observed_at_unix_ms`, 3 `optional double velocity_meters_per_second`, 4 `optional double heading_degrees`.

`RouteLeg`: 1 `uint32 duration_seconds`, 2 `uint32 distance_meters`, 3 `bool reachable`.

`ItinerarySegment`: 1 `string activity_id`, 2 `Location location`, 3 `string time_zone_name`, 4 `int64 scheduled_start_unix_ms`, 5 `int64 scheduled_end_unix_ms`, 6 `RouteLeg inbound_route`, 7 `SegmentDisposition disposition`, 8 `repeated PlanReasonCode reasons`.

`ItineraryPlan`: 1 `string plan_id`, 2 `uint64 source_runtime_epoch`, 3 `uint64 source_planner_state_version`, 4 `repeated ItinerarySegment preserved_prefix`, 5 `repeated ItinerarySegment revised_suffix`, 6 `int64 created_at_unix_ms`, 7 `uint64 source_accepted_mutation_sequence`.

`PlannerStats`: 1 `uint64 candidates_evaluated`, 2 `uint64 candidates_pruned`, 3 `uint32 search_depth`, 4 `uint32 queue_wait_microseconds`, 5 `uint32 provider_microseconds`, 6 `uint32 planner_microseconds`, 7 `uint32 serialization_microseconds`, 8 `bool deadline_hit`.

`ResultQuality`: 1 `PlanQuality plan_quality`, 2 `RoutingQuality routing_quality`, 3 `RecoveryState recovery_state`.

### Stream-control payloads

`OpenStream`: 1 `string backend_instance_id`, 2 `string protocol_version`, 3 `repeated string capabilities`.

`StreamReady`: 1 `string cpp_instance_id`, 2 `string protocol_version`, 3 `repeated string capabilities`, 4 `uint32 max_message_bytes`, 5 `uint32 max_snapshot_bytes`, 6 `uint32 max_active_trips`, 7 `StatusCode status`.

Both sides require protocol string `liveroute.v1` and these exact capability strings: `finalized_mutation_watermark`, `result_quality_metadata`, `epoch_scoped_observations`, and `snapshot_schema_1`. Capabilities are sorted lexicographically and unique. Unknown capabilities are ignored for negotiation, but any missing required V1 capability returns `UNSUPPORTED_VERSION` and closes the stream without trip admission.

`SnapshotBlob`: 1 `uint32 snapshot_schema_version`, 2 `uint64 source_runtime_epoch`, 3 `uint64 source_planner_state_version`, 4 `uint64 trip_revision`, 5 `uint64 covered_finalized_mutation_sequence`, 6 `uint32 payload_size_bytes`, 7 `bytes checksum_sha256`, 8 `bytes payload`.

`BootstrapTrip`: 1 `oneof base { SnapshotBlob snapshot = 1; TripDefinition full_trip = 2; }`, 3 `uint64 finalized_mutation_sequence`, 4 `uint64 trip_revision`, 5 `optional CurrentObservation current_observation`, 6 `uint64 current_observation_sequence`, 7 `optional ItineraryPlan accepted_plan`.

For a higher epoch, fields 5-6 are absent/zero. For same-epoch stream rebinding they carry the backend's latest observation and watermark. A snapshot may cover only finalized durable state; telemetry is never serialized in it.

`ConfirmFinalizedMutations`: 1 `uint64 finalized_mutation_sequence`.

`FinalizedMutationsAcknowledged`: 1 `StatusCode status`, 2 `bool retryable`, 3 `uint64 finalized_mutation_sequence`.

`RequestSnapshot`: 1 `SnapshotReason reason`, 2 `uint64 minimum_finalized_mutation_sequence`, 3 `uint64 minimum_planner_state_version`.

`TripSnapshot`: 1 `StatusCode status`, 2 `bool retryable`, 3 `SnapshotBlob snapshot`.

`DeactivateTrip`: 1 `DeactivationReason reason`, 2 `bool final_snapshot_required`.

`TripDeactivated`: 1 `StatusCode status`, 2 `bool retryable`, 3 `bool final_snapshot_produced`.

`Ping`: 1 `string nonce`, 2 `int64 sent_at_unix_ms`.

`Pong`: 1 `string nonce`, 2 `int64 received_at_unix_ms`.

### Event payloads

`ApplyTripEvent`: 1 `string event_id`, 2 `int64 occurred_at_unix_ms`, 3 `optional int64 command_expires_at_unix_ms`, then one `event` oneof:

| No. | Type | Name | Class |
| ---: | --- | --- | --- |
| 20 | `LocationUpdated` | `location_updated` | telemetry |
| 21 | `VelocityUpdated` | `velocity_updated` | telemetry |
| 22 | `HeadingUpdated` | `heading_updated` | telemetry |
| 23 | `ActivityStatusChanged` | `activity_status_changed` | durable |
| 24 | `ActivityDelayed` | `activity_delayed` | durable |
| 25 | `TripEdited` | `trip_edited` | durable |
| 26 | `ReservationChanged` | `reservation_changed` | durable |
| 27 | `MandatoryDeadlineChanged` | `mandatory_deadline_changed` | durable |
| 28 | `RouteDeviationDetected` | `route_deviation_detected` | high observation |
| 29 | `OperatingHoursChanged` | `operating_hours_changed` | durable |
| 30 | `PlaceFoundClosed` | `place_found_closed` | durable |
| 31 | `TravelDelay` | `travel_delay` | durable |
| 32 | `PlanDecisionEvent` | `plan_decision` | durable/CAS |
| 33 | `AdvisoryUpdate` | `advisory_update` | advisory |

Payload fields:

- `LocationUpdated`: 1 `Location location`.
- `VelocityUpdated`: 1 `double meters_per_second`.
- `HeadingUpdated`: 1 `double degrees`.
- `ActivityStatusChanged`: 1 `string activity_id`, 2 `ActivityState state`.
- `ActivityDelayed`: 1 `string activity_id`, 2 `uint32 delay_seconds`; this replaces the activity's current total delay rather than incrementing it.
- `TripEdited`: one `operation` of 1 `AddActivity add`, 2 `ReplaceActivity replace`, 3 `RemoveActivity remove`, 4 `ReorderActivities reorder`.
- `AddActivity`: 1 `Activity activity`, 2 `uint32 ordinal`.
- `ReplaceActivity`: 1 `Activity activity`.
- `RemoveActivity`: 1 `string activity_id`.
- `ReorderActivities`: 1 `repeated string activity_ids` containing every remaining activity exactly once.
- `ReservationChanged`: 1 `string activity_id`, 2 `optional int64 reservation_start_unix_ms`, 3 `uint32 reservation_grace_seconds`.
- `MandatoryDeadlineChanged`: 1 `string activity_id`, 2 `int64 latest_finish_unix_ms`.
- `RouteDeviationDetected`: 1 `Location location`, 2 `uint32 distance_from_route_meters`.
- `OperatingHoursChanged`: 1 `string activity_id`, 2 `repeated TimeWindow open_windows`.
- `PlaceFoundClosed`: 1 `string activity_id`, 2 `int64 observed_at_unix_ms`.
- `TravelDelay`: 1 `string from_activity_id`, 2 `string to_activity_id`, 3 `uint32 additional_seconds`; this replaces the current extra delay for that directed leg.
- `PlanDecisionEvent`: 1 `PlanDecision decision`, 2 `string plan_id`, 3 `uint64 source_runtime_epoch`, 4 `uint64 source_planner_state_version`.
- `AdvisoryUpdate`: 1 `AdvisoryKind kind`, 2 `string source`, 3 `bytes opaque_payload`, with a configured byte limit. Candidate search never reads opaque provider data; an adapter must normalize any advisory effect first.

`EventAcknowledged`: 1 `EventDisposition disposition`, 2 `StatusCode status`, 3 `bool retryable`, 4 `StaleReason stale_reason`, 5 `string event_id`, 6 `uint64 resolved_mutation_sequence`, 7 `uint64 resolved_observation_sequence`, 8 `bool replan_scheduled`, 9 `string safe_message`.

`ReplanResult`: 1 `StatusCode status`, 2 `bool retryable`, 3 `ItineraryPlan plan`, 4 `NotificationType notification`, 5 `repeated PlanReasonCode reasons`, 6 `PlannerStats stats`, 7 `ResultQuality quality`, 8 `string safe_message`.

`PlannerError`: 1 `StatusCode status`, 2 `bool retryable`, 3 `StaleReason stale_reason`, 4 `string safe_message`, 5 `uint64 related_mutation_sequence`, 6 `uint64 related_observation_sequence`, 7 `uint64 related_planner_state_version`.

### Snapshot payload

The `SnapshotBlob.payload` is the serialized bytes of `TripStateSnapshot` schema version 1:

`TripStateSnapshot`: 1 `TripDefinition trip`, 2 `uint64 trip_revision`, 3 `uint64 accepted_mutation_sequence`, 4 `uint64 finalized_mutation_sequence`, 5 `optional ItineraryPlan accepted_plan`, 6 `uint32 snapshot_schema_version`.

It contains durable state only. It does not contain current location, velocity, heading, observation sequence, runtime epoch authority, pending work, stream bindings, cancellation objects, provider responses, or ephemeral proposals.

## Finalized Mutation Watermark and Snapshot Safety

A mutation is **accepted** when C++ applies it in memory. It is **finalized** only when PostgreSQL commits its terminal result:

- for an accepted command, the canonical trip/saved-plan mutation, new trip revision, command outcome, and outbox state commit together;
- for a terminal rejection, the rejected command outcome and consumed mutation sequence commit together without a trip-revision change.

The **covered finalized mutation sequence** is the highest contiguous mutation sequence whose terminal PostgreSQL outcome is committed and whose accepted effects, if any, are included in the snapshot. Rejected sequences are covered because their durable terminal outcome exists and they have no state effect to serialize.

Protocol:

1. C++ acknowledges accepted/rejected mutation `N` and advances its accepted mutation watermark to `N`.
2. The backend commits the corresponding PostgreSQL finalization transaction.
3. The backend sends idempotent `ConfirmFinalizedMutations(N)`. It is a cumulative watermark, not one message per mutation requirement; repeats are safe.
4. C++ advances its finalized watermark only when `current <= N <= accepted_mutation_sequence`. A lower value is a duplicate; a larger value is `INVALID_ARGUMENT` and does not mutate state.
5. C++ returns `SNAPSHOT_NOT_READY` while accepted and finalized watermarks differ. It never serializes an unfinalized mutation.
6. Bootstrap carries PostgreSQL's finalized watermark, so a lost confirmation or either process restarting converges without depending on the old stream.

C++ may compute speculatively after accepting a durable mutation, but a plan carries `source_accepted_mutation_sequence`. The backend keeps at most one latest unpublished plan per trip and does not expose it over WebSocket until PostgreSQL's finalized watermark is at least that source sequence. If finalization fails, clients never observe a plan based on noncanonical durable state. Telemetry may continue in C++, but its resulting plans obey the same publication fence. After finalization, the backend publishes only if the epoch/state version is still current; otherwise normal stale-result rules discard it.

The report is necessary because C++ cannot otherwise know whether the database transaction after its acknowledgement committed. Without it, a snapshot could get ahead of canonical PostgreSQL state; pruning outbox rows from such a snapshot could remove the replay evidence needed to recover.

## WebSocket JSON Contract

### Encoding and compatibility

- UTF-8 JSON text frames only; binary frames close with protocol error.
- `protocol_version` is the exact string `liveroute.v1`.
- JSON Schema draft 2020-12 is used.
- Every object has `additionalProperties: false` except the explicit `extensions` object.
- `extensions` is optional, has namespaced string keys, and arbitrary JSON values. Receivers ignore unknown extension keys but preserve them for durable-command canonicalization.
- Standard fields cannot be added, removed, renamed, or change type within `liveroute.v1`. Such changes require `liveroute.v2`. This strict rule avoids pretending a new optional field is compatible with old validators.
- Every Protobuf `uint64` version, sequence, epoch, and counter is encoded in WebSocket JSON as a canonical unsigned decimal string matching `0|[1-9][0-9]{0,19}` so the future browser client never loses precision. Unix-millisecond timestamps, durations, sizes, ranks, and bounded counts use JSON integers and must remain in JavaScript's exact safe-integer range. UUIDs and enums use the exact strings defined here.
- Coordinates are finite numbers; latitude is `[-90, 90]`, longitude is `[-180, 180]`.
- JSON domain objects use the Protobuf field names in `snake_case`. Domain enum values use lowercase snake case (`walking`, `activity_delay`, `best_so_far`, and so on); only stable `StatusCode` values are uppercase as listed. Optional fields are omitted rather than sent as `null`. Protobuf `bytes` values, where exposed, use unpadded base64url.

### Client envelope

Every client message contains:

- `protocol_version`: constant `liveroute.v1`;
- `message_id`: canonical UUID;
- `kind`: one exact value below;
- `payload`: kind-specific object;
- optional `extensions`.

Trip-scoped kinds additionally contain `trip_id`. Connection-scoped `authenticate`, `ping`, and `pong` must omit it.

Client kinds and payloads:

| Kind | Scope | Exact payload |
| --- | --- | --- |
| `authenticate` | connection | `token` string matching `[A-Za-z0-9_-]{43}` |
| `subscribe_trip` | trip | optional `last_runtime_epoch`, `last_planner_state_version`, `last_trip_revision` canonical unsigned decimal strings |
| `unsubscribe_trip` | trip | empty object |
| `trip_command` | trip | `command_kind`, optional `command_expires_at_unix_ms`, and `command`; command kinds are `activity_status_changed`, `activity_delayed`, `trip_edited`, `reservation_changed`, `mandatory_deadline_changed`, `operating_hours_changed`, `place_found_closed`, `travel_delay`, `accept_plan`, `reject_plan` and use the JSON-equivalent Protobuf fields above |
| `telemetry_update` | trip | `observation_kind` (`location`, `velocity`, `heading`, `route_deviation`), `observed_at_unix_ms`, and matching `observation` object |
| `resynchronize_trip` | trip | `last_runtime_epoch`, `last_planner_state_version`, `last_trip_revision` canonical unsigned decimal strings, and `outstanding_message_ids` unique UUID array bounded by configuration |
| `ping` | connection | `nonce` string 1-64 bytes and `sent_at_unix_ms` |
| `pong` | connection | `nonce` string 1-64 bytes and `received_at_unix_ms` |

`command_expires_at_unix_ms` is a logical product expiry. Omission means the durable command does not expire merely because transport retries take time.

### Server envelope

Every server message contains:

- `protocol_version`: constant `liveroute.v1`;
- `server_message_id`: canonical UUID;
- `kind`;
- `status`: exact `StatusCode` string without the Protobuf prefix;
- `retryable`: boolean;
- `payload`;
- optional `in_reply_to_message_id`;
- optional `extensions`.

Trip-scoped messages additionally contain `trip_id` and canonical unsigned decimal strings `trip_revision`, `runtime_epoch`, `planner_state_version`, `accepted_mutation_sequence`, and `accepted_observation_sequence`.

Server kinds:

| Kind | Exact payload |
| --- | --- |
| `connection_ready` | `user_id`, `backend_instance_id`, `heartbeat_interval_ms`, `idle_timeout_ms`, `max_frame_bytes`, `max_outstanding_resync_ids` |
| `subscription_state` | `subscribed` boolean plus current durable trip and current active plan when available |
| `command_acknowledgement` | `phase` (`durable_recorded`, `planner_applied`, `rejected`, `expired`), `message_id`, optional `mutation_sequence`, optional `outcome` |
| `telemetry_status` | `message_id`, `disposition` (`accepted`, `coalesced`, `dropped`, `rejected`), optional `observation_sequence` |
| `planner_notification` | `notification`, `reasons`, and result-quality fields |
| `revised_plan` | JSON-equivalent `ItineraryPlan`, reasons, stats, and result-quality fields |
| `resynchronization_state` | current durable trip, current active/durable plan, and one outcome for every requested outstanding `message_id` |
| `error` | optional `stale_reason`, `safe_message`, and structured field violations |
| `ping` | nonce and timestamp |
| `pong` | nonce and timestamp |

### Connection lifecycle and close codes

- The client must authenticate within the configured authentication timeout. Ping/pong is allowed before authentication; no trip message is.
- A successful authentication emits `connection_ready`. A second authenticate message is a protocol error.
- The backend sends ping at the advertised heartbeat interval and closes when no valid application message or pong is received within the idle timeout.
- Exactly one reader goroutine and one writer goroutine own a connection. Other work sends through bounded channels.

Close codes:

| Code | Retry | Meaning |
| ---: | --- | --- |
| 1000 | no | Normal client/server close. |
| 1001 | yes | Graceful server shutdown/restart. |
| 1002 | no | Invalid frame type or protocol state. |
| 1008 | no | Origin/policy violation. |
| 1009 | no for same frame | Frame or decoded message too large. |
| 4001 | no without new credentials | Authentication missing/failed. |
| 4003 | no for same user/trip | Authorization denied. |
| 4008 | yes | Authentication or heartbeat/idle timeout. |
| 4029 | yes | Bounded connection/service capacity exhausted. |
| 4503 | yes | Planner/PostgreSQL/backend temporarily unavailable. |

Before a capacity/transient close, the backend attempts a bounded `error` message. Failure to enqueue it does not delay closure; the client recovers through resynchronization.

## PostgreSQL Schema Contract

PostgreSQL 18 is the v1 development major version, pinned to the current supported minor image digest. The initial migration creates the following exact logical columns. The implementation may choose physical index names, but not change column meaning or constraints without updating this contract.

### Tables

`users`:

- `id uuid primary key`
- `display_name text not null`
- `default_time_zone_name text not null`
- `created_at timestamptz not null default clock_timestamp()`

`development_auth_tokens`:

- `id uuid primary key`
- `user_id uuid not null references users(id) on delete cascade`
- `token_sha256 bytea not null unique`, exactly 32 bytes
- `expires_at timestamptz null`
- `revoked_at timestamptz null`
- `created_at timestamptz not null default clock_timestamp()`

`trips`:

- `id uuid primary key`
- `owner_user_id uuid not null references users(id) on delete cascade`
- `default_time_zone_name text not null`
- `trip_revision bigint not null default 0 check (trip_revision >= 0)`
- `next_mutation_sequence bigint not null default 1 check (next_mutation_sequence >= 1)`
- `finalized_mutation_sequence bigint not null default 0 check (finalized_mutation_sequence >= 0)`
- `accepted_plan_id uuid null`
- `created_at`, `updated_at timestamptz not null`

After `saved_plans` exists, add a deferrable composite foreign key `(id, accepted_plan_id)` to `saved_plans(trip_id, id)`; a null accepted plan is allowed.

`trip_activities`:

- `id uuid primary key`
- `trip_id uuid not null references trips(id) on delete cascade`
- `ordinal integer not null check (ordinal >= 0)`
- `place_id text not null`
- `display_name text not null`
- `latitude double precision not null`
- `longitude double precision not null`
- `time_zone_name text not null`
- `inbound_travel_mode text not null check in ('walking','driving')`
- `activity_class text not null check in ('fixed','flexible')`
- `activity_state text not null check in ('planned','started','completed','skipped')`
- `activity_delay_seconds integer not null default 0 check >= 0`
- `found_closed_at timestamptz null`
- `priority_rank integer not null`
- `utility_score integer not null`
- `reservation_start timestamptz null`
- `reservation_grace_seconds integer not null default 0 check >= 0`
- `mandatory_deadline timestamptz null`
- `min_duration_seconds`, `preferred_duration_seconds`, `max_duration_seconds integer not null check >= 0`, with `min <= preferred <= max`
- `mandatory`, `can_shorten`, `can_move`, `can_skip boolean not null`
- unique `(trip_id, ordinal)` and unique `(trip_id, id)`

`activity_open_windows`:

- `trip_id uuid not null`
- `activity_id uuid not null`
- `window_index integer not null check >= 0`
- `opens_at`, `closes_at timestamptz not null`, with `opens_at < closes_at`
- composite primary key `(activity_id, window_index)`
- composite foreign key `(trip_id, activity_id)` to `trip_activities(trip_id, id)` on delete cascade

`trip_travel_delays`:

- `trip_id uuid not null references trips(id) on delete cascade`
- `from_activity_id uuid not null`
- `to_activity_id uuid not null`
- `additional_seconds integer not null check >= 0`
- `observed_at timestamptz not null`
- primary key `(trip_id, from_activity_id, to_activity_id)`
- composite foreign keys from `(trip_id, from_activity_id)` and `(trip_id, to_activity_id)` to `trip_activities(trip_id, id)` on delete cascade

`saved_plans`:

- `id uuid primary key`
- `trip_id uuid not null references trips(id) on delete cascade`
- `source_runtime_epoch bigint not null check > 0`
- `source_planner_state_version bigint not null check >= 0`
- `trip_revision bigint not null check >= 0`
- `schema_version integer not null check = 1`
- `payload bytea not null`
- `payload_size_bytes integer not null check >= 0`
- `checksum_sha256 bytea not null`, exactly 32 bytes
- `created_at timestamptz not null`
- unique `(trip_id, id)`

`command_intents`:

- `id uuid primary key`
- `trip_id uuid not null references trips(id) on delete cascade`
- `message_id uuid not null`
- `event_id uuid not null`
- `mutation_sequence bigint not null check > 0`
- `expected_trip_revision bigint not null check >= 0`
- `command_kind text not null` checked against `activity_status_changed`, `activity_delayed`, `trip_edited`, `reservation_changed`, `mandatory_deadline_changed`, `operating_hours_changed`, `place_found_closed`, `travel_delay`, `accept_plan`, `reject_plan`
- `command_expires_at timestamptz null`
- `digest_algorithm text not null check = 'rfc8785-sha256-v1'`
- `payload_digest bytea not null`, exactly 32 bytes
- `command_payload jsonb not null`
- `state text not null check in ('pending','applied','rejected','expired')`
- `outcome_status text null`, checked against the stable status strings when non-null
- `outcome_payload jsonb null`
- `resulting_trip_revision bigint null`
- `resulting_planner_state_version bigint null`
- `recorded_at timestamptz not null`
- `finalized_at timestamptz null`
- unique `(trip_id, message_id)`, `(trip_id, event_id)`, and `(trip_id, mutation_sequence)`

`planner_outbox`:

- `id uuid primary key`
- `command_intent_id uuid not null unique references command_intents(id) on delete cascade`
- `trip_id uuid not null references trips(id) on delete cascade`
- `mutation_sequence bigint not null`
- `event_schema_version integer not null check = 1`
- `event_payload jsonb not null`
- `delivery_state text not null check in ('pending','paused_internal','accepted','terminal_rejected')`
- `attempt_count bigint not null default 0 check >= 0`
- `next_attempt_at timestamptz not null default clock_timestamp()`
- `last_attempt_at timestamptz null`
- `last_status text null`
- `claim_owner uuid null`
- `claim_expires_at timestamptz null`
- `finalization_confirmed_at timestamptz null`
- `created_at`, `updated_at timestamptz not null`
- unique `(trip_id, mutation_sequence)`

The outbox does not persist a dispatch-authority runtime epoch or a reusable `request_id`. An optional audit epoch may be logged outside the payload but is never read to authorize replay.

`planner_snapshots`:

- `id uuid primary key`
- `trip_id uuid not null references trips(id) on delete cascade`
- `snapshot_schema_version integer not null check = 1`
- `source_runtime_epoch bigint not null check > 0`
- `source_planner_state_version bigint not null check >= 0`
- `trip_revision bigint not null check >= 0`
- `covered_finalized_mutation_sequence bigint not null check >= 0`
- `payload_size_bytes integer not null check >= 0`
- `checksum_sha256 bytea not null`, exactly 32 bytes
- `payload bytea not null`
- `created_at timestamptz not null`
- `invalidated_at timestamptz null`
- `invalidation_reason text null`
- unique `(trip_id, source_runtime_epoch, source_planner_state_version, covered_finalized_mutation_sequence)`

`trip_runtime_leases`:

- `trip_id uuid primary key references trips(id) on delete cascade`
- `holder_id uuid not null`
- `runtime_epoch bigint not null check > 0`
- `lease_expires_at timestamptz not null`
- `renewed_at timestamptz not null`

Required indexes cover owner-to-trip lookup, pending outbox `(next_attempt_at, trip_id)`, command outcome `(trip_id, message_id)`, latest valid snapshots `(trip_id, covered_finalized_mutation_sequence desc)`, and expiring leases.

### Transaction isolation and lock order

- Use PostgreSQL `READ COMMITTED` with explicit row locks; no transaction performs network I/O.
- Every per-trip mutation transaction locks `trips` first with `SELECT ... FOR UPDATE`, then `command_intents`, then `planner_outbox`. Snapshot transactions lock `trips` before snapshot/outbox rows. This fixed order prevents application-level deadlocks.
- Serialization/deadlock errors are retried by the backend with bounded database-attempt backoff; the transaction body is idempotent.

### Recording transaction

1. Lock the trip and verify ownership/current revision.
2. Look up `(trip_id, message_id)` and compare algorithm/digest.
3. Reject mismatched reuse; return matching stored outcome without allocating another sequence.
4. Require no other pending command for the trip.
5. Allocate `next_mutation_sequence`, then increment the stored next value.
6. Insert `command_intents` and lease-neutral `planner_outbox` rows.
7. Commit, then emit `durable_recorded`.

### Lease transactions

- First acquisition inserts epoch 1 using PostgreSQL `clock_timestamp()`.
- Renewal succeeds only for the same holder id and epoch before `lease_expires_at`; it extends expiry without changing epoch.
- A different holder, including a restarted backend with a new instance id, acquires only after PostgreSQL says the old lease expired. It locks the lease row, increments the prior epoch by exactly one, and sets the new holder/expiry atomically.
- A missing renewal response, transaction ambiguity, or local time disagreement is treated as loss of authority. Dispatch stops before the safety margin; local clocks never extend a lease.
- The single-host Compose supervisor ensures only one configured backend replica, but the database lease rule remains the correctness boundary.

### Finalization transaction

For C++ acceptance:

1. Lock trip, intent, and outbox; verify the correlated event, expected revision, and sequence.
2. Apply the normalized canonical trip/saved-plan mutation.
3. Increment trip revision by one.
4. Set trip finalized mutation watermark to the command sequence.
5. Mark intent `applied`, store outcome/versions, and mark outbox `accepted`.
6. Commit; then emit `planner_applied` and send `ConfirmFinalizedMutations`.

For terminal C++ rejection or logical expiry, do not change canonical trip data or trip revision; mark intent `rejected`/`expired`, advance the finalized mutation watermark, mark the outbox terminally rejected, commit, then report the terminal outcome and confirm the finalized watermark.

If finalization fails, the trip admits no later durable command. Replay obtains C++'s duplicate outcome and retries the same finalization transaction.

### Outbox claiming and retry

- Claim due rows in bounded batches with `FOR UPDATE SKIP LOCKED`.
- A claim has a short PostgreSQL-time lease in `claim_expires_at`; process failure makes it reclaimable.
- Each dispatch reads the currently valid trip lease and generates a new `request_id` and attempt expiry.
- Full-jitter capped exponential scheduling uses `random(0, min(30 seconds, 250 milliseconds * 2^min(attempt_count, 7)))` after transient failures.
- Durable rows do not stop after a fixed attempt count. A maximum count would silently abandon the durability guarantee. Retries stop only on applied/terminal outcome, trip deletion, or `paused_internal`; explicit administrative repair moves a paused row back to pending or terminally resolves it through an audited operation.
- Attempt thresholds raise observable warnings, but do not delete or dead-letter the command automatically.

Stream reconnection separately uses full-jitter backoff from 100 milliseconds capped at 10 seconds. Successful connection resets the stream backoff.

### Durable command and dispatch deadlines

- `command_expires_at_unix_ms` is optional durable product semantics and is stored in the intent. Omitted means no retry-count/time-based logical expiry.
- `expires_at_unix_ms` in the gRPC envelope is per dispatch attempt and is never used as durable identity.
- On each attempt the backend sets transport expiry to `now + configured planner_attempt_timeout`. Logical command expiry is carried separately in `ApplyTripEvent` and never shortens the transport attempt in a way that could prevent the sequence from being resolved.
- `DEADLINE_EXCEEDED` or transport cancellation leaves durable work pending and schedules another attempt while logical time remains.
- Once logical expiry passes before C++ acceptance, the backend still dispatches the event with a fresh attempt deadline; C++ resolves and consumes the sequence as terminal `COMMAND_EXPIRED`. Transport failure before that acknowledgement leaves it pending and retryable.
- Already accepted C++ work is finalized even if the client disconnects or the logical deadline passes after acceptance.

### Snapshot validation, retention, and pruning

- Snapshot schema version 1 is the only V1 compatible version.
- Checksum is SHA-256 over the exact payload bytes; size must equal the declared size and remain under the configured maximum.
- Metadata must not be older than the stored valid snapshot, must not exceed the trip's finalized mutation watermark, and must match the decoded `TripStateSnapshot`.
- Unknown schema versions and checksum/parse/metadata failures mark the candidate invalid; they never replace a valid snapshot.
- The backend retains the two newest non-invalid compatible snapshots per trip. It may delete older compatible snapshots only in the same transaction that commits a new valid snapshot. Invalid rows retain metadata but may have payload purged by a later maintenance policy; V1 does not need such maintenance.
- Snapshot commit and deletion of terminal outbox rows with `mutation_sequence <= covered_finalized_mutation_sequence` occur atomically.
- `command_intents` are never pruned before trip deletion.
- Recovery tries the newest compatible snapshot, then the previous compatible snapshot, then rebuilds from the fully normalized canonical trip/activity/window/travel-delay data at its finalized watermark and replays only newer uncovered outbox work. It never starts from a known-corrupt snapshot.

## GPS/Telemetry Coalescing and Boundary Detection

The backend performs only schema/auth/admission checks and assigns observation sequences. It does not decide whether a coordinate crosses a route/geofence boundary because it does not own authoritative live trip state.

On a healthy stream, the backend sends admitted observations in sequence and bounds its queue. When disconnected or overloaded it retains only the newest observation per type/trip and explicitly marks replaced samples `COALESCED` or `DROPPED`; old telemetry is never replayed.

The C++ owner shard performs domain classification before expensive work:

1. apply the newest non-stale observation;
2. compare it with the shard-owned current route, geofences, and slack thresholds;
3. promote a current route-deviation/boundary condition to high-priority replan work;
4. replace any older pending ordinary-location replan with one latest trigger.

Transient boundary crossings that must never be lost must arrive as an explicit high/critical domain event such as `RouteDeviationDetected` or an activity lifecycle event; they cannot rely on intermediate GPS samples surviving overload. The backend never attempts to infer such events.

## United States Time Normalization

V1 accepts only IANA time-zone identifiers whose pinned tzdata `zone1970.tab` entry includes country code `US`. This covers United States DST/non-DST exceptions without maintaining a hand-written offset table.

- Durable trip/activity input retains the IANA zone name for display and audit.
- The V1 WebSocket/domain fields above carry already-normalized UTC Unix milliseconds plus the relevant IANA zone name. CLI/fixture and seeded-hours adapters accept local wall time, apply the rules below, and send/store normalized UTC. The backend independently validates the allowed zone, UTC ranges, ordering, and duration constraints before C++ admission.
- A local timestamp is converted with the pinned IANA tzdata version to signed UTC Unix epoch milliseconds.
- A nonexistent spring-forward wall time is rejected as `INVALID_ARGUMENT` by the local-time ingestion adapter.
- An ambiguous fall-back wall time requires an explicit `utc_offset_seconds` choice matching one valid offset at ingestion; otherwise it is rejected.
- Seeded recurring hours are expanded into concrete UTC `TimeWindow` values for the trip date before planner admission.
- Overnight hours are split/expanded into a window whose close is later than open; exceptional closure dates override recurring hours.
- The V1 "current day" planning horizon is the local calendar day in `TripDefinition.default_time_zone_name`; its two local midnights are converted to UTC with pinned tzdata. Activities in other US zones participate when their UTC schedule falls inside that trip-day interval.
- Planner search, deadlines, comparisons, and output scheduling use only UTC Unix epoch values.
- Every output segment also carries its IANA `time_zone_name`; the backend/client converts UTC to display-local time after replanning. The planner never performs display conversion.
- The tzdata version is recorded in the build/deployment manifest. A tzdata change invalidates normalized seed fixtures and requires their regeneration/tests, but not a planner API change.

## Authentication Artifacts

- A V1 development bearer token is exactly 32 random bytes encoded as unpadded base64url: 43 characters.
- Raw tokens enter the backend only through the Docker secret file `/run/secrets/liveroute_dev_token`; they never appear in Compose YAML, URLs, database rows, logs, metrics, or error text.
- A one-shot `liveroute-admin seed-dev` container runs after migrations, reads the secret plus the nonsecret configured development user UUID/display name/time zone, and transactionally upserts the user and SHA-256 token digest. It emits identifiers only, never the raw token or digest.
- Authentication performs fixed-length validation, SHA-256, indexed digest lookup, expiry/revocation checks, and constant-time digest comparison.
- The default local token has no automatic expiry but may be revoked/replaced; non-loopback deployment is not allowed to use this development-token mode without TLS.
- The first non-ping message must be `authenticate`. Authentication must complete before the configured timeout.
- Authorization checks the authenticated user against `trips.owner_user_id` for every subscribe, resync, command, and telemetry message, including messages on an already-authorized connection.
- Origin is matched exactly against a configured allowlist. Origin omission is allowed only when `allow_originless_local_clients=true` and the backend bind address is loopback.

## Configuration and Readiness

Exact numeric queue/worker tuning is not a pre-source product decision. Implementation may begin with required typed configuration keys, but a finite `config/local-v1.yaml` must exist before the first runnable Compose acceptance test. It contains no secrets and every capacity is positive and bounded. Production-universal defaults are not claimed.

Hard protocol limits that affect schemas are fixed for V1:

- WebSocket frame: 256 KiB;
- decoded WebSocket message: 256 KiB;
- gRPC message: 4 MiB;
- snapshot payload: 2 MiB;
- resynchronization outstanding command IDs: 128.

Other initial values—shards, workers, queue capacities, database pool, OSRM concurrency, lease duration, heartbeat, attempt timeout, and shutdown deadline—are selected in the local config during the skeleton milestone and then tuned from tests. Startup rejects missing, zero, unbounded, overflow-prone, or mutually inconsistent values.

Readiness means dependency usability rather than mere process existence:

- PostgreSQL liveness: container process; readiness: `pg_isready` plus expected Goose migration version.
- OSRM car/foot readiness: fixed small Table request succeeds with expected dimensions.
- C++ liveness/readiness: standard gRPC health service reports `liveroute.v1.LiveRoutePlanner` as `SERVING` after queues/executors/config initialize, and separately reports `liveroute.v1.LiveRoutePlanner/osrm-car` and `/osrm-foot` as `SERVING` only after their fixed Table probes pass.
- Backend `/healthz`: event loop/process is alive; it does not query dependencies.
- Backend `/readyz`: migrations are current, PostgreSQL ping succeeds, at least one planner stream is `StreamReady`, and both named OSRM profile health services report `SERVING`.
- A backend that loses readiness may keep existing connections long enough to emit bounded transient errors, but Compose/test admission waits for `/readyz`.

Container-first development requires Docker Engine/compatible container runtime and Docker Compose on the host. PostgreSQL, Go, C++ build tools, Protobuf/Buf, and OSRM run in containers; the user does not install those packages directly on the host.

## Reproducible OSRM Inputs

This is primarily an engineering fixture choice, not a product decision. V1 selects a small Rhode Island extract for default development/CI because it is a bounded United States dataset; it does not define future product coverage.

- Start from official OSRM release `v26.5.0` and pin the resolved GHCR image digest in the repository manifest.
- Use the repository-supplied car and foot profile files from that same source release and record their SHA-256 digests.
- Resolve a Rhode Island Geofabrik extract to immutable bytes during the skeleton milestone, record its source URL, retrieval date, size, and SHA-256 in `config/osrm-dataset.lock`, and use a content-addressed cache/artifact. Never depend on a mutable `latest` URL without checking the recorded digest.
- Build car and foot MLD artifacts separately from the same locked extract.
- Record OSRM release, image digest, profile digests, extract digest, preprocessing flags, and dataset version in readiness output and cache keys.
- A different demo/service geography changes the dataset lock, not the planner or provider contract. Selecting that broader geography is a later product/demo choice.

## Compatibility Mechanism

### Protobuf

- Pin Buf CLI in the tool container.
- Use `buf lint` and the `FILE` breaking category, which is stricter than binary-wire-only compatibility and protects generated Go/C++ APIs.
- Check in `proto/baseline/liveroute-v1.binpb` after the initial schema review.
- CI runs `buf build`, `buf breaking --against proto/baseline/liveroute-v1.binpb`, regenerates Go/C++ outputs, and fails on a dirty generated-code diff.
- Removed fields/enums are deprecated and their names/numbers reserved; no number is reused. An incompatible change creates package `liveroute.v2`.

### WebSocket JSON

- Check in the draft-2020-12 schema and its canonical SHA-256 after review.
- Tests validate a checked-in positive/negative corpus with both the Go validator and the CLI/integration client decoder.
- The v1 schema is frozen except for new namespaced `extensions` keys, which do not alter standard validation. Any standard-field change creates `liveroute.v2`.
- CI recomputes the canonical schema digest; an intentional v1 baseline replacement requires explicit contract review and regenerated corpus, not an unnoticed edit.

### Database and snapshots

- Goose migrations are immutable after application; new changes use a new sequential migration. CI migrates a clean PostgreSQL database up, checks exact constraints/indexes, and migrates from the previous released schema.
- Snapshot schema version 1 uses the Protobuf descriptor baseline and golden payload/checksum fixtures. Unknown versions must fall back without state mutation.

## Required Failure and Compatibility Tests

In addition to the existing planner/runtime/OSRM tests, V1 requires:

- higher-epoch bootstrap discards old observations, proposals, provider jobs, and planning results;
- same-epoch reconnect retains the observation watermark and rejects older observations as `STALE`;
- backend restart resets observation sequencing under a higher epoch and accepts the first new sample;
- crashes after intent commit, after C++ acceptance, after PostgreSQL finalization, after finalization confirmation, and before each client acknowledgement converge without double application;
- a plan computed from an accepted-but-unfinalized mutation is never published; the latest still-current plan is released only after PostgreSQL finalization;
- snapshot request racing an unresolved accepted mutation returns `SNAPSHOT_NOT_READY` and later succeeds after confirmation;
- terminal rejection advances finalized/mutation watermarks without advancing trip revision;
- concurrent/stale lease acquisition, renewal safety-margin expiry, and stale-holder dispatch are fenced;
- outbox claim expiry, duplicate claims, transaction rollback, serialization/deadlock retry, and retry jitter never lose a durable row;
- logical command expiry differs from per-attempt deadline and consumes its sequence terminally;
- checksum mismatch, unknown snapshot version, metadata mismatch, newest-snapshot corruption, and fallback to the second snapshot/full rebuild work;
- WebSocket unknown standard fields, extension fields, unsupported versions, every close code, auth timeout, heartbeat timeout, origin denial, oversized frame, and full essential buffer behave as contracted;
- development token is never present in logs/errors and raw tokens are absent from PostgreSQL;
- US DST nonexistent/ambiguous time, explicit fall-back offset, Arizona/Hawaii non-DST behavior, overnight hours, and exceptional closures normalize correctly;
- OSRM null, `NoSegment`, `TooBig`, malformed JSON, wrong dimensions, nonfinite/negative/overflow numbers, timeout, cancellation, and byte limit map to exact statuses;
- invalid/zero/unbounded/inconsistent configuration fails startup;
- shutdown while a durable response, finalization confirmation, snapshot, provider request, and planner job are in flight either drains within the deadline or leaves recoverable outbox state;
- Protobuf baseline, JSON schema digest/corpus, and previous database migration compatibility checks run in the standard check target.

## Implementation-Gate Policy

One agent may own and continuously implement all of V1, and numeric roadmap phase order is not mandatory. V1 must not, however, land as one unverified all-components-at-once batch. The following are correctness gates, not staffing constraints:

1. contract artifacts and migrations validate;
2. deterministic internal domain/provider/runtime tests pass;
3. gRPC reconnect/epoch/finalization tests pass;
4. PostgreSQL/WebSocket recovery tests pass;
5. OSRM/hours/planner correctness tests pass;
6. failure/load/resource tuning and performance reports pass.

An agent may work ahead locally across gates, but a later gate cannot be used to waive an earlier invariant. This keeps failures attributable and prevents storage/wire changes from being discovered after the planner/runtime depends on them.

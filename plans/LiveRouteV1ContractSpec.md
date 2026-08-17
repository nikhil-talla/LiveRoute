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
- Buf CLI, pinned in its own container, for Protobuf lint, descriptor images, and breaking-change checks. Buf is not the C++ generator.
- C++ Protobuf/gRPC generation uses the `linux/amd64` image `bmigeri/devcon-cpp@sha256:369f7744d6f9632b1c8142981f01c7b4c98db51b0096686dbd25f9ebb9eaa6f4`, which contains `protoc` 31.1, Protobuf C++ 31.1.0, gRPC C++ 1.78.1, and `/opt/grpc/bin/grpc_cpp_plugin`. The same Protobuf/gRPC versions compile and link the generated C++ code.
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
- `outbox_id`, `snapshot_id`, `plan_id`, `proposal_id`, backend instance id, and C++ instance id: lowercase RFC 4122 UUID strings.
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
- `trip_revision` is durable. Atomic `create_trip` commits revision 1 with the initial user-authored plan; later revisions increment by one for either a canonical-first user trip/current-plan edit committed in PostgreSQL or an accepted runtime-first durable mutation finalized in PostgreSQL.
- `mutation_sequence` is durable, starts at 1 with `create_trip`, and is never reused. Canonical-first user edits, accepted runtime-first commands, and terminally rejected runtime-first commands all consume a sequence. Only a canonical-first committed edit or accepted runtime-first mutation advances `trip_revision`.
- Additive V1.5 HTTP-saved trips are the one initialization exception: while
  inactive and before their first absolute execution plan exists they retain
  `next_mutation_sequence = 1` and `finalized_mutation_sequence = 0`. Each
  activation consumes the current next value `N` as a finalized full-bootstrap
  baseline checkpoint, advances the next value to `N + 1`, and bootstraps C++
  with accepted/finalized watermark `N`. This checkpoint is not an
  `ApplyTripEvent`; it has no synthetic command intent or outbox row. The HTTP
  idempotency record, durable execution operation, and immutable target plan are
  its audit record. Reactivation never resets or reuses the sequence. Therefore
  zero remains valid only for an inactive saved trip with no active absolute
  plan, never for an admitted C++ runtime.
- `planner_state_version` is scoped to `runtime_epoch`, starts at 0 after a higher-epoch bootstrap, and increments once for each accepted state-changing durable event or telemetry observation in that epoch.
- `planning_generation` is internal, starts at 0 per runtime epoch, and increments whenever accepted input invalidates planning work.
- `observation_sequence` is owned by the backend in memory, scoped to `(trip_id, runtime_epoch)`, starts at 1, and may contain gaps. It is never stored in PostgreSQL or a durable snapshot.
- A valid opaque advisory consumes its observation sequence but does not advance `planner_state_version` or `planning_generation`, because candidate search cannot read or be invalidated by its opaque bytes. `RECOMMENDATION_REFRESH` may request a new attempt over the unchanged current snapshot; the other advisory kinds only acknowledge the watermark.
- Freshness is compared lexicographically as `(runtime_epoch, planner_state_version)`, never by planner state version alone.
- The authoritative current plan is identified durably by `(trip_id, plan_id, plan_revision)`. It changes only through `create_trip`, canonical-first `trip_edited`/`replace_current_plan`, or fresh user acceptance of an engine proposal.
- An engine proposal is identified by `(runtime_epoch, proposal_id, source_planner_state_version, base_current_plan_id)`. Acceptance/rejection must match all four values.

When a higher runtime epoch is bootstrapped, C++ atomically:

1. fences the old epoch;
2. cancels and discards old-epoch provider/planner work and pending telemetry;
3. clears the current observation, observation watermark, active proposal, and observation-derived unpublished result while retaining the bootstrapped authoritative current plan;
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
| 14 | `INFEASIBLE` | false | No engine proposal satisfies protected constraints; the user-selected current plan remains authoritative. |
| 15 | `PROVIDER_UNAVAILABLE` | true | Required route/hours data was unavailable and no permitted data source could answer. |
| 16 | `DURABILITY_UNAVAILABLE` | true | PostgreSQL cannot safely record/finalize durability-dependent work. |
| 17 | `UNAVAILABLE` | true | Transient stream/service failure not covered by a more specific code. |
| 18 | `UNSUPPORTED_VERSION` | false | Protocol, capability, or snapshot schema is unsupported. |
| 19 | `SNAPSHOT_NOT_READY` | true | Accepted and PostgreSQL-finalized mutation watermarks do not yet match. |
| 20 | `SNAPSHOT_INCOMPATIBLE` | false | Snapshot schema/checksum/metadata cannot be used. |
| 21 | `INTERNAL` | false | Invariant violation or non-classified defect; operators investigate instead of automatically retrying forever. |
| 22 | `MATRIX_TOO_LARGE` | false | The requested route matrix cannot be served within the fixed V1 location/request limit. Normal preflight rejects before I/O; OSRM `TooBig` maps identically if configured limits drift. |

All old/late ownership and version cases use `STALE`. A structured stale reason preserves diagnostics:

`EPOCH`, `MUTATION_SEQUENCE`, `OBSERVATION_SEQUENCE`, `TRIP_REVISION`, `PLANNER_STATE_VERSION`, or `PLAN_PROPOSAL`.

Owner-shard event coordination is transactional across `TripState` and its
runtime-version record. Version checks have a non-mutating preview operation.
For a new admissible request, apply the event to a candidate state first, then
commit the corresponding version advancement and candidate state together on
the single owner shard. Duplicate/stale/inactive preview outcomes never invoke
domain mutation. A runtime-first durable domain rejection consumes the next
mutation sequence with no trip/state/planning-version advancement and returns
its terminal status; this includes `STALE/PLAN_PROPOSAL`. Invalid observation
or advisory work consumes no observation sequence. Invalid canonical-first
mirror work consumes no mutation sequence because C++ does not yet contain its
PostgreSQL-authoritative effect; the backend must retry a matching mirror or
perform full canonical bootstrap. An impossible mismatch between a successful
preview and the immediately following owner-shard commit is `INTERNAL`.

The transport adapter maps coordinator outcomes without interpretation:

| Coordinator outcome | Public status | Retryable |
| --- | --- | --- |
| accepted | `OK` | false |
| duplicate | `DUPLICATE` | false |
| stale version or proposal | `STALE` plus the exact structured reason | false |
| invalid envelope/domain transition | `INVALID_ARGUMENT` | false |
| inactive runtime | `INACTIVE_TRIP` | true internally |
| preview/commit invariant mismatch | `INTERNAL` | false |

Successful admission returns an internal immutable planning seed, not a public
`ReplanResult`. The seed contains the accepted state snapshot and trigger plus
the exact runtime epoch, planner-state version, planning generation, trip
revision, accepted mutation sequence, and base current-plan id. It contains no
provider handle, provider response, or matrix. Provider work runs later on its
bounded executor. Concrete operating-hours windows are already normalized in
the accepted activity snapshot; a refresh becomes a typed
`OperatingHoursChanged` event before this transaction. Route input is current
location followed by remaining activities in authoritative current-plan suffix
order. Each matrix destination column uses that destination activity's
`inbound_travel_mode`; mixed walking/driving snapshots may be acquired as one
Table matrix per distinct mode and combined by destination column. Provider
status mapping occurs before the matrix-backed planner attempt. Planner search
status mapping occurs after `run_replan_attempt`; only a present validated
proposal is eligible for the generation-fenced commit and later persistence.

`degraded` is not a status. It previously mixed successful-but-lower-quality output with actual failures. Successful results use `status = OK` plus:

- `plan_quality`: `COMPLETE`, `BEST_SO_FAR`, or `NO_NEW_PROPOSAL`;
- `routing_quality`: `FRESH`, `STALE_CACHE`, or `UNAVAILABLE`;
- `recovery_state`: `CURRENT` or `NOT_ADVANCING`.

Examples:

- A deadline returns a valid partial proposal: `OK` + `BEST_SO_FAR`.
- OSRM fails and no proposal can be computed: `PROVIDER_UNAVAILABLE`.
- The requested matrix exceeds the fixed V1 limit: `MATRIX_TOO_LARGE`; retrying the same points unchanged cannot succeed.
- A Phase 17 stale fallback admitted by `liveroute-route-cache-v1` produces a
  proposal: `OK` + `STALE_CACHE`.
- PostgreSQL is down but still-valid-lease telemetry is accepted: `OK` + `NOT_ADVANCING`.

Telemetry has a separate disposition: `ACCEPTED`, `COALESCED`, `DROPPED`, or `REJECTED`. A dropped/coalesced obsolete sample is not a planner error.

For a new runtime-first durable mutation, C++ consumes/advances the mutation sequence only on `ACCEPTED`, or on terminal `REJECTED` with `STALE`, `INVALID_ARGUMENT`, `NOT_FOUND`, `COMMAND_EXPIRED`, or `INFEASIBLE`. `INACTIVE_TRIP`, `RESOURCE_EXHAUSTED`, `DEADLINE_EXCEEDED`, `CANCELLED`, `PROVIDER_UNAVAILABLE`, and `UNAVAILABLE` leave the sequence pending for retry. `INTERNAL` also leaves it pending, pauses automatic dispatch for that trip, and requires an explicit operator repair/retry decision; later durable commands remain blocked rather than silently bypassing uncertain state. `DUPLICATE` returns the already-recorded resolution and does not apply anything again. A canonical-first `TripEdited` or `CurrentPlanReplaced` mirror may resolve only as accepted/duplicate; structurally incompatible normalized data is `INTERNAL`, leaves the sequence pending, and never converts the already-committed user edit into a terminal rejection.

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
| 28 | `TripBootstrapped` | `trip_bootstrapped` |

Fields 20-28 form one `payload` oneof.

### Common enums

- `StatusCode`: the numeric table above.
- `StaleReason`: 0 `UNSPECIFIED`, 1 `EPOCH`, 2 `MUTATION_SEQUENCE`, 3 `OBSERVATION_SEQUENCE`, 4 `TRIP_REVISION`, 5 `PLANNER_STATE_VERSION`, 6 `PLAN_PROPOSAL`.
- `EventDisposition`: 0 `UNSPECIFIED`, 1 `ACCEPTED`, 2 `DUPLICATE`, 3 `STALE`, 4 `REJECTED`.
- `TravelMode`: 0 `UNSPECIFIED`, 1 `WALKING`, 2 `DRIVING`.
- `ActivityClass`: 0 `UNSPECIFIED`, 1 `FIXED`, 2 `FLEXIBLE`.
- `ActivityState`: 0 `UNSPECIFIED`, 1 `PLANNED`, 2 `STARTED`, 3 `COMPLETED`, 4 `SKIPPED`.
- `PlanDecision`: 0 `UNSPECIFIED`, 1 `ACCEPT`, 2 `REJECT`.
- `PlanOrigin`: 0 `UNSPECIFIED`, 1 `USER_AUTHORED`, 2 `ACCEPTED_ENGINE_PROPOSAL`.
- `PlanEntryState`: 0 `UNSPECIFIED`, 1 `SCHEDULED`, 2 `OMITTED`.
- `SegmentDisposition`: 0 `UNSPECIFIED`, 1 `PRESERVED`, 2 `MOVED`, 3 `SHORTENED`, 4 `SKIPPED`, 5 `ADDED`. Numeric value 3 remains reserved for wire compatibility, but the `liveroute-v1-lexicographic-1` planner never emits `SHORTENED` and a V1 proposal containing it is invalid.
- `NotificationType`: 0 `UNSPECIFIED`, 1 `NONE`, 2 `LOW_SLACK_WARNING`, 3 `CRITICAL_LATENESS`, 4 `PLAN_CHANGE_SUGGESTED`, 5 `INFEASIBLE_SCHEDULE`.
- `PlanReasonCode`: 0 `UNSPECIFIED`, 1 `LATE_DEPARTURE`, 2 `ACTIVITY_DELAY`, 3 `ROUTE_DEVIATION`, 4 `HOURS_CHANGED`, 5 `PLACE_CLOSED`, 6 `RESERVATION_AT_RISK`, 7 `TRAVEL_DELAY`, 8 `USER_EDIT`, 9 `DEADLINE_BUDGET`, 10 `NO_FEASIBLE_PLAN`.
- `PlanQuality`: 0 `UNSPECIFIED`, 1 `COMPLETE`, 2 `BEST_SO_FAR`, 3 `NO_NEW_PROPOSAL`.
- `RoutingQuality`: 0 `UNSPECIFIED`, 1 `FRESH`, 2 `STALE_CACHE`, 3 `UNAVAILABLE`.
- `RecoveryState`: 0 `UNSPECIFIED`, 1 `CURRENT`, 2 `NOT_ADVANCING`.
- `SnapshotReason`: 0 `UNSPECIFIED`, 1 `PERIODIC`, 2 `DURABLE_BOUNDARY`, 3 `DEACTIVATION`, 4 `SHUTDOWN`.
- `DeactivationReason`: 0 `UNSPECIFIED`, 1 `BACKEND_REQUEST`, 2 `IDLE_EVICTION`, 3 `TRIP_DELETED`, 4 `SHUTDOWN`.
- `AdvisoryKind`: 0 `UNSPECIFIED`, 1 `RECOMMENDATION_REFRESH`, 2 `WEATHER_CHANGED`, 3 `CROWD_CHANGED`, 4 `SOCIAL_UPDATE`.

### Domain messages

`Location`: 1 `double latitude`, 2 `double longitude`.

`TimeWindow`: 1 `int64 opens_at_unix_ms`, 2 `int64 closes_at_unix_ms`.

`ActivityTiming`: 1 `repeated TimeWindow open_windows`, 2 `optional int64 reservation_start_unix_ms`, 3 `uint32 reservation_grace_seconds`, 4 `uint32 min_duration_seconds`, 5 `uint32 preferred_duration_seconds`, 6 `uint32 max_duration_seconds`, 7 `bool mandatory`, 8 `bool can_shorten`, 9 `bool can_move`, 10 `bool can_skip`, 11 `optional int64 mandatory_deadline_unix_ms`. Field 8 is retained for V1 wire/storage compatibility but does not authorize the V1 suggestion engine to shorten an activity.

`Activity`: 1 `string activity_id`, 2 `string place_id`, 3 `string display_name`, 4 `Location location`, 5 `string time_zone_name`, 6 `TravelMode inbound_travel_mode`, 7 `ActivityClass activity_class`, 8 `ActivityState activity_state`, 9 `int32 priority_rank`, 10 `int32 utility_score`, 11 `ActivityTiming timing`, 12 `uint32 activity_delay_seconds`, 13 `optional int64 found_closed_at_unix_ms`.

`TravelDelayState`: 1 `string from_activity_id`, 2 `string to_activity_id`, 3 `uint32 additional_seconds`, 4 `int64 observed_at_unix_ms`.

`TripDefinition`: 1 `string trip_id`, 2 `string owner_user_id`, 3 `string default_time_zone_name`, 4 `repeated Activity activities`, 5 `uint32 completed_prefix_count`, 6 `string current_activity_id`, 7 `string current_plan_id`, 8 `repeated TravelDelayState travel_delays`.

`completed_prefix_count` counts leading entries of the separately supplied authoritative `CurrentPlan.segments`; it never indexes `TripDefinition.activities`. Those leading entries must reference activities whose state is `COMPLETED` or `SKIPPED`. When `current_activity_id` is present, it must identify the `STARTED` activity at `CurrentPlan.segments[completed_prefix_count]`, and that one additional segment is also preserved. No later current-plan segment may reference a terminal or started activity. `TripDefinition.activities` retains trip-definition order only: its zero-based position is `original_trip_ordinal` for deterministic candidate identity and tie-breaking. A canonical edit or replacement may reorder future current-plan segments but must not move the completed prefix or started activity out of this shape.

`CurrentObservation`: 1 `Location location`, 2 `int64 observed_at_unix_ms`, 3 `optional double velocity_meters_per_second`, 4 `optional double heading_degrees`.

`CurrentPlanSegment`: 1 `string activity_id`, 2 `PlanEntryState state`, 3 `optional int64 scheduled_start_unix_ms`, 4 `optional int64 scheduled_end_unix_ms`.

`CurrentPlan`: 1 `string plan_id`, 2 `uint64 plan_revision`, 3 `PlanOrigin origin`, 4 `repeated CurrentPlanSegment segments`, 5 `int64 created_at_unix_ms`, 6 `optional string source_proposal_id`.

The repeated `CurrentPlan.segments` order is the user's authoritative itinerary and contains every trip activity exactly once using canonical activity ids. A `SCHEDULED` entry requires both times, `start < end`, and no overlap with the next scheduled entry; an `OMITTED` entry forbids both times. It intentionally omits provider-computed route metrics, planner reasons, quality, and source runtime versions. Travel-time, operating-hours, reservation, and deadline infeasibility do not invalidate a structurally valid current plan; they become warnings and proposal inputs.

`RouteLeg`: 1 `uint32 duration_seconds`, 2 `uint32 distance_meters`, 3 `bool reachable`.

`ProposalSegment`: 1 `string activity_id`, 2 `Location location`, 3 `string time_zone_name`, 4 `optional int64 scheduled_start_unix_ms`, 5 `optional int64 scheduled_end_unix_ms`, 6 `optional RouteLeg inbound_route`, 7 `SegmentDisposition disposition`, 8 `repeated PlanReasonCode reasons`. `SKIPPED` forbids fields 4-6. Every scheduled segment requires fields 4-5 with `start < end`. A scheduled revised-suffix segment also requires a reachable field 6 derived from the attempt's immutable travel matrix. A scheduled preserved-prefix segment may omit field 6 because `CurrentPlan` intentionally stores no provider route and the suffix matrix begins at the live replanning boundary; if field 6 is present on such a prefix segment, it must be reachable and structurally valid.

`PlanProposal`: 1 `string proposal_id`, 2 `uint64 source_runtime_epoch`, 3 `uint64 source_planner_state_version`, 4 `string base_current_plan_id`, 5 `uint64 source_trip_revision`, 6 `uint64 source_accepted_mutation_sequence`, 7 `repeated ProposalSegment preserved_prefix`, 8 `repeated ProposalSegment revised_suffix`, 9 `int64 created_at_unix_ms`.

The concatenated preserved prefix and revised suffix contain every trip activity exactly once. The preserved prefix matches immutable/completed current-plan entries; the revised suffix expresses every retained, moved, added-back, or omitted future activity explicitly. V1 never shortens an activity. This makes proposal acceptance a deterministic conversion rather than a second planner run.

V1 assigns the single segment disposition deterministically. An omitted proposal segment is `SKIPPED`. A scheduled revised-suffix segment whose authoritative baseline entry was omitted is `ADDED`. A scheduled segment whose baseline was scheduled is `MOVED` when its interval or its relative position in the common-scheduled suffix changed, otherwise `PRESERVED`. Scheduled preserved-prefix entries are `PRESERVED`. `SHORTENED` is never emitted, so no moved-versus-shortened precedence exists.

`PlannerStats`: 1 `uint64 candidates_evaluated`, 2 `uint64 candidates_pruned`, 3 `uint32 search_depth`, 4 `uint32 queue_wait_microseconds`, 5 `uint32 provider_microseconds`, 6 `uint32 planner_microseconds`, 7 `uint32 serialization_microseconds`, 8 `bool deadline_hit`. `deadline_hit` records the actual wall-clock stop cause and is therefore `true` even when a complete feasible candidate makes the visible result `OK` + `BEST_SO_FAR`; candidate/expansion/beam limits and cancellation alone leave it `false`.

`ResultQuality`: 1 `PlanQuality plan_quality`, 2 `RoutingQuality routing_quality`, 3 `RecoveryState recovery_state`.

`StoredPlanProposal`: 1 `PlanProposal proposal`, 2 `NotificationType notification`, 3 `repeated PlanReasonCode reasons`, 4 `PlannerStats stats`, 5 `ResultQuality quality`.

### Stream-control payloads

`OpenStream`: 1 `string backend_instance_id`, 2 `string protocol_version`, 3 `repeated string capabilities`.

`StreamReady`: 1 `string cpp_instance_id`, 2 `string protocol_version`, 3 `repeated string capabilities`, 4 `uint32 max_message_bytes`, 5 `uint32 max_snapshot_bytes`, 6 `uint32 max_active_trips`, 7 `StatusCode status`.

Both sides require protocol string `liveroute.v1` and these exact capability strings: `canonical_first_plan_sync`, `durable_plan_proposals`, `epoch_scoped_observations`, `finalized_mutation_watermark`, `result_quality_metadata`, `snapshot_schema_1`, and `user_authoritative_current_plan`. Capabilities are sorted lexicographically and unique. Unknown capabilities are ignored for negotiation, but any missing required V1 capability returns `UNSUPPORTED_VERSION` and closes the stream without trip admission.

`SnapshotBlob`: 1 `uint32 snapshot_schema_version`, 2 `uint64 source_runtime_epoch`, 3 `uint64 source_planner_state_version`, 4 `uint64 trip_revision`, 5 `uint64 covered_finalized_mutation_sequence`, 6 `uint32 payload_size_bytes`, 7 `bytes checksum_sha256`, 8 `bytes payload`.

`BootstrapTrip`: 1 `oneof base { SnapshotBlob snapshot = 1; TripDefinition full_trip = 2; }`, 3 `uint64 finalized_mutation_sequence`, 4 `uint64 trip_revision`, 5 `optional CurrentObservation current_observation`, 6 `uint64 current_observation_sequence`, 7 `optional CurrentPlan current_plan`.

For a snapshot base, field 7 is absent because `TripStateSnapshot` contains the current plan at that covered watermark. For a full-trip base, field 7 is required and its plan id must equal `TripDefinition.current_plan_id`; the full trip, plan, revision, and finalized watermark describe one PostgreSQL-consistent read. For a higher epoch, fields 5-6 are absent/zero. For same-epoch stream rebinding they carry the backend's latest observation and watermark. A snapshot may cover only finalized durable state; telemetry is never serialized in it.

`TripBootstrapped`: 1 `StatusCode status`, 2 `bool retryable`, 3 `string current_plan_id`, 4 `uint64 accepted_mutation_sequence`, 5 `uint64 finalized_mutation_sequence`, 6 `string safe_message`. On successful full canonical bootstrap, fields 3-5 exactly match the loaded plan and PostgreSQL watermark; the backend may use that acknowledgement to resolve covered canonical-first mirror rows. Snapshot bootstrap reports only the snapshot-covered values, after which uncovered events are replayed normally.

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
| 25 | `TripEdited` | `trip_edited` | canonical-first durable mirror |
| 26 | `ReservationChanged` | `reservation_changed` | durable |
| 27 | `MandatoryDeadlineChanged` | `mandatory_deadline_changed` | durable |
| 28 | `RouteDeviationDetected` | `route_deviation_detected` | high observation |
| 29 | `OperatingHoursChanged` | `operating_hours_changed` | durable |
| 30 | `PlaceFoundClosed` | `place_found_closed` | durable |
| 31 | `TravelDelay` | `travel_delay` | durable |
| 32 | `PlanDecisionEvent` | `plan_decision` | durable/CAS |
| 33 | `AdvisoryUpdate` | `advisory_update` | advisory |
| 34 | `CurrentPlanReplaced` | `current_plan_replaced` | canonical-first durable mirror |

Payload fields:

- `LocationUpdated`: 1 `Location location`.
- `VelocityUpdated`: 1 `double meters_per_second`.
- `HeadingUpdated`: 1 `double degrees`.
- `ActivityStatusChanged`: 1 `string activity_id`, 2 `ActivityState state`.
- `ActivityDelayed`: 1 `string activity_id`, 2 `uint32 delay_seconds`; this replaces the activity's current total delay rather than incrementing it.
- `TripEdited`: one `operation` of 1 `AddActivity add`, 2 `ReplaceActivity replace`, 3 `RemoveActivity remove`, 4 `ReorderActivities reorder`, plus 5 `CurrentPlan resulting_current_plan`. The backend has already committed both the normalized activity edit and this complete user-authored resulting plan; C++ mirrors them and does not approve feasibility.
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
- `PlanDecisionEvent`: 1 `PlanDecision decision`, 2 `string proposal_id`, 3 `uint64 source_runtime_epoch`, 4 `uint64 source_planner_state_version`, 5 `string base_current_plan_id`, 6 `optional CurrentPlan resulting_current_plan`. The backend deterministically converts the stored proposal, assigns id/revision/creation time, and durably records the exact serialized field 6 before dispatch. It is required for `ACCEPT` and forbidden for `REJECT`, so C++ and PostgreSQL install byte-identical immutable current-plan metadata across retries.
- `AdvisoryUpdate`: 1 `AdvisoryKind kind`, 2 `string source`, 3 `bytes opaque_payload`, with a configured byte limit. Candidate search never reads opaque provider data; an adapter must normalize any advisory effect first.
- `CurrentPlanReplaced`: 1 `CurrentPlan current_plan`. It is already canonical in PostgreSQL when delivered. C++ validates transport/domain compatibility, mirrors it, invalidates prior proposal work, and never rejects it for nonoptimality or schedule feasibility.

`EventAcknowledged`: 1 `EventDisposition disposition`, 2 `StatusCode status`, 3 `bool retryable`, 4 `StaleReason stale_reason`, 5 `string event_id`, 6 `uint64 resolved_mutation_sequence`, 7 `uint64 resolved_observation_sequence`, 8 `bool replan_scheduled`, 9 `string safe_message`, 10 `string resulting_current_plan_id`.

Field 10 is required as a canonical lowercase UUID exactly when a canonical-first `TripEdited` or `CurrentPlanReplaced` resolves as `ACCEPTED` or `DUPLICATE`. It identifies the `CurrentPlan` actually installed in C++ and must equal the plan carried by that event. It is absent for every runtime-first or observation event and for stale, rejected, inactive, or internal outcomes. The backend must compare field 10, the response envelope's `trip_revision`, and field 6 against the stored mirror event before marking runtime sync complete. A missing, malformed, or mismatched value is an `INTERNAL` compatibility fault: leave the mirror unresolved, pause later runtime-first dispatch, and recover through corrected replay or a verified full canonical bootstrap. This field is an additive wire-compatible V1 correction; checked-in generated bindings and the descriptor baseline are updated together.

`ReplanResult`: 1 `StatusCode status`, 2 `bool retryable`, 3 `PlanProposal proposal`, 4 `NotificationType notification`, 5 `repeated PlanReasonCode reasons`, 6 `PlannerStats stats`, 7 `ResultQuality quality`, 8 `string safe_message`.

Field 3 is present exactly when `status = OK` and `plan_quality` is `COMPLETE` or `BEST_SO_FAR`. It is absent for `NO_NEW_PROPOSAL` and every error status. Only a present proposal is eligible for durable proposal persistence/publication.

### V1 reason and notification derivation

Reason derivation is deterministic and is not selected by a handler, provider, or serializer. Every result/segment reason list is deduplicated and serialized in ascending `PlanReasonCode` numeric order. The triggering event contributes this intrinsic cause:

| Trigger payload | Intrinsic reason |
| --- | --- |
| `LocationUpdated`, `VelocityUpdated`, `HeadingUpdated` | none |
| `ActivityStatusChanged`, `TripEdited`, `ReservationChanged`, `MandatoryDeadlineChanged`, `PlanDecisionEvent`, `CurrentPlanReplaced` | `USER_EDIT` |
| `ActivityDelayed` | `ACTIVITY_DELAY` |
| `RouteDeviationDetected` | `ROUTE_DEVIATION` |
| `OperatingHoursChanged` | `HOURS_CHANGED` |
| `PlaceFoundClosed` | `PLACE_CLOSED` |
| `TravelDelay` | `TRAVEL_DELAY` |
| `AdvisoryUpdate` | none; opaque advisory bytes never directly explain or alter a candidate |

Two state-derived facts may add reasons independently of the trigger. Add `LATE_DEPARTURE` when the authoritative next scheduled suffix activity cannot be reached by its current-plan start from `effective_suffix_start_unix_ms` using the current-location matrix leg. Add `RESERVATION_AT_RISK` when replaying the authoritative scheduled suffix from that same effective start, waiting until each current-plan start when early and applying each matrix leg, reaches any remaining reservation after `reservation_start_unix_ms + reservation_grace_seconds * 1000`. Checked overflow is an input failure, not a reason. These facts are computed from the same immutable planning snapshot and matrix used by the attempt.

Search outcome then adds only its outcome reason: `BEST_SO_FAR`, `DEADLINE_EXCEEDED`, and `CANCELLED` add `DEADLINE_BUDGET`; `SEARCH_LIMITED` adds `DEADLINE_BUDGET`; `EXHAUSTIVE_INFEASIBLE` adds `NO_FEASIBLE_PLAN`; `COMPLETE` adds nothing; invalid input returns no planner reasons. Result-level reasons are the sorted union of intrinsic, state-derived, and outcome reasons. A proposal segment receives only the intrinsic plus state-derived reasons, and only when that segment actually differs from the authoritative baseline by scheduled/omitted state, interval, or common-scheduled relative order. Unchanged/preserved segments have an empty reason list. Outcome-only `DEADLINE_BUDGET` is never copied onto proposal segments.

Notification selection uses this exact precedence:

1. `EXHAUSTIVE_INFEASIBLE` -> `INFEASIBLE_SCHEDULE`.
2. `SEARCH_LIMITED`, `DEADLINE_EXCEEDED`, `CANCELLED`, or invalid input with no proposal -> `NONE`.
3. Otherwise, signed next-event slack `< 0` -> `CRITICAL_LATENESS`.
4. Otherwise, a present proposal that changes the authoritative plan -> `PLAN_CHANGE_SUGGESTED`.
5. Otherwise, known next-event slack `<= 20 minutes` -> `LOW_SLACK_WARNING`.
6. Otherwise, including unknown slack -> `NONE`.

Slack is `authoritative next scheduled start - earliest matrix-based arrival` in signed checked milliseconds from the same immutable snapshot. The notification boundary bands are therefore `< 0`, `[0, 20 minutes]`, and `> 20 minutes`. V1 has no separate `< 10 minute` raw-telemetry admission rule: slack is computed only after an explicit feasibility-changing trigger starts an attempt. Outcome rules take precedence so bounded search never presents a false infeasibility notification and exhaustive infeasibility always uses its contracted notification.

### V1 planner feasibility and deterministic objective

The fixed V1 objective identifier is `liveroute-v1-lexicographic-1`. V1 has no scalar objective weights and none of these criteria are runtime-tunable. `priority_rank` is a signed 32-bit user rank where a lower numeric value is more important; equal ranks are allowed. `utility_score` is a signed 32-bit value where a higher value is preferred. Candidate score arithmetic uses checked signed 64-bit integer counts or milliseconds only; it never uses floating point. The configured 64-activity/current-day V1 bounds make the worst-case accepted sums safe; startup must reject future limit changes that cannot prove the same, and any request conversion that would overflow is `INVALID_ARGUMENT` before search.

A candidate is feasible only when all of these hard constraints hold. `effective_suffix_start_unix_ms` is `max(current_time, scheduled_end)` over every scheduled preserved-prefix entry; this preserves an activity already in progress instead of allowing the revised suffix to overlap it. Matrix index zero is the current location projected at that effective suffix start. When no preserved scheduled entry ends after `current_time`, the effective suffix start is exactly `current_time`.

- the completed prefix and any started activity are unchanged;
- every remaining activity appears exactly once as scheduled or skipped;
- `mandatory = true` or `can_skip = false` forbids skipping;
- a currently scheduled remaining activity keeps its authoritative current-plan duration exactly when retained or moved; an omitted activity that is added back uses `max(1, preferred_duration_seconds)` seconds. The resulting positive duration must be within `[min_duration_seconds, max_duration_seconds]`. V1 never shortens a scheduled activity, regardless of `can_shorten`;
- `can_move = false` preserves the current plan's exact scheduled interval, or preserves omission when no interval exists;
- subject to the preceding `can_move` rule, a `FIXED` activity uses its exact current-plan interval when one exists. Without one, it can be scheduled only when `reservation_start_unix_ms` supplies an anchor; otherwise it remains omitted and is infeasible if it cannot be skipped;
- when a reservation exists, scheduled start is in the inclusive interval `[reservation_start_unix_ms, reservation_start_unix_ms + reservation_grace_seconds * 1000]`. Starting before the reservation is not permitted;
- any `mandatory_deadline_unix_ms` is an inclusive latest finish;
- the complete scheduled interval lies within one normalized operating-hours window; an empty window set makes the activity unschedulable, and a present `found_closed_at_unix_ms` makes it unschedulable for the current planning day;
- scheduled intervals do not overlap, every required current-location/activity and activity/activity travel-matrix cell is reachable, and arrival plus checked travel time does not exceed the next start;
- every scheduled interval lies inside the normalized current-day planning horizon.

Hours, reachability, overlap, mandatory/deadline, fixed/movement, minimum-duration, and reservation-window violations are never soft penalties. A candidate containing one is pruned. If exhaustive search proves no complete feasible candidate exists, `ReplanResult` is `status = INFEASIBLE`, `retryable = false`, proposal absent, `notification = INFEASIBLE_SCHEDULE`, reasons containing `NO_FEASIBLE_PLAN`, and `plan_quality = NO_NEW_PROPOSAL`. If the deadline/cancellation budget stops search before any complete feasible candidate is found, the result is `DEADLINE_EXCEEDED`/`CANCELLED` rather than a false infeasibility claim. If at least one complete candidate was found first, budget expiration returns the best one as `OK` + `BEST_SO_FAR`. The planner never emits a knowingly invalid proposal, and the user-authored current plan remains authoritative even when it violates these proposal constraints.

Complete feasible candidates are compared by the following tuple, in this exact order:

1. Minimize `skips_by_priority`: for each distinct `priority_rank` present in the remaining suffix, ordered from lowest rank value to highest, count skipped optional activities at that rank and compare the count vectors lexicographically. This means one additional skip at a more important rank is never exchanged for any number of less-important activities.
2. Maximize `scheduled_utility`, the checked signed 64-bit sum of `utility_score` for scheduled remaining activities. This selects among activities sharing the same skip-rank counts.
3. Minimize `total_lateness_ms`. Per scheduled activity, lateness is `max(0, start - reservation_start)` when a reservation exists, otherwise `max(0, start - current_plan_start)` when the activity is currently scheduled, otherwise zero.
4. Minimize `total_preferred_shortfall_ms`, the sum of `max(0, preferred_duration - scheduled_duration)`. Under V1's fixed-duration generation this cannot prefer a shortened alternative: every retained/moved activity keeps its baseline duration and every added-back activity uses its preferred duration.
5. Minimize `total_travel_ms`, including travel from the current location to the first scheduled suffix activity and between scheduled suffix activities.
6. Minimize `changed_activity_count`. The authoritative baseline suffix is the ordered `CurrentPlan.segments` suffix after the preserved prefix, including omitted entries. Build the baseline scheduled sequence and candidate scheduled sequence, then restrict both to activities scheduled in both plans. A remaining activity counts once when its scheduled/omitted state differs, its start or end differs while scheduled in both, or its zero-based position differs between those two restricted common-scheduled sequences. Multiple differences on one activity still count once. Adding or removing an activity therefore does not falsely mark every later unchanged activity as reordered.
7. Minimize `total_start_shift_ms`, the sum of absolute start-time differences for activities scheduled in both plans.
8. Minimize the final scheduled suffix end time.
9. Minimize the canonical plan key lexicographically: scheduled activities' original trip ordinals in proposed order, then each scheduled entry's `(ordinal, start_unix_ms, end_unix_ms)`, then skipped activities' original ordinals in ascending order.

All maximization/minimization comparisons above are exact; generated proposal ids, creation time, container iteration order, pointer values, thread scheduling, and random numbers never participate. If the entire tuple is equal, the candidates are user-visibly identical under V1 and either serialized result must use the same canonical segment order.

Beam retention is deterministic as well. At a common search depth, a partial candidate is compared using the same tuple projected optimistically: every undecided activity is assumed scheduled, its utility is included, and its future lateness, preferred shortfall, travel, and change costs are zero. Realized skips and costs remain in the tuple. Equal projected tuples are ordered by the canonical sequence of expansion decisions `(activity_ordinal, decision, start_unix_ms, end_unix_ms)`, where `decision` is `0` for scheduled and `1` for skipped and a skipped decision uses zero for both times. Beam truncation retains the first `beam_width` candidates under that total order. The best-so-far result is the best complete feasible candidate actually reached under the same complete-candidate comparator; partial or hard-infeasible candidates are never returned.

Before partial-candidate comparison and beam truncation, V1 applies one sound protected-activity lower-bound check. For each still-undecided activity that cannot legally be skipped (`mandatory = true` or `can_skip = false`), run the ordinary finite scheduled-alternative generator with `arrival_unix_ms` equal to the partial candidate's last scheduled end, or `current_time` when it has no scheduled decision. This deliberately assumes zero travel to that protected activity and skips all other undecided work. If even that optimistic invocation emits no scheduled alternative, the partial candidate is hard-infeasible and is removed before `max_candidates` accounting and beam retention. Passing the check does not prove that the activity is reachable or that the partial candidate has a completion; actual matrix travel remains mandatory when the activity is expanded. V1 does not use direct-route travel as this lower bound because the input may contain different inbound travel modes and does not promise a cross-mode triangle inequality.

V1 candidate generation is deliberately finite and boundary-derived. The beam searches activity order and optional omission; it is not a continuous-time optimizer and does not sample a minute grid. At each depth, parent partial candidates are visited in their deterministic partial-candidate order. Each parent considers every still-undecided activity in ascending `original_trip_ordinal`. A child decides that activity as one scheduled interval or, when `mandatory = false` and `can_skip = true`, as skipped.

For a scheduled child, `arrival_unix_ms` is the parent's last scheduled end plus the immutable matrix travel duration, or `effective_suffix_start_unix_ms` plus current-location travel for the first suffix activity. Unreachable or checked-overflowing travel produces no scheduled alternative. Candidate intervals are then generated by these exact rules:

1. If `can_move = false`, or `activity_class = FIXED` and the authoritative current plan contains an interval, the only scheduled alternative is that exact current-plan interval. It is emitted only if it passes every hard constraint. When `can_move = false` and the current plan omits the activity, there is no scheduled alternative. A `FIXED` activity without a current interval uses the ordinary boundary rule below only when it has a reservation anchor; otherwise it has no scheduled alternative.
2. For every normalized open window in ascending `(opens_at_unix_ms, closes_at_unix_ms)` order, form `earliest_start = max(arrival_unix_ms, opens_at_unix_ms, reservation_start_unix_ms when present)`. Emit it as a start boundary only when it is inside the inclusive reservation-start range when present, before the planning-horizon end, and leaves room for a positive legal duration ending at or before the window close, planning-horizon end, and mandatory deadline when present.
3. For a movable non-fixed activity that has a current-plan interval, also emit its current-plan start as a start boundary in the containing window when it is distinct from that window's `earliest_start`, is not before arrival, satisfies the reservation-start range, and leaves room for a positive legal duration. No other start instant is generated.
4. At each emitted start, use exactly one duration. A currently scheduled activity uses its authoritative current-plan duration `scheduled_end_unix_ms - scheduled_start_unix_ms`; moving it changes its placement, never its length. An omitted activity being added back uses `max(1, preferred_duration_seconds)` seconds. Emit the interval only when that entire fixed duration fits the containing window, planning horizon, mandatory deadline, and `[min_duration_seconds, max_duration_seconds]`. `can_shorten` does not add another duration.
5. Independently, a movable non-fixed activity's exact current-plan interval is included when it passes every hard constraint. This preserves an unchanged user interval as an alternative. Exact duplicate `(activity_ordinal, start_unix_ms, end_unix_ms)` alternatives produced by the window or baseline rules are removed.

Within one activity, scheduled alternatives are considered by ascending start time; at one start, the exact unchanged current-plan interval comes first. The skip child comes after every scheduled alternative. Thus each normalized window contributes at most two start boundaries with one fixed duration each, plus at most one exact baseline interval per activity and one skip. Actual travel-matrix duration from the current or previously scheduled location determines the earliest reachable start, so moving activities back never ignores distance between them. The complete and partial comparator prefer every no-skip candidate over a candidate that skips an activity; when skipping is necessary, `skips_by_priority` preserves lower numeric (more important) ranks before higher numeric (less important) ranks. Adding time-grid samples, variable durations, or another start boundary is a new planner-policy version, not an implementation detail.

`BeamSearchInput.remaining_activities` is ordered exactly as that authoritative current-plan suffix; it is not sorted by original trip ordinal. Its vector position is the baseline suffix-order representation, while `original_trip_ordinal` remains the stable trip-definition identity used for deterministic activity consideration and canonical keys. Travel-matrix index `i + 1` describes `remaining_activities[i]`. An input assembler must preserve this order, including omitted segments, rather than trying to reconstruct it from timestamps or trip-definition order.

The input assembler copies the first `completed_prefix_count` authoritative current-plan segments and the immediately following started segment, when present, into `preserved_prefix`. It copies every later current-plan segment into `remaining_activities` in exact current-plan order, looks up its `Activity` by id, and assigns that activity's position in `TripDefinition.activities` as `original_trip_ordinal`. It rejects inconsistent progress shape, missing/duplicate ids, invalid normalized horizons, or a matrix other than `(remaining_count + 1)²`; it never repairs order or derives progress from timestamps.

After one depth is generated, hard-infeasible children are absent, the remaining children are sorted by the normative partial comparator, and only the first `beam_width` continue. The planner records `search_was_truncated = true` whenever this retention step discards at least one hard-feasible partial candidate. One `expansion` is one actual invocation of the finite candidate generator for an ordered `(parent partial candidate, still-undecided activity)` pair. It increments `max_expansions` immediately before that invocation and may yield zero, one, or several locally hard-feasible scheduled/skip children. Hypothetical timestamps, durations, unreachable scheduled routes, and other alternatives rejected before emission are not enumerated and do not count as expansions. `max_candidates` is the cumulative number of emitted hard-feasible children admitted after protected-activity pruning and before beam truncation. Before the next parent/activity invocation, the planner checks cancellation, deadline, and `max_expansions`; an emitted child that would exceed `max_candidates` is not admitted and ends the attempt. Consequently, interruption or either budget limit selects a deterministic prefix of actual generator work before applying the same comparator and best-so-far rules.

A complete candidate reached after ordinary beam-width truncation is still `OK` + `COMPLETE`: `COMPLETE` means the retained search reached a complete valid proposal, not that every discarded branch was exhaustively compared. If no complete candidate is reached, `INFEASIBLE` is permitted only when `search_was_truncated = false`, neither candidate budget ended the attempt, and the generated finite search space was exhausted. If beam-width truncation, `max_expansions`, or `max_candidates` prevents that proof, return `status = OK`, `retryable = false`, proposal absent, `notification = NONE`, reasons containing `DEADLINE_BUDGET`, and `plan_quality = NO_NEW_PROPOSAL`. This bounded-search result is not a claim that the authoritative current plan or the full finite proposal space is infeasible. An actual wall-clock deadline or cancellation before any complete candidate remains `DEADLINE_EXCEEDED` or `CANCELLED`; if a complete candidate was reached first, any of those interruptions returns that candidate as `OK` + `BEST_SO_FAR`.

Changing criterion order, semantics, or tie-breaking is a user-visible planner-policy change: it requires a new objective identifier and new deterministic golden fixtures rather than a configuration-only deployment change.

`PlannerError`: 1 `StatusCode status`, 2 `bool retryable`, 3 `StaleReason stale_reason`, 4 `string safe_message`, 5 `uint64 related_mutation_sequence`, 6 `uint64 related_observation_sequence`, 7 `uint64 related_planner_state_version`.

### Snapshot payload

The `SnapshotBlob.payload` is the serialized bytes of `TripStateSnapshot` schema version 1:

`TripStateSnapshot`: 1 `TripDefinition trip`, 2 `uint64 trip_revision`, 3 `uint64 accepted_mutation_sequence`, 4 `uint64 finalized_mutation_sequence`, 5 `CurrentPlan current_plan`, 6 `uint32 snapshot_schema_version`.

It contains durable state only. It does not contain current location, velocity, heading, observation sequence, runtime epoch authority, pending work, stream bindings, cancellation objects, provider responses, or proposal history. PostgreSQL retains proposals separately; higher-epoch bootstrap marks pending old-epoch proposals stale rather than restoring them as active.

## Finalized Mutation Watermark and Snapshot Safety

A mutation is **accepted** when C++ applies it in memory. It is **finalized** when PostgreSQL commits its terminal result:

- for a runtime-first accepted command, the canonical trip/current-plan/proposal-decision mutation, new trip revision, command outcome, and outbox state commit together after C++ acceptance;
- for canonical-first `create_trip`, `trip_edited`, or `replace_current_plan`, the normalized user trip/current-plan state, new trip revision, command outcome, and finalized sequence commit before C++ mirror acceptance;
- for a terminal rejection, the rejected command outcome and consumed mutation sequence commit together without a trip-revision change.

The **covered finalized mutation sequence** is the highest contiguous mutation sequence whose terminal PostgreSQL outcome is committed and whose accepted effects, if any, are included in the snapshot. Rejected sequences are covered because their durable terminal outcome exists and they have no state effect to serialize.

Runtime-first protocol:

1. C++ acknowledges accepted/rejected mutation `N` and advances its accepted mutation watermark to `N`.
2. The backend commits the corresponding PostgreSQL finalization transaction.
3. The backend sends idempotent `ConfirmFinalizedMutations(N)`. It is a cumulative watermark, not one message per mutation requirement; repeats are safe.
4. C++ advances its finalized watermark only when `current <= N <= accepted_mutation_sequence`. A lower value is a duplicate; a larger value is `INVALID_ARGUMENT` and does not mutate state.
5. C++ returns `SNAPSHOT_NOT_READY` while accepted and finalized watermarks differ. It never serializes an unfinalized mutation.
6. Bootstrap carries PostgreSQL's finalized watermark, so a lost confirmation or either process restarting converges without depending on the old stream.

Canonical-first user-edit protocol:

1. The backend atomically commits mutation `N`, any normalized activity edit, the immutable current-plan revision/pointer, trip revision, applied command outcome, finalized watermark `N`, and the mirror outbox row.
2. It emits `canonical_committed`; the user plan is authoritative even while C++ remains at accepted mutation `N-1`.
3. The backend serializes later runtime-first dispatch behind the mirror row and delivers `CurrentPlanReplaced(N)` or canonical-first `TripEdited(N)` with the prior expected trip revision.
4. C++ applies the authoritative trip/current-plan state, advances its accepted mutation watermark to `N`, invalidates proposals/work based on the older plan, and acknowledges. Feasibility is not a rejection condition.
5. The backend marks mirror delivery accepted, sends `ConfirmFinalizedMutations(N)`, and emits `runtime_synced`. If a higher-epoch full bootstrap loads PostgreSQL state already finalized through `N`, that bootstrap sets the accepted/finalized watermarks to `N` and idempotently resolves the mirror row.
6. An unexpected normalized trip/current-plan incompatibility pauses runtime delivery as `INTERNAL`; it never rolls back PostgreSQL or changes the user-selected current plan.

`create_trip` commits mutation/finalized sequence 1 but has no active-runtime mirror. The first full bootstrap contains the initial `CurrentPlan` and initializes both C++ watermarks to 1.

C++ may compute speculatively after accepting a durable mutation, but a proposal carries `source_accepted_mutation_sequence` and `base_current_plan_id`. The backend keeps at most one latest unpublished proposal per trip and does not expose it over WebSocket until PostgreSQL's finalized watermark is at least that source sequence and a `plan_proposals` row containing the exact `StoredPlanProposal` bytes commits. If finalization or proposal persistence fails, clients never observe that suggestion and the authoritative current plan remains unchanged. Telemetry may continue in C++, but its resulting proposals obey the same publication fence. After finalization, the backend publishes only if the epoch/state version/base plan are still current; otherwise normal stale-result rules discard it.

Finalization confirmation is necessary because C++ cannot infer PostgreSQL commit order. Runtime-first work could otherwise enter a snapshot before its database transaction commits, while canonical-first work can place PostgreSQL ahead of the C++ mirror. Equality of accepted and finalized watermarks proves that the snapshot and canonical database cover the same contiguous command prefix before outbox pruning.

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
| `create_trip` | new trip | `default_time_zone_name`, `activities` as JSON-equivalent `Activity` array, and `current_plan` as `UserPlanDraft` |
| `subscribe_trip` | trip | optional `last_runtime_epoch`, `last_planner_state_version`, `last_trip_revision` canonical unsigned decimal strings |
| `unsubscribe_trip` | trip | empty object |
| `trip_command` | trip | `command_kind`, optional `command_expires_at_unix_ms`, and `command`; command kinds are `activity_status_changed`, `activity_delayed`, `trip_edited`, `reservation_changed`, `mandatory_deadline_changed`, `operating_hours_changed`, `place_found_closed`, `travel_delay`, `replace_current_plan`, `accept_proposal`, `reject_proposal` and use the JSON-equivalent fields defined here |
| `telemetry_update` | trip | `observation_kind` (`location`, `velocity`, `heading`, `route_deviation`), `observed_at_unix_ms`, and matching `observation` object |
| `resynchronize_trip` | trip | `last_runtime_epoch`, `last_planner_state_version`, `last_trip_revision` canonical unsigned decimal strings, and `outstanding_message_ids` unique UUID array bounded by configuration |
| `ping` | connection | `nonce` string 1-64 bytes and `sent_at_unix_ms` |
| `pong` | connection | `nonce` string 1-64 bytes and `received_at_unix_ms` |

`command_expires_at_unix_ms` is a logical product expiry on `trip_command`. Omission means the durable command does not expire merely because transport retries take time. For canonical-first `trip_edited`/`replace_current_plan`, expiry is checked before the PostgreSQL transaction begins; once committed, runtime mirroring never expires or reverses the user edit. `create_trip` has no expiry field and is attempted immediately.

Every ordinary runtime-first command (`activity_status_changed`, `activity_delayed`, `reservation_changed`, `mandatory_deadline_changed`, `operating_hours_changed`, `place_found_closed`, and `travel_delay`) contains required `expected_trip_revision` as a canonical unsigned decimal string inside `command`. The backend compares it with the authoritative trip revision before recording the mutation and rejects a mismatch as `STALE/TRIP_REVISION`. Proposal decisions instead use their complete proposal source tuple as their concurrency guard and do not add `expected_trip_revision`.

`create_trip` requires a client-generated top-level canonical UUID `trip_id` even though the row does not exist yet. The authenticated user becomes `owner_user_id`. `UserPlanDraft` contains 1 `plan_id` canonical UUID and 2 `segments` as JSON-equivalent `CurrentPlanSegment`; the backend supplies origin, plan revision, creation time, and any proposal source. The initial draft must reference every submitted activity exactly once.

For `replace_current_plan`, `command` is `expected_trip_revision` as a canonical unsigned decimal string plus `current_plan` as `UserPlanDraft`. For `trip_edited`, `command` is `expected_trip_revision`, one JSON-equivalent `TripEdited.operation`, and a complete post-edit `current_plan` `UserPlanDraft` referencing the resulting activity set. The backend rejects a revision mismatch as `STALE/TRIP_REVISION`, then assigns the next immutable plan revision and `USER_AUTHORED` origin. For `accept_proposal`/`reject_proposal`, `command` contains `proposal_id`, `source_runtime_epoch`, `source_planner_state_version`, and `base_current_plan_id`; all must match the stored pending proposal and active C++ proposal. A client that wants proposal contents despite a stale tuple must submit them as a new `replace_current_plan`, making the explicit user choice authoritative.

User-plan validation rejects malformed identifiers, a plan id already used by any non-idempotent command, unknown/duplicate/missing activities, invalid scheduled/omitted presence, nonfinite/out-of-range values, disallowed zones, `start >= end`, overlapping/reversed scheduled order, unsafe sizes, or stale expected revision. It does not reject a structurally valid user schedule merely because routing, hours, reservation, or deadline analysis finds it infeasible. The client does not send route legs, planner source versions, reasons, stats, or result quality in `UserPlanDraft`.

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

Trip-scoped messages additionally contain `trip_id` and canonical unsigned decimal strings `trip_revision`, `runtime_epoch`, `planner_state_version`, `accepted_mutation_sequence`, and `accepted_observation_sequence`. When no C++ runtime is active, the four runtime/accepted fields are `"0"`; `trip_revision` still reports PostgreSQL authority. A `canonical_committed` acknowledgement may therefore show a newer trip revision while runtime fields remain zero or behind, with `recovery_state = NOT_ADVANCING`, until `runtime_synced`.

Server kinds:

| Kind | Exact payload |
| --- | --- |
| `connection_ready` | `user_id`, `backend_instance_id`, `heartbeat_interval_ms`, `idle_timeout_ms`, `max_frame_bytes`, `max_outstanding_resync_ids` |
| `subscription_state` | `subscribed` boolean plus current durable trip, authoritative `CurrentPlan`, optional latest stored pending `StoredPlanProposal`, and runtime-sync state |
| `command_acknowledgement` | `phase` (`durable_recorded`, `planner_applied`, `canonical_committed`, `runtime_synced`, `rejected`, `expired`), `message_id`, optional `mutation_sequence`, optional `outcome`, and `recovery_state` |
| `telemetry_status` | `message_id`, `disposition` (`accepted`, `coalesced`, `dropped`, `rejected`), optional `observation_sequence` |
| `planner_notification` | `notification`, `reasons`, and result-quality fields |
| `plan_proposal` | JSON-equivalent `StoredPlanProposal`; it is advisory and never changes the authoritative current plan |
| `resynchronization_state` | current durable trip, authoritative `CurrentPlan`, optional latest stored pending `StoredPlanProposal`, runtime-sync state, and one outcome for every requested outstanding `message_id` |
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
- `current_plan_id uuid not null`
- `created_at`, `updated_at timestamptz not null`

`create_trip` explicitly inserts revision/finalized mutation sequence 1 and next mutation sequence 2 rather than relying on the zero/one column defaults used while constructing the transaction. After `itinerary_plans` exists, add a deferrable initially deferred composite foreign key `(id, current_plan_id)` to `itinerary_plans(trip_id, id)`. Every committed trip therefore has exactly one authoritative current-plan pointer.

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

`itinerary_plans`:

- `id uuid primary key`
- `trip_id uuid not null references trips(id) on delete cascade`
- `plan_revision bigint not null check > 0`
- `origin text not null check in ('user_authored','accepted_engine_proposal')`
- `authored_by_user_id uuid not null references users(id)`
- `source_proposal_id uuid null`
- `schema_version integer not null check = 1`
- `payload bytea not null`
- `payload_size_bytes integer not null check >= 0`
- `checksum_sha256 bytea not null`, exactly 32 bytes
- `created_at timestamptz not null`
- unique `(trip_id, id)` and `(trip_id, plan_revision)`
- check that `source_proposal_id` is null exactly for `user_authored` and non-null exactly for `accepted_engine_proposal`

The payload is exact serialized `CurrentPlan` bytes and must agree with the row id/revision/origin/source/creation metadata. The backend captures one PostgreSQL transaction timestamp truncated to milliseconds, uses its Unix-millisecond value in the payload, and stores the same instant in `created_at`. Plan rows are immutable and retained until their trip is deleted. An accepted engine proposal creates a new plan row; it does not repurpose or overwrite the proposal payload.

`plan_proposals`:

- `id uuid primary key`
- `trip_id uuid not null references trips(id) on delete cascade`
- `base_current_plan_id uuid not null`
- `source_runtime_epoch bigint not null check > 0`
- `source_planner_state_version bigint not null check >= 0`
- `source_trip_revision bigint not null check >= 1`
- `source_accepted_mutation_sequence bigint not null check >= 1`
- `schema_version integer not null check = 1`
- `payload bytea not null`
- `payload_size_bytes integer not null check >= 0`
- `checksum_sha256 bytea not null`, exactly 32 bytes
- `state text not null check in ('pending','accepted','rejected','stale','superseded')`
- `decision_message_id uuid null`
- `resulting_current_plan_id uuid null`
- `created_at timestamptz not null`
- `decided_at timestamptz null`
- unique `(trip_id, id)`
- check that `state = 'pending'` exactly when `decided_at is null`
- check that `resulting_current_plan_id` is non-null exactly when `state = 'accepted'`

The payload is exact serialized `StoredPlanProposal` bytes and must agree with the source columns; `created_at` is the exact millisecond instant in `PlanProposal.created_at_unix_ms`. Add composite foreign keys `(trip_id, base_current_plan_id)` and `(trip_id, resulting_current_plan_id)` to `itinerary_plans(trip_id, id)`, with the resulting-plan foreign key allowing null. After both tables exist, add a deferrable initially deferred composite foreign key `(trip_id, source_proposal_id)` from `itinerary_plans` to `plan_proposals`; it permits the accepted proposal and derived plan to be updated atomically. A partial unique index on `plan_proposals(trip_id) where state = 'pending'` permits at most one actionable proposal per trip. Proposal rows and payloads are retained until trip deletion.

`command_intents`:

- `id uuid primary key`
- `trip_id uuid not null references trips(id) on delete cascade`
- `message_id uuid not null`
- `event_id uuid not null`
- `mutation_sequence bigint not null check > 0`
- `expected_trip_revision bigint not null check >= 0`
- `command_kind text not null` checked against `create_trip`, `activity_status_changed`, `activity_delayed`, `trip_edited`, `reservation_changed`, `mandatory_deadline_changed`, `operating_hours_changed`, `place_found_closed`, `travel_delay`, `replace_current_plan`, `accept_proposal`, `reject_proposal`
- `application_order text not null check in ('canonical_first','runtime_first')`; only `create_trip`, `trip_edited`, and `replace_current_plan` use `canonical_first`
- `command_expires_at timestamptz null`
- `digest_algorithm text not null check = 'rfc8785-sha256-v1'`
- `payload_digest bytea not null`, exactly 32 bytes
- `command_payload jsonb not null`
- `state text not null check in ('pending','applied','rejected','expired')`
- `outcome_status text null`, checked against the stable status strings when non-null
- `outcome_payload jsonb null`
- `resulting_trip_revision bigint null`
- `resulting_current_plan_id uuid null`; required exactly for `canonical_first` intents and references the immutable plan created by that command
- `resulting_planner_state_version bigint null`
- `planned_current_plan_id uuid null`; required only for `accept_proposal`, generated during runtime-first recording and copied into every retry
- `planned_current_plan_payload bytea null` and `planned_current_plan_checksum_sha256 bytea null`; both required only for `accept_proposal`, with checksum exactly 32 bytes and payload decoding to the planned id/revision/origin/source metadata
- `runtime_sync_state text not null check in ('not_required','pending','synced','paused_internal')`
- `recorded_at timestamptz not null`
- `finalized_at timestamptz null`
- unique `(trip_id, message_id)`, `(trip_id, event_id)`, and `(trip_id, mutation_sequence)`

Add a composite foreign key from `(trip_id, resulting_current_plan_id)` to `itinerary_plans(trip_id, id)`. `resulting_current_plan_id` is non-null exactly when `application_order = 'canonical_first'`; runtime-first proposal acceptance continues to use the separate pre-dispatch `planned_current_plan_id`. Mirror acknowledgement and replay correlation read this typed intent column and never infer identity from the live `trips.current_plan_id` or parse an undocumented path from `planner_outbox.event_payload`. Forward migration 3 backfills pre-existing canonical intents only by the unique `(trip_id, plan_revision = resulting_trip_revision)` plan row and fails rather than guessing if canonical history is inconsistent.

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

For `event_schema_version = 1`, `event_payload` is exactly a JSON object with
the two members `format` and `protobuf_base64`. `format` is the literal
`liveroute.v1.ApplyTripEvent/protobuf;version=1`; `protobuf_base64` is padded
RFC 4648 standard Base64 of deterministic Protobuf serialization of exactly
one `liveroute.v1.ApplyTripEvent`. Writers emit the members in that order.
Readers reject missing, duplicate, or unknown members, non-canonical Base64,
non-deterministic Protobuf encodings, an empty `event_id`, a non-positive
`occurred_at_unix_ms`, or an unset event oneof. The stored event contains the
stable event id, occurrence time, optional logical command expiry, and typed
event body only. The dispatcher supplies `request_id`, `trip_id`, current
lease epoch, mutation/observation sequences, expected versions, and attempt
expiry in the outer `PlannerStreamRequest`; those transport-authority values
are never persisted in `event_payload`.

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

Required indexes cover owner-to-trip lookup, immutable plan history `(trip_id, plan_revision desc)`, proposal history `(trip_id, created_at desc)`, the one-pending-proposal partial uniqueness rule, pending outbox `(next_attempt_at, trip_id)`, command outcome `(trip_id, message_id)`, latest valid snapshots `(trip_id, covered_finalized_mutation_sequence desc)`, and expiring leases.

### Transaction isolation and lock order

- Use PostgreSQL `READ COMMITTED` with explicit row locks; no transaction performs network I/O.
- Every per-trip mutation transaction locks `trips` first with `SELECT ... FOR UPDATE`, then existing `command_intents`, `planner_outbox`, `plan_proposals`, and `itinerary_plans` rows in that order and by UUID within a table. Proposal-persistence transactions lock the trip then proposals. Snapshot transactions lock the trip before snapshot/outbox rows. This fixed order prevents application-level deadlocks.
- Serialization/deadlock errors are retried by the backend with bounded database-attempt backoff; the transaction body is idempotent.

### Runtime-first recording transaction

1. Lock the trip and verify ownership/current revision.
2. Look up `(trip_id, message_id)` and compare algorithm/digest.
3. Reject mismatched reuse; return matching stored outcome without allocating another sequence.
4. Require no unresolved runtime-first command or canonical-first mirror row for the trip.
5. Allocate `next_mutation_sequence`, then increment the stored next value.
6. For `accept_proposal`, deterministically convert the stored proposal into a complete `CurrentPlan`, generate its id once, assign the next plan revision and one database timestamp, and store its exact payload/checksum/id in the intent plus field 6 of the outbox `PlanDecisionEvent`. Insert the `runtime_first` pending `command_intents` row and lease-neutral `planner_outbox` row.
7. Commit, then emit `durable_recorded`.

### Canonical-first user-edit transactions

`create_trip` is one transaction:

1. Require authentication; use the top-level client `trip_id` and the authenticated user as owner.
2. If the trip exists, authorize without revealing another owner's data and compare the existing creation intent algorithm/digest. Return its stored result on an exact match; otherwise return `INVALID_ARGUMENT` without mutating either trip.
3. Validate the complete trip/activity/current-plan draft and normalize payload bytes before writes.
4. Insert the trip at `trip_revision = 1`, `next_mutation_sequence = 2`, `finalized_mutation_sequence = 1`, plus normalized activities/windows/delays and immutable `USER_AUTHORED` `CurrentPlan` revision 1. The deferred foreign key permits the trip and current plan to be inserted together.
5. Insert an applied `canonical_first` command intent at mutation sequence 1 with expected trip revision 0, resulting revision 1, `resulting_current_plan_id` equal to the initial plan id, and `runtime_sync_state = 'not_required'`. No planner outbox exists because a new trip has no active C++ state.
6. Commit, then emit `canonical_committed`. A later activation uses a full bootstrap that initializes both C++ watermarks to 1.

`replace_current_plan` and `trip_edited` use the same transaction shape:

1. Lock the trip; verify owner, expected trip revision, idempotency digest, no unresolved runtime-first command, and available bounded canonical-mirror capacity. Earlier pending canonical-first mirrors are allowed.
2. For `trip_edited`, validate and apply the normalized activity operation in the transaction's post-edit model. Validate its complete `UserPlanDraft`; for `replace_current_plan`, validate the draft against the unchanged activity set. Assign the next plan revision under the trip lock and serialize an exact `USER_AUTHORED` `CurrentPlan`.
3. Allocate mutation sequence `N`; apply any activity edit, insert the immutable plan, update `trips.current_plan_id`, trip revision, next mutation sequence, and finalized mutation watermark, and mark any pending proposal `superseded`.
4. Insert an applied `canonical_first` command intent containing the exact `resulting_current_plan_id` and a pending lease-neutral `TripEdited(N)` or `CurrentPlanReplaced(N)` outbox row with the prior expected trip revision. The mirror row is inserted even when the trip is inactive so a snapshot-based future activation cannot miss the canonical-first change.
5. Set the intent `runtime_sync_state = 'pending'`, commit, then emit `canonical_committed`. Product success does not wait for C++.

Mirror rows never expire logically and dispatch in mutation-sequence order. On C++ acknowledgement, lock trip/intent/outbox, verify the response-envelope resulting revision, field 10 current-plan id against the intent's immutable `resulting_current_plan_id`, and the resolved sequence, mark the row accepted and the intent runtime sync `synced`, commit, then send `ConfirmFinalizedMutations(N)` and emit `runtime_synced`. Never compare an individual mirror acknowledgement with the live `trips.current_plan_id`, because later canonical edits may already have advanced it. A full canonical bootstrap through `N` may resolve every pending canonical-first mirror row with sequence `<= N` identically after verifying the bootstrapped trip revision/current-plan id. Unexpected normalized-data rejection sets the affected outbox `paused_internal` and intent runtime sync `paused_internal`; it preserves PostgreSQL state and requires repair/rebootstrap.

### Proposal persistence transaction

Before publishing a C++ result, lock the trip and verify its runtime epoch, source planner version, source trip revision, source accepted mutation sequence at or below PostgreSQL finalization, and `base_current_plan_id` against current state. Serialize exact `StoredPlanProposal` bytes. If `(trip_id, proposal_id)` already exists, an exact source/schema/size/checksum/payload match returns that stored proposal idempotently; any difference is `INTERNAL` and never overwrites history. Otherwise mark any older pending proposal `superseded`, insert the new pending row, and commit without changing trip revision or mutation sequence. Only then emit `plan_proposal`. A source mismatch discards the result as stale. A database failure leaves at most one bounded unpublished result for retry and never changes the current plan.

On higher-epoch acquisition, mark pending proposals from older epochs `stale` while retaining their payload/history. They are not bootstrapped as active proposals.

### Lease transactions

- First acquisition inserts epoch 1 using PostgreSQL `clock_timestamp()`.
- Renewal succeeds only for the same holder id and epoch before `lease_expires_at`; it extends expiry without changing epoch.
- A different holder, including a restarted backend with a new instance id, acquires only after PostgreSQL says the old lease expired. It locks the lease row, increments the prior epoch by exactly one, and sets the new holder/expiry atomically.
- A missing renewal response, transaction ambiguity, or local time disagreement is treated as loss of authority. Dispatch stops before the safety margin; local clocks never extend a lease.
- The single-host Compose supervisor ensures only one configured backend replica, but the database lease rule remains the correctness boundary.

### Runtime-first finalization transaction

For C++ acceptance:

1. Lock trip, intent, and outbox; verify the correlated event, expected revision, and sequence.
2. Apply the normalized canonical trip mutation or proposal decision. Fresh proposal acceptance verifies the pre-recorded planned-current-plan checksum, metadata, and deterministic mapping from the still-pending stored proposal, inserts those exact bytes as the immutable `ACCEPTED_ENGINE_PROPOSAL` current-plan revision, updates `trips.current_plan_id`, and marks the proposal accepted in the same transaction. C++ installs the exact `CurrentPlan` carried in the accepted event after performing the same mapping validation. Proposal rejection only marks the proposal rejected.
3. Increment trip revision by one.
4. Set trip finalized mutation watermark to the command sequence.
5. Mark intent `applied`, store outcome/versions, and mark outbox `accepted`.
6. Commit; then emit `planner_applied` and send `ConfirmFinalizedMutations`.

For terminal C++ rejection or logical expiry, do not change canonical trip/current-plan data or trip revision; mark intent `rejected`/`expired`, advance the finalized mutation watermark, mark the outbox terminally rejected, commit, then report the terminal outcome and confirm the finalized watermark. A stale proposal decision also marks its still-pending proposal `stale` without making it current.

If finalization fails, the trip admits no later durable command. Replay obtains C++'s duplicate outcome and retries the same finalization transaction.

### Outbox claiming and retry

- Claim due rows in bounded batches with `FOR UPDATE SKIP LOCKED`.
- A claim has a short PostgreSQL-time lease in `claim_expires_at`; process failure makes it reclaimable.
- Each dispatch reads the currently valid trip lease and generates a new `request_id` and attempt expiry.
- Full-jitter capped exponential scheduling uses `random(0, min(30 seconds, 250 milliseconds * 2^min(attempt_count, 7)))` after transient failures.
- Durable rows do not stop after a fixed attempt count. A maximum count would silently abandon the durability guarantee. Runtime-first retries stop only on applied/terminal outcome, trip deletion, or `paused_internal`. A canonical-first mirror stops only after C++/bootstrap convergence, trip deletion, or `paused_internal`; it cannot be terminally rejected as a user-plan outcome. Explicit administrative repair moves a paused row back to pending or resolves it through an audited full bootstrap.
- Attempt thresholds raise observable warnings, but do not delete or dead-letter the command automatically.

Stream reconnection separately uses full-jitter backoff from 100 milliseconds capped at 10 seconds. Successful connection resets the stream backoff.

### Durable command and dispatch deadlines

- `command_expires_at_unix_ms` is optional durable product semantics and is stored in the intent. Omitted means no retry-count/time-based logical expiry.
- `expires_at_unix_ms` in the gRPC envelope is per dispatch attempt and is never used as durable identity.
- On each attempt the backend sets transport expiry to `now + configured planner_attempt_timeout`. Logical command expiry is carried separately in `ApplyTripEvent` and never shortens the transport attempt in a way that could prevent the sequence from being resolved.
- `DEADLINE_EXCEEDED` or transport cancellation leaves durable work pending and schedules another attempt while logical time remains.
- Once logical expiry passes before C++ acceptance, the backend still dispatches the event with a fresh attempt deadline; C++ resolves and consumes the sequence as terminal `COMMAND_EXPIRED`. Transport failure before that acknowledgement leaves it pending and retryable.
- Already accepted C++ work is finalized even if the client disconnects or the logical deadline passes after acceptance.
- Canonical-first `trip_edited`/`replace_current_plan` expiry is evaluated before its PostgreSQL transaction. After `canonical_committed`, the C++ mirror event carries no logical expiry and retries until synchronized; time cannot undo the user-selected trip/current-plan edit. `create_trip` is immediate and has no logical expiry.

### Snapshot validation, retention, and pruning

- Snapshot schema version 1 is the only V1 compatible version.
- Checksum is SHA-256 over the exact payload bytes; size must equal the declared size and remain under the configured maximum.
- Metadata must not be older than the stored valid snapshot, must not exceed the trip's finalized mutation watermark, and must match the decoded `TripStateSnapshot`.
- Snapshot recency is ordered by `(source_runtime_epoch, source_planner_state_version, covered_finalized_mutation_sequence, trip_revision)`, newest first. A higher runtime epoch is newer even though its planner-state version resets; across an epoch change, trip revision and the covered finalized watermark must not regress. `created_at` and snapshot id are deterministic descending tie-breakers only and never override version recency.
- Unknown schema versions and checksum/parse/metadata failures mark the candidate invalid; they never replace a valid snapshot.
- The backend retains the two newest non-invalid compatible snapshots per trip. It may delete older compatible snapshots only in the same transaction that commits a new valid snapshot. Invalid rows retain metadata but may have payload purged by a later maintenance policy; V1 does not need such maintenance.
- Snapshot commit and deletion of terminal outbox rows with `mutation_sequence <= covered_finalized_mutation_sequence` occur atomically.
- `command_intents` are never pruned before trip deletion.
- Recovery tries the newest compatible snapshot, then the previous compatible snapshot, then rebuilds from the fully normalized canonical trip/activity/window/travel-delay/current-plan data at its finalized watermark and replays only newer uncovered outbox work. A canonical-first mirror row newer than the snapshot must be replayed or resolved by choosing a full canonical bootstrap through its sequence. Proposal history is read separately from PostgreSQL and is never used as snapshot authority. Recovery never starts from a known-corrupt snapshot.

## GPS/Telemetry Coalescing and Boundary Detection

The backend performs schema/auth/admission checks, assigns observation sequences, and performs transport-only latest-value coalescing. It does not infer a route deviation, geofence crossing, deadline-risk transition, or another domain boundary from raw coordinates.

On a healthy stream, the backend sends admitted observations in sequence and bounds its queue. When disconnected or overloaded it retains only the newest observation per type/trip and explicitly marks replaced samples `COALESCED` or `DROPPED`; old telemetry is never replayed.

In V1, ordinary `LocationUpdated`, `VelocityUpdated`, and `HeadingUpdated` events are state-only observations:

1. the C++ owner shard applies every admitted non-stale observation and advances the observation and planner-state versions;
2. the change invalidates stale proposals and in-flight planning generations;
3. when no explicit feasibility-changing replan is running or pending, the acknowledgement has `replan_scheduled = false` and no route-provider or planner work starts;
4. when an explicit high/critical replan is already running or pending, the newest ordinary observation replaces its planning snapshot, requests cancellation of the obsolete attempt, and schedules at most one replacement while retaining the highest-priority explicit trigger;
5. a stale observation is acknowledged as `STALE/OBSERVATION_SEQUENCE` and starts no work.

Feasibility-driven replanning starts only from an explicit feasibility-changing domain event, including `RouteDeviationDetected`, activity lifecycle, reservation/deadline, operating-hours, place-closed, or travel-delay changes. `RECOMMENDATION_REFRESH` is the sole non-feasibility exception: while idle, it may explicitly rerun the unchanged current snapshot; while any attempt is running or pending, it is acknowledged as redundant and does not cancel, fence, or enqueue a second attempt. A boundary that must never be lost is therefore represented explicitly and is never inferred from an intermediate GPS sample. Current-route geometry, configurable geofences, coordinate-derived boundary detection, and an ETA/progress-only response are not V1 contracts; adding any of them requires an additive state/wire contract and a new planner-policy version. Signed slack remains a result/notification fact computed from an explicitly triggered attempt, not a raw-GPS admission trigger.

## United States Time Normalization

V1 accepts only IANA time-zone identifiers whose pinned tzdata `zone1970.tab` entry includes country code `US`. This covers United States DST/non-DST exceptions without maintaining a hand-written offset table.

The normative data lock is `config/tzdata.lock`: IANA release `2026c`, artifact `tzdata2026c.tar.gz`, SHA-512 `e0b4b7044b66fbc27bc21d13d18063abcdf78ab58d5ba5fd64bd1a88d86e9d495f45add4d8e65bb6c40249f9c94ca29b72c8ebba8d0e4c468f2965ac77932ef0`. Images and fixture tools must install or compile this artifact after checking the digest; an operating-system package whose embedded release cannot be proven equal is not accepted. The release string is exposed in readiness/build metadata.

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

### Operating-hours provider boundary

The authoritative V1 serving input is the user's normalized activity
`open_windows`, persisted by the backend and mirrored to C++. Replanning never
performs a place-hours lookup. The provider boundary below is an optional local
import/normalization adapter for seed data; it may produce windows before a user
plan is persisted, but its source metadata is not authoritative planner input.

The internal transport-independent types are:

- `LocalDate`: a valid proleptic-Gregorian date formatted externally as exactly `YYYY-MM-DD`, from `1970-01-01` through `9999-12-31`.
- `LocalDateRange`: `start_date_inclusive` and `end_date_exclusive`; start must precede end and the range contains at most 32 local dates.
- `HoursInfo`: `PlaceId place_id`, canonical US `time_zone_name`, exact `LocalDateRange covered_range`, sorted/nonoverlapping UTC `TimeWindow open_windows`, `string source_version`, and `string tzdata_release`.
- `HoursProviderError`: `NOT_FOUND`, `INVALID_SOURCE`, `DEADLINE_EXCEEDED`, `CANCELLED`, or `UNAVAILABLE`.
- `HoursLookupResult`: a tagged union containing exactly one `HoursInfo` or `HoursProviderError`.

`PlaceHoursProvider::get_hours(PlaceId, LocalDateRange, Deadline, std::stop_token)` returns `HoursLookupResult`. It performs no planner search and never exposes local civil-time or provider payload types to the planner. The successful `open_windows` are the union of intervals whose local opening date is in the requested half-open range. They are converted with the place's zone, sorted, and coalesced when they overlap or touch; each returned window has `opens_at_unix_ms < closes_at_unix_ms`.

When enabled, the V1 seeded provider loads `schema/hours/liveroute-v1-hours-seed.schema.json` once at startup. Additional semantic validation is mandatory for that importer:

- top-level `tzdata_release` exactly equals `config/tzdata.lock`;
- `places` are strictly sorted by `place_id` and contain no duplicate place id;
- each zone is allowed by the pinned US-zone rule, and the activity zone must equal the seed place zone;
- weekday and exception intervals are in ascending local-open order and do not overlap; exception dates are strictly increasing and unique;
- local times are exact `HH:MM:SS`; `closes_day_offset` is 0 or 1, with offset 0 requiring close later than open and offset 1 limiting the interval to at most 24 hours;
- an exception replaces the recurring weekday completely for its local date; an empty exception interval list means closed;
- recurring intervals carry no UTC-offset choice. If a recurring endpoint is nonexistent or ambiguous on any date in the readiness-validation domain, its local opening date requires an exception rather than a guessed conversion;
- a nonexistent exception endpoint is invalid. An ambiguous exception endpoint requires the matching `opens_utc_offset_seconds` or `closes_utc_offset_seconds`; an optional offset on an unambiguous endpoint must equal its sole valid offset.

Seeded-hours DST validation is eager over an exact, non-clock-relative domain. It covers every recurring interval whose local opening date can occur in a valid `LocalDateRange`: `[1970-01-01, 9999-12-31)`, through `9999-12-30` inclusive, plus any next-local-day closing endpoint produced by `closes_day_offset = 1`. Every explicit exception is validated regardless of its date. There is no configurable rolling horizon and no first-request/lazy semantic validation.

This validation must be transition-directed rather than implemented as day-by-day expansion. At startup, for each distinct seed zone, the provider enumerates every UTC-offset transition from pinned TZif data whose skipped or repeated local-time interval can intersect the validation domain. This includes explicit TZif transitions and recurrences derived from its POSIX footer through the upper bound. It then compares those finite gap/fold intervals with that zone's distinct recurring opening and closing endpoint tuples. A next-day close remains associated with its interval's local opening date; an exception for that opening date replaces the recurring interval before endpoint validation. The transition enumeration is cached once per distinct zone and reused across places. An equivalent implementation is allowed only if it proves the same complete-domain result before readiness; an implementation must not trade correctness for an arbitrary startup timeout.

Any schema or semantic error, including one found by the complete-domain transition scan, prevents seeded-hours readiness; it is not treated as a closed place. Once the provider reports ready, every structurally valid request range is guaranteed not to discover a new seed DST gap/fold error. A defensive post-readiness detection of source corruption or a violated validation invariant returns `INVALID_SOURCE`, returns no partial windows, and immediately marks seeded-hours and the base planner service not ready until restart with valid inputs. A missing requested `place_id` returns `NOT_FOUND`. Cancellation/deadline errors preserve their names; a later remote-provider failure maps to `UNAVAILABLE`.

For a seeded success, `source_version` is `seed-v1:` followed by lowercase SHA-256 hex over the ASCII bytes `liveroute-hours-seed-v1`, one LF byte, the UTF-8 `tzdata_release`, one LF byte, and the RFC-8785 canonical bytes of the complete place object, in that order.

This version changes when either the place rules or tzdata release changes and is suitable for bounded hours-cache keys.

## Authentication Artifacts

- A V1 development bearer token is exactly 32 random bytes encoded as unpadded base64url: 43 characters.
- Raw tokens enter the backend only through the Docker secret file `/run/secrets/liveroute_dev_token`; they never appear in Compose YAML, URLs, database rows, logs, metrics, or error text.
- A one-shot `liveroute-admin seed-dev` container runs after migrations, reads the secret plus the nonsecret configured development user UUID/display name/time zone, and transactionally upserts the user and SHA-256 token digest. It emits identifiers only, never the raw token or digest.
- Authentication performs fixed-length validation, SHA-256, indexed digest lookup, expiry/revocation checks, and constant-time digest comparison.
- The default local token has no automatic expiry but may be revoked/replaced; non-loopback deployment is not allowed to use this development-token mode without TLS.
- The first non-ping message must be `authenticate`. Authentication must complete before the configured timeout.
- Authorization checks the authenticated user against `trips.owner_user_id` for every existing-trip subscribe, resync, command, and telemetry message, including messages on an already-authorized connection. `create_trip` instead assigns the authenticated user as owner atomically and reveals no existing trip owned by someone else.
- Origin is matched exactly against a configured allowlist. Origin omission is allowed only when `allow_originless_local_clients=true` and the backend bind address is loopback.

## Configuration and Readiness

Exact numeric queue/worker tuning is not a pre-source product decision. Implementation may begin with required typed configuration keys, but a finite `config/local-v1.yaml` must exist before the first runnable Compose acceptance test. It contains no secrets and every capacity is positive and bounded. Production-universal defaults are not claimed.

Hard protocol limits that affect schemas are fixed for V1:

- WebSocket frame: 256 KiB;
- decoded WebSocket message: 256 KiB;
- gRPC message: 4 MiB;
- snapshot payload: 2 MiB;
- resynchronization outstanding command IDs: 128.

Other initial values—shards, workers, queue capacities including unresolved canonical mirrors per trip, database pool, OSRM concurrency, lease duration, heartbeat, attempt timeout, and shutdown deadline—are selected in the local config during the skeleton milestone and then tuned from tests. Startup rejects missing, zero, unbounded, overflow-prone, or mutually inconsistent values.

Readiness means dependency usability rather than mere process existence:

- PostgreSQL liveness: container process; readiness: `pg_isready` plus expected Goose migration version.
- OSRM car/foot readiness: fixed small Table request succeeds with expected dimensions.
- C++ liveness/readiness: standard gRPC health service reports `liveroute.v1.LiveRoutePlanner` as `SERVING` after queues/executors and required serving configuration initialize. If the optional seeded-hours importer is enabled, it must also complete its full-domain semantic/DST validation before the base service becomes `SERVING`; a defensive post-readiness seeded-source invariant failure changes the base service to `NOT_SERVING`. Manual-hours serving does not require or initialize that importer. The service separately reports `liveroute.v1.LiveRoutePlanner/osrm-car` and `/osrm-foot` as `SERVING` only after their fixed Table probes pass.
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

## Exact OSRM Result Mapping

The adapter validates and maps in this order. It does not retry within the live request, and it never passes OSRM text or JSON through a public error.

| Condition | LiveRoute result | Retryable | Required behavior |
| --- | --- | --- | --- |
| Non-finite/out-of-range coordinate, unsupported mode, or invalid source/destination index before I/O | `INVALID_ARGUMENT` | false | Do not call OSRM. |
| Location count, matrix cells, or encoded request exceeds a configured V1 limit | `MATRIX_TOO_LARGE` | false | Do not call OSRM; report the configured limit safely. |
| Provider admission/concurrency queue is full | `RESOURCE_EXHAUSTED` | true | Do not mutate state or start HTTP work. |
| Caller/supersession stop requested | `CANCELLED` | false for replaceable work | Cancel libcurl work cooperatively. Durable event delivery remains governed by its separate acknowledgement. |
| Request deadline expires | `DEADLINE_EXCEEDED` | context-specific | Cancel the attempt; do not perform an in-request retry. |
| DNS/connect/TLS/reset failure or HTTP 5xx | `PROVIDER_UNAVAILABLE` | true | Mark the profile probe unhealthy after the configured threshold. |
| HTTP 429 | `RESOURCE_EXHAUSTED` | true | Do not retry inside the request. |
| Complete recognized OSRM error body | mapping below | mapping below | The recognized OSRM code takes precedence over a generic HTTP 4xx mapping. |
| Other HTTP 4xx, including a missing fixed endpoint | `INTERNAL` | false | Treat as adapter/configuration incompatibility and fail the profile readiness probe. |
| Decompressed response exceeds the byte limit | `RESOURCE_EXHAUSTED` | false for the same request | Abort parsing and record only bounded metadata. |
| Malformed JSON, wrong/missing dimensions, mismatched null duration/distance cells, or nonfinite/negative/overflow values | `PROVIDER_UNAVAILABLE` | true | Discard the entire response; never publish a partial matrix. |
| Unknown OSRM `code` or `NotImplemented` | `INTERNAL` | false | Pinned provider/API mismatch; fail readiness. |

Recognized OSRM codes map exactly:

| OSRM `code` | LiveRoute result | Retryable | Meaning |
| --- | --- | --- | --- |
| `Ok` | successful `TravelTimeMatrix` | false | Require exact dimensions. Matching null duration/distance cells become `reachable = false`; numeric duration and distance are rounded upward to whole seconds/meters before checked `uint32` conversion. |
| `NoTable` | successful all-unreachable `TravelTimeMatrix` | false | The provider answered but no route exists. Diagonal cells are zero/reachable; all other requested cells are unreachable. Planner feasibility decides the resulting proposal status. |
| `NoSegment` | `PROVIDER_UNAVAILABLE` | false for unchanged coordinates/dataset | At least one coordinate cannot be snapped to the pinned dataset/profile. |
| `TooBig` | `MATRIX_TOO_LARGE` | false | Same public result as the LiveRoute preflight limit. If preflight admitted it, also mark the profile unready because configured limits disagree. |
| `InvalidUrl`, `InvalidService`, `InvalidVersion`, `InvalidOptions`, `InvalidQuery`, or `InvalidValue` | `INTERNAL` | false | The validated adapter generated an invalid request for the pinned endpoint. |

OSRM `fallback_speed` is forbidden in V1, so unreachable cells are never fabricated. The Phase 17 route cache may replace only the completely covered `PROVIDER_UNAVAILABLE` case admitted by `liveroute-route-cache-v1` below and must return `OK` with `routing_quality = STALE_CACHE`.

## Benchmark Artifact and Aggregation Contract

Phase 16 benchmark output is a machine-readable artifact, not parsed console
text. Each measured process invocation writes one UTF-8 JSON document validated
by `schema/benchmark/liveroute-benchmark-v1.schema.json`. JSON numbers are finite
integers unless a field explicitly says otherwise; durations are unsigned integer
microseconds. Human-readable tables are derived output and are never aggregation
input.

The root object has exactly these required members and the optional
`attachments` member defined below:

- `schema_version`: fixed string `liveroute.benchmark.v1`;
- `run_id`: UUID generated for this invocation;
- `started_at`: UTC RFC 3339 timestamp;
- `benchmark_name`: stable executable/report name;
- `dimensions`: an object with exactly `mode`, nullable `workload_profile`,
  nullable `seed`, `parameters`, `build`, `environment`, `protocol_version`,
  `planner_policy_version`, `osrm_dataset_version`, `route_cache_policy_version`,
  and `route_cache_enabled`. `mode` is one of `runtime`, `grpc`, `websocket`,
  `planner`, `serialization`, `shard-runtime`, `osrm`, or `hours`.
  `route_cache_policy_version` is null while the cache is unavailable/disabled
  and otherwise equals `liveroute-route-cache-v1`. `parameters` contains every
  consumed benchmark CLI and service-config value as a lexicographically named
  finite integer, Boolean, or string; omission of a consumed value is invalid.
  `build` has exactly
  nullable 40-lowercase-hex `git_commit`, Boolean `worktree_dirty`, `build_type`,
  `compiler_id`, `compiler_version`, `target_arch`, and
  `container_image_digest`. `environment` has exactly `os_name`,
  `kernel_version`, `cpu_model`, `logical_cpu_count`, nullable
  `cpu_quota_millicores`, and `memory_limit_bytes`. Unavailable nullable values
  remain JSON `null`; they are not guessed;
- `measurement`: object containing excluded `warmup_operations`, included
  `measured_operations`, `elapsed_microseconds`, and `completed_operations`;
- `counters`: object from stable metric name to unsigned integer count;
- `histograms`: object from stable stage name to `{unit, count,
  sum_microseconds, max_microseconds, upper_bounds_microseconds,
  bucket_counts}`;
- `gauges`: object from stable gauge name to `{last, maximum}`.

`attachments`, when present, maps only `callgrind` and/or
`compiler_vectorization_report` to an exact `{relative_path, sha256}` object.
The path is relative to `artifacts/benchmarks/` and the digest covers the
retained bytes; it uses `/` separators and contains no empty, `.` or `..` path
segment. Attachments are evidence identities, not benchmark dimensions, and
therefore do not affect compatible-run partitioning.

Every histogram uses the same 19 non-cumulative buckets with inclusive upper
bounds `1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000,
50000, 100000, 250000, 500000, 1000000, null`, where final `null` means positive
infinity. `bucket_counts` has exactly 19 entries, its sum equals `count`, and
`sum_microseconds`/`max_microseconds` cover the same observations. Empty
histograms have zero count/sum/max and all-zero buckets. A benchmark must retain
raw bucket counts; emitting only p50/p95/p99 is invalid.

V1 counter names are `accepted_events`, `duplicate_events`, `stale_events`,
`dropped_critical_events`, `dropped_high_events`, `dropped_normal_events`,
`dropped_advisory_events`, `coalesced_updates`, `replan_triggers`,
`replan_cancellations`, `rejected_overload_requests`, `deadline_misses`,
`osrm_failures`, `hours_provider_failures`, `infeasible_replans`,
`route_cache_fresh_hits`, `route_cache_misses`, `route_cache_stale_hits`,
`route_cache_insertions`, `route_cache_evictions`, `planner_allocation_calls`,
`planner_allocated_bytes`, `planner_allocation_scope_overflows`,
`planner_expansions`, `planner_candidates`, `profile_instructions`,
`profile_data_references`, `profile_l1d_misses`, `profile_ll_data_misses`,
`profile_branches`, and `profile_branch_misses`. V1
histogram names are
`deserialization`, `queue_wait`, `event_application`, `osrm_request`,
`hours_provider_request`, `matrix_conversion`, `planner`, `serialization`,
`total_request`, `acknowledgement`, `route_cache_lookup`, `route_cache_hit_path`,
and `osrm_backed_path`. V1 gauge names are the four priority queue depths plus
`route_cache_entries` and `route_cache_bytes`. A metric irrelevant to a benchmark
is absent; a relevant metric with no observations is present with zero values.
Unknown names require a new artifact schema version rather than silent discard.

The `deserialization` histogram measures only application-controlled adapter
work. For WebSocket it begins when `coder/websocket` returns a text message and
ends after strict JSON parsing plus client-envelope schema validation produces
the internal validated envelope. For gRPC it begins at C++ `OnReadDone` entry,
after gRPC has produced the generated request object, and ends after envelope
validation plus Protobuf-to-domain conversion produces the internal request.
Rejected messages are observed when validation terminates. WebSocket framing,
gRPC HTTP/2 processing, and framework-internal Protobuf wire decoding are not
included or claimed. `hours_provider_request`, `hours_provider_failures`, and
any future hours-cache metric are relevant only when the optional hours-import
adapter is actually exercised; they are absent from ordinary manual-hours V1
artifacts.

The report generator accepts one or more raw artifacts and partitions them by
byte-for-byte equality of `benchmark_name` plus the RFC-8785 canonical bytes of
the schema-validated `dimensions` object. It never combines different seeds,
workloads, build types, hardware, provider datasets, planner policies, or cache
policies. Within one partition it:

- sums measurement operation/duration fields and counters using checked `uint64`
  arithmetic;
- sums corresponding histogram bucket counts/count/sum, takes the maximum of
  `max_microseconds`, and rejects different bucket boundaries;
- computes p50/p95/p99 from the merged histogram as the first inclusive bucket
  whose cumulative count reaches `ceil(percentile * count / 100)`; it never
  averages per-run percentiles. A percentile selecting the final bucket is JSON
  `null` and renders as `>1000000 us`, not as zero or a fabricated exact value;
- stores exact throughput primitives as summed `completed_operations` and
  `elapsed_microseconds`, derives integer
  `throughput_millioperations_per_second = floor(completed_operations *`
  `1,000,000,000 / elapsed_microseconds)` with checked arithmetic, and represents
  every other rate as an integer `{numerator, denominator}` pair from summed
  primitives rather than averaging per-run rates;
- reports each gauge's maximum as the maximum across runs and `last` from the
  latest `(started_at, run_id)` artifact;
- sorts output groups by canonical dimensions and metric names so input-file
  order cannot change the result.

The aggregate artifact is one JSON document validated by
`schema/benchmark/liveroute-benchmark-aggregate-v1.schema.json`, with
`schema_version = liveroute.benchmark.aggregate.v1` and a `groups` array holding
the dimensions, run count, merged primitives, derived throughput, and derived
p50/p95/p99. Integer overflow, schema mismatch, duplicated `run_id`, incomplete
metric shape, or zero aggregate elapsed time fails generation. V1 does not define
a universal regression threshold: before/after artifacts are evidence and are
shown as separate compatible groups. Raw and aggregate artifacts live under
`artifacts/benchmarks/`, are excluded from Git, and contain no token, precise
location, request body, database URL, or raw provider response.

## Phase 18 Allocation Measurement and Acceptance

Allocation evidence is planner-scoped, not a process-global total. The benchmark
executable may provide benchmark-only replacements for every throwing,
`nothrow`, array, and aligned global allocation/deallocation function so ordinary
standard-library allocations cannot escape measurement. Those hooks must not
allocate, must preserve standard allocation semantics, and update counters only
when a thread-local `PlannerAllocationScope` is active. The serving binary does
not replace global allocation functions, and allocations on unrelated threads
are never attributed to an active planner attempt. Counter overflow or nested
scope misuse increments `planner_allocation_scope_overflows`, invalidates the run,
and is never saturated or reported as a successful measurement.

The measured scope is exactly one attempt on one planner worker:

1. Fixture construction, immutable `TripState`, normalized hours/facts,
   `TravelTimeMatrix`, trigger/context construction, provider work, and any
   scratch warm-up complete before the scope starts.
2. The scope starts immediately before `assemble_beam_search_input` and includes
   input assembly, proposal-source/budget construction, `run_replan_attempt`,
   beam search, decision reconstruction, `PlanProposal`/metadata construction,
   `PlannerStats`, and `assemble_stored_plan_proposal` including its validation.
3. It ends immediately after the optional `StoredPlanProposal` or terminal
   no-proposal result is fully constructed. Runtime status mapping, generation
   commit, completion-queue capture, Protobuf/JSON serialization, persistence,
   logging, metrics snapshotting, and benchmark-artifact construction are outside
   the scope.

One successful call to an allocation function counts once and adds its requested
byte size; reallocations count as the actual allocation calls/bytes performed.
Frees, retained capacity, and bytes allocated before scope entry do not reduce
the totals. The same start/end boundary defines the planner-latency histogram
used for Phase 18 comparison. Process-wide allocator/RSS/perf measurements may
be reported as supplemental diagnostics but cannot replace the scoped acceptance
numbers.

The fixed baseline suite is `planner-allocation-v1`. It runs in the pinned
`RelWithDebInfo` C++ image with one planner worker and no concurrent provider,
transport, logging, or cache activity. It uses the same deterministic feasible
fixture and seed `1` at suffix sizes `4, 8, 16, 32, 64`, with beam width `32`,
`max_candidates = 4096`, `max_expansions = 16384`, and a 60-second benchmark-only
attempt deadline that must never be hit. Each process performs 10 excluded
warm-up attempts and 200 measured attempts per suffix size using the same
worker-owned `PlannerScratch`; five independent process runs produce five raw
artifacts per baseline or candidate. A result/proposal canonical digest is
checked on every attempt so an optimization cannot trade correctness or search
work for allocation reduction. Baseline and candidate artifacts must have equal
dimensions except for explicit `variant` and build identity, and aggregation
compares corresponding suffix-size groups rather than combining them.

Immediately before the first Phase 18 change, the unoptimized correctness-passing
Phase 17 build is measured and its actual calls/replan, bytes/replan, latency
percentiles, throughput, artifact run ids, and SHA-256 digests are recorded in
`plans/summaries/optimization-evidence-ledger.md`; no absolute baseline number is invented in a
plan. The Phase 18 candidate target is quantitative:

- across the five suffix-size groups with equal operation weighting, allocation
  calls per replan are at most 50% of baseline and allocated bytes per replan are
  at most 60% of baseline;
- no individual suffix-size group may increase calls/replan, bytes/replan, or
  planner p99 to a worse fixed histogram bucket;
- aggregate completed-operations throughput is at least 95% of baseline, no
  attempt hits its deadline, allocation-scope overflow is zero, and canonical
  result digests and all correctness tests remain identical.

If the aggregate target is not reached, Phase 18 is not represented as a measured
success. Individual experiments may still be retained only when their own
compatible artifacts show a reduction in calls or bytes, no worse p99 bucket,
throughput at least 95% of their baseline, and a maintainability reason recorded
in the optimization ledger. Neutral, rejected, or reverted experiments remain in
that ledger with their measurements; illustrative numbers in older planning text
are never substituted for actual artifacts.

## Phase 19 Data Layout, Benchmark, and Profiling Contract

Phase 19 changes only the private in-memory representation used by planner hot
loops. `BeamSearchInput`, `PlanningActivity`, domain objects, proposal output,
the lexicographic objective, candidate set/order, budgets, and every wire or
storage contract remain unchanged. The accepted Phase 18 build is the Phase 19
array-of-structures baseline.

The candidate implementation is an internal `PlannerActivityColumns` owned and
reused by one planner worker's `PlannerScratch`. It is built exactly once after
`BeamSearchInput::is_valid()` succeeds and before initial scoring. Public
standalone generation/scoring entry points may build a temporary equivalent
view, but the beam candidate loop uses the one prepared view and performs no
conversion back to `PlanningActivity`. Proposal reconstruction may read the
original input after search completes.

For `N` remaining activities, all per-activity columns have exactly `N` entries
in authoritative suffix order:

- `activity_ids` (`ActivityId`), `original_trip_ordinals` (`size_t`), and
  `matrix_location_indices` (`size_t`, exactly the activity index plus one);
- `priority_ranks` and `utility_scores` (`int32`);
- `minimum_duration_ms`, `scheduled_duration_ms`, `preferred_duration_ms`, and
  `maximum_duration_ms` (`int64`). Scheduled duration is the exact current-plan
  interval for a scheduled activity; otherwise it is
  `max(1, preferred_duration_seconds) * 1000`. Every seconds-to-milliseconds
  conversion is checked. These columns preserve duration validation and do not
  authorize shortening;
- `earliest_open_ms`, `latest_close_ms`, `baseline_start_ms`,
  `baseline_end_ms`, `reservation_start_ms`, `reservation_latest_start_ms`, and
  `mandatory_deadline_ms` (`int64`). An absent optional value stores zero and is
  read only when its corresponding flag is set;
- `flags` (`uint16`) with fixed bits: bit 0 baseline scheduled, bit 1 mandatory,
  bit 2 movable, bit 3 skippable, bit 4 reservation present, bit 5 mandatory
  deadline present, and bit 6 found closed. Other bits are zero in V1.

Because an activity can have multiple open windows, earliest/latest columns are
only rejection bounds. Exact feasibility uses flattened `window_opens_ms` and
`window_closes_ms` (`int64`) plus `window_offsets` (`size_t`) of length `N + 1`;
activity `i` owns the half-open range `[window_offsets[i],
window_offsets[i + 1])`. Opens/closes remain paired, sorted, nonoverlapping, and
equivalent to the normalized input. An empty range stores zero in that
activity's earliest/latest columns, and those bounds are not read. Parallel
`sorted_ordinals` (`size_t`) and `sorted_activity_indices` (`uint8`) vectors have
`N` entries and provide binary ordinal lookup without imposing a new numeric
limit on the stable ordinal; the index vector also defines increasing
original-trip-ordinal expansion order. All column/window vectors reuse worker
capacity and are cleared without releasing it between attempts. No column owns
strings, provider objects, Protobuf, JSON, or database state.
`TravelTimeMatrix` remains the separate immutable row-major matrix.

Only generator feasibility, protected-activity lower-bound evaluation, ordinal/
matrix-index lookup, scoring, and beam expansion may consume the column view.
Candidate/result types and deterministic comparators do not change. Batch
scoring is permitted only for siblings already emitted in normative order and
must produce the same score and retention order as scalar scoring. Explicit
prefetch or vectorization is optional, must be isolated behind an internal
implementation path, and is retained only under the same evidence gate; Phase
19 does not require either technique when the compiler/profile does not support
it.

Phase 19 has two schema-validated benchmark names. Both reuse the exact
`planner-allocation-v1` fixture, seed, suffix sizes `4, 8, 16, 32, 64`, beam
width `32`, `max_candidates = 4096`, `max_expansions = 16384`, 60-second
benchmark-only deadline, single planner worker, planner boundary, result digest,
and isolation rules. Their required `parameters` include `variant`
(`aos-baseline` or `soa-candidate`), `layout_version` (`aos-v1` or `soa-v1`),
`suffix_size`, all three search limits, `attempt_deadline_ms`, and
`result_digest`.

- `planner-layout-timing-v1` performs 10 excluded warmups and 200 measured
  attempts per suffix in each of five independent processes. It emits planner
  latency, elapsed/completed operations, allocation calls/bytes/overflows,
  deadline misses, expansions, and admitted candidates. The profiler is not
  active during timing.
- `planner-layout-profile-v1` performs one excluded warmup and one instrumented
  attempt per suffix in each of three independent processes. It emits
  expansions/candidates plus Callgrind totals normalized into
  `profile_instructions = Ir`, `profile_data_references = Dr + Dw`,
  `profile_l1d_misses = D1mr + D1mw`, `profile_ll_data_misses = DLmr + DLmw`,
  `profile_branches = Bc + Bi`, and `profile_branch_misses = Bcm + Bim`, using
  checked `uint64` addition. Its parameters also contain `profiler_name =
  callgrind`, `profiler_version = 3.22.0`, and fixed cache geometry. It requires
  a `callgrind` attachment containing the retained raw file's relative path and
  SHA-256. Profiled wall time is diagnostic only and is never compared with
  native timing.

The reproducible profiler stage derives from the locked amd64 C++ tool image and
extracts, without host installation or an unpinned `apt install`, Ubuntu
`valgrind_3.22.0-0ubuntu3_amd64.deb` whose SHA-256 is
`744e081e5cf3d5c598b499dbb7d2250ea3f2869dde4a4d7b231fe6114f347d7d`.
The implementation records the built profiler-image digest in
`config/tool-images.lock` before accepting evidence. The invocation is exactly:

```text
valgrind --tool=callgrind --cache-sim=yes --branch-sim=yes
  --instr-atstart=no --collect-atstart=no
  --I1=32768,8,64 --D1=32768,8,64 --LL=8388608,16,64
  --callgrind-out-file=ARTIFACT PROFILE_BENCHMARK_ARGUMENTS
```

The benchmark uses `CALLGRIND_START_INSTRUMENTATION` immediately before the
Phase 18 planner boundary and `CALLGRIND_STOP_INSTRUMENTATION` immediately after
it. Profiling runs with network disabled, one process/worker, and no provider,
transport, cache, logging, or artifact-writing activity inside collection.
Missing events, more than one `events`/`summary` set, counter overflow, a
nonzero Valgrind exit, or a raw-file digest mismatch invalidates the run. The
pinned GCC build also emits one vectorization report for planner translation
units with `-fopt-info-vec-optimized-missed`; its compiler version, command, file
digest, and findings are recorded in `plans/summaries/optimization-evidence-ledger.md`. Native
`perf`, Heaptrack, and flame graphs may supplement diagnosis but are neither
required nor accepted in place of these artifacts.

Baseline and candidate must have identical result digests and identical summed
expansion/candidate counts for each suffix; zero admitted candidates invalidates
a profile run. A data-layout change is retained only when all of these gates
pass, using checked integer cross-multiplication rather than rounded displayed
rates. When a guarded baseline counter is zero, the candidate counter must also
be zero:

- combined native completed-operation throughput for suffix sizes `16, 32, 64`
  is at least 105% of baseline; every individual suffix retains at least 95% of
  baseline throughput and has no worse planner p99 histogram bucket;
- across profiled suffix sizes `16, 32, 64`, admitted-candidate-weighted L1 data
  misses per candidate are at most 90% of baseline, or instructions per
  candidate are at most 95% of baseline;
- instructions, data references, L1 data misses, last-level data misses, and
  branch misses per candidate each remain at most 105% of baseline for every
  individual suffix;
- allocation calls and bytes per operation do not exceed 105% of the accepted
  Phase 18 build, deadline misses and allocation-scope overflows remain zero,
  canonical results match, and all planner/correctness tests pass.

If these gates do not pass, Phase 19 is completed as a measured neutral or
rejected experiment: the SoA/prefetch/vectorization change is reverted, the
existing representation remains authoritative, and the actual artifacts and
reason are recorded in the optimization ledger. A neutral result is useful
evidence but is not described as an optimization.

## Phase 20 Tail-Latency Experiment Contract

Phase 20 does not change search budgets, provider timeouts, queue priorities,
cache warmup, user-visible results, or overload/status behavior without a
separate contract decision. Its initial V1 scope is three independent,
semantics-preserving AoS planner experiments against the accepted post-Phase-19
serving path:

1. `validated-input`: after `run_beam_search` validates its immutable input once,
   internal candidate generation, lower-bound generation, and child scoring omit
   repeated full-input validation; public standalone generation/scoring continues
   to validate every call;
2. `lower-bound-scratch`: the protected-activity lower-bound check reuses a
   worker-owned byte bitmap instead of allocating a new bitmap per child;
3. `partial-beam-selection`: when hard-feasible children exceed `beam_width`,
   `partial_sort` orders exactly the retained prefix under the unchanged total
   comparator instead of sorting discarded children. At or below the width, the
   existing full sort remains.

An internal three-bit benchmark mask selects these experiments: bit 0 is
`validated-input`, bit 1 is `lower-bound-scratch`, and bit 2 is
`partial-beam-selection`. Serving uses only the bits retained by evidence. The
mask affects computation strategy only; every mask must produce identical
outcome, decisions, score, result digest, expansion count, candidate count,
truncation/deadline/cancellation flags, and budget behavior.

`planner-tail-v1` reuses the Phase 19 native fixture, planner boundary, suffix
sizes `4, 8, 16, 32, 64`, seed `1`, beam `32`, `max_candidates = 4096`,
`max_expansions = 16384`, 60-second benchmark-only deadline, 10 excluded warmups,
200 measured attempts, and five independent processes per variant. Variants are
`tail-baseline` (mask 0), `validated-input` (mask 1),
`lower-bound-scratch` (mask 2), `partial-beam-selection` (mask 4), and
`combined-candidate` (mask 7, all three experiments together). Required
counters are deadline misses, allocation calls/bytes/scope overflows,
expansions, and candidates; the planner histogram and canonical result digest
are required.

An individual experiment is retained only when correctness/work counts match,
deadlines and scope overflows remain zero, every suffix preserves at least 95%
of baseline throughput with no worse p99 bucket, neither allocation measure
exceeds 105% of baseline, and at least one material benefit occurs: combined
suffix-16/32/64 throughput reaches 102% of baseline, a suffix of size 8 or larger
improves by at least one p99 bucket, or one allocation measure falls to at most
95% while combined throughput remains at least 98%. Comparisons use summed
primitives and checked integer cross-multiplication.

The mask-7 combined candidate is measured independently against mask 0 to
detect interactions. It must preserve all correctness/work/deadline/allocation
guards, have no worse per-suffix p99 bucket or throughput below 95%, and provide
at least one of the same material benefits. A bit failing its individual gate
cannot be rescued by mask 7. The serving candidate is the OR of individually
retained bits; if that is a nonzero subset other than mask 7, measure that exact
subset as an additional `combined-candidate` five-process variant and apply the
same combined gates. Mask 0 needs no duplicate measurement. A failed individual
or combined experiment is disabled in serving and recorded as neutral/rejected.
Phase 20 may truthfully complete with fewer than three retained changes, but
documentation must say that three targeted experiments were measured rather
than claiming three improvements. Existing many-trips, hot-trip, bursty-GPS,
and provider-timeout load checks must still pass to verify bounded rejection/
coalescing/cancellation; their instrumented p99 is reported by stage and never
blended with planner-only latency.

## V1 Route-Cache Policy

Phase 17 caches individual raw provider `RouteEstimate` pairs outside the planner
and assembles an immutable `TravelTimeMatrix` after lookup/fill. Trip-specific
delay overlays and planner state are not cached. The policy identifier is
`liveroute-route-cache-v1` and is included in benchmark dimensions.

The key is the tuple `(origin_cell, destination_cell,
departure_time_bucket, travel_mode, provider_dataset_version)`:

- A coordinate cell is two signed E5 integers. Each latitude/longitude is
  multiplied by `100000` and rounded to the nearest integer, with exact halves
  rounded away from zero after finite/range validation. Missing pairs are fetched
  using those E5 grid-point coordinates divided by `100000`, so cache contents do
  not depend on which request first populated a cell. The maximum per-axis
  displacement is `0.000005` degree. The compact key never stores the original
  floating-point coordinate.
- The generic time-dependent-provider bucket is
  `floor(departure_unix_ms / 900000)` (15 UTC minutes), using mathematical floor
  for pre-epoch values. The pinned V1 OSRM Table provider is time-independent and
  therefore always uses the reserved bucket `0`; it must not create artificial
  misses as wall time advances.
- `travel_mode` distinguishes driving and walking. The exact locked OSRM dataset
  version/profile identity is part of `provider_dataset_version`; a different
  dataset or profile cannot hit an old entry.

At startup, distinct exact provider dataset/profile identity strings are sorted
by UTF-8 bytes and assigned bounded nonzero `uint32` namespace ids; V1 has only
the car and foot identities. The cache retains that complete id-to-string table,
so a compact entry key stores the namespace id without accepting hash collisions
as identity. Hash input is the full key encoded as two's-complement big-endian
origin/destination E5 `int32` components, big-endian departure-bucket `int64`,
one-byte travel mode (`1` walking, `2` driving), matching the V1 wire enum, and
big-endian namespace
`uint32`. FNV-1a-64 with offset basis `14695981039346656037` and prime
`1099511628211` hashes those bytes; `hash % shard_count` selects ownership. Full
key equality still resolves hash collisions. Namespace-table storage counts
toward the memory bound, and a provider-identity/config reload constructs a new
empty cache rather than reinterpreting old namespace ids.

The initial local configuration added with the cache implementation is exact:

```yaml
route_cache:
  enabled: true
  policy_version: liveroute-route-cache-v1
  shard_count: 16
  max_entries: 131072
  max_bytes: 67108864
  coordinate_scale: 100000
  time_bucket_seconds: 900
  fresh_ttl_seconds: 21600
  stale_if_error_max_age_seconds: 86400
  eviction_scan_limit: 64
```

The cache is process-local, non-persistent, and starts empty. Hashing the complete
key selects one of 16 shards; a shard owns its lock, entries, byte/entry budget,
and second-chance clock hand. The global entry/byte bounds are divided equally
between shards and startup fails if the configured fixed storage plus index and
eviction metadata exceeds `max_bytes`. Keys, values, timestamps, occupancy, and
eviction metadata all count toward the byte bound. No untracked per-entry heap
allocation or unbounded auxiliary index is allowed.

An inserted successful reachable or unreachable estimate is fresh for 21,600
seconds from `inserted_at` measured by monotonic time. Hits do not extend this
deadline. Provider failures and malformed/partial matrices are never cached.
Fresh lookup is bounded; an expired entry behaves as a miss but may remain for
stale fallback until its total age exceeds 86,400 seconds. Insertion first reuses
an empty or over-age slot. Otherwise the per-shard second-chance hand examines at
most 64 slots, clears encountered reference bits, evicts the first unreferenced
slot, and, if all 64 were referenced, evicts the 64th slot. Thus eviction work is
bounded even under a fully hot cache. Concurrent misses are allowed to issue
duplicate provider requests; the existing bounded provider executor/concurrency
limit is the V1 stampede bound, and no unbounded waiter/single-flight table is
introduced.

For a matrix request, all fresh pair hits are retained. If any pair misses, form
the ordered unique set of source cells having at least one miss and destination
cells having at least one miss, then make one OSRM Table request for their
sources-by-destinations cross-product using canonical E5 representatives. This
may refresh extra pairs from that bounded cross-product; every returned valid
pair is inserted. It never makes one provider request per missing pair. The
result is successful only when every required pair is freshly hit or successfully
fetched. If that provider request returns
`PROVIDER_UNAVAILABLE`, the adapter may instead use stale entries only when every
otherwise-missing pair has an entry no older than 86,400 seconds; the whole result
then returns `OK` with `routing_quality = STALE_CACHE`. Stale data is not used for
`CANCELLED`, `DEADLINE_EXCEEDED`, `RESOURCE_EXHAUSTED`, `MATRIX_TOO_LARGE`,
`INVALID_ARGUMENT`, or `INTERNAL`, is never mixed with an incomplete matrix, and
use does not refresh its age. A dataset/profile version change constructs a new
empty cache after provider readiness succeeds; no old entry is reinterpreted
under a new namespace.

Metrics separately count fresh hits, misses, stale-fallback hits, insertions, and
evictions, and report lookup latency and current/maximum logical bytes and entry
count. Cache-hit p99 and OSRM-backed p99 are separate benchmark histograms; a
mixed total may be shown only in addition to, never instead of, those two paths.

## Compatibility Mechanism

### Protobuf

- Pin Buf CLI in the tool container.
- Use `buf lint` and the `FILE` breaking category, which is stricter than binary-wire-only compatibility and protects generated Go/C++ APIs.
- Check in `proto/baseline/liveroute-v1.binpb` after the initial schema review.
- Buf performs lint, descriptor construction, and breaking checks only; its image is not assumed to contain `protoc`.
- C++ generation runs on `linux/amd64` from the exact `cpp_grpc_toolchain` digest in `config/tool-images.lock`. It first asserts `protoc --version` is `libprotoc 31.1`, `pkg-config --modversion protobuf` is `31.1.0`, and `pkg-config --modversion grpc++` is `1.78.1`.
- From the repository root, invoke `/opt/grpc/bin/protoc -I proto --cpp_out=gen/cpp --grpc_out=gen/cpp --plugin=protoc-gen-grpc=/opt/grpc/bin/grpc_cpp_plugin` with input files sorted by UTF-8 repository-relative path. Generated `*.pb.h`, `*.pb.cc`, `*.grpc.pb.h`, and `*.grpc.pb.cc` are checked in. No host compiler/plugin is permitted.
- CI runs Buf checks, regenerates C++ outputs in a clean temporary tree with that image, byte-compares them with `gen/cpp`, compiles them against the same Protobuf/gRPC runtime versions, and fails on any difference.
- Go generation uses `protoc` 31.1, `protoc-gen-go` 1.36.6, and
  `protoc-gen-go-grpc` 1.5.1 from `docker/go-proto/Dockerfile`. Both image
  stages use the immutable `linux/amd64` base digests recorded under
  `go_proto_toolchain` in `config/tool-images.lock`; plugin module versions are
  exact `go install module@version` inputs. Checked-in bindings live under
  `backend/gen/liveroute/v1`, use Protobuf-Go 1.36.11 and gRPC-Go 1.82.1 at
  runtime, and are regenerated into a temporary directory and byte-compared by
  `scripts/check-go-proto-generation.sh`. Host `protoc` or Go plugins are not
  permitted.
- Removed fields/enums are deprecated and their names/numbers reserved; no number is reused. An incompatible change creates package `liveroute.v2`.

### WebSocket JSON

- `schema/websocket/liveroute-v1-schema-manifest.json` is the sole V1 schema-bundle baseline. Its file list contains the client and server envelope schemas in ascending UTF-8 repository-relative path order.
- Parse every schema as UTF-8 JSON while rejecting duplicate object names, non-integer JSON numbers, integers outside `[-9007199254740991, 9007199254740991]`, unpaired surrogates, and non-ASCII object member names. These restrictions make the checked-in schema language an exact subset of RFC 8785.
- Canonicalize each parsed schema using RFC 8785: no insignificant whitespace, UTF-8 output, minimal JSON escapes, and lexicographically sorted object names. SHA-256 those canonical bytes and store the lowercase 64-hex digest beside the path.
- Compute the bundle digest by concatenating, for each manifest entry, `UTF8(path)`, one NUL byte, the ASCII lowercase file digest, and one LF byte; SHA-256 the concatenation. The algorithm identifiers are `rfc8785-json-schema-integer-subset-v1` and `sha256-path-nul-file-digest-lf-v1`.
- `scripts/check-websocket-envelope.py --print-manifest` prints the only valid replacement manifest. Normal checker execution fails on a missing/extra/reordered path, changed canonical file digest, changed bundle digest, duplicate key, or forbidden number/string/key.
- Tests validate a checked-in positive/negative corpus with both the Go validator and the CLI/integration client decoder.
- The v1 schema is frozen except for new namespaced `extensions` keys, which do not alter standard validation. Any standard-field change creates `liveroute.v2`.
- An intentional V1 baseline replacement requires explicit contract review plus a regenerated manifest and corpus, not an unnoticed edit.

### Database and snapshots

- Goose migrations are immutable after application; new changes use a new sequential migration. CI migrates a clean PostgreSQL database up, checks exact constraints/indexes, and migrates from the previous released schema.
- Snapshot schema version 1 uses the Protobuf descriptor baseline and golden payload/checksum fixtures. Unknown versions must fall back without state mutation.

## Required Failure and Compatibility Tests

In addition to the existing planner/runtime/OSRM tests, V1 requires:

- higher-epoch bootstrap discards old observations, active proposals, provider jobs, and planning results while marking older pending PostgreSQL proposal rows stale and retaining their history;
- same-epoch reconnect retains the observation watermark and rejects older observations as `STALE`;
- backend restart resets observation sequencing under a higher epoch and accepts the first new sample;
- crashes after intent commit, after C++ acceptance, after PostgreSQL finalization, after finalization confirmation, and before each client acknowledgement converge without double application;
- a proposal computed from an accepted-but-unfinalized mutation is never published; the latest still-current proposal is stored and released only after PostgreSQL finalization;
- duplicate `create_trip`, `trip_edited`, and `replace_current_plan` messages return the stored canonical result without duplicate trips, activity edits, plan revisions, mutation sequences, or mirror rows;
- a structurally valid user plan commits and remains current while C++ is unavailable; ordered mirror replay or full bootstrap later converges without rolling it back;
- multiple canonical-first edits while C++ is unavailable remain bounded, receive consecutive revisions/sequences, preserve idempotency, and are all covered by ordered replay or one verified full canonical bootstrap before runtime-first dispatch resumes;
- structurally malformed user plans are rejected, while travel-time/hours/reservation/deadline-infeasible but structurally valid plans remain current and produce warnings/proposals;
- a canonical-first trip/current-plan edit supersedes pending proposals, preserves exact agreement between the activity set and current plan, blocks later runtime-first dispatch until its mirror converges, and cannot be undone by mirror deadline, transport failure, or unexpected C++ incompatibility;
- every `plan_proposal` is committed as exact `StoredPlanProposal` bytes before WebSocket publication; persistence failure publishes nothing and leaves the current plan unchanged;
- duplicate proposal delivery with identical identity/payload is idempotent, while changed bytes under one proposal id are `INTERNAL` and never overwrite history;
- proposal generation alone never changes `trips.current_plan_id`; fresh acceptance atomically creates a separate current-plan revision and marks the proposal accepted, while rejection and stale acceptance leave the current plan unchanged;
- proposal acceptance retry/crash windows install byte-identical pre-recorded current-plan id/revision/origin/source/creation metadata in C++ and PostgreSQL;
- applying stale proposal contents through `replace_current_plan` records a new `USER_AUTHORED` plan instead of bypassing proposal freshness;
- snapshot request racing an unresolved accepted mutation returns `SNAPSHOT_NOT_READY` and later succeeds after confirmation;
- terminal rejection advances finalized/mutation watermarks without advancing trip revision;
- concurrent/stale lease acquisition, renewal safety-margin expiry, and stale-holder dispatch are fenced;
- outbox claim expiry, duplicate claims, transaction rollback, serialization/deadlock retry, and retry jitter never lose a durable row;
- logical command expiry differs from per-attempt deadline and consumes its sequence terminally;
- checksum mismatch, unknown snapshot version, metadata mismatch, newest-snapshot corruption, and fallback to the second snapshot/full rebuild work;
- WebSocket unknown standard fields, extension fields, unsupported versions, every close code, auth timeout, heartbeat timeout, origin denial, oversized frame, and full essential buffer behave as contracted;
- development token is never present in logs/errors and raw tokens are absent from PostgreSQL;
- planner objective tests prove each hard violation is pruned rather than scored, lower-rank skip counts dominate every later criterion, utility/lateness/fixed-duration-shortfall/travel/current-plan-disruption criteria apply in their exact order, signed utility and duplicate ranks behave deterministically, and checked arithmetic rejects overflow;
- beam tests vary candidate insertion order and equal-score branches, exercise the exact optimistic partial key/canonical decision key, and produce byte-identical visible proposal schedules and the same valid best-so-far candidate for equal input and budget;
- candidate-generation tests prove that starts are limited to the exact current interval, per-window earliest-arrival boundary, and legal current-plan-start boundary; a scheduled activity keeps its exact baseline duration; an omitted activity uses its preferred positive duration when added back; insufficient room never creates a shortened alternative; duplicates are removed; fixed/immovable omission is preserved; skip is last; and no minute-grid or variable-duration alternative appears;
- candidate-generation budget tests cover multiple windows, reservation and mandatory-deadline caps, zero minimum/preferred durations, checked arrival/end overflow, exact parent/activity/start/duration/skip order, zero-result generator invocations, exclusion of pre-emission invalid alternatives from expansion counts, and deterministic interruption at each `max_expansions` and `max_candidates` boundary;
- protected-activity pruning tests prove that a partial is removed before beam retention when a required future activity has no legal interval even with zero travel and all other undecided work omitted, that passing the optimistic check does not bypass actual matrix reachability, and that optional activities are not treated as protected;
- preserved-prefix tests prove that every entry is reproduced unchanged, duplicate prefix/suffix activity ids are rejected, and an in-progress preserved interval advances the effective suffix start so no revised entry overlaps it;
- changed-activity tests use an authoritative suffix order different from trip-definition order and cover swaps, insertion of a previously omitted activity, omission of a previously scheduled activity, interval-only changes, and the count-once rule without treating unchanged later activities as reordered;
- result-metadata tests cover every trigger-to-reason mapping, sorted/deduplicated causal unions, matrix-derived late-departure/reservation-risk facts, changed-versus-preserved segment inheritance, outcome-only reason isolation, and notification precedence at negative, zero, 20-minute, and unknown slack;
- exhaustive infeasibility, beam-truncated or candidate-budget-limited search without a complete candidate, deadline/cancellation before any complete candidate, and interruption after a complete candidate produce the exact `INFEASIBLE`, `OK/NO_NEW_PROPOSAL`, terminal attempt status, and `OK/BEST_SO_FAR` result shapes respectively;
- `LocalDateRange` rejects empty/reversed/over-32-day ranges; seeded hours reject duplicate/unsorted places and exceptions, wrong tzdata, invalid interval order, recurring DST gaps/folds without an opening-date exception anywhere in the complete readiness-validation domain, and nonexistent/incorrect-offset exception endpoints;
- seeded-hours readiness tests cover a gap/fold beyond the last explicit TZif transition via POSIX-footer recurrence, an ambiguous next-day close resolved by the preceding opening-date exception, the upper requestable-date boundary, and defensive post-readiness `INVALID_SOURCE`/`NOT_SERVING` behavior;
- US DST nonexistent/ambiguous time, explicit fall-back offset, Arizona/Hawaii non-DST behavior, overnight hours, exceptional closures, source-version changes, and exact UTC window coalescing normalize correctly;
- OSRM `Ok`/null, `NoTable`, `NoSegment`, `TooBig`, every invalid-request code, `NotImplemented`, unknown code, HTTP 429/4xx/5xx, malformed JSON, wrong dimensions, mismatched nulls, nonfinite/negative/overflow numbers, timeout, cancellation, queue/byte/location limits map to the exact table above;
- benchmark raw/aggregate JSON schemas reject missing dimensions, unknown metric
  names, malformed bucket shapes, duplicate run ids, overflow, zero elapsed time,
  and incompatible aggregation; golden tests prove merged-bucket percentile,
  summed-throughput/rate, latest-gauge, and input-order behavior;
- planner-allocation tests cover every global allocation-function form, inactive
  and unrelated-thread exclusion, nested-scope/overflow invalidation, exact scope
  boundaries, scratch warm-up exclusion, allocation/byte accounting, identical
  canonical result digests, and baseline/candidate target evaluation;
- planner-layout tests cover every column/flag and absent-value encoding,
  multiple/empty flattened-window ranges, capacity reuse, input/view equivalence,
  scalar-versus-column generation/scoring/lower-bound/search equivalence, exact
  candidate/expansion counts, and unchanged interruption/budget behavior;
- Phase 19 benchmark/profile tests reject layout/variant mismatches, missing or
  duplicated Callgrind event sets, unchecked event sums, wrong simulated-cache
  geometry/tool version, raw-profile digest mismatch, profiled timing used as
  native evidence, and every failed retention guard; a golden Callgrind fixture
  proves exact event-name mapping and checked ratio comparison;
- route-cache tests cover positive/unreachable entries, exact E5 half rounding
  including negative coordinates, pre-epoch 15-minute floor buckets, OSRM bucket
  zero, canonical key bytes/FNV hash/shard selection, mode/dataset namespace
  isolation and reload, fresh/expired/over-age boundaries, no TTL refresh
  on hit, bounded second-chance eviction, byte/entry limits, partial-hit
  cross-product fill, concurrent duplicate misses, and exact allowed/forbidden
  stale-fallback statuses with complete-matrix enforcement;
- invalid/zero/unbounded/inconsistent configuration fails startup;
- shutdown while a durable response, finalization confirmation, snapshot, provider request, and planner job are in flight either drains within the deadline or leaves recoverable outbox state;
- Protobuf baseline and JSON corpus cover `CurrentPlan`, `PlanProposal`, canonical-first `TripEdited`/`CurrentPlanReplaced`, `create_trip`, `trip_edited`, `replace_current_plan`, proposal decisions, and `MATRIX_TOO_LARGE`; pinned generated C++ output, JSON schema manifest/digest/corpus, hours-seed schema/semantics, and previous database migration compatibility checks run in the standard check target.

## Implementation-Gate Policy

One agent may own and continuously implement all of V1, and numeric roadmap phase order is not mandatory. V1 must not, however, land as one unverified all-components-at-once batch. The following are correctness gates, not staffing constraints:

1. contract artifacts and migrations validate;
2. deterministic internal domain/provider/runtime tests pass;
3. gRPC reconnect/epoch/finalization tests pass;
4. PostgreSQL/WebSocket recovery tests pass;
5. OSRM/hours/planner correctness tests pass;
6. failure/load/resource tuning and performance reports pass.

An agent may work ahead locally across gates, but a later gate cannot be used to waive an earlier invariant. This keeps failures attributable and prevents storage/wire changes from being discovered after the planner/runtime depends on them.

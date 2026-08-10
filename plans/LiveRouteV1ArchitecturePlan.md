# LiveRoute V1 Architecture Contract

## Status and Scope

This document fixes the v1 service boundaries and runtime semantics needed to begin implementation. It is authoritative for cross-component behavior together with the exact stack, field, status, storage, recovery, time, authentication, readiness, and compatibility rules in `plans/LiveRouteV1ContractSpec.md`. The implementation plan and feature roadmap describe component work and optional sequencing; when wording differs, this architecture and the contract specification govern.

V1 is:

```text
WebSocket client <-> Backend gateway
                       |-- PostgreSQL
                       |     users, trips, current plans, engine proposals, snapshots,
                       |     command intents, planner outbox, active-trip leases
                       |
                       `-- bounded pool of bidirectional gRPC streams
                             <-> C++ planner service
                                   sharded in-memory active trips
                                   bounded provider I/O
                                   bounded planner workers
                                   -> profile-specific local OSRM instances
```

V1 is a single-host Docker Compose deployment with exactly one backend process and one C++ planner process. PostgreSQL and separate car/foot OSRM instances run on the same private container network. The lease epoch, instance identifiers, and stream pool remain part of V1 because they make restarts safe and preserve a clean future scaling boundary, but V1 does not implement cross-backend routing, C++ replica placement, high availability, or rolling upgrades.

V1 implements the WebSocket gateway and exercises it with CLI, load, and integration clients. A React/TypeScript browser frontend is a V1.5 extension. In this document, "WebSocket client" includes those V1 clients and the future browser client.

Only the backend WebSocket/health endpoints are exposed outside the private container network. The C++ planner, PostgreSQL, and OSRM endpoints are internal services.

The V1 backend uses Go 1.26 with `net/http`, `coder/websocket`, gRPC-Go, `pgx/v5`, Goose SQL migrations, and draft-2020-12 JSON Schema validation. Exact dependency and pinning policy is in `plans/LiveRouteV1ContractSpec.md`. This choice does not enter the C++ planner or change its transport-independent domain interfaces.

## Ownership Boundaries

- The WebSocket client owns its local presentation state, if any, and generates an idempotency key for every command.
- The backend authenticates users, authorizes trip access, owns WebSocket sessions, assigns durable per-trip mutation sequence numbers, and is the only component that accesses PostgreSQL.
- PostgreSQL is the durable source of truth for users, trip definitions, the user-selected current plan, engine-generated plan proposals and their decisions, planner recovery snapshots, uncheckpointed durable planner mutations, and active-trip lease epochs.
- The single V1 backend holds the active-trip leases. Each lease carries a monotonically increasing `runtime_epoch`; stale epochs are rejected by the C++ service. This fencing remains mandatory across backend restarts even though horizontal backend scaling is deferred.
- The user-selected current plan is authoritative. The backend validates and commits a user-authored initial or replacement plan to PostgreSQL without asking C++ to approve its optimality or feasibility. C++ mirrors that current plan for an active trip and may change it only in response to a user-authorized current-plan replacement or accepted proposal.
- The C++ service owns the mutable in-memory mirror and live observations for active trips. Each trip belongs to exactly one shard, and only that shard mutates the in-memory state. Planner output is advisory until the user accepts it.
- OSRM is an external provider from the planner's perspective. Only `OsrmTravelTimeProvider` performs OSRM HTTP and JSON work.
- The planner receives immutable internal C++ inputs, including `TravelTimeMatrix`; it never sees Protobuf, WebSocket, PostgreSQL, HTTP, JSON, or OSRM types.

## Delivery and Acknowledgement Semantics

LiveRoute uses at-least-once delivery plus idempotency for durable commands. It does not claim exactly-once delivery.

| Input class | Examples | PostgreSQL | Backend-to-C++ replay | Overload behavior |
| --- | --- | --- | --- | --- |
| Runtime-first durable mutation | activity lifecycle, reservation/deadline/hours change, accepted/rejected engine proposal | Record command intent/outbox before dispatch; finalize domain mutation after C++ acceptance | Retry until applied or rejected terminally | Return retryable overload; never silently drop |
| Canonical-first user edit | create trip with user plan, replace user-selected current plan, edit the trip/activity set plus resulting user plan | Commit normalized trip/current-plan state, command outcome, revision, and finalized sequence first; post-creation edits always enqueue an ordered C++ mirror event | Deliver when active or resolve through a full canonical bootstrap; retry until covered | Preserve the committed user state; report bounded admission or runtime-sync state explicitly |
| Telemetry/latest value | location, velocity, heading | Do not persist every sample | Do not replay obsolete samples | Coalesce or drop with explicit status |
| Advisory | weather/crowd/recommendation refresh | Persist only if independently required by product data | Best effort | Delay or drop with explicit status |
| Planner snapshot | state needed to rehydrate an active trip | Persist asynchronously with version metadata | Used as bootstrap base | Retain previous compatible snapshot on failure |
| Engine proposal | C++-generated alternative itinerary | Persist separately before publishing; never change the current-plan pointer | Recompute or discard when its source tuple is stale | Keep the current user plan unchanged |

Runtime-first backend acknowledgements are two-stage:

1. `durable_recorded`: the command intent and planner-outbox record committed atomically before dispatch.
2. `planner_applied`: the C++ shard accepted the command and returned its resulting state version; the backend then finalized the durable domain mutation and outbox state atomically. A terminal C++ rejection instead finalizes the intent as rejected without changing canonical trip/plan data.

Canonical-first user edits use `canonical_committed` after the PostgreSQL transaction makes the normalized trip/current-plan state authoritative. If an active runtime must be updated, a later `runtime_synced` acknowledgement reports that C++ mirrors the committed state. C++ unavailability never rolls back or rejects the canonical user edit; it leaves runtime sync pending and blocks later runtime-first mutations for that trip until ordered convergence or rebootstrap.

Telemetry receives only an `accepted`, `coalesced`, `dropped`, or `overloaded` admission status. It has no durability guarantee.

The backend reports a runtime-first durable command as complete only after the second transaction commits. If either process crashes between the two stages, the recorded intent is replayed and the C++ idempotency result allows finalization. A canonical-first user edit is complete for product authority at `canonical_committed`, even if its active-runtime mirror remains pending. The backend keeps applied durable outbox rows until a persisted planner snapshot covers their mutation sequence. A snapshot transaction stores the new snapshot and prunes covered outbox rows atomically.

`command_intents` is the durable idempotency and outcome history; it is separate from the prunable delivery outbox. V1 retains command-intent rows for the lifetime of the trip. Reusing a `message_id` with the same canonical payload returns the recorded pending or terminal outcome. Reusing it with a different payload is a terminal `IDEMPOTENCY_KEY_REUSED` error. Deleting a trip deletes its command-intent history in the same controlled lifecycle. Duplicate client commands, outbox deliveries, gRPC messages, and responses are therefore safe even after covered outbox rows have been pruned.

## Client WebSocket Contract

V1 uses UTF-8 JSON text frames and exact protocol string `liveroute.v1`. `plans/LiveRouteV1ContractSpec.md` fixes message kind strings, payload fields, close codes, limits, and compatibility. The implementation must transcribe that contract into a checked-in draft-2020-12 JSON Schema before the WebSocket handler is implemented. The backend enforces the frame limit before JSON parsing, rejects a missing or unsupported protocol version, rejects unknown standard fields, and ignores only unknown namespaced keys inside the explicit `extensions` object.

Every client message has:

- `protocol_version`
- `message_id` generated by the client
- `kind`
- `payload`

Trip-scoped messages additionally require `trip_id`. Connection-scoped `authenticate`, `ping`, and `pong` messages omit `trip_id`. A `message_id` is a canonical lowercase RFC-4122 UUID: it is a trip-scoped idempotency key for commands and a correlation identifier for other client messages.

Every server message has `protocol_version`, `server_message_id`, `kind`, `status`, `retryable`, and `payload`, plus `in_reply_to_message_id` when it responds to a client message. Trip-scoped server messages also include `trip_id`, `trip_revision`, `planner_state_version`, accepted mutation/observation watermarks, and the current `runtime_epoch` when an active runtime exists.

Client-to-server message kinds are:

- authenticate
- create trip with its initial user-authored current plan
- subscribe trip
- unsubscribe trip
- trip command
- telemetry update
- trip state resynchronization
- ping
- pong

Server-to-client message kinds are:

- connection ready
- subscription state
- command acknowledgement (`durable_recorded`, `planner_applied`, `canonical_committed`, `runtime_synced`, or terminal rejection)
- telemetry admission status
- planner notification
- persisted plan proposal
- trip state resynchronization
- structured error
- ping
- pong

The first non-ping client message must be `authenticate`; normal messages are accepted only after `connection_ready`. `create_trip` is the only trip-scoped command authorized before its `trip_id` exists: the authenticated user becomes its owner, and the initial current plan commits in the same transaction. There is no global WebSocket ordering guarantee. Clients use `message_id`, `trip_revision`, `planner_state_version`, and sequence watermarks rather than frame arrival order to determine freshness.

Reconnect uses resynchronization, not an unbounded WebSocket replay log. The client reconnects, reauthenticates, supplies its last observed trip/planner versions and a configured-bounded list of outstanding command `message_id` values, and subscribes again. The backend returns the current durable trip and authoritative current plan, the latest stored pending proposal when one exists, runtime-sync state, and the durable outcome of each requested command intent. If the trip is not active, the backend may rehydrate it before reporting live planner state; PostgreSQL current-plan authority does not depend on activation. Repeated commands use `message_id` idempotency.

Each connection has bounded inbound admission and a bounded outbound buffer. Replaceable progress/telemetry notifications may be coalesced. Durable acknowledgements and terminal errors are not silently dropped; if they cannot be delivered, the backend closes the connection with a retryable reason so the client resynchronizes. Protocol/authentication/authorization violations close with a non-retryable reason; capacity or transient service failures close with a retryable reason.

## V1 Security and Trust Boundary

V1 is a local development/demo deployment, not an Internet production deployment. Its minimum security contract is:

- Docker Compose exposes only the backend on loopback by default; PostgreSQL, C++, and OSRM remain on the private network.
- Plain `ws://` is allowed only on loopback. Any non-loopback deployment must terminate TLS and expose `wss://`; internal mTLS and production certificate automation are future deployment work.
- The V1 `authenticate` payload carries an unpadded base64url encoding of exactly 32 random bytes. The raw token enters only through `/run/secrets/liveroute_dev_token`; PostgreSQL stores only its SHA-256 digest mapped to a seeded user. Exact validation, revocation, close-code, origin, and redaction rules are in `plans/LiveRouteV1ContractSpec.md`. User-facing login and external identity-provider integration arrive with or after the React V1.5 client.
- The backend authorizes trip ownership on every existing-trip subscribe, resynchronize, command, and telemetry message. `create_trip` is the sole exception: the authenticated user becomes owner of the new client-generated trip id in the same transaction. Authentication alone never grants access to an existing trip.
- Browser-compatible connections enforce a configured origin allowlist. Non-browser CLI/load clients may omit `Origin` only in explicitly enabled local-development mode.
- Frame size, decoded payload size, per-connection admission, and per-trip pending-command limits are enforced before expensive work. V1 uses local bounded admission rather than a distributed rate-limiting service.
- Logs and error text exclude bearer tokens, precise location payloads, and raw PostgreSQL/OSRM responses. Safe identifiers and aggregate metrics may be recorded.

## Bidirectional gRPC and Protobuf Contract

The v1 package is `liveroute.v1`. Enums reserve zero as `UNSPECIFIED`; removed field numbers and names are reserved; incompatible changes require a new package version.

```proto
service LiveRoutePlanner {
  rpc PlanTrips(stream PlannerStreamRequest)
      returns (stream PlannerStreamResponse);
}

message PlannerStreamRequest {
  string request_id = 1;
  string trip_id = 2;
  uint64 runtime_epoch = 3;
  uint64 mutation_sequence = 4;
  uint64 observation_sequence = 5;
  optional uint64 expected_planner_state_version = 6;
  int64 expires_at_unix_ms = 7;
  optional uint64 expected_trip_revision = 8;

  oneof payload {
    OpenStream open_stream = 20;
    BootstrapTrip bootstrap_trip = 21;
    ApplyTripEvent apply_event = 22;
    RequestSnapshot request_snapshot = 23;
    DeactivateTrip deactivate_trip = 24;
    Ping ping = 25;
    ConfirmFinalizedMutations confirm_finalized_mutations = 26;
  }
}

message PlannerStreamResponse {
  string request_id = 1;
  string trip_id = 2;
  uint64 runtime_epoch = 3;
  uint64 accepted_mutation_sequence = 4;
  uint64 accepted_observation_sequence = 5;
  uint64 planner_state_version = 6;
  uint64 trip_revision = 7;

  oneof payload {
    StreamReady stream_ready = 20;
    EventAcknowledged event_acknowledged = 21;
    ReplanResult replan_result = 22;
    TripSnapshot trip_snapshot = 23;
    TripDeactivated trip_deactivated = 24;
    PlannerError error = 25;
    Pong pong = 26;
    FinalizedMutationsAcknowledged finalized_mutations_acknowledged = 27;
    TripBootstrapped trip_bootstrapped = 28;
  }
}
```

The envelope and payload field names, types, numbers, enum values, and validity rules are fixed in `plans/LiveRouteV1ContractSpec.md`. Implementation must transcribe them under package `liveroute.v1`, generate a checked-in Buf descriptor baseline, and pass compatibility tests before a gRPC handler is implemented. Once assigned, field names and numbers are never reused.

| Message | Purpose | Required content |
| --- | --- | --- |
| `OpenStream` | Establish the backend-to-C++ stream and negotiate capabilities. | backend instance id, protocol version, supported capability flags |
| `StreamReady` | Confirm that the C++ service accepted the stream and negotiated limits/capabilities. | C++ instance id, accepted protocol version/capabilities, configured message/resource limits safe to expose |
| `BootstrapTrip` | Load or restore an active trip into the C++ runtime. | snapshot schema/version/checksum and bytes when available; otherwise full trip definition plus authoritative current plan; checkpoint mutation watermark; current observation; backend trip revision |
| `TripBootstrapped` | Confirm the exact bootstrap base C++ loaded. | status, retryability, current-plan id, accepted/finalized mutation watermarks |
| `ApplyTripEvent` | Deliver one trip event or update to the C++ runtime. | event id, event occurrence time, and exactly one typed event delta; never a complete mutable trip |
| `EventAcknowledged` | Report whether an event was accepted, duplicated, rejected, or otherwise resolved. | disposition, retryability, resolved mutation/observation sequence, resulting planner state/trip versions, whether replanning was scheduled, and the installed current-plan id for accepted/duplicate canonical-first plan mirrors |
| `RequestSnapshot` | Ask C++ to serialize a checkpoint at or beyond a requested finalized watermark, only when accepted/finalized watermarks match. | reason and minimum planner/finalized mutation watermark requested |
| `TripSnapshot` | Return a versioned checkpoint for persistence and later recovery. | schema version, runtime epoch, planner state version, covered finalized mutation sequence, payload size, checksum, serialized snapshot bytes |
| `ConfirmFinalizedMutations` | Report PostgreSQL's highest contiguous terminally finalized mutation sequence to C++. | cumulative finalized mutation watermark |
| `FinalizedMutationsAcknowledged` | Confirm C++ advanced or already held that finalized watermark. | status, retryability, finalized watermark |
| `DeactivateTrip` | Remove a trip from the active C++ runtime, optionally after producing a final snapshot. | reason and whether a final snapshot is required |
| `ReplanResult` | Return a newly computed proposal or explicitly report why no new proposal exists. | proposal id, base current-plan id, source runtime epoch/state version, trip revision and accepted mutation sequence, preserved prefix, revised suffix, skipped/added/moved activities, notification decision, structured reasons, planner/provider stats, and result-quality fields; V1 never shortens an activity, and `planning_generation` remains internal C++ stale-result metadata checked before emission |
| `PlannerError` | Report a structured planner or protocol failure and whether retrying is appropriate. | stable status enum, retryable flag, safe diagnostic text, related state/sequence versions |

Envelope validity is message-specific:

| Request payload | Required envelope fields | Fields that must be absent/zero |
| --- | --- | --- |
| `OpenStream` | `request_id` | `trip_id`, epoch, both sequences, both expected versions, expiry |
| `BootstrapTrip` | `request_id`, `trip_id`, current `runtime_epoch`, expiry | both sequences and both expected versions |
| durable `ApplyTripEvent` | `request_id`, `trip_id`, current epoch, `mutation_sequence`, `expected_trip_revision`, expiry | `observation_sequence`; expected planner version unless this is a plan decision |
| telemetry/advisory `ApplyTripEvent` | `request_id`, `trip_id`, current epoch, `observation_sequence`, expiry | `mutation_sequence` and both expected versions |
| `RequestSnapshot` or `DeactivateTrip` | `request_id`, `trip_id`, current epoch, expiry | both sequences and both expected versions |
| `ConfirmFinalizedMutations` | `request_id`, `trip_id`, current epoch, expiry | both envelope sequences and both expected versions; finalized watermark is in the payload |
| `Ping` | `request_id` | trip id, epoch, both sequences, both expected versions, expiry |

Mutation and observation sequences start at 1; zero means not applicable. Optional expected-version fields use Protobuf presence and are never interpreted through a sentinel value. Envelope violations are rejected before domain conversion.

`ApplyTripEvent` contains a `oneof` covering the v1 event model:

- location, velocity, and heading updates
- activity started, completed, skipped, or delayed
- reservation changed
- mandatory deadline changed
- route deviation detected
- operating hours changed
- place found closed
- travel delay
- user replaced the authoritative current plan
- user accepted or rejected an engine proposal
- recommendation refresh, weather, crowd, or social advisory update

Event priority is derived by the C++ admission/domain layer from event type and current trip state; callers cannot promote their own work.

Trip/bootstrap domain messages contain canonical UUIDs, coordinates, travel mode, activity ordering, completed prefix/current activity, fixed or flexible classification, user priority/utility, reservation data, normalized open windows, minimum/preferred/maximum duration, movable/shortenable/skippable/mandatory flags, and the authoritative current plan/version metadata. The `can_shorten` field is retained for wire/storage compatibility, but the V1 suggestion engine never shortens an activity. The current plan contains the user-selected ordered schedule, not browser-supplied routing metrics or planner reason/quality fields. V1 `open_windows` are user-authored canonical activity input; the backend validates and persists them, and the normal serving path performs no place-hours lookup. V1 accepts IANA zones present for country `US` in the pinned tzdata. CLI or optional fixture/seed ingestion converts local input into signed UTC Unix-epoch milliseconds; the backend validates normalized ranges/order before C++ and the planner uses only UTC values. IANA zone names remain on durable activities, current-plan segments, and proposal segments for post-planning display conversion. The seeded-hours implementation remains an optional local import/normalization adapter and tested fixture. When used, its readiness eagerly validates DST gap/fold safety across the complete requestable V1 date domain using the bounded transition-directed procedure; it has no rolling or lazy validation horizon. The exact `LocalDateRange`, `HoursInfo`, provider-result, seed schema, tzdata lock, DST gap/fold, overnight-window, and exceptional-closure rules are fixed in `plans/LiveRouteV1ContractSpec.md`.

Current-plan segments contain activity id, scheduled/omitted state, and user-selected start/end only when scheduled; the repeated order is authoritative. Engine-proposal segments additionally contain location, IANA zone, optional inbound route duration/distance/reachability, disposition, and structured reason codes. Metrics use integer durations with documented units. Protobuf messages are converted into validated internal C++ types before shard admission; generated message objects are not stored in `TripState` or passed to planner search.

V1 result metadata is derived on the C++ side from the immutable trigger/snapshot, matrix, and search outcome using the exact table and precedence in `plans/LiveRouteV1ContractSpec.md`. Event causes and state-derived lateness/reservation risk form sorted, unique causal reasons; only changed proposal segments inherit those causes. Search-limit/deadline/infeasibility reasons remain result-level outcome metadata. Notification precedence is exhaustive infeasibility, terminal/search-limited suppression, critical negative slack, changed-plan suggestion, low slack, then none. Handlers and serializers do not choose labels.

V1 proposal selection uses the fixed `liveroute-v1-lexicographic-1` objective in `plans/LiveRouteV1ContractSpec.md`, not tunable scalar weights. Hard feasibility is enforced before comparison. The planner first prefers moving all events without skips, using matrix travel between the current/previous location and each candidate activity; only then does it prefer skipping less-important (higher numeric `priority_rank`) optional events over more-important ones. Remaining ties maximize user utility, minimize lateness/travel/current-plan disruption, and use a canonical schedule key. This policy affects suggestions only and never changes the user-authoritative current plan without explicit acceptance.

The V1 beam branches on remaining-activity order and allowed omission, not arbitrary timestamps. Schedule alternatives use only the contract's finite boundary set: an exact legal current-plan interval, each window's earliest reachable legal start, and a legal current-plan start. Moving a scheduled activity preserves its exact current-plan duration; adding back an omitted activity uses its preferred positive duration. No shortened duration is generated. The effective suffix start is the later of current time and every scheduled preserved-prefix end, so an unchanged started activity cannot overlap revised work. Parent, activity, start, and skip order and both candidate budgets are normative in `plans/LiveRouteV1ContractSpec.md`; there is no continuous-time search or minute-grid sampling in V1.

The beam input retains remaining activities in the authoritative `CurrentPlan.segments` suffix order, including omitted entries; original trip ordinals remain separate stable identities. `changed_activity_count` is only a late proposal-ranking tie-breaker and counts each state, interval, or common-scheduled relative-order change once. `max_expansions` bounds actual parent/activity generator invocations. It does not require constructing or counting hard-invalid routes, timestamps, or durations that the finite generator rejects before emission. Before beam retention, a partial route is hard-pruned when a still-required activity has no scheduled alternative even under the contract's zero-travel, skip-all-other-undecided lower bound. Beam-width or candidate-budget truncation without a complete candidate returns a non-infeasibility `OK/NO_NEW_PROPOSAL` result; only an untruncated exhaustive search may return `INFEASIBLE`.

Proposal acceptance and rejection are runtime-first durable trip events. A decision includes `source_runtime_epoch`, `proposal_id`, `source_planner_state_version`, and `base_current_plan_id`, and returns `STALE` with reason `PLAN_PROPOSAL` when the tuple no longer matches the active proposal. For acceptance, the backend converts the stored proposal into a complete new `CurrentPlan` and durably records its id/revision/creation time/payload before C++ dispatch so C++ and PostgreSQL install byte-identical metadata across retries. Accepting a fresh proposal causes the backend finalization transaction to create that immutable current-plan revision with origin `accepted_engine_proposal`; rejecting it never changes the current plan. Applying a stale proposal anyway is a separate user-authored `replace_current_plan` command, not a stale-check bypass.

Protocol rules:

- `request_id` correlates asynchronous acknowledgements and results. Responses may complete out of input order.
- `runtime_epoch` starts at 1 and rejects messages from an older backend lease holder. A higher-epoch bootstrap cancels old work and clears all non-durable telemetry/proposals.
- `mutation_sequence` orders durable commands and must be contiguous after bootstrap.
- A terminally rejected durable command still resolves and consumes its mutation sequence without incrementing planner state version, so later commands do not deadlock behind a gap.
- `observation_sequence` is backend-owned in-memory state scoped to `(trip_id, runtime_epoch)` and starts at 1. Gaps are valid and older observations are ignored. It resets only on a higher epoch; same-epoch stream reconnect preserves it.
- `trip_revision` starts at 1 with atomic trip/current-plan creation. For runtime-first work, the backend records the current value as `expected_trip_revision`; C++ matches and advances its mirror before PostgreSQL advances during finalization. For a canonical-first user trip/current-plan edit, PostgreSQL advances first and the mirror event carries the prior expected revision so C++ reaches the same value later. A terminal runtime-first rejection leaves both revisions unchanged.
- `planner_state_version` is scoped to the runtime epoch, starts at 0 after higher-epoch bootstrap, and increases for every accepted change to C++ trip state, including telemetry. Freshness compares `(runtime_epoch, planner_state_version)`; it is independent of durable `trip_revision`, which starts at 1.
- `expected_planner_state_version` is present only for accepting/rejecting a specific proposal or another explicitly compare-and-set planner operation. Ordinary durable trip mutations do not set it, so intervening telemetry cannot create a false version conflict.
- The C++ runtime has an internal `planning_generation` that increases whenever accepted input invalidates an in-flight plan.
- An engine proposal is identified by `runtime_epoch`, `proposal_id`, `source_planner_state_version`, and `base_current_plan_id`; a proposal decision must match all four against the current proposal.
- Per-message deadlines use `expires_at_unix_ms` because an RPC deadline cannot express independent deadlines on a long-lived stream. The C++ boundary rejects expired messages and converts remaining time to a monotonic internal deadline. Deployed hosts require synchronized clocks.
- Message size limits are enforced before domain conversion. V1 hard limits are a 256 KiB WebSocket frame/decoded message, 4 MiB gRPC message, 2 MiB snapshot, and 128 resynchronization command IDs. Oversized work receives `RESOURCE_EXHAUSTED` or the contracted WebSocket close code.
- One read and one write may be in flight per C++ callback reactor. Additional outbound messages wait in a bounded per-stream queue.

The single V1 backend uses a fixed, bounded stream pool to the single C++ process. Trips are consistently assigned to a stream for connection locality; C++ trip sharding remains authoritative for state ownership. Stream count and queue capacity are configuration validated at startup, not request-driven.

On stream failure:

- The backend reconnects with bounded exponential backoff and jitter.
- Obsolete telemetry is discarded or replaced by the newest sample.
- Durable outbox entries remain pending and are resent at least once.
- Before replay, the backend verifies or renews its existing lease; if that lease is no longer valid, it acquires a higher epoch. It then bootstraps the trip from the latest snapshot and covered mutation watermark and sends uncovered durable mutations as bounded individual stream messages.
- Durable outbox payloads are lease-neutral. On every dispatch or replay, the backend reads its current lease and wraps the unchanged command id, mutation sequence, and event payload in the current `runtime_epoch`; an epoch stored for audit is never reused as dispatch authority.
- The V1 outbox event body is the strict versioned JSONB/Base64 wrapper around
  deterministic `ApplyTripEvent` Protobuf bytes defined in the contract spec.
  It is not browser JSON. The Go persistence/gateway boundary constructs that
  typed stable event once; the dispatcher strictly decodes it and supplies only
  per-attempt envelope authority. Malformed stored bodies pause for internal
  repair rather than being reinterpreted or retried forever.
- The C++ service treats duplicate bootstrap/events idempotently and rejects stale epochs or versions.

The stable status set is `OK`, `DUPLICATE`, `STALE`, `INVALID_ARGUMENT`, `UNAUTHENTICATED`, `PERMISSION_DENIED`, `NOT_FOUND`, `IDEMPOTENCY_KEY_REUSED`, `INACTIVE_TRIP`, `RESOURCE_EXHAUSTED`, `DEADLINE_EXCEEDED`, `COMMAND_EXPIRED`, `CANCELLED`, `INFEASIBLE`, `PROVIDER_UNAVAILABLE`, `DURABILITY_UNAVAILABLE`, `UNAVAILABLE`, `UNSUPPORTED_VERSION`, `SNAPSHOT_NOT_READY`, `SNAPSHOT_INCOMPATIBLE`, `INTERNAL`, and `MATRIX_TOO_LARGE`. All old epoch/sequence/version/proposal conditions use `STALE` plus a structured stale reason. `degraded` is not a status: successful lower-quality results use explicit plan/routing/recovery-quality fields. Numeric values and retryability rules are fixed in `plans/LiveRouteV1ContractSpec.md`.

## C++ Concurrent Runtime and Commit Protocol

### State and work ownership

- `hash(trip_id) % shard_count` selects the sole owner shard for the process lifetime.
- Each shard owns its trip map, four bounded priority lanes, result-return queue, and per-trip sequencing/deduplication metadata.
- Ordinary mutexes, condition variables, and `std::jthread` are the v1 synchronization baseline. Lock-free queues are not required.
- Provider I/O and planner CPU work use separate bounded executors so OSRM waits do not occupy shard threads or planner workers.
- `PlannerScratch` is worker-owned and reused only by that worker.
- A trip has at most one running planning generation and one coalesced pending replan trigger.

### Event-to-plan sequence

1. The gRPC reactor validates the envelope shape, reserves capacity for any required essential response, and attempts bounded admission to the owner shard. If either reservation fails, it rejects before state mutation.
2. The shard previews `runtime_epoch`, mutation/observation sequence, expected trip revision, optional expected planner state version, and idempotency without mutating either state or watermarks. A duplicate/stale/inactive request returns immediately.
3. The shard applies the event to a candidate copy of `TripState`. If valid, it commits version admission and the candidate state together on the single owner shard. State-changing durable events and telemetry advance `planner_state_version` and `planning_generation` exactly once. Opaque advisory input advances only the shared observation watermark; because it does not alter planner input, it neither invalidates an otherwise-current attempt nor clears a proposal. If runtime-first durable domain validation fails, the shard terminally consumes only that mutation sequence and returns the domain status. A stale proposal decision does the same with `STALE/PLAN_PROPOSAL`. Invalid telemetry/advisory work does not advance its observation watermark. An invalid canonical-first mirror is not terminally consumed because C++ does not contain its already-canonical effect; it remains unresolved for matching full bootstrap/repair.
4. If no full replan is required, the shard emits an acknowledgement/state update immediately.
5. An accepted planning-input change yields an immutable planning seed containing the accepted `TripState`, trigger, runtime epoch, state version, planning generation, trip revision, accepted mutation sequence, and base current-plan id. Ordinary location/velocity/heading input is acknowledged without dispatch when no explicit feasibility-changing replan is active; when one is active, the latest observation refreshes its bounded replacement seed while preserving the highest-priority explicit trigger. An idle `RECOMMENDATION_REFRESH` may create a seed over the unchanged snapshot without advancing planner versions; it is redundant and acknowledged without dispatch while any attempt is running or pending. A dispatch wrapper adds its attempt deadline and cancellation source after capacity is reserved. The seed contains no provider objects or matrix.
6. Hours refresh and route acquisition run off-shard through bounded provider executors. Hours adapters produce normalized typed events before the admission transaction; the accepted snapshot therefore already contains concrete UTC windows. Route acquisition uses current location plus authoritative remaining-suffix locations and produces the immutable matrix. For mixed inbound modes, matrix cell `(origin, destination)` uses the destination activity's `inbound_travel_mode`; adapters may issue one Table request per distinct mode and select the corresponding destination columns. Provider errors are mapped before planner invocation using the fixed contract table.
7. A planner worker searches only in memory and returns best-so-far with the captured tags.
8. The result returns to the owner shard. The shard commits it only when runtime epoch, trip identity, state version, and planning generation still match.
9. A stale result is discarded. If newer accepted state still requires planning, the shard schedules one replacement using the latest coalesced trigger.
10. The shard records the latest committed engine proposal separately from the authoritative current plan in `TripState` and emits it through the trip's current stream binding when one exists. The proposal carries its base current-plan id and source accepted-mutation watermark; the backend withholds it from WebSocket clients until PostgreSQL's finalized watermark covers that source and the proposal is durably stored.

State mutation is never rolled back because a proposal became stale or OSRM failed. Provider/planner outcome is represented separately from whether the event was accepted. An engine proposal never replaces the authoritative current plan by itself. A proposal has a `runtime_epoch`, `proposal_id`, `source_planner_state_version`, and `base_current_plan_id`; acceptance is rejected if that tuple is no longer current.

Durable acceptance and PostgreSQL finalization are distinct. Runtime-first work is accepted by C++ before PostgreSQL finalization; canonical-first user trip/current-plan edits are finalized in PostgreSQL before C++ mirror acceptance. In both cases C++ advances its accepted mutation watermark only when its in-memory state covers the event, and the backend sends cumulative `ConfirmFinalizedMutations` only up to that accepted watermark. C++ advances confirmation idempotently and returns `SNAPSHOT_NOT_READY` while accepted and finalized watermarks differ. Bootstrap carries PostgreSQL's current plan/finalized watermark and may converge both sides atomically. A snapshot's covered finalized mutation sequence is the highest contiguous PostgreSQL-finalized command sequence whose accepted effects, if any, are present in that snapshot.

C++ may compute a proposal speculatively after durable acceptance, but every proposal carries `source_accepted_mutation_sequence`. The backend keeps only one bounded latest unpublished proposal per trip and never publishes it until PostgreSQL's finalized watermark covers that source and a `plan_proposals` row commits. The persistence transaction marks any older pending proposal superseded without changing the current plan or trip revision. This prevents clients from observing a proposal based on a mutation that failed to become canonical and guarantees that every published suggestion can be recovered alongside the unchanged user plan. Finalization releases only a still-current epoch/state-version/base-plan result.

A stream is only a transport binding, not state ownership. Bootstrap binds the trip and epoch to the current stream; a stream break invalidates that binding. An in-flight result that commits after a reconnect uses the new binding. If no binding exists, C++ retains the latest proposal in the bounded `TripState`; the backend still persists it before client publication. PostgreSQL, not that retained result, supplies the authoritative current plan during bootstrap/resynchronization.

Higher-priority state changes request cooperative cancellation of superseded work. Cancellation is an optimization; epoch/version/generation checking is the correctness mechanism.

### Queueing and overload

Bounded resources exist at every asynchronous handoff:

- per-stream inbound admission
- per-shard critical/high/normal/advisory lanes
- provider request queue and per-profile OSRM concurrency
- planner work queue
- per-shard completion queue
- per-stream essential and replaceable outbound capacity
- per-WebSocket inbound admission
- per-WebSocket outbound queue
- per-trip backend pending-command queue
- durable backend outbox dispatcher batch/in-flight set

All capacities and worker counts are explicit configuration with safe upper bounds and startup validation. Typed configuration keys may be implemented before tuning, but a finite `config/local-v1.yaml` is required before the first runnable Compose acceptance test. Initial tunable values are selected from representative tests rather than claimed as universal defaults; protocol limits fixed in `plans/LiveRouteV1ContractSpec.md` apply immediately.

Critical/high work receives preference, but the dispatcher admits normal work after a configured bounded priority burst. Queue-full behavior is type-specific:

- durable mutation: retryable rejection; backend retains outbox entry
- telemetry: replace pending latest value, otherwise drop explicitly
- advisory: delay or drop explicitly
- provider/planner job: reserve its completion slot before dispatch; otherwise return `RESOURCE_EXHAUSTED` without blocking the shard; a valid best-so-far result remains `OK` with explicit quality metadata
- essential gRPC response: reserve one outbound slot before request admission; if no slot is available, refuse admission and close that stream with retryable `RESOURCE_EXHAUSTED` so a committed acknowledgement is never stranded
- replaceable replan/progress result: retain only the latest bounded value per trip and coalesce it into the current stream or next bootstrap

Essential responses are event acknowledgements, terminal errors for admitted durable events, requested snapshots, and deactivation results. Reservations move with the response from admission through the single in-flight gRPC write and are then released. A stream failure may still lose bytes in transport, so the backend recovers durable outcomes by replay; capacity exhaustion itself never causes post-commit loss. Shard threads never wait for an outbound slot or network write.

The backend assigns observation sequences and performs only transport-level latest-value coalescing when disconnected or capacity-bound. It never infers route/geofence/deadline boundaries. The C++ owner shard applies non-stale ordinary observations as state-only updates. With no explicit feasibility-changing replan active, they do not start provider/planner work. While an explicit high/critical replan is active, the latest ordinary observation cancels the obsolete attempt and refreshes at most one bounded replacement seed without replacing its higher-priority cause. A boundary that must never be lost is an explicit high/critical domain event, not an assumption that every intermediate GPS sample survives overload. V1 has no current-route geometry, configurable geofence, coordinate-derived boundary classifier, or ETA-only response contract.

## Active Trip Lifecycle

- A backend must hold the PostgreSQL runtime lease before `BootstrapTrip`.
- Bootstrap carries `runtime_epoch`, the latest compatible snapshot, and its mutation watermark. Subsequent durable mutations are replayed as bounded individual messages.
- Bootstrap is idempotent for the same epoch and snapshot version. A higher epoch replaces older ownership, cancels/discards old work and telemetry, resets epoch-scoped observation/planner versions, and rebuilds only durable state; a lower epoch is `STALE` and rejected.
- Active-trip count, activities per trip, snapshot bytes, and pending work per trip are bounded. Capacity exhaustion is explicit.
- V1 does not rebalance a live trip between C++ shards. Changing shard count requires a controlled service restart and backend rehydration.
- Backend-directed deactivation is preferred. Idle eviction is allowed only with no running work and requires a final snapshot response before the trip is removed.
- If final snapshot persistence fails, the backend retains replayable outbox state and reports `DURABILITY_UNAVAILABLE`; it does not claim successful checkpointing.
- C++ restart begins empty. The backend verifies its current leases or acquires higher epochs where needed, then rehydrates active trips from PostgreSQL.

## OSRM Provider Contract

### Deployment

- Run OSRM locally through the repository's container workflow; do not require host package installation.
- Start from official OSRM release `v26.5.0`; resolve and pin the GHCR image digest. V1's bounded default development/CI geography is a Rhode Island extract. Record its immutable bytes/source/retrieval date/size/SHA-256 plus the car/foot profile digests and preprocessing flags in `config/osrm-dataset.lock`. A broader demo geography is a later product/demo choice, not a planner-contract change.
- Build separate datasets and `osrm-routed` instances for the supplied car and foot profiles because profiles are applied during preprocessing, not selected dynamically at query time.
- Use the same bounded geographic extract for both profiles and MLD preprocessing for v1.
- Configure distinct internal endpoints for driving and walking. The C++ provider maps `TravelMode` to an endpoint; planner code does not.
- Readiness requires each profile endpoint to answer a fixed small Table request successfully. Container liveness alone is insufficient.

### Request and response policy

- Use the documented HTTP GET Table endpoint with `annotations=duration,distance`.
- Send current location plus remaining unique activity locations in deterministic order.
- Validate finite longitude/latitude ranges, supported mode, total locations, and estimated request size before I/O.
- Configure a LiveRoute matrix-location limit no greater than the OSRM `--max-table-size`; reject larger work with non-retryable `MATRIX_TOO_LARGE` in v1 rather than issuing unbounded/chunked work.
- Use libcurl multi behind a small provider wrapper to obtain connection reuse, bounded asynchronous I/O, timeout handling, response-byte limits, and cooperative cancellation.
- Do not automatically retry OSRM inside a latency-bounded live request. A retry would consume the same deadline unpredictably. Health checks and later events provide recovery.
- Apply the complete precedence and OSRM-code mapping table in `plans/LiveRouteV1ContractSpec.md`. `Ok` requires exact matrix dimensions, matching null cells, nonnegative finite values, and checked round-up conversion; `NoTable` is a fresh all-unreachable matrix, while `TooBig` is `MATRIX_TOO_LARGE`.
- A `null` duration/distance cell is represented as `reachable == false`; v1 does not fabricate straight-line fallback travel times.
- Malformed JSON, unexpected dimensions, every documented OSRM error code, HTTP/transport errors, timeout, cancellation, queue admission, and response-byte limits map exactly as specified; no generic provider-error fallback may change those public statuses.
- Record OSRM transport, parse, matrix-conversion, and total provider latency separately from planner latency.

The correctness-first path is uncached. Phase 17 adds the exact process-local
`liveroute-route-cache-v1` pair cache from
`plans/LiveRouteV1ContractSpec.md`: deterministic signed E5 coordinate cells,
the static OSRM departure bucket, mode and locked dataset/profile identity,
16 ownership shards, 131,072 entries, a complete 64 MiB memory bound, six-hour
fresh TTL, 24-hour maximum stale-if-error age, and a bounded 64-slot
second-chance eviction scan. It caches raw provider estimates outside the planner;
the planner still receives only an immutable `TravelTimeMatrix`. Stale data may
replace only a completely covered `PROVIDER_UNAVAILABLE` result and is labeled
`routing_quality = STALE_CACHE`; it is never silently presented as fresh data.

Dataset updates build new profile artifacts out of band, pass readiness/integration checks, then replace endpoints. A dataset-version change invalidates incompatible cache entries.

## PostgreSQL Durability and Recovery

Minimum logical tables are:

- `users`
- `development_auth_tokens`
- `trips` and normalized trip/activity constraints
- immutable `itinerary_plans` for user-selected current-plan history
- durable `plan_proposals` for engine suggestions and their decision state
- `command_intents`
- `planner_snapshots`
- `planner_outbox`
- `trip_runtime_leases`

The exact columns, checks, foreign keys, indexes, retention, and lock order are in `plans/LiveRouteV1ContractSpec.md`. Required constraints include one immutable current-plan revision selected by each trip, at most one pending engine proposal, separate retained proposal/current-plan payloads, unique client command/event id per trip in `command_intents`, RFC-8785/SHA-256 canonical payload digest metadata, unique durable mutation sequence per trip, monotonically increasing trip revision/finalized watermark/runtime epoch, one current lease holder, token digests rather than raw tokens, and snapshot metadata containing schema version, source epoch/state version, covered finalized mutation sequence, size, and SHA-256.

The backend serializes runtime-first durable commands per trip and allows at most one unresolved runtime-first mutation. A runtime-first command is admitted only when no canonical-first mirror is pending. Canonical-first user edits may continue while C++ is unavailable because PostgreSQL is authoritative, but their unresolved mirror rows are bounded by configured per-trip capacity and receive consecutive mutation sequences. A full mirror capacity returns retryable overload before commit. Later runtime-first commands wait without durability acknowledgement until all earlier canonical mirrors converge. Telemetry may continue under its independent observation sequence. Ordered mirror delivery or a full canonical bootstrap through the latest finalized sequence preserves identical C++ ordering without making user editing depend on C++ availability.

Lease acquire/renew transactions use PostgreSQL server time and atomically set holder id, expiry, and a strictly higher epoch on acquisition. The backend renews before a configured safety margin; an uncertain or late renewal is treated as expired. On expiry it stops trip dispatch before attempting another acquisition. This simple fencing rule is retained for restart correctness even with one V1 backend process.

Runtime-first durable command recording transaction:

1. Lock/compare the trip revision.
2. Detect duplicate client `message_id`. Compare the stored `rfc8785-sha256-v1` digest over validated `{protocol_version, kind, trip_id, payload, extensions-if-present}`; return the stored state/outcome on a match and reject `IDEMPOTENCY_KEY_REUSED` on a difference.
3. Validate the command against durable backend rules without changing canonical trip/current-plan state.
4. Allocate the next mutation sequence and record the current trip revision as `expected_trip_revision`.
5. Insert the pending command intent and a lease-neutral planner outbox payload containing stable `event_id` (equal to client `message_id`), trip id, digest metadata, expected trip revision, mutation sequence, optional logical command expiry, and event data. Do not persist a reusable per-attempt `request_id` or epoch as future dispatch authority.
6. Commit, then emit `durable_recorded` to the client.

The outbox dispatcher claims bounded due batches using PostgreSQL-time claim leases and `FOR UPDATE SKIP LOCKED`, and adds the currently held runtime epoch plus a new per-attempt `request_id`/transport expiry to each dispatch. After a correlated C++ acceptance of runtime-first work, one transaction applies the canonical trip/current-plan/proposal-decision mutation, advances trip revision and the finalized mutation watermark, and marks the intent/outbox entry applied. After a terminal C++ rejection/expiry, one transaction advances the finalized watermark and marks the intent terminal without changing canonical trip/current-plan data or trip revision; a stale proposal may be marked `stale` as proposal metadata. Only then does the backend emit `planner_applied`, `rejected`, or `expired`, send cumulative `ConfirmFinalizedMutations`, and admit the next durable command. Resolved outbox rows remain replayable until a snapshot covering their finalized sequence commits; command-intent outcome rows remain for the trip lifetime.

Canonical-first user-edit transactions are deliberately different:

1. `create_trip` inserts the authenticated user's trip, normalized activities, command outcome, immutable user-authored plan revision 1, and `current_plan_id` in one transaction. It starts with trip revision 1 and finalized mutation sequence 1. An inactive trip needs no outbox row; its first bootstrap loads the complete authoritative state and watermark.

   This paragraph specifies the completed V1 WebSocket `create_trip`. It does not
   govern the additive V1.5 HTTP saved-trip operation. `POST /api/v1/trips` uses
   the separate relative saved-plan authority and deliberately has no
   `current_plan_id`, absolute activity rows, command intent, or mutation sequence
   until activation materializes an execution plan, as specified normatively in
   `plans/LiveRouteV15HTTPContract.md`.
2. `replace_current_plan` locks the trip, validates the idempotency digest and expected trip revision, stores a new immutable `user_authored` plan revision, advances the trip revision/mutation/finalized watermarks, updates `current_plan_id`, marks pending proposals superseded, and records the command as applied in one transaction.
3. `trip_edited` uses the same canonical-first shape: atomically apply the normalized add/replace/remove/reorder operation and a complete user-authored resulting current plan over the post-edit activity set. This prevents an activity edit from leaving the immutable current plan structurally inconsistent.
4. Each post-creation transaction stores the new immutable plan id on its retained canonical command intent and inserts an ordered `CurrentPlanReplaced` or `TripEdited` outbox event carrying the prior expected trip revision, even when the trip is inactive. The typed intent identity—not the opaque outbox JSON or the possibly newer live trip pointer—is compared with C++ acknowledgement field 10. This lets a later snapshot-based activation replay the canonical-first change or resolve it through a full canonical bootstrap. The backend emits `canonical_committed` after commit; it does not wait for C++ approval.
5. C++ treats a structurally valid canonical-first event as authoritative input, updates its mirror, invalidates older proposals/work, and acknowledges the sequence. The backend then marks delivery accepted, sends finalization confirmation, and emits `runtime_synced`. An unexpected C++ domain rejection is an `INTERNAL` compatibility fault: preserve the PostgreSQL trip/current plan, pause later runtime-first dispatch, and recover by corrected code/data plus full bootstrap rather than rolling back the user choice.
6. If restart or stream loss occurs first, a full bootstrap from PostgreSQL may cover the already-finalized edit sequence; after verifying the returned watermark/current-plan id, the backend marks the mirror outbox row accepted idempotently.

The backend validates user plan shape and safety boundaries before commit: canonical identifiers, ownership, finite/ranged values, allowed time zones, known activities exactly once, ordered non-overlapping segments, `start < end`, size limits, and optimistic trip revision. It does not reject the plan for being nonoptimal or for travel-time, operating-hours, reservation, or deadline infeasibility. Those conditions are retained as facts for C++ warnings and suggestions. The client never supplies trusted route duration/distance, provider quality, planner reasons, or planner source versions in a current-plan write.

When C++ returns a still-current proposal whose source mutation sequence is finalized, the backend transactionally inserts its immutable `plan_proposals` row and marks any older pending proposal superseded before publishing `plan_proposal`. Accepting a fresh proposal uses the runtime-first path: after C++ freshness acknowledgement, finalization derives a new immutable `accepted_engine_proposal` current-plan revision from the stored proposal, updates the current pointer, and marks the proposal accepted atomically. Rejection marks the proposal rejected and leaves the current pointer unchanged. Proposal history and current-plan history are retained for the trip lifetime.

Transient durable dispatch uses full-jitter capped exponential retry from 250 ms to 30 s and does not stop after a fixed attempt count. Attempts stop only on an acknowledged terminal/applied outcome, trip deletion, or an explicit paused-internal administrative repair state. Optional logical expiry is carried to C++ and becomes a terminal acknowledged `COMMAND_EXPIRED`; it is distinct from the fresh per-attempt gRPC deadline. Attempt timeout/cancellation never silently abandons recorded work.

Snapshot transaction:

1. Require accepted and confirmed-finalized C++ watermarks to match; otherwise handle `SNAPSHOT_NOT_READY`. Reject a snapshot older than stored compatible metadata or ahead of PostgreSQL's finalized mutation watermark.
2. Verify schema version, declared size, and checksum.
3. Store the snapshot and metadata.
4. Retain the two newest non-invalid schema-v1 snapshots; insert the new valid snapshot before deleting any older compatible snapshot.
5. Prune terminal outbox rows covered by the snapshot; never prune `command_intents` before trip deletion.
6. Commit snapshot retention and outbox pruning atomically.

Snapshots occur after meaningful durable boundaries, before clean deactivation, and periodically according to configured time/event thresholds. They are not synchronously written for every GPS update.

When PostgreSQL is unavailable, new durable commands, user trip/current-plan edits, trip activation, lease changes, and proposal decisions return `DURABILITY_UNAVAILABLE`. Unstored C++ proposals are not published. Already-active trips continue bounded telemetry/replanning only while their lease is certainly valid; successful non-proposal responses carry `recovery_state = NOT_ADVANCING`. When the lease expires, the backend stops dispatching that trip until PostgreSQL recovers. The last committed user plan remains authoritative throughout the outage.

## Failure Matrix

| Failure | Required v1 behavior |
| --- | --- |
| Client disconnect | Keep durable command outcome; reconnect and resynchronize current state/outstanding command outcomes |
| Slow client | Coalesce replaceable messages; bounded buffer; close with retryable reason if essential output cannot queue |
| Backend process restart | Reacquire higher runtime epoch, discard all non-durable observations, restore durable snapshot, wrap uncovered outbox mutations in the new epoch, and replay them; first new telemetry uses observation sequence 1 |
| gRPC stream break | Drop obsolete telemetry, reconnect with backoff, bootstrap, replay durable work idempotently |
| C++ process restart | Start empty; reject events for inactive trips until backend bootstrap completes |
| Stale backend lease holder | Reject by `runtime_epoch` without state mutation |
| Shard queue full | Type-specific explicit overload; no unbounded spill queue |
| OSRM timeout/unavailable | Cancel provider work; use explicitly allowed labeled cache or return `PROVIDER_UNAVAILABLE` |
| Planner wall-clock deadline | Return `OK` + `BEST_SO_FAR` when a valid plan exists; otherwise `DEADLINE_EXCEEDED` |
| Beam/candidate budget exhausted without a complete candidate | Return `OK` + `NO_NEW_PROPOSAL`, `NONE`, and `DEADLINE_BUDGET`; never claim `INFEASIBLE` |
| Untruncated finite search exhausted without a complete candidate | Return `INFEASIBLE` with `INFEASIBLE_SCHEDULE` and `NO_FEASIBLE_PLAN` |
| Result superseded in flight | Discard by generation/version check; schedule one latest replacement if still required |
| PostgreSQL unavailable | Preserve the last committed current plan; reject new plan writes and do not publish unstored proposals; continue `NOT_ADVANCING` telemetry only for already-active trips with a certainly valid lease, then stop at lease expiry |
| C++ unavailable during user edit | Commit the authoritative trip/current plan in PostgreSQL, report `canonical_committed` with runtime sync pending, and converge by ordered replay or full bootstrap; never roll back the user edit |
| Proposal persistence fails | Keep the current plan unchanged, withhold the proposal from clients, and retain at most one still-current unpublished proposal for bounded retry |
| Snapshot incompatible/corrupt | Reject snapshot; restore previous compatible snapshot and replay remaining durable mutations |
| Graceful shutdown | Stop admission, cancel advisory/telemetry work, drain durable acknowledgements within shutdown deadline, checkpoint or leave replayable outbox |

## Configuration Contract

Configuration must cover and validate:

- shard count and active-trip/memory limits
- per-priority shard queue capacities and fairness burst
- provider and planner worker/concurrency limits
- per-profile OSRM endpoint, matrix limit, connect/total timeout, and response-byte limit
- gRPC stream pool size, message limit, inbound/outbound capacities, reconnect backoff bounds
- WebSocket frame/decoded-message limits, inbound/outbound capacities, per-connection admission rate, allowed origins, and heartbeat/idle timeouts
- per-trip backend pending runtime-first command and unresolved canonical-mirror capacities
- PostgreSQL pool, outbox batch/in-flight limits, lease duration/renewal/safety margin, and snapshot thresholds
- planner deadline/candidate/search budgets
- Phase 17 route-cache enablement, policy version, coordinate/time
  quantization, shard/entry/byte bounds, fresh/stale TTLs, and eviction-scan bound
- shutdown deadline

Invalid, zero, unbounded, or mutually inconsistent resource configuration fails startup. Secrets enter through the deployment secret mechanism and never appear in repository config or logs.

Phase 16 raw and aggregate benchmark reports use the exact versioned JSON
artifacts and merge rules in `plans/LiveRouteV1ContractSpec.md`. Percentiles are
derived from merged fixed-bucket counts, never averaged from per-run percentile
values, and reports never aggregate mismatched workload/build/hardware/provider/
planner/cache dimensions.

Phase 18 allocation acceptance is scoped from planner-input assembly through
stored-proposal construction on one planner worker. Benchmark-only global
allocation hooks are gated by thread-local attribution; global process activity
and the serving binary are not instrumented. The exact baseline suite, relative
targets, latency/throughput guards, and optimization-ledger evidence are normative
in `plans/LiveRouteV1ContractSpec.md`.

Phase 19 preserves the public array-of-structures planner input and derives one
private worker-owned structure-of-arrays view per valid attempt. Only bounded
candidate-generation/scoring/search helpers consume it; proposal reconstruction
and all service boundaries remain unchanged. The exact columns, flattened-window
ownership, native timing suite, checksum-pinned Callgrind workflow, quantitative
retention gates, and mandatory revert-on-neutral policy are normative in
`plans/LiveRouteV1ContractSpec.md`.

Phase 20 changes no public planner, transport, storage, provider, cache, budget,
or overload contract. Its validate-once, lower-bound scratch, and partial-beam
selection candidates are private benchmark-selectable strategies. Serving uses
only a mask that passes the predeclared planner-tail correctness, work-count,
allocation, throughput, and p99 gates; a measured rejection leaves the accepted
mask-zero AoS path authoritative.

Compose readiness is dependency-aware: PostgreSQL must answer `pg_isready` at the expected Goose migration version; each OSRM profile must pass a fixed Table request; C++ must report `SERVING` through the standard gRPC health service; backend `/healthz` reports process liveness while `/readyz` requires PostgreSQL, migrations, a `StreamReady` planner connection, and both OSRM profiles. Container-first development requires Docker/Compose on the host, not host installation of PostgreSQL, Go, C++, Protobuf, or OSRM packages.

## Implementation Gates

One agent may implement all V1 components in one continuous effort; numeric roadmap phase order is not mandatory. A single unverified all-components batch is not acceptable because wire/storage/recovery failures would be difficult to isolate and could force broad rework. These are correctness gates, not staffing or commit-count requirements:

1. Create the container-first workspace, transcribe the exact contract specification into Protobuf/JSON Schema/SQL/config artifacts, pin inputs, and pass schema/migration/compatibility/readiness checks.
2. Implement internal C++ domain types, `TravelTimeMatrix`, seeded normalized hours, bounded sharded runtime, deterministic providers, and a planner stub; pass ordering, idempotency, epoch, generation-checked commit, cancellation, and overload tests.
3. Implement the bidirectional gRPC stream, Go backend stream pool, bootstrap, finalization-watermark confirmation, reconnect, and replay; pass correlated-response and failure-injection tests.
4. Implement PostgreSQL current-plan/proposal/command-intent/outbox/snapshot/lease transactions and the WebSocket gateway; pass end-to-end user-plan authority, proposal isolation/decision, durable command, authentication, telemetry, reconnect, and crash-window tests.
5. Implement OSRM and the deadline-bounded beam-search suffix replanner without changing planner interfaces; pass provider and planner correctness tests.
6. Add GPS triggering/coalescing, notification decisions, observability, full failure/load tests, and measured configuration tuning.
7. Add bounded caching and allocation/data-layout optimizations only with before/after evidence.

An agent may work ahead across gates locally, but completion of a later component does not waive an earlier contract or recovery check.

## V1 Architecture Acceptance Criteria

- Every serving milestone uses the concurrent sharded runtime and bidirectional planner stream.
- Complete Protobuf and WebSocket JSON Schemas pass compatibility tests before handler implementation.
- Generated schemas match every field number/type/status/close-code rule in `plans/LiveRouteV1ContractSpec.md`.
- Durable commands survive backend/C++ restart without double application.
- A structurally valid user-authored plan becomes and remains authoritative after PostgreSQL commit even when C++ is unavailable; C++ never replaces it without a user command.
- Every published engine proposal is stored separately from the current plan, identifies its base current plan/source versions, and cannot change `current_plan_id` until a fresh user acceptance finalizes.
- A command retry remains idempotent after its covered outbox row is pruned.
- Lease-neutral outbox work replays under the backend's current epoch after restart.
- Per-attempt deadlines may retry without expiring durable intent; only optional logical command expiry terminates it.
- C++ snapshots only when accepted and PostgreSQL-finalized mutation watermarks match; finalization confirmation recovers idempotently after lost responses/restarts.
- Telemetry bursts remain bounded and converge to the latest accepted observation.
- Higher epochs discard all old telemetry/active proposals/work, mark still-pending durable proposal records stale, and reset epoch-scoped observation/planner versions; same-epoch reconnect preserves them.
- Same-trip mutations are ordered; different trips progress concurrently.
- Shards never block on OSRM, planner execution, PostgreSQL, WebSocket, or gRPC writes.
- Stale planning results cannot overwrite newer trip state.
- PostgreSQL and transport types do not enter the planner.
- The planner candidate loop performs only bounded in-memory work and immutable matrix/constraint lookups.
- Every queue, message, trip, provider call, and planner search has an explicit bound or deadline.
- Essential response capacity is reserved before state mutation; shards never wait on network output.
- The V1 local trust boundary enforces authentication, trip authorization, origins, size/admission limits, private service ports, and sensitive-log redaction.
- Planner inputs are UTC Unix times normalized with pinned US IANA tzdata; output retains IANA zones for display conversion.
- OSRM car/foot integration, stream reconnect, C++ restart, PostgreSQL outage, overload, cancellation, and snapshot replay have automated integration tests.
- Latency reports separate WebSocket/backend, persistence, gRPC, queueing, provider, planner, serialization, and total time.

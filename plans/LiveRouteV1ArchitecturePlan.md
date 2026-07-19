# LiveRoute V1 Architecture Contract

## Status and Scope

This document fixes the v1 service boundaries and runtime semantics needed to begin implementation. It is authoritative for cross-component behavior; the implementation plan and feature roadmap describe component work and sequencing.

V1 is:

```text
WebSocket client <-> Backend gateway
                       |-- PostgreSQL
                       |     users, trips, saved plans, snapshots,
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

The backend implementation language is intentionally not part of this contract. The WebSocket, Protobuf, PostgreSQL, and acknowledgement contracts allow that choice without changing the C++ planner or client protocol.

## Ownership Boundaries

- The WebSocket client owns its local presentation state, if any, and generates an idempotency key for every command.
- The backend authenticates users, authorizes trip access, owns WebSocket sessions, assigns durable per-trip mutation sequence numbers, and is the only component that accesses PostgreSQL.
- PostgreSQL is the durable source of truth for users, trip definitions, accepted/saved plans, planner recovery snapshots, uncheckpointed durable planner mutations, and active-trip lease epochs.
- The single V1 backend holds the active-trip leases. Each lease carries a monotonically increasing `runtime_epoch`; stale epochs are rejected by the C++ service. This fencing remains mandatory across backend restarts even though horizontal backend scaling is deferred.
- The C++ service owns mutable active trip state in memory. Each trip belongs to exactly one shard, and only that shard mutates the trip.
- OSRM is an external provider from the planner's perspective. Only `OsrmTravelTimeProvider` performs OSRM HTTP and JSON work.
- The planner receives immutable internal C++ inputs, including `TravelTimeMatrix`; it never sees Protobuf, WebSocket, PostgreSQL, HTTP, JSON, or OSRM types.

## Delivery and Acknowledgement Semantics

LiveRoute uses at-least-once delivery plus idempotency for durable commands. It does not claim exactly-once delivery.

| Input class | Examples | PostgreSQL | Backend-to-C++ replay | Overload behavior |
| --- | --- | --- | --- | --- |
| Durable mutation | activity lifecycle, trip edit, reservation/deadline change, accepted/rejected plan | Record command intent/outbox before dispatch; finalize domain mutation after C++ acceptance | Retry until applied or rejected terminally | Return retryable overload; never silently drop |
| Telemetry/latest value | location, velocity, heading | Do not persist every sample | Do not replay obsolete samples | Coalesce or drop with explicit status |
| Advisory | weather/crowd/recommendation refresh | Persist only if independently required by product data | Best effort | Delay or drop with explicit status |
| Planner snapshot | state needed to rehydrate an active trip | Persist asynchronously with version metadata | Used as bootstrap base | Retain previous compatible snapshot on failure |
| Saved/accepted plan | user-selected durable plan | Record intent, validate/apply in C++, then finalize saved plan | Retry mutation until applied | Reject command if durability is unavailable |

Backend acknowledgements are two-stage:

1. `durable_recorded`: the command intent and planner-outbox record committed atomically before dispatch.
2. `planner_applied`: the C++ shard accepted the command and returned its resulting state version; the backend then finalized the durable domain mutation and outbox state atomically. A terminal C++ rejection instead finalizes the intent as rejected without changing canonical trip/plan data.

Telemetry receives only an `accepted`, `coalesced`, `dropped`, or `overloaded` admission status. It has no durability guarantee.

The backend reports a durable command as complete only after the second transaction commits. If either process crashes between the two stages, the recorded intent is replayed and the C++ idempotency result allows finalization. The backend keeps applied durable outbox rows until a persisted planner snapshot covers their mutation sequence. A snapshot transaction stores the new snapshot and prunes covered outbox rows atomically.

`command_intents` is the durable idempotency and outcome history; it is separate from the prunable delivery outbox. V1 retains command-intent rows for the lifetime of the trip. Reusing a `message_id` with the same canonical payload returns the recorded pending or terminal outcome. Reusing it with a different payload is a terminal `IDEMPOTENCY_KEY_REUSED` error. Deleting a trip deletes its command-intent history in the same controlled lifecycle. Duplicate client commands, outbox deliveries, gRPC messages, and responses are therefore safe even after covered outbox rows have been pruned.

## Client WebSocket Contract

V1 uses UTF-8 JSON text frames. A checked-in `liveroute.v1` JSON Schema is the normative wire schema and must exist before the WebSocket handler is implemented. The backend enforces the configured frame limit before JSON parsing, rejects a missing or unsupported protocol version, and ignores unknown optional fields only when the schema marks them as forward-compatible.

Every client message has:

- `protocol_version`
- `message_id` generated by the client
- `kind`
- `payload`

Trip-scoped messages additionally require `trip_id`. Connection-scoped `authenticate`, `ping`, and `pong` messages omit `trip_id`. A `message_id` is an opaque string: it is an idempotency key for commands and a correlation identifier for other client messages.

Every server message has `protocol_version`, `server_message_id`, `kind`, `status`, `retryable`, and `payload`, plus `in_reply_to_message_id` when it responds to a client message. Trip-scoped server messages also include `trip_id`, `trip_revision`, `planner_state_version`, accepted mutation/observation watermarks, and the current `runtime_epoch` when an active runtime exists.

Client-to-server message kinds are:

- authenticate
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
- command acknowledgement (`durable_recorded`, `planner_applied`, or terminal rejection)
- telemetry admission status
- planner notification
- revised plan
- trip state resynchronization
- structured error
- ping
- pong

The first non-ping client message must be `authenticate`; normal messages are accepted only after `connection_ready`. There is no global WebSocket ordering guarantee. Clients use `message_id`, `trip_revision`, `planner_state_version`, and sequence watermarks rather than frame arrival order to determine freshness.

Reconnect uses resynchronization, not an unbounded WebSocket replay log. The client reconnects, reauthenticates, supplies its last observed trip/planner versions and a configured-bounded list of outstanding command `message_id` values, and subscribes again. The backend returns the current durable trip, the durable outcome of each requested command intent, and the latest active planner state/plan. If the trip is not active, the backend first rehydrates it before reporting planner state. Repeated commands use `message_id` idempotency.

Each connection has bounded inbound admission and a bounded outbound buffer. Replaceable progress/telemetry notifications may be coalesced. Durable acknowledgements and terminal errors are not silently dropped; if they cannot be delivered, the backend closes the connection with a retryable reason so the client resynchronizes. Protocol/authentication/authorization violations close with a non-retryable reason; capacity or transient service failures close with a retryable reason.

## V1 Security and Trust Boundary

V1 is a local development/demo deployment, not an Internet production deployment. Its minimum security contract is:

- Docker Compose exposes only the backend on loopback by default; PostgreSQL, C++, and OSRM remain on the private network.
- Plain `ws://` is allowed only on loopback. Any non-loopback deployment must terminate TLS and expose `wss://`; internal mTLS and production certificate automation are future deployment work.
- The V1 `authenticate` payload carries an opaque development bearer token mapped to a seeded user. The token enters through the deployment secret mechanism, is never placed in a URL, and is redacted from logs. User-facing login and external identity-provider integration arrive with or after the React V1.5 client.
- The backend authorizes trip ownership on every subscribe, resynchronize, command, and telemetry message; authentication alone never grants trip access.
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
  }
}
```

The envelope field numbers above and the following payload semantics are fixed. During architecture-first implementation step 2, the repository must add the complete payload messages with exact field types and numbers under `liveroute.v1`, plus descriptor-based compatibility tests. Those checked-in `.proto` files are the normative wire schema; no gRPC handler is implemented before that schema review. Once assigned, field numbers are never reused.

| Message | Required content |
| --- | --- |
| `OpenStream` | backend instance id, protocol version, supported capability flags |
| `StreamReady` | C++ instance id, accepted protocol version/capabilities, configured message/resource limits safe to expose |
| `BootstrapTrip` | snapshot schema/version/checksum and bytes when available; otherwise full trip definition; checkpoint mutation watermark; current observation; backend trip revision |
| `ApplyTripEvent` | event id, event occurrence time, and exactly one typed event delta; never a complete mutable trip |
| `EventAcknowledged` | disposition, retryability, resolved mutation/observation sequence, resulting planner state/trip versions, whether replanning was scheduled |
| `RequestSnapshot` | reason and minimum state/mutation watermark requested |
| `TripSnapshot` | schema version, runtime epoch, planner state version, covered finalized mutation sequence, payload size, checksum, serialized snapshot bytes |
| `DeactivateTrip` | reason and whether a final snapshot is required |
| `ReplanResult` | plan id, source planner state version/generation, preserved prefix, revised suffix, skipped/shortened/moved activities, notification decision, structured reasons, planner/provider stats, degraded flags |
| `PlannerError` | stable status enum, retryable flag, safe diagnostic text, related state/sequence versions |

Envelope validity is message-specific:

| Request payload | Required envelope fields | Fields that must be absent/zero |
| --- | --- | --- |
| `OpenStream` | `request_id` | `trip_id`, epoch, both sequences, both expected versions, expiry |
| `BootstrapTrip` | `request_id`, `trip_id`, current `runtime_epoch`, expiry | both sequences and both expected versions |
| durable `ApplyTripEvent` | `request_id`, `trip_id`, current epoch, `mutation_sequence`, `expected_trip_revision`, expiry | `observation_sequence`; expected planner version unless this is a plan decision |
| telemetry/advisory `ApplyTripEvent` | `request_id`, `trip_id`, current epoch, `observation_sequence`, expiry | `mutation_sequence` and both expected versions |
| `RequestSnapshot` or `DeactivateTrip` | `request_id`, `trip_id`, current epoch, expiry | both sequences and both expected versions |
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
- user accepted or rejected a proposed plan
- recommendation refresh, weather, crowd, or social advisory update

Event priority is derived by the C++ admission/domain layer from event type and current trip state; callers cannot promote their own work.

Trip/bootstrap domain messages contain opaque IDs, coordinates, travel mode, activity ordering, completed prefix/current activity, fixed or flexible classification, user priority/utility, reservation data, normalized open windows, minimum/preferred/maximum duration, movable/shortenable/skippable/mandatory flags, and current plan/version metadata. Times crossing the boundary are UTC epoch values; named time zones are retained in durable trip data for display and future normalization, but planner constraints are normalized before candidate search.

Itinerary segments contain activity id, location, scheduled start/end, inbound route duration/distance/reachability, disposition, and structured reason codes. Metrics use integer durations with documented units. Protobuf messages are converted into validated internal C++ types before shard admission; generated message objects are not stored in `TripState` or passed to planner search.

Plan acceptance and rejection are durable trip events. An acceptance includes `plan_id` and `source_planner_state_version`, and is rejected as stale when either no longer matches the active proposal.

Protocol rules:

- `request_id` correlates asynchronous acknowledgements and results. Responses may complete out of input order.
- `runtime_epoch` rejects messages from a stale backend lease holder.
- `mutation_sequence` orders durable commands and must be contiguous after bootstrap.
- A terminally rejected durable command still resolves and consumes its mutation sequence without incrementing planner state version, so later commands do not deadlock behind a gap.
- `observation_sequence` orders replaceable telemetry; gaps are valid and older observations are ignored.
- `trip_revision` changes only when a durable canonical mutation is accepted. The backend records the current value as `expected_trip_revision`; C++ accepts the event only when its mirrored revision matches, then advances its mirror by one. PostgreSQL advances to that same value during finalization. A terminal rejection leaves both revisions unchanged.
- `planner_state_version` increases for every accepted change to C++ trip state, including telemetry, and is independent of `trip_revision`.
- `expected_planner_state_version` is present only for accepting/rejecting a specific proposal or another explicitly compare-and-set planner operation. Ordinary durable trip mutations do not set it, so intervening telemetry cannot create a false version conflict.
- The C++ runtime has an internal `planning_generation` that increases whenever accepted input invalidates an in-flight plan.
- A proposed plan is identified by `plan_id` and `source_planner_state_version`; a plan decision must match both the current proposal and its source version.
- Per-message deadlines use `expires_at_unix_ms` because an RPC deadline cannot express independent deadlines on a long-lived stream. The C++ boundary rejects expired messages and converts remaining time to a monotonic internal deadline. Deployed hosts require synchronized clocks.
- Message size limits are configured and enforced before domain conversion. Oversized bootstrap/snapshot messages receive `RESOURCE_EXHAUSTED`.
- One read and one write may be in flight per C++ callback reactor. Additional outbound messages wait in a bounded per-stream queue.

The single V1 backend uses a fixed, bounded stream pool to the single C++ process. Trips are consistently assigned to a stream for connection locality; C++ trip sharding remains authoritative for state ownership. Stream count and queue capacity are configuration validated at startup, not request-driven.

On stream failure:

- The backend reconnects with bounded exponential backoff and jitter.
- Obsolete telemetry is discarded or replaced by the newest sample.
- Durable outbox entries remain pending and are resent at least once.
- Before replay, the backend verifies or renews its existing lease; if that lease is no longer valid, it acquires a higher epoch. It then bootstraps the trip from the latest snapshot and covered mutation watermark and sends uncovered durable mutations as bounded individual stream messages.
- Durable outbox payloads are lease-neutral. On every dispatch or replay, the backend reads its current lease and wraps the unchanged command id, mutation sequence, and event payload in the current `runtime_epoch`; an epoch stored for audit is never reused as dispatch authority.
- The C++ service treats duplicate bootstrap/events idempotently and rejects stale epochs or versions.

Terminal statuses include accepted, duplicate, stale sequence, version conflict, stale epoch, inactive trip, invalid argument, resource exhausted, deadline exceeded, cancelled, infeasible, provider unavailable, and internal error. Retryability is an explicit field, not inferred from display text.

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
2. The shard validates `runtime_epoch`, mutation/observation sequence, expected trip revision, optional expected planner state version, and event idempotency according to the message-specific rules above.
3. The shard applies the accepted event, increments `planner_state_version`, and increments `planning_generation` if the event invalidates planning input.
4. If no full replan is required, the shard emits an acknowledgement/state update immediately.
5. If replanning is required, the shard captures an immutable `PlanningInput` tagged with trip id, state version, planning generation, deadline, and cancellation source.
6. Route/hours acquisition runs off-shard through bounded provider executors and produces immutable normalized inputs.
7. A planner worker searches only in memory and returns best-so-far with the captured tags.
8. The result returns to the owner shard. The shard commits it only when runtime epoch, trip identity, state version, and planning generation still match.
9. A stale result is discarded. If newer accepted state still requires planning, the shard schedules one replacement using the latest coalesced trigger.
10. The shard records the latest committed plan in `TripState` and emits it through the trip's current stream binding when one exists.

State mutation is never rolled back because a plan became stale or OSRM failed. Provider/planner outcome is represented separately from whether the event was accepted. A proposed plan has a `plan_id` and `source_planner_state_version`; acceptance is rejected if that proposal is no longer current.

A stream is only a transport binding, not state ownership. Bootstrap binds the trip and epoch to the current stream; a stream break invalidates that binding. An in-flight result that commits after a reconnect uses the new binding. If no binding exists, C++ retains the latest committed plan in the bounded `TripState` and returns it during the next idempotent bootstrap/resynchronization.

Higher-priority state changes request cooperative cancellation of superseded work. Cancellation is an optimization; generation checking is the correctness mechanism.

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

All capacities and worker counts are explicit configuration with safe upper bounds and startup validation. Initial values are selected from measured representative workloads; this plan does not invent universal numeric defaults.

Critical/high work receives preference, but the dispatcher admits normal work after a configured bounded priority burst. Queue-full behavior is type-specific:

- durable mutation: retryable rejection; backend retains outbox entry
- telemetry: replace pending latest value, otherwise drop explicitly
- advisory: delay or drop explicitly
- provider/planner job: reserve its completion slot before dispatch; otherwise return a degraded/resource-exhausted result without blocking the shard
- essential gRPC response: reserve one outbound slot before request admission; if no slot is available, refuse admission and close that stream with retryable `RESOURCE_EXHAUSTED` so a committed acknowledgement is never stranded
- replaceable replan/progress result: retain only the latest bounded value per trip and coalesce it into the current stream or next bootstrap

Essential responses are event acknowledgements, terminal errors for admitted durable events, requested snapshots, and deactivation results. Reservations move with the response from admission through the single in-flight gRPC write and are then released. A stream failure may still lose bytes in transport, so the backend recovers durable outcomes by replay; capacity exhaustion itself never causes post-commit loss. Shard threads never wait for an outbound slot or network write.

## Active Trip Lifecycle

- A backend must hold the PostgreSQL runtime lease before `BootstrapTrip`.
- Bootstrap carries `runtime_epoch`, the latest compatible snapshot, and its mutation watermark. Subsequent durable mutations are replayed as bounded individual messages.
- Bootstrap is idempotent for the same epoch and snapshot version. A higher epoch replaces older ownership; a lower epoch is rejected.
- Active-trip count, activities per trip, snapshot bytes, and pending work per trip are bounded. Capacity exhaustion is explicit.
- V1 does not rebalance a live trip between C++ shards. Changing shard count requires a controlled service restart and backend rehydration.
- Backend-directed deactivation is preferred. Idle eviction is allowed only with no running work and requires a final snapshot response before the trip is removed.
- If final snapshot persistence fails, the backend retains replayable outbox state and reports degraded durability; it does not claim successful checkpointing.
- C++ restart begins empty. The backend verifies its current leases or acquires higher epochs where needed, then rehydrates active trips from PostgreSQL.

## OSRM Provider Contract

### Deployment

- Run OSRM locally through the repository's container workflow; do not require host package installation.
- Pin the OSRM image version and the OSM extract URL/checksum used for repeatable development and tests.
- Build separate datasets and `osrm-routed` instances for the supplied car and foot profiles because profiles are applied during preprocessing, not selected dynamically at query time.
- Use the same bounded geographic extract for both profiles and MLD preprocessing for v1.
- Configure distinct internal endpoints for driving and walking. The C++ provider maps `TravelMode` to an endpoint; planner code does not.
- Readiness requires each profile endpoint to answer a fixed small Table request successfully. Container liveness alone is insufficient.

### Request and response policy

- Use the documented HTTP GET Table endpoint with `annotations=duration,distance`.
- Send current location plus remaining unique activity locations in deterministic order.
- Validate finite longitude/latitude ranges, supported mode, total locations, and estimated request size before I/O.
- Configure a LiveRoute matrix-location limit no greater than the OSRM `--max-table-size`; reject larger work with `MATRIX_TOO_LARGE` in v1 rather than issuing unbounded/chunked work.
- Use libcurl multi behind a small provider wrapper to obtain connection reuse, bounded asynchronous I/O, timeout handling, response-byte limits, and cooperative cancellation.
- Do not automatically retry OSRM inside a latency-bounded live request. A retry would consume the same deadline unpredictably. Health checks and later events provide recovery.
- Require OSRM `code == Ok`, exact matrix dimensions, nonnegative finite values, and bounded numeric conversion.
- A `null` duration/distance cell is represented as `reachable == false`; v1 does not fabricate straight-line fallback travel times.
- Malformed JSON, unexpected dimensions, `NoSegment`, `TooBig`, timeout, cancellation, and non-success transport status map to structured provider errors.
- Record OSRM transport, parse, matrix-conversion, and total provider latency separately from planner latency.

The correctness-first path is uncached. The later bounded cache is keyed by profile, normalized coordinates, and OSRM dataset version. Stale cache use is allowed only under an explicit degraded policy and is labeled in the result; it is never silently presented as fresh routing data.

Dataset updates build new profile artifacts out of band, pass readiness/integration checks, then replace endpoints. A dataset-version change invalidates incompatible cache entries.

## PostgreSQL Durability and Recovery

Minimum logical tables are:

- `users`
- `trips` and normalized trip/activity constraints
- `saved_plans`
- `command_intents`
- `planner_snapshots`
- `planner_outbox`
- `trip_runtime_leases`

Required constraints include unique client command id per trip in `command_intents`, a canonical payload digest for idempotency-key reuse detection, unique durable mutation sequence per trip, monotonically increasing trip revision/runtime epoch, one current lease holder, and snapshot metadata containing planner schema version, planner state version, covered mutation sequence, and checksum.

The backend serializes durable commands per trip and allows at most one recorded unresolved durable mutation at a time. Later commands wait without a durability acknowledgement in a bounded in-memory per-trip queue; a full queue receives retryable overload, and a backend restart requires those unacknowledged clients to retry. Telemetry may continue under its independent observation sequence. This keeps PostgreSQL revision finalization and C++ mutation ordering identical.

Lease acquire/renew transactions use PostgreSQL server time and atomically set holder id, expiry, and a strictly higher epoch on acquisition. The backend renews before a configured safety margin; an uncertain or late renewal is treated as expired. On expiry it stops trip dispatch before attempting another acquisition. This simple fencing rule is retained for restart correctness even with one V1 backend process.

Durable command recording transaction:

1. Lock/compare the trip revision.
2. Detect duplicate client command id. Return the stored state/outcome when the payload digest matches; reject `IDEMPOTENCY_KEY_REUSED` when it differs.
3. Validate the command against durable backend rules without changing canonical trip/saved-plan state.
4. Allocate the next mutation sequence and record the current trip revision as `expected_trip_revision`.
5. Insert the pending command intent and a lease-neutral planner outbox payload containing request id, trip id, payload digest, expected trip revision, mutation sequence, and event data. Do not persist an epoch as future dispatch authority.
6. Commit, then emit `durable_recorded` to the client.

The outbox dispatcher uses bounded batches and in-flight limits, and adds the currently held lease epoch to each gRPC dispatch. After a correlated C++ acceptance, one transaction applies the canonical trip/saved-plan mutation, advances the trip revision by one, and marks the intent/outbox entry applied. After a terminal C++ rejection, one transaction marks the intent rejected without applying the canonical mutation or advancing trip revision. Only then does the backend emit `planner_applied` or `rejected` and admit the next durable command for that trip. Resolved outbox rows remain replayable until a snapshot covering their sequence commits; command-intent outcome rows remain for the trip lifetime.

Snapshot transaction:

1. Reject a snapshot older than the stored runtime epoch, planner state version, or finalized mutation watermark; do not checkpoint unfinalized command intents.
2. Verify schema version, declared size, and checksum.
3. Store the snapshot and metadata.
4. Prune covered outbox rows.
5. Commit atomically.

Snapshots occur after meaningful durable boundaries, before clean deactivation, and periodically according to configured time/event thresholds. They are not synchronously written for every GPS update.

When PostgreSQL is unavailable, new durable commands, trip activation, lease changes, and plan acceptance fail explicitly. Already-active trips continue bounded telemetry/replanning in degraded mode until their existing lease can no longer be safely renewed; every response states that recovery state is not advancing. When the lease expires, the backend stops dispatching that trip until PostgreSQL recovers.

## Failure Matrix

| Failure | Required v1 behavior |
| --- | --- |
| Client disconnect | Keep durable command outcome; reconnect and resynchronize current state/outstanding command outcomes |
| Slow client | Coalesce replaceable messages; bounded buffer; close with retryable reason if essential output cannot queue |
| Backend process restart | Reacquire higher runtime epoch, restore snapshot, wrap uncovered outbox mutations in the new epoch, and replay them |
| gRPC stream break | Drop obsolete telemetry, reconnect with backoff, bootstrap, replay durable work idempotently |
| C++ process restart | Start empty; reject events for inactive trips until backend bootstrap completes |
| Stale backend lease holder | Reject by `runtime_epoch` without state mutation |
| Shard queue full | Type-specific explicit overload; no unbounded spill queue |
| OSRM timeout/unavailable | Cancel provider work; use explicitly allowed valid cache or return degraded/provider-unavailable result |
| Planner deadline | Return best valid plan found so far when available; otherwise structured deadline/degraded result |
| Result superseded in flight | Discard by generation/version check; schedule one latest replacement if still required |
| PostgreSQL unavailable | Reject durability-dependent commands; continue labeled degraded telemetry only for already-active trips with a still-valid lease, then stop at lease expiry |
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
- per-trip backend pending-command capacity
- PostgreSQL pool, outbox batch/in-flight limits, lease duration/renewal/safety margin, and snapshot thresholds
- planner deadline/candidate/search budgets
- shutdown deadline

Invalid, zero, unbounded, or mutually inconsistent resource configuration fails startup. Secrets enter through the deployment secret mechanism and never appear in repository config or logs.

## Architecture-First Implementation Sequence

1. Create the container-first workspace for PostgreSQL, car OSRM, foot OSRM, backend, and C++ service; pin inputs and add readiness checks.
2. Define internal C++ domain identifiers/versions, complete `liveroute.v1` Protobuf schemas, WebSocket JSON Schemas, compatibility tests, and initial PostgreSQL migrations together.
3. Implement the bounded sharded C++ runtime with an in-memory provider and deterministic planner stub; test ordering, idempotency, generation-checked commit, cancellation, and overload.
4. Implement the bidirectional gRPC stream and backend stream pool; exercise bootstrap, asynchronous correlated responses, disconnect, and replay with a CLI/load client.
5. Implement PostgreSQL command-intent/outbox/snapshot/lease transactions and the WebSocket gateway; complete an end-to-end durable command and telemetry flow.
6. Implement `TravelTimeMatrix`, seeded hours, the OSRM provider, and provider failure tests without changing planner interfaces.
7. Implement the minimal correct suffix replanner, then bounded beam search and planner correctness tests.
8. Add GPS triggering/coalescing, notification decisions, observability, and end-to-end failure/load tests.
9. Tune explicit capacities and worker counts from measurements; add bounded caching and allocation/data-layout optimizations only with before/after evidence.

## V1 Architecture Acceptance Criteria

- Every serving milestone uses the concurrent sharded runtime and bidirectional planner stream.
- Complete Protobuf and WebSocket JSON Schemas pass compatibility tests before handler implementation.
- Durable commands survive backend/C++ restart without double application.
- A command retry remains idempotent after its covered outbox row is pruned.
- Lease-neutral outbox work replays under the backend's current epoch after restart.
- Telemetry bursts remain bounded and converge to the latest accepted observation.
- Same-trip mutations are ordered; different trips progress concurrently.
- Shards never block on OSRM, planner execution, PostgreSQL, WebSocket, or gRPC writes.
- Stale planning results cannot overwrite newer trip state.
- PostgreSQL and transport types do not enter the planner.
- The planner candidate loop performs only bounded in-memory work and immutable matrix/constraint lookups.
- Every queue, message, trip, provider call, and planner search has an explicit bound or deadline.
- Essential response capacity is reserved before state mutation; shards never wait on network output.
- The V1 local trust boundary enforces authentication, trip authorization, origins, size/admission limits, private service ports, and sensitive-log redaction.
- OSRM car/foot integration, stream reconnect, C++ restart, PostgreSQL outage, overload, cancellation, and snapshot replay have automated integration tests.
- Latency reports separate WebSocket/backend, persistence, gRPC, queueing, provider, planner, serialization, and total time.

# LiveRoute V1 Plan Summary

## Overall Topology

```text
React/TypeScript client      V1.5
CLI/load clients              V1
        |
        | WebSocket JSON
        v
Go backend
        |
        | PostgreSQL
        |
        | bidirectional gRPC + Protobuf
        v
C++ planner
        |
        | provider adapters
        v
OSRM / hours provider
```

V1 uses CLI, load, and integration clients. The React/TypeScript browser is planned for V1.5.

## Backend Queues

The backend has bounded handling in both directions:

```text
Client -> Backend:
  bounded per-connection/per-trip inbound admission

Backend -> Client:
  bounded WebSocket outbound queue

C++ -> Backend:
  bounded gRPC response handling/stream admission

Backend -> C++:
  bounded gRPC outbound queue
```

There is not necessarily one global backend inbound queue. The backend reads a message, authenticates and validates it, then admits it into the appropriate bounded per-connection, per-trip, outbox, telemetry, or stream-processing path.

PostgreSQL is different: its outbox is durable retry storage, not a runtime queue.

## Users and Trips

The backend:

1. Authenticates the connection.
2. Associates it with a user.
3. Authorizes access to each trip separately.
4. Applies per-connection and per-trip limits.
5. Serializes durable commands for each trip.

A user cannot access another user's trip merely because they know its ID. Authorization is checked for subscribe, resynchronize, command, and telemetry messages.

Different trips can progress concurrently. For one trip, durable mutations remain ordered. At most one runtime-first mutation is unresolved; a bounded number of canonical-first user edits may commit while C++ is unavailable, and later runtime-first work waits for their ordered mirror/bootstrap convergence.

## Connection Flow

```text
WebSocket opens
  -> client authenticates
  <- backend sends connection_ready
  -> client subscribes or resynchronizes a trip
  -> client sends commands and telemetry
  <- backend sends acknowledgements, plans, notifications, and errors
```

The first non-ping message must be `authenticate`. Ordinary trip messages are not accepted until authentication succeeds. The backend also authorizes the user for each requested trip.

## Client Data

### Telemetry

The client sends location, velocity, heading, and possibly route-deviation observations.

The backend validates and authorizes them, assigns observation sequences, coalesces or drops obsolete samples when overloaded, and sends accepted observations to C++.

The C++ owner shard decides whether an observation indicates route deviation, a geofence/boundary crossing, low deadline slack, or another condition requiring replanning. The backend does not determine route deviation because it does not own authoritative live trip state.

### Manual Trip Edits

For runtime-first changes such as activity lifecycle or reservation edits:

```text
Client
  -> Backend: trip_command
Backend
  -> PostgreSQL: record command intent + outbox
Backend
  -> C++: ApplyTripEvent
C++
  -> applies and validates the event
  -> triggers replanning if appropriate
Backend
  -> PostgreSQL: finalizes canonical trip state
  -> Client: planner_applied
```

User trip/activity editing and creation or replacement of the authoritative current plan are canonical-first:

```text
Client
  -> Backend: create_trip, trip_edited, or replace_current_plan
Backend
  -> validates structure/auth/version
  -> PostgreSQL: commits immutable current-plan revision + current pointer
  -> Client: canonical_committed
Backend
  -> C++: ordered CurrentPlanReplaced mirror when needed
C++
  -> mirrors the user plan; does not approve its optimality
Backend
  -> Client: runtime_synced
```

Each activity-set edit includes the complete resulting user plan, so canonical activities and the immutable current-plan revision cannot diverge. The user state remains canonical if C++ is unavailable. Later runtime-first mutations wait for mirror replay or full bootstrap convergence.

### Initial Trip Input

The client supplies activity names, coordinates, hours, reservations, durations, priorities, and an ordered initial schedule. The backend validates structural invariants and atomically persists the trip plus user-authored current plan. C++ receives the normalized trip/current plan during bootstrap and treats it as the baseline for suggested replanning.

V1 hours come from the versioned seed shape and pinned tzdata defined in `plans/LiveRouteV1ContractSpec.md`; C++ receives only normalized UTC windows. A frontend destination-search or geocoding API is not currently defined in the contract. Adding place lookup would be a separate V1/V1.5 provider decision.

## Lease Ownership

A lease is held by the backend process for a trip, not by each request worker.

V1 has one backend process, but the PostgreSQL lease still protects against stale work after a restart. The backend lease manager renews the lease. Dispatch checks that the lease is still valid before sending work to C++.

The C++ service does not check PostgreSQL for every request. PostgreSQL is accessed only by the backend. C++ receives the current `runtime_epoch` and rejects messages from an older epoch.

One C++ shard owns a trip's mutable state, but that shard worker also handles other trips. A trip is processed sequentially by its owner shard; it does not permanently block the worker or the entire service.

## PostgreSQL

PostgreSQL stores:

- users and authorization data;
- canonical trip/activity data;
- immutable user-selected current-plan history;
- separately stored engine proposals and decisions;
- command intent and outcome history;
- pending planner outbox commands;
- recovery snapshots;
- active-trip leases and runtime epochs.

If the WebSocket closes, the backend uses this durable state to answer a later resynchronization request.

## Snapshots

A snapshot is a serialized checkpoint of the C++ planner's active in-memory state. It allows recovery without replaying the entire trip history.

A snapshot includes durable trip state, planner versions, the authoritative current plan, and finalized mutation watermarks. Proposal history remains separately durable in PostgreSQL rather than inside the snapshot. Temporary provider requests, cancellation objects, stream bindings, and ordinary GPS telemetry are excluded.

Recovery is:

```text
latest compatible snapshot
  -> replay newer uncovered durable commands
  -> wait for a new observation if current telemetry was discarded
```

## Protobuf and gRPC

The backend does not send WebSocket JSON directly to C++. It:

1. Receives JSON from the client.
2. Validates and converts it into a backend command.
3. Serializes a Protobuf message.
4. Sends it over the bidirectional gRPC stream.

C++ deserializes Protobuf at the service boundary, converts it into internal C++ types, and sends Protobuf responses back. The backend then converts those responses into WebSocket JSON.

Protobuf is the agreed binary wire representation and schema; gRPC provides the communication channel. Protobuf types do not enter the planner's internal search loop.

## C++ Responsibilities

C++ is responsible for:

- owning active trip state;
- mirroring initial/current user plans and applying ordered live edits;
- validating event ordering and versions;
- acquiring route and hours data through provider adapters;
- evaluating the user plan and running later suggested replans;
- deciding whether accepted events require replanning;
- discarding stale planner results;
- producing advisory plan proposals;
- deciding planner notification types such as low slack or critical lateness.

The user's selected plan is the ultimate authority. C++ never makes a proposal current on its own. The backend stores each proposal separately before publishing it; only a fresh explicit user acceptance or a direct user-authored replacement creates a new authoritative current-plan revision.

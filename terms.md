# LiveRoute Terms

Concise definitions for the v1 architecture.

## Services and State

- **Service:** A separately running software process, such as the backend, C++ planner, PostgreSQL, or an OSRM instance. Multiple services can run on one computer using containers.
- **V1 deployment:** One backend process and one C++ planner process plus PostgreSQL and separate car/foot OSRM instances on a single-host private Docker Compose network. React is a V1.5 client.
- **WebSocket client:** A CLI, load, or integration client in V1, or the React browser client in V1.5, that uses the same backend WebSocket contract.
- **Active trip:** A trip currently loaded into the C++ planner's in-memory runtime for live updates and replanning.
- **Planner snapshot:** A persisted checkpoint of the C++ planner state for an active trip. It is used to restore the trip after a restart; it is not the complete source of truth for the trip.
- **Rehydrate:** Reconstruct the C++ planner's in-memory trip state from a snapshot and newer durable mutations.
- **Bootstrap base:** The initial state loaded when C++ starts managing a trip. The backend then replays durable mutations that occurred after the snapshot.
- **Domain mutation:** A durable change to canonical trip or plan data, such as adding an activity, changing a reservation, or accepting a plan.

## Commands and Delivery

- **Client command:** A requested action sent by a WebSocket client to the backend, such as editing a trip or accepting a plan.
- **Durable client command:** A client command whose intent must survive disconnects and service restarts. The backend records it in PostgreSQL before dispatching it to C++.
- **Durable:** Persisted in a way that can be recovered after a process or connection failure.
- **At-least-once delivery:** A command may be delivered more than once because of retries. The receiver must make duplicate deliveries safe.
- **Idempotency:** Processing the same command again does not apply its state change twice. Client `message_id` values and durable mutation identifiers support this. V1 retains the command outcome for the trip lifetime even after delivery outbox pruning.
- **Command intent:** A durable PostgreSQL record containing a command's `message_id`, canonical payload digest, mutation sequence, and pending or terminal outcome. It is the long-lived idempotency record.
- **Outbox entry:** A durable, lease-neutral PostgreSQL delivery row for a recorded command that still needs C++ delivery or replay. It may be pruned after a compatible snapshot covers it; pruning does not remove the command intent.
- **Dispatch:** The backend taking an outbox entry, wrapping it in the currently held runtime epoch, and sending it to the C++ service.
- **Two-stage acknowledgement:** `durable_recorded` confirms the command is persisted; `planner_applied` confirms C++ accepted it and the backend committed the resulting domain mutation.
- **`durable_recorded`:** The backend has committed the command intent and outbox entry. The command is recoverable, but C++ has not necessarily applied it yet.
- **`planner_applied`:** C++ accepted the command and the backend committed the resulting canonical trip/plan mutation.
- **Terminal rejection:** A final rejection that will not be fixed by retrying the same command, such as invalid input, a stale plan, or an infeasible request.
- **Retryable overload:** A temporary capacity problem, such as a full bounded queue. The command was not silently discarded and may be retried later.
- **Reject when durability is unavailable:** Refuse a command if PostgreSQL cannot safely record it. The backend must not claim success for a change it cannot recover.
- **Telemetry/latest-value data:** Replaceable information such as location, velocity, or heading. It may be coalesced or dropped because newer data supersedes it.
- **Drop with explicit status:** Intentionally discard data under pressure while reporting `dropped` or an equivalent status to make the outcome visible.

## WebSocket Terms

- **Subscribe trip:** Ask the backend to send this WebSocket connection live updates for a particular trip.
- **Unsubscribe trip:** Stop live updates for that connection. It does not delete or deactivate the trip.
- **`protocol_version`:** The version of the message envelope and communication contract. It is separate from trip revisions and planner state versions.
- **Authenticate:** Prove which user owns the client session. V1 sends an opaque development bearer token in the first non-ping WebSocket message; the backend then authorizes each requested trip. User-facing login is V1.5 or later.
- **Connection-ready:** The backend's confirmation that authentication and protocol setup succeeded and normal messages may proceed.
- **Revised plan:** A structured planner result containing the plan version, preserved prefix, revised suffix, and changes such as moved, shortened, or skipped activities. It is more than a single change event.
- **Resynchronization:** Reconnecting, authenticating, reporting last-observed versions and a bounded list of outstanding command IDs, subscribing again, and receiving the current durable trip, requested command outcomes, and latest planner state/plan.
- **Replay log:** Sending every message missed during a disconnect. V1 avoids an unbounded replay log and uses resynchronization instead.
- **Lost response recovery:** The client does not recover a missing response from the old WebSocket queue. It reconnects and obtains the current result from durable command intents/backend state and pending outbox processing.
- **Essential message:** A message whose outcome must not silently disappear, such as `durable_recorded`, `planner_applied`, a terminal rejection, or a terminal error.
- **Delivery through the connection:** Delivery from the backend application to the client application over the current WebSocket. It is not delivery to PostgreSQL or C++.

## Queues and Buffers

- **Backend-to-client outbound queue:** Bounded memory in the backend for acknowledgements, plans, errors, and notifications waiting to be sent over a WebSocket.
- **Backend-to-C++ outbound queue:** Bounded memory in the backend for commands and telemetry waiting to be sent over the gRPC stream.
- **Backend inbound admission:** Bounded handling of messages arriving from clients. It uses per-connection and per-trip limits rather than an unbounded global queue.
- **Client outbound buffer:** Client-side/WebSocket memory for commands and location updates. V1 does not define a separate application-level client queue.
- **PostgreSQL outbox:** Durable retry storage, not a runtime output buffer. The backend reads pending rows and dispatches them through its bounded C++ queue.
- **Full outbound buffer:** The backend cannot safely retain an essential message for the current WebSocket, so it closes that connection with a retryable reason. The client reconnects and resynchronizes.

## Ordering and Ownership

- **Backend lease holder:** The backend process currently authorized to coordinate an active trip. V1 has one backend process, but the lease still fences restarts.
- **Runtime epoch:** A monotonically increasing ownership generation attached to a backend lease. C++ rejects messages from an older epoch.
- **Mutation sequence:** A contiguous per-trip order number for durable commands.
- **Observation sequence:** An order number for replaceable telemetry. Gaps are allowed and older observations may be ignored.
- **Trip revision:** The durable canonical trip version. It advances only after C++ accepts a durable mutation and PostgreSQL finalizes that mutation.
- **Expected trip revision:** The durable revision against which a command was recorded. C++ compares its mirrored trip revision before accepting that durable command.
- **Planner state version:** A version that increases whenever C++ accepts a state-changing durable event or telemetry observation. It is independent of trip revision.
- **Expected planner state version:** An optional compare-and-set version used for a decision about a specific proposed plan or another explicitly version-sensitive planner operation. Ordinary trip mutations omit it so telemetry does not create false conflicts.
- **Current stream binding:** The gRPC stream currently carrying one trip and runtime epoch. It is transport routing only; the shard remains the state owner, and the latest plan is retained for bootstrap when no stream is bound.
- **`request_id`:** An opaque identifier used to correlate an asynchronous request with its response. It is commonly a UUID or ULID, so it is represented as a string.
- **`trip_id`:** An opaque identifier for a trip, commonly a UUID or another application-defined format.
- **`expires_at_unix_ms`:** A signed Unix-epoch timestamp in milliseconds defining when a message expires. It is `int64` because timestamps and time arithmetic are signed.

# LiveRoute V1.5 HTTP, Authentication, and Persistence Contract

## Authority and versioning

This document and `schema/http/liveroute-v1.5.openapi.yaml` are normative for
the V1.5 browser/backend boundary. The OpenAPI `info.version` and wire contract
identifier are `liveroute.http.v1.1`; `/api/v1` is the HTTP major-version prefix.
The product milestone remains V1.5. The completed `liveroute.v1` WebSocket and
gRPC contracts are unchanged.

`plans/LiveRouteV1ContractSpec.md` remains authoritative after a trip becomes
active. If this document conflicts with that contract on canonical-plan
ownership, proposal finalization, versions, or planner delivery, the V1 contract
wins and this document must be corrected before implementation.

Exact fixed policy values live in `config/v15-contract-policy.yaml`. Exact
frontend versions live in `config/frontend-toolchain.lock`. The timezone data
artifact lives in `config/timezone-boundaries.lock`.

## JSON and HTTP compatibility

- Request and response bodies are UTF-8 `application/json`. Objects reject
  unknown properties. Optional properties are omitted rather than `null`.
- The decoded JSON body limit is 262,144 bytes. Larger bodies return `413`.
- UUIDs are lowercase canonical UUID strings. Versions/revisions that can map to
  `uint64` are canonical unsigned decimal strings, never JSON numbers.
- Unix-millisecond instants and bounded millisecond offsets are JSON integers in
  JavaScript's exact safe-integer range.
- Coordinates are finite numbers with latitude in `[-90, 90]` and longitude in
  `[-180, 180]`.
- Responses include `X-Content-Type-Options: nosniff`, `Cache-Control: no-store`
  for auth and user data, and a request correlation id. Errors use the exact
  OpenAPI `Problem` schema and never expose provider payloads or credentials.
- Additive response fields require a new HTTP contract minor and compatibility
  corpus. Removing/renaming fields, changing types, or tightening previously
  valid input requires `/api/v2`.

Trip reads return `ETag: "trip-revision-N"`. Every trip mutation other than
creation requires `If-Match` with that exact quoted value. Missing is `428
Precondition Required`; malformed is `400`; a current-revision mismatch is `412
Precondition Failed`. Successful mutations return the new ETag.

## HTTP idempotency

`Idempotency-Key` is a canonical UUID and is required on trip/activity writes,
place resolution/acceptance, activation, and deactivation. Its identity is
`(authenticated user, HTTP method, normalized path, key)`.

The request digest is SHA-256 over RFC 8785 canonical JSON of:

```json
{
  "method": "POST",
  "path": "/api/v1/trips",
  "if_match": "",
  "content_type": "application/json",
  "body": {}
}
```

Query strings are forbidden on idempotent mutation endpoints. `if_match` is the
exact header value or the empty string. The body is the schema-validated JSON
value. A matching replay returns the original status, response body, ETag, and
resource identity without reapplying work. Reusing a key with a different digest
returns `409 IDEMPOTENCY_KEY_REUSED`. Records are retained for 30 days; resource
history and the completed V1 command-intent retention policy are unaffected.

`POST /places/resolve` additionally stores an HMAC-SHA-256 of its canonical
request rather than the raw temporary Search Box coordinate in the idempotency
row. The row stores the non-secret HMAC key id so a replay remains comparable
after rotation. It records whether the one permitted provider request was
started. A replay never sends another Mapbox request. An ambiguous provider
outcome returns the same retryable failure for that key; only a new explicit
user action with a new key may start another chargeable request.

## Google OIDC login

1. `POST /api/v1/auth/google/nonce` is unauthenticated but requires an allowed
   `Origin`. It creates 32 random bytes for a browser-binding cookie and 32 random
   bytes for the OIDC nonce, stores only their SHA-256 digests, expires them after
   five minutes, sets the binding cookie, and returns the nonce.
2. The browser passes that exact nonce to Google Identity Services and posts the
   returned ID token plus a browser-selected canonical US IANA
   `default_time_zone_name` to `POST /api/v1/auth/google`. The binding cookie is
   required. The timezone is validated against pinned tzdata 2026c and is used
   only when creating a new LiveRoute user; an existing user's stored default is
   not silently changed by login.
3. The backend consumes the nonce once in the same transaction that creates the
   LiveRoute session. Reuse, expiry, or binding mismatch is
   `UNAUTHENTICATED`.
4. Verification accepts issuer `https://accounts.google.com` or
   `accounts.google.com`, requires the configured web client id as `aud`, checks
   `azp` when present, verifies signature/`kid`/`exp`/`iat`/nonce, requires a
   nonempty `sub`, and requires `email_verified=true` when email is retained.
   Identity is keyed only by `(canonical issuer, sub)`. Email and display name
   are mutable profile data, not authorization keys. A missing/blank `name`
   claim produces the deterministic display name `LiveRoute user`; it never
   changes authorization behavior.
5. JWKS honors provider cache headers up to 24 hours. An unknown `kid` causes one
   bounded refresh; failure is retryable authentication unavailability, never a
   signature bypass. No Google access or refresh token is requested or stored.

The production OIDC browser-binding cookie is
`__Secure-liveroute_oidc_binding` with `Secure`, `HttpOnly`, `SameSite=Lax`,
`Path=/api/v1/auth/google`, no `Domain`, and a five-minute lifetime. Local
loopback development uses `liveroute_dev_oidc_binding` with the same attributes
except `Secure`. It is deleted after success or terminal failure. The
`__Host-` prefix is intentionally not used because that prefix requires
`Path=/`, which would defeat this cookie's narrower authentication-only path.

## LiveRoute sessions and CSRF

A session token is 32 cryptographically random bytes encoded as 43-character
unpadded base64url. PostgreSQL stores only SHA-256. Session time uses PostgreSQL
`clock_timestamp()`:

- idle lifetime: 7 days;
- absolute lifetime: 30 days from initial authentication;
- idle expiry is touched at most once per 5 minutes and never exceeds absolute
  expiry;
- rotate after 24 hours on the next authenticated response;
- the replaced token remains valid for already in-flight requests for 60 seconds;
  it cannot mint WebSocket tickets or start a second rotation; and
- logout revokes the complete session family, including rotation predecessors.

Production uses `__Host-liveroute_session` with `Secure`, `HttpOnly`,
`SameSite=Lax`, `Path=/`, and no `Domain`. Production serves frontend, HTTP API,
and WebSocket from one HTTPS origin supplied by `LIVEROUTE_PUBLIC_ORIGIN`; the
backend refuses startup when it is missing, non-HTTPS, contains a path/query, or
does not exactly equal its public origin.

Local development uses frontend `http://localhost:5173`, backend
`http://localhost:8080`, and host-only `liveroute_dev_session` with `HttpOnly`,
`SameSite=Lax`, `Path=/`, and no `Secure` attribute. This exception is accepted
only when the backend binds a loopback address. Development CORS allows exactly
that frontend origin, credentials, the documented methods, and
`Content-Type`, `If-Match`, `Idempotency-Key`, and `X-CSRF-Token`; it exposes
`ETag`, `X-Request-ID`, and `X-LiveRoute-CSRF-Token`. Wildcard origins are
forbidden. Production is same-origin and emits no CORS allowance.

CSRF tokens are unpadded base64url HMAC-SHA-256 over the presented raw session
token and literal context `liveroute.csrf.v1`, using a server key identified on
the session row. `GET /session` returns the token; mutating authenticated HTTP
requests send it as `X-CSRF-Token`. Verification is constant-time. Rotation
returns the replacement token in `X-LiveRoute-CSRF-Token`. Current and previous
CSRF keys are external secrets; a retired key remains available until every
session referencing it has reached absolute expiry. Origin validation is still
required and is not replaced by CSRF.

CSRF and place-resolution request-digest HMAC keys have non-secret identifiers,
are supplied only through the runtime secret store, and are activated for at
most 90 days. A retired key remains available for at least 30 days and, for
CSRF, until every session row referencing it has reached absolute expiry.
Startup rejects an absent current key, duplicate key ids, or a retained database
row whose referenced key is unavailable. LiveRoute stores no application data
that requires a reversible encryption key in V1.5.

## WebSocket tickets

`POST /api/v1/auth/ws-ticket` requires session, origin, and CSRF checks. It
returns 32 random bytes as 43-character unpadded base64url, stores only SHA-256,
binds the ticket to the session and user, and expires it after 60 seconds.
Consumption is a single PostgreSQL update requiring unused, unexpired ticket and
live session. The existing WebSocket `authenticate` message consumes it once.
Disconnect before use does not extend it; clients request a new ticket. Session
revocation invalidates all its unused tickets.

## Foreground browser GPS admission

The exact ordinary-location policy is `browser_gps` in
`config/v15-contract-policy.yaml`. It is a V1.5 client admission policy over the
unchanged `liveroute.v1` `telemetry_update/location` message; reported accuracy
is used locally and is not added to the strict V1 wire payload.

- Start `watchPosition` only after an active trip's WebSocket subscription is
  confirmed. Use `enableHighAccuracy = true`, `maximumAge = 2000`, and
  `timeout = 10000`. Stop the watch on deactivation, unsubscribe, component
  teardown, terminal permission denial, or when the document becomes hidden.
  Visibility restoration starts a new watch and requires a new callback.
- A callback may update the local Mapbox marker/accuracy indicator, but is
  eligible for backend telemetry only when latitude, longitude, timestamp, and
  `coords.accuracy` are finite; coordinates are in range; accuracy is greater
  than zero and at most 50 meters; the sample is at most 10 seconds old; and its
  timestamp is no more than one second in the future. `observed_at_unix_ms` is
  the integer-truncated Geolocation timestamp, not the send time.
- Send the first eligible subscribed sample immediately. Thereafter a sample is
  send-worthy when it is at least 10 meters from the last sent coordinate or
  five seconds have elapsed since the last send. Distance uses the WGS-84
  coordinate pair and the haversine formula with mean Earth radius
  6,371,008.8 meters. Even when callbacks are faster, send at most one location
  frame per 1,000 milliseconds. V1.5 sends no separate velocity or heading
  telemetry; unavailable/null browser values are never synthesized.
- At most one location message may await its matching `telemetry_status`. While
  it is awaiting status or the one-second rate window is closed, retain only the
  newest eligible sample in one replaceable in-memory slot. Replacement creates
  no message id and sends no status. On any matching telemetry disposition,
  clear the in-flight slot and send the still-fresh pending sample when the rate
  window permits. A sample that becomes older than 10 seconds is discarded.
- If no matching `telemetry_status` arrives within 10 seconds, close that stream
  and enter the existing reconnect/resynchronization flow. Do not retry its
  message id or payload. On socket loss, `navigator.onLine = false`, or hidden
  document, discard both slots. GPS samples and telemetry frames are never put
  in localStorage, IndexedDB, Cache Storage, a service worker queue, or another
  durable buffer. After subscription recovery, only a fresh Geolocation callback
  may restart transmission; offline samples are not replayed.
- `PERMISSION_DENIED` stops collection and requires a user-visible retry after
  permission changes. `POSITION_UNAVAILABLE` and `TIMEOUT` send nothing and
  leave the foreground watch eligible to recover. If no eligible callback has
  arrived for 15 seconds, show location as stale/degraded, but do not fabricate a
  coordinate, trigger a C++ replan, or change the canonical itinerary.

Ordinary accepted location telemetry remains state-only under the completed V1
contract. The 50-meter admission threshold does not confirm route deviation.

### Same-destination route-deviation baseline

`route_deviation` in `config/v15-contract-policy.yaml` is the normative
`liveroute-navigation-v1-baseline-1` policy. It is a conservative deterministic
baseline, not a claim of measured optimality. Recorded traces may propose a
later policy version, but implementation is not blocked on those measurements.

Only the current leg from the latest accepted location to the current canonical
next activity is examined. For each polyline segment, convert longitude/latitude
deltas to a local equirectangular meter plane centered on the GPS sample using
mean Earth radius 6,371,008.8 meters and `cos(sample latitude)`, normalizing
longitude delta to `[-pi, pi]`; clamp the scalar projection to the segment and
take the minimum Euclidean point-to-segment distance. Degenerate segments use
point distance. The nonnegative distance used for hysteresis is:

```text
effective_distance = max(0, polyline_distance - coords.accuracy)
```

Deviation confirmation uses only ordinary-GPS-eligible samples whose accuracy
is at most 25 meters. Walking enters the off-route state at effective distance
20 meters and exits at 10 meters. Driving enters at 35 meters and exits at 15
meters. Entry requires three qualifying samples, each at least one second after
the previous counted sample and all within a rolling 10-second span. A sample
below the entry threshold resets the entry count. Exit requires two equivalently
spaced samples at or below the exit threshold; a sample above the exit threshold
resets the exit count. Samples in the band preserve the current state without
advancing either count. A missing/changed route, canonical plan, or next activity
resets both counters and cannot itself declare deviation.

On confirmed entry, request one Mapbox Directions route from the newest eligible
coordinate to the **same next activity**, using that activity's inbound travel
mode. Only one request may be in flight. Its timeout is five seconds. Network
errors, timeout, HTTP 429, and HTTP 5xx receive exactly one automatic retry two
seconds after failure; other HTTP errors, malformed/no-route responses, and an
abort are not retried. A successful reroute starts a 15-second incident cooldown;
an exhausted/terminal failure starts a 30-second cooldown. A canonical next-
activity change cancels in-flight work, resets hysteresis, and bypasses either
cooldown because it requires a new canonical route. Hidden/offline/disconnected/
inactive state aborts work and uses the ordinary no-replay rule.

Failure keeps the previous canonical route visible as stale/degraded, continues
the local position marker, emits no route-deviation telemetry, and permits a new
three-sample incident only after the failure cooldown. Success replaces only the
Mapbox road/walking route to the same destination. It generates one new canonical
UUID `navigation_route_id`, never reused for another successful response, and
rounds the provider duration and distance upward to nonnegative integer seconds
and meters. `updated_eta_unix_ms` is the triggering sample timestamp plus the
rounded duration; `previous_eta_unix_ms` is the absolute ETA of the route being
replaced. The route-deviation payload distance is the nearest nonnegative integer
raw polyline distance; accuracy-adjustment is only for local hysteresis.

The browser sends one V1.5 navigation extension after successful rerouting. Its
exact strict value schema is
`schema/websocket/liveroute-v1.5-navigation-extension.schema.json`; it requires
`policy_version = liveroute-navigation-v1-baseline-1` and the fields documented
in the frontend plan. The backend validates current next-activity identity,
rejects a route id already accepted in the current runtime epoch, and treats a
first new id after reconnect as the new baseline because location telemetry is
never replayed. The observation is materially replan-worthy exactly when
`updated_eta_unix_ms -
previous_eta_unix_ms >= 300000`, or when the changed ETA crosses from on-time to
late for one of the current next activity's authoritative boundaries:

- scheduled start;
- reservation start plus grace;
- applicable open-window close minus its fixed activity duration; or
- mandatory deadline minus its fixed activity duration.

Boundary crossing means `previous_eta <= boundary < updated_eta`; absent
boundaries are ignored. Checked integer arithmetic is mandatory. An improved or
smaller delay that crosses no boundary updates navigation state but schedules no
C++ attempt. At most one material event is emitted for a navigation route id.
This backend filter never changes the canonical itinerary; any C++ result remains
a user-approval proposal.

## Places and permanent geocoding

Temporary Mapbox Search Box data exists only in browser memory. Place resolution
accepts a coordinate, makes at most one backend Mapbox Geocoding v6 reverse call
with `permanent=true&country=US`, and persists no Search Box name, id, response,
or coordinate.

The provider boundary is fixed by `config/v15-contract-policy.yaml`: 500 ms
connect timeout, 3 s total timeout, 256 KiB response limit, 16 global and one
per-user in flight, five attempts per user per minute, and zero automatic
retries. Its exact safe mapping is:

| Condition | HTTP/problem code | Retryable | Readiness consequence |
| --- | --- | --- | --- |
| Local per-user rate or per-user/global concurrency limit | `429 RESOURCE_EXHAUSTED` | true | none |
| DNS, connect, TLS, timeout, EOF, or provider `5xx` | `503 PROVIDER_UNAVAILABLE` | true | none |
| Provider `429` | `503 PROVIDER_UNAVAILABLE` | true | none; never retry automatically |
| Provider `401` or `403` | `503 PROVIDER_UNAVAILABLE` | false | permanent-geocoder readiness false until configuration changes/restart |
| Provider `400`, `404`, or `422`; empty feature list; out-of-US feature | `422 PLACE_NOT_RESOLVED` | false | none |
| Other provider `4xx` | `503 PROVIDER_UNAVAILABLE` | false | none |
| Oversize/malformed provider JSON, invalid/nonfinite provider coordinate, or missing required feature shape | `503 PROVIDER_UNAVAILABLE` | true | none |
| No valid pinned US timezone polygon | `422 PLACE_NOT_RESOLVED` | false | none |

No row is persisted as a Place on any failure. Provider response bodies are
never logged or returned.

The first response feature is the deterministic permanent candidate. Its
coordinate is authoritative; `full_address` is optional. The local resolver uses
the checksum-pinned 2026c `timezones-with-oceans.geojson.zip`, accepts only zones
present for country `US` in pinned tzdata 2026c, chooses the lexicographically
lowest valid US IANA name when polygons overlap or the point lies on a shared
boundary, and rejects a point in no valid polygon. No external timezone request
is made.

The resolution token is 32 cryptographically pseudorandom bytes produced as
`HMAC-SHA-256(active HTTP HMAC key, "liveroute.place-resolution-token.v1" || NUL || resolution_attempt_id)`
and encoded as 43-character unpadded base64url. The idempotency row retains the
non-secret HMAC key id, so an exact response can be reconstructed during the
required retired-key retention period without persisting the raw token in
`response_body`; PostgreSQL stores only the token's SHA-256. The token is bound
through its immutable resolution-attempt row to the user/permanent coordinate/address/
timezone, single-use, and valid for 10 minutes. `POST /places` creates one
immutable Place or exactly replays it. Place correction creates a new Place and
a revision-checked trip edit.

## Saved trips and execution transitions

- `GET /api/v1/session` returns the authenticated user's stored
  `default_time_zone_name`. A new-trip editor initializes its trip timezone from
  that value. `POST /api/v1/trips` still carries the selected
  `default_time_zone_name` explicitly so creation is deterministic and so a
  user-visible trip-specific selection can override the initial value. The
  backend validates the submitted US IANA zone against pinned tzdata 2026c; it
  never derives the trip timezone from browser locale, current coordinates, the
  first activity, or a Mapbox response. Changing a trip timezone does not change
  the user's stored default.
- Adding a confirmed Place to a new-trip editor creates one local activity with
  the exact `trip_creation.new_activity_defaults` values in
  `config/v15-contract-policy.yaml`: `unscheduled`; inbound mode `driving`;
  class `flexible`; `priority_rank = 0`; `utility_score = 0`; one relative
  availability window `[0, 86400000)`; no reservation start or mandatory
  deadline; zero reservation grace; fixed 3,600-second minimum/preferred/maximum
  duration; `mandatory = false`; `can_shorten = false`; `can_move = true`; and
  `can_skip = true`. Ordinal is the activity's current zero-based UI position
  and `place_id` is the accepted durable Place. Optional properties for the
  absent reservation and deadline are omitted, never sent as `null`.
- These defaults are user-visible editor values, not backend inference. The user
  may change travel mode, duration, schedule, availability, importance,
  mandatory/movable/skippable settings, reservation, or deadline before saving.
  Reordering changes contiguous ordinals but does not silently rewrite equal
  priority/utility values. The backend rejects missing required activity fields
  rather than applying defaults of its own.
- An HTTP-saved trip is canonical immediately in PostgreSQL and never requires
  C++ approval. Creation requires a trimmed UTF-8 trip name of 1-120 bytes and at
  least one activity.
- V1.5 HTTP creation is intentionally not the completed V1 WebSocket
  `create_trip` operation. In one idempotent transaction it generates the trip,
  saved-plan, and activity ids; validates that every referenced Place belongs to
  the authenticated user; inserts `trips` at `trip_revision = 1`,
  `next_mutation_sequence = 1`, `finalized_mutation_sequence = 0`,
  `execution_state = inactive`, `saved_plan_id = <new saved plan>`, and
  `current_plan_id = active_execution_plan_id = activated_at =
  transition_operation_id = NULL`; and inserts saved-plan revision 1 plus its
  complete relative activities/windows. The completed idempotency record stores
  the exact response for replay.
- That creation transaction does **not** insert `itinerary_plans`,
  `trip_activities`, `activity_open_windows`, `trip_travel_delays`, a command
  intent, planner outbox work, a runtime lease, or a snapshot. For an inactive
  V1.5 trip, the row selected by `trips.saved_plan_id` is the sole canonical
  itinerary definition. `trip_revision` versions saved user state, while the
  accepted/finalized mutation sequences remain the runtime-command watermark and
  therefore begin at zero.
- Saved relative plan revisions are immutable. Each activity appears once with
  a unique contiguous ordinal in `[0, activity_count)`. Create requests must
  provide exactly those ordinals. Adding at ordinal `i` shifts existing
  ordinals `>= i` up by one; replacing an activity with ordinal `i` moves it and
  shifts the intervening range; deleting compacts every later ordinal. The
  operation publishes one complete new revision or none. Each activity is
  either `scheduled` with
  `0 <= start_offset_ms < end_offset_ms <= 86400000`, or `unscheduled` with both
  offsets absent. Relative constraint offsets use the same domain.
- Activity duration is fixed for execution; `can_shorten` is always false.
- Inactive editing creates a new saved-plan revision and advances trip revision
  atomically. It creates no C++ lease, runtime, or outbox work.
- Activation requires every activity scheduled and every materialized value
  inside the completed V1 current-day planning horizon. It creates one immutable
  absolute execution plan plus the corresponding absolute V1 activity/window
  representation, sets `current_plan_id` and `active_execution_plan_id` to that
  execution plan, and uses the durable `inactive -> activating -> active`
  operation described in the frontend plan. This materialization is derived once
  from the locked saved-plan revision and never rewrites that saved revision.
- Activation atomically consumes the current `next_mutation_sequence` value `N`
  as the absolute execution baseline checkpoint, sets
  `finalized_mutation_sequence = N`, and advances
  `next_mutation_sequence = N + 1`. The first activation therefore changes the
  initial `(next = 1, finalized = 0)` state to `(next = 2, finalized = 1)`;
  later activations continue the durable sequence and never reset or reuse it.
  `BootstrapTrip.finalized_mutation_sequence`, and the successful
  `TripBootstrapped` accepted/finalized watermarks, are exactly `N`.
- The activation baseline checkpoint is not a replayable `ApplyTripEvent` and
  creates no synthetic `command_intents` or `planner_outbox` row. Its durable
  identity and audit are the completed HTTP idempotency record, the activation
  `trip_execution_operations` row, and its immutable
  `target_execution_plan_id`. Full bootstrap is the only delivery mechanism for
  that baseline. Subsequent runtime mutations begin at `N + 1`; there is no
  active V1.5 runtime with a zero accepted/finalized mutation watermark.
- `starting_location` is a required, validated, idempotency-bound activation
  input but is not durable canonical state and does not affect absolute-plan
  materialization. After the initiating process successfully bootstraps the new
  runtime, it may admit that coordinate through the ordinary telemetry path as
  observation sequence 1 with the server activation receipt time. It must not
  store the coordinate in `trip_execution_operations`, execution plans,
  snapshots, idempotency responses, or logs. If activation is resumed after a
  process crash, recovery bootstraps the durable plan with
  `current_observation` absent and `current_observation_sequence = 0`, exactly as
  required for a higher epoch, then waits for fresh client telemetry. Losing the
  advisory seed is permitted and never rolls back or fails an otherwise
  successful activation.
- Deactivation uses `active|activating -> deactivating -> inactive`, fences
  runtime authority, invalidates proposals, and clears execution-only pointers.
  Startup recovery resumes a bounded page of transition rows. Stable states have
  no transition-operation pointer.
- A partial unique database index enforces at most one activating, active, or
  deactivating trip per user.

Proposal accept/reject and active activity lifecycle commands remain on the
existing WebSocket contract. Proposal acceptance is final only at
`planner_applied` plus the refreshed matching `subscription_state`.

## Provider-enabled startup readiness

- A provider-enabled backend remains not ready until the pinned timezone
  boundary artifact has passed its size/SHA-256 checks, the matching tzdata zone
  table has been validated, and the immutable US boundary index is fully loaded.
  Place resolution must not run against a partially loaded resolver.
- The container-first local acceptance ceiling for this cold start is exactly
  300 seconds from Compose startup through a successful backend `/readyz`.
  `scripts/check-websocket-load.sh` enforces this ceiling. Exceeding it fails the
  check; the harness must not silently extend the deadline.
- This five-minute ceiling is an operational timeout, not a latency target or
  evidence that the current approximately 182 MB GeoJSON loading path is
  optimal. A future preprocessing/index optimization must preserve the locked
  source digest, US-zone filtering, lexicographic boundary tie-break, and
  no-polygon rejection behavior, and must be accepted from measured startup and
  resolver-equivalence tests before the ceiling is reduced.

## Retention and secret handling

- External identities, sessions, nonce/ticket digests, Places, saved-plan
  revisions, and execution-operation audit rows remain until their owning user
  is deleted, except expired unconsumed nonces/tickets may be deleted after 24
  hours and completed HTTP idempotency records after 30 days.
- Consumed place-resolution rows are retained while their immutable Place exists;
  failed/unconsumed rows may be deleted after 30 days.
- Google client secret (if configured), session/CSRF/HMAC keys, Mapbox backend
  token, and cookie/ticket raw values never enter source, committed config,
  structured logs, error payloads, or analytics.
- Logs may contain internal user/trip/place ids and request ids, but not email,
  ID tokens, cookies, CSRF tokens, Search Box payloads, coordinates submitted for
  resolution, Permanent Geocoding bodies, or resolution tokens.

# LiveRoute V1.5 Frontend + Trip Execution Planning Instructions

## Goal

Build the frontend and trip-execution flow for **LiveRoute**, a travel-planning application where users:

1. Plan trips using POI search and a map.
2. Save and edit reusable inactive trips in backend storage.
3. Optionally assign a display-only future date/time for planning and preview.
4. Press **Go** to activate a trip and begin live trip execution or simulation.
5. See their current location and accepted trip route on the map.
6. Send live telemetry/events to the backend during execution.
7. Receive **replan proposals** from the C++ planner.
8. Explicitly accept or reject proposed changes.

The user's currently accepted trip plan is **canonical and authoritative**.

The containerized backend, PostgreSQL durability, C++ planner, OSRM integration,
and `liveroute.v1` WebSocket/gRPC contracts are the completed V1 foundation.
Everything specified in this document is V1.5 product and integration work. V1.5
reuses the existing wire protocol and may add HTTP resources and recognized
namespaced WebSocket extensions, but it must not silently change strict V1 wire
fields or reinterpret completed V1 behavior.

The C++ planner must **never directly mutate the canonical plan**. It may only propose a revised plan. A proposed replan becomes canonical only after explicit user acceptance.

Saved and active state are separate concerns:

- An inactive trip is a reusable, editable user-authored definition and relative plan.
- An active trip has a resettable execution plan materialized from that definition at activation time.
- Only active trips have a C++ runtime, receive live telemetry, or produce replan suggestions.
- Deactivation preserves the saved trip and tears down/reset execution-only state.

---

# High-Level Architecture

```text
                         Mapbox
            Search UI + Maps + Directions API
                          ▲
                          │
                          │
┌────────────────────────────────────────────────────┐
│                React / TypeScript Client           │
│                                                    │
│ Trips │ Planner │ Live Trip                        │
│                                                    │
│ POI search                                         │
│ Direct Mapbox API access                           │
│ Map rendering                                      │
│ GPS collection                                     │
│ User interaction                                   │
│ Replan approval/rejection                          │
└────────────────────────┬───────────────────────────┘
                         │
                  HTTP + WebSocket
                         │
                         ▼
┌────────────────────────────────────────────────────┐
│                     Backend                        │
│                                                    │
│ Google OIDC login + LiveRoute sessions             │
│ Trip CRUD                                          │
│ Canonical plan ownership                           │
│ Telemetry ingestion                                │
│ Replan proposal lifecycle                          │
│ Persistence                                        │
│ Navigation-observation validation/coalescing       │
└───────────────┬───────────────────────┬────────────┘
                │                       │
                ▼                       ▼
           PostgreSQL              C++ Planner
                                        │
                                        ▼
                                   OSRM Table
```

---

# Core Responsibility Split

## React / TypeScript Client

Responsible for:

- Rendering the map.
- Searching/selecting POIs.
- Showing itinerary activities.
- Collecting browser/device GPS.
- Sending telemetry/events to the backend.
- Showing the currently accepted route.
- Showing replan proposals.
- Letting the user accept or reject proposals.
- Showing saved inactive trips and the user's active trip.

The client should **not** decide how the itinerary should be replanned.

---

## Backend

Responsible for:

- Authentication and authorization.
- Trip CRUD.
- Durable canonical trip state.
- Canonical plan revisions.
- Activity/place persistence.
- Telemetry/event ingestion.
- Activating/deactivating trips.
- Enforcing at most one activating, active, or deactivating trip per user.
- Forwarding relevant events to the C++ planner.
- Receiving replan proposals.
- Validating proposal version/revision.
- Applying accepted proposals to the canonical plan.
- Persisting accepted/rejected proposal outcomes.
- Publishing final canonical state after asynchronous command finalization.

---

## C++ Planner

Responsible for:

- Maintaining active planner state.
- Consuming trip telemetry/events.
- Acquiring OSRM travel-time matrices.
- Evaluating whether the current canonical itinerary is still feasible.
- Producing a proposed revised itinerary when useful.
- Explaining why the proposal is beneficial/necessary.

The planner **does not own the canonical plan**.

The planner is bootstrapped and kept resident only for an active trip. Saving,
listing, or editing an inactive trip must not create or retain a planner runtime.

It must return proposals tied to the specific canonical trip revision it planned against.

Example:

```text
proposal_id
source_trip_revision
source_planner_state_version
proposed_plan
change_summary
reason
```

An old proposal must not overwrite or replace a newer user-modified canonical plan.

---

## OSRM

OSRM exists primarily as an **internal planner dependency**.

Use the OSRM **Table service** to obtain travel-time/distance matrices for:

- Current user location.
- Remaining activities.
- Candidate activity transitions.

The planner must perform candidate evaluation using an in-memory matrix.

The planner candidate-search loop must never perform HTTP calls.

```text
Trip event
    ↓
C++ planner needs travel estimates
    ↓
OSRM Table request
    ↓
TravelTimeMatrix
    ↓
In-memory candidate evaluation
```

OSRM does **not** need to provide the visible user route.

---

## Mapbox

Use Mapbox directly from the web client for ordinary map, temporary Search Box,
and Directions requests. The Go backend does not proxy those requests and does
not receive Mapbox route geometry. Permanent Geocoding is the one exception:
the backend performs the storage-authorizing request after a user selects a
temporary search result. The browser uses a least-privilege public token
restricted to the exact development and production origins. The backend uses a
separate server-held token for Permanent Geocoding; neither token may have
unneeded secret scopes, and no secret token may enter browser code or a
committed file.

### Mapbox Search Box API

Use for:

- Restaurant search.
- Attraction search.
- Museum search.
- Hotel search.
- POI autocomplete/selection.

Search Box is temporary discovery data. When a user selects a result, the
frontend may display its business/POI name, pin, and coordinate only for the
current selection session. Do not persist, log, cache, place into analytics, or
later reconstruct any Search Box suggestion/retrieval field. The selected
coordinate is sent to the backend only as transient input to the Permanent
Geocoding flow below.

### Mapbox GL JS

Use for:

- Map rendering.
- POI markers.
- Current-location marker / blue-dot style display.
- Route geometry rendering.
- Zooming/panning.
- Showing the full accepted itinerary route.

### Mapbox Directions API

Use for the **visible route** through the currently accepted itinerary.

Mapbox does not decide which activities belong in the itinerary.

The backend/user-owned canonical plan determines the stop order.

Example:

```text
Canonical plan:
Museum → Restaurant → Park → Train

        ↓

Mapbox Directions:
Museum → Restaurant → Park → Train

        ↓

Mapbox GL JS:
draw route geometry
```

### Web versus future native navigation

The first frontend is React/TypeScript in a browser. Mapbox's complete
Navigation SDK, including built-in off-route detection and automatic rerouting,
is reserved for future native iOS and Android applications. It is not a V1.5 web
dependency.

The web client uses GL JS plus Directions and implements the bounded browser
route-progress/deviation adapter specified below. A future native client may
replace that adapter with Mapbox Navigation SDK observations without changing
canonical-plan ownership or the backend/C++ boundary.

This supports foreground web route display, browser GPS tracking, written
maneuvers, and same-destination rerouting. It is not equivalent to a native
turn-by-turn navigation product: web V1.5 does not promise SDK-quality map
matching, background tracking, voice guidance, lane guidance, or mobile
operating-system lifecycle integration. Those capabilities remain native-app
work.

External capability reference: [Mapbox Navigation products overview](https://docs.mapbox.com/help/getting-started/navigation/).

### Temporary POI discovery and durable Mapbox geocoding

V1.5 deliberately uses two Mapbox products with different data lifetimes:

1. Browser Search Box `/suggest` and `/retrieve` provide temporary POI search.
2. The frontend shows the selected temporary POI name and pin and lets the user
   choose **Use this location**.
3. Only then, the frontend sends the selected coordinate to
   `POST /api/v1/places/resolve`. The backend holds it only in request memory and
   sends exactly one Mapbox Geocoding v6 reverse request with `permanent=true`
   and `country=US`.
4. The backend selects the first (most-specific) feature in the permanent
   response. The durable candidate uses that feature's returned coordinate and
   `full_address` when present. It does not copy the temporary Search Box
   business name, provider id, address, or coordinate.
5. The frontend replaces the temporary preview with the permanent candidate's
   pin, durable address (when present), and exact coordinates. The user must
   explicitly confirm this final durable result.
6. `POST /api/v1/places` consumes the token-bound candidate and persists it.
   Rejecting it or leaving the flow persists nothing.

This second confirmation is the mismatch recovery path. If reverse geocoding
selects a nearby address or a point the user does not want, the user cancels,
adjusts the pin or searches again, and starts a new resolution attempt. The
backend never silently treats the temporary Search Box coordinate as durable.

The durable routing destination is the coordinate returned by the accepted
Permanent Geocoding feature. The durable label is its `full_address`; if absent,
the UI and wire-level `Activity.display_name` use a deterministic coordinate
label formatted as `latitude, longitude` with six decimal places. No business
or POI name is stored. This means a selected restaurant may later appear in a
saved trip as its address rather than its temporary Search Box business name.

Mapbox Geocoding v6 does not provide the activity's required IANA timezone.
The backend therefore derives `time_zone_name` locally from the accepted durable
coordinate using a versioned, checksum-pinned US IANA timezone-boundary dataset.
`config/timezone-boundaries.lock` fixes its dataset, license, 2026c release,
archive and extracted-file digests, boundary tie-break, and container install
path; `docker/timezone-boundaries/Dockerfile` verifies and installs it. No
coordinate or place data is sent to another geocoding provider.

Cost and request controls are normative:

- use one unique Search Box `session_token` for each interactive selection
  attempt and terminate it with at most one `/retrieve`;
- debounce `/suggest` in the browser and never call Permanent Geocoding for
  keystrokes, suggestions, map movement, preview, reload, or activation;
- issue one Permanent Geocoding request only after **Use this location**;
- do not automatically repeat a chargeable Permanent request after an ambiguous
  transport outcome; return a retryable resolution failure and require a new
  explicit user action; and
- reuse the accepted PostgreSQL Place for every later edit, preview, activation,
  OSRM lookup, and Directions request. Correcting a Place is an explicit new
  resolution flow and trip edit.

External source references: [Mapbox Search Box API](https://docs.mapbox.com/api/search/search-box/),
[Mapbox Geocoding v6 API](https://docs.mapbox.com/api/search/geocoding/), and
[Mapbox search-product comparison](https://docs.mapbox.com/help/getting-started/search/).

---

# Main Frontend Modes

The app should have three main user modes.

```text
Trips → Planner → Live Trip
```

---

# 1. Trips Page

This is the main trip dashboard.

Display:

- Saved inactive trips as name-only rows/cards.
- The user's single active trip, if one exists, in a separate active section.

Example backend request:

```http
GET /api/v1/trips
```

User-visible execution states:

```text
INACTIVE
ACTIVE
```

Every saved trip has a user-authored `trip_name`. This is distinct from activity
and location display names; locations are never user-renamed. Selecting an
inactive trip opens the editable planner and loads its activities, stored place
names/coordinates, constraints, and optional planning schedule. A trip becomes
saveable once it has a trip name and at least one valid activity; an empty trip
is client-local and must not be persisted.

The authenticated session includes the user's stored canonical US IANA
`default_time_zone_name`. A new trip initializes its timezone from that value;
the editor may expose a user-visible trip-specific override, and the create
request always sends the selected value explicitly. Browser locale, GPS, the
first Place, and Mapbox data never silently choose or overwrite it.

The optional planning date/start time is presentation metadata available inside
the planner. It can anchor preview clock labels, but it is not displayed in the
inactive name-only list, does not automatically activate a trip, and is not the
execution clock.

---

# 2. Planning Mode

Suggested desktop layout:

```text
┌─────────────────────┬──────────────────────────┐
│ Itinerary           │                          │
│                     │       Mapbox map         │
│ Day 1               │                          │
│ 10:00 Museum        │   ● Museum               │
│ 12:30 Restaurant    │         ● Restaurant     │
│ 15:00 Park          │                  ● Park  │
│                     │                          │
│ + Add activity      │                          │
│                     │                          │
│       [ GO ]        │                          │
└─────────────────────┴──────────────────────────┘
```

The user should be able to:

- Add activities.
- Search for places.
- Reorder activities.
- Remove activities.
- Set expected duration.
- Set reservations/deadlines.
- Mark activities flexible or mandatory.
- Set user-authored availability windows and the remaining exact activity fields
  required by the canonical schema, or apply documented deterministic defaults.
- Save an inactive trip with at least one activity.
- Optionally set a planning-only display date/start time.
- See the route preview on the map.
- Start the trip with **Go**.

When a confirmed durable Place is first added, the editor uses the exact
`trip_creation.new_activity_defaults` object from
`config/v15-contract-policy.yaml`: an unscheduled, flexible, driving activity;
equal neutral priority and utility (`0`); a visible fixed one-hour duration; an
all-day relative availability window; no reservation or deadline; and movable,
skippable, non-mandatory flags with shortening disabled. The user can edit these
values before saving. The backend never fills omitted activity fields, and
reordering changes ordinals without changing priority or utility.

---

# Relative Saved Time and Activation Materialization

Inactive saved plans use offsets relative to trip activation rather than fixed
execution timestamps. Each scheduled activity has a relative start and end
offset; availability windows, reservations, and deadlines that constrain the
simulation are likewise represented relative to activation. Durations remain
fixed under the existing V1 no-shortening rule.

The V1.5 REST/storage representation has two saved-segment states:

```text
scheduled   -> start_offset_ms and end_offset_ms are present
unscheduled -> neither offset is present
```

For a scheduled segment, offsets are integer milliseconds satisfying
`0 <= start_offset_ms < end_offset_ms <= 86400000`. Relative open-window,
reservation, and deadline offsets use the same nonnegative, at-most-24-hour
domain and preserve the V1 ordering rules. The frontend does not invent a time
for an unscheduled activity, and `unscheduled` is not the V1 execution-plan
state `omitted`.

If a display schedule is present, the frontend adds these offsets to the
display-only anchor to show projected local clock times. If absent, it displays
durations and relative offsets. Changing the display anchor does not change the
relative plan.

The optional display anchor contains a local date, local time, and an explicit
US IANA timezone. It is presentation-only. A nonexistent DST-gap local time is
rejected; an ambiguous DST-fold time uses the earlier offset and the UI displays
the timezone abbreviation. Omitting any anchor component omits the entire
anchor. It never supplies an execution timestamp.

On activation, the backend records `activated_at` and deterministically
materializes every relative offset as an absolute UTC Unix-millisecond value:

```text
absolute execution time = activated_at + saved relative offset
```

Only this materialized absolute execution plan is bootstrapped into C++. This
preserves the existing planner's normalized Unix-time contract while allowing a
saved trip to be reused at a later date. Materialization is user-authorized by
the activation request and is persisted as the initial canonical execution plan;
it does not require planner approval.

Activation rejects with a validation error when any saved activity is
`unscheduled`, or when materialization would place any scheduled interval,
window, reservation, or deadline outside the V1 planning horizon: the local
calendar day containing `activated_at` in the trip's default timezone. The user
must schedule the missing activity or activate at a time that fits the saved
relative plan. The backend does not ask C++ to schedule an incomplete saved
trip.

The REST DTO and PostgreSQL migration must distinguish the saved relative plan
from the active absolute execution plan. Only the latter uses the existing V1
`CurrentPlanSegment` union, where every activity is `scheduled` or `omitted`.
Consequently, `POST /api/v1/trips` stores only `trips` metadata and the immutable
saved-plan/activity/window tables. Its new inactive trip has no `current_plan_id`
and creates no row in the legacy absolute execution tables. Activation performs
the first conversion to `itinerary_plans`, `trip_activities`, and absolute open
windows under the locked saved revision; saving never invents an activation
instant merely to satisfy the older V1 representation.

---

# POI Search and Selection

The user chooses a Mapbox result and confirms its exact location before it can
enter the canonical trip.

Flow:

```text
User types "Shake Shack"
        ↓
Mapbox Search Box
        ↓
Search results
        ↓
User selects the correct POI
        ↓
Map shows the Mapbox name, pin, and coordinates
        ↓
User chooses "Use this location"
        ↓
Backend makes one Mapbox reverse-geocoding request with permanent=true
        ↓
UI shows the permanent address/pin/coordinate
        ↓
User confirms the durable result
        ↓
Backend stores only the accepted permanent result plus local timezone
```

`POST /api/v1/places/resolve` accepts only the selected temporary coordinate.
The backend validates its range, redacts it from ordinary request logs, calls
Mapbox Geocoding v6 reverse with `permanent=true` and `country=US`, resolves the
returned durable coordinate to an IANA timezone locally, and returns a
short-lived LiveRoute resolution token binding the selected permanent feature's
exact durable fields. It does not persist a `Place` and does not accept or bind
the temporary Search Box name.

`POST /api/v1/places` consumes that token idempotently and persists the bound
fields. The browser cannot alter the coordinate, address, or timezone
between resolution and persistence. Cancelling or failing either step creates
no `Place` and does not mutate a trip.

An empty permanent response, an out-of-US result, an invalid coordinate, or a
failed local timezone lookup makes resolution fail without mutating canonical
state. A missing `full_address` is allowed because the deterministic coordinate
label remains available. The final confirmation, not the temporary selection,
authorizes persistence.

Once accepted, the stored coordinate is the sole destination used for inactive
planning preview, activation, OSRM, and Mapbox Directions. The frontend must not
query Mapbox Search by name or reverse-search the coordinate during activation.
Mapbox may produce a different road route or ETA later as roads, traffic, or
routing data change, but it must still route to that same stored destination.

An accepted Place coordinate is immutable. Activation must not re-resolve it or
silently replace it with newer provider data. Correcting a saved destination is
an explicit place replacement through the same resolution/acceptance flow and a
revision-checked trip edit; existing trip history continues to reference the old
Place. This keeps reloads, concurrent sessions, OSRM, and Mapbox destinations in
agreement even when an external provider's data later changes.

The V1.5 Place contract contains:

```json
{
  "internal_place_id": "...",
  "formatted_address": "120 Main Street, ...",
  "latitude": 42.281,
  "longitude": -83.748,
  "time_zone_name": "America/Detroit"
}
```

`formatted_address` is optional. The other fields are required. Ordinary row
creation/audit metadata may be stored separately, but V1.5 does not persist the
temporary business name, a Search Box provider id, or additional Search Box
payload. The backend assigns `internal_place_id` and activation never re-fetches
the location.

There is no user-authored label. The activity's required wire-level
`display_name` is derived from the canonical Place: `formatted_address` when
present, otherwise the exact six-decimal coordinate label. The temporary Search
Box business name is never copied into Activity or Place storage. Provider and
address metadata remains a Go backend/persistence concern and does not enter the
transport-independent C++ planner domain.

Activities should reference the application's place ID rather than duplicating place data everywhere.

Every trip stop uses this same Activity-to-Place representation, whether the
place originated as a business/POI, postal address, or independently supplied
coordinate. The API and UI must not introduce separate "named activity" and
"location activity" variants.

The user-confirmed permanent pin, not the temporary POI pin or address text,
identifies the routing destination. The UI must make any movement between the
temporary and permanent pins visible before the user confirms the Place.

---

# Why Coordinates Matter

The selected POI's latitude/longitude is ultimately required for routing.

OSRM receives geographic coordinates for the places involved in replanning.

Conceptual ordered set:

```text
0 = current user location
1 = museum
2 = restaurant
3 = park
4 = train station
```

OSRM then provides pairwise travel times for planner evaluation.

Important:

```text
Application struct may store:
latitude, longitude

OSRM URL format expects:
longitude,latitude
```

Do not accidentally reverse them.

---

# Planning Route Preview

During planning, the user should be able to see the trip route on Mapbox.

Example canonical plan:

```text
Museum → Restaurant → Park → Train
```

Send those ordered stops to Mapbox Directions.

Mapbox returns route geometry.

Mapbox GL JS renders the result.

This route is a preview of the **currently accepted/canonical planned order**.

Mapbox must not silently optimize/reorder the itinerary.

LiveRoute permits up to 64 activities while one Mapbox Directions request permits
fewer waypoints. The frontend groups consecutive legs by travel mode and splits
each group into ordered requests within the provider waypoint limit, overlapping
the boundary coordinate between chunks. It renders the resulting geometries as
one logical route without optimization. It caches them by canonical plan id,
route mode, ordered coordinates, and Mapbox profile; a plan/destination change
invalidates only affected chunks. Proposed-route requests use a separate cache
namespace and are made only while that proposal is visible.

---

# 3. Starting a Trip

When the user presses **Go**:

```text
INACTIVE
   ↓
ACTIVE
```

Client:

```http
POST /api/v1/trips/{trip_id}/activate
```

The activation request includes a required starting location, obtained from
browser GPS or explicitly selected by the user for a simulation. The backend
uses its receipt time as `activated_at`; a client must not choose the
authoritative execution clock.

Backend then:

1. Authenticates the LiveRoute session and validates trip ownership.
2. Requires an inactive saved trip with at least one activity.
3. Rejects with `409 Conflict` if this user already has another activating,
   active, or deactivating trip.
4. Locks the trip revision and materializes the saved relative plan at
   `activated_at` as a new user-authored canonical execution plan.
5. Durably enters `activating` with a unique operation id, then acquires the
   PostgreSQL runtime lease and bootstraps C++ from the committed absolute
   execution plan.
6. Marks the trip active only after bootstrap succeeds. A transient bootstrap
   failure leaves the recoverable operation in `activating`; it does not create
   a second execution plan or report the trip as active.
7. Returns the active canonical state, including each stored activity display
   label and accepted permanent coordinate, with its revision/plan identity.

## Durable execution lifecycle

`inactive` and `active` are the user-visible states. PostgreSQL additionally
stores `activating` and `deactivating` so crashes cannot strand or duplicate a
runtime transition:

```text
inactive -> activating -> active -> deactivating -> inactive
```

Each transition operation stores `operation_id`, request idempotency key,
source trip revision, target execution-plan id (activation only), and last
durable step. A partial unique constraint permits at most one trip per user in
`activating`, `active`, or `deactivating`. `POST .../activate` and
`POST .../deactivate` require an expected trip revision and idempotency key.
Exact replay returns the original operation/result; reuse with different input
is a conflict.

Activation transaction one locks the user and trip, validates the fully
scheduled relative plan, chooses `activated_at` once, creates exactly one
immutable absolute execution plan, and commits `activating`. After commit, a
bounded coordinator obtains the lease and bootstraps the exact plan. Its success
transaction compares `operation_id`, revision, plan id, and lease epoch before
changing `activating` to `active`. A stale worker cannot complete a newer
operation.

Transient lease, planner, or transport failures return `202 Accepted` with the
same operation id and transition status; the client polls the existing
`GET /api/v1/trips/{trip_id}` representation. A startup recovery worker scans a
bounded page of `activating`/`deactivating` rows and resumes their last durable
step. A terminal validation failure before transaction one changes no state. A
terminal bootstrap incompatibility records the failure, fences/releases any
acquired runtime authority, removes only the unstarted execution instance, and
returns the trip to `inactive`; the reusable saved plan remains unchanged.

Deactivation first commits `deactivating`, fences new commands and proposals,
and records its operation id. The coordinator then deactivates or epoch-fences
the C++ runtime and releases its lease. A final compare-and-set transaction
clears execution-only pointers and marks `inactive`. A crash at either point is
resumed by the recovery worker. An inactive exact replay succeeds without
creating work. The API returns `202 Accepted` while a transition is incomplete
and `200 OK` with the terminal trip state after completion.

The client then opens `/ws`, sends the existing `authenticate` message first,
sends `subscribe_trip`, and waits for `subscription_state` before admitting live
controls. The client independently asks Mapbox Directions for the visible route;
the backend does not proxy or return Mapbox geometry. The client sends the
stored coordinates directly to Directions and does not call Mapbox Search to
rediscover activity names or locations during activation.

## Deactivating and reusing a trip

```http
POST /api/v1/trips/{trip_id}/deactivate
```

Deactivation is idempotent. It stops new trip commands, deactivates the C++
runtime, releases the lease, unsubscribes/ends the active session, invalidates
pending proposals, and makes the trip inactive. It preserves the reusable saved
activity definition, user-authored relative order/timing/constraints, and
explicit user edits.

Execution-only state is reset: current activity, completed prefix, activity
started/completed/skipped state, delays, found-closed observations, navigation
baseline, observation sequence, absolute materialized execution plan, and
unaccepted proposals. A later activation creates a new runtime epoch and a new
absolute execution plan. Planner-proposed skips/moves accepted for one execution
do not silently rewrite the reusable saved template; an explicit user edit is
required to change that template.

Subscribing over WebSocket never activates an inactive trip. While a trip is
`activating` or `deactivating`, subscriptions may observe transition status but
must not admit live controls. Only `active` permits V1 telemetry and trip
commands.

---

# Live Trip Mode

The UI should shift from editing toward execution.

Example:

```text
┌──────────────────────────────────────────────┐
│                  Mapbox                     │
│                                             │
│                 ───────● Restaurant         │
│             ─────                           │
│       🔵 ────                               │
│      You                                    │
│                                             │
├──────────────────────────────────────────────┤
│ Next: Restaurant               12 min        │
│ Reservation: 12:30 PM                       │
│ Planned arrival: 12:18 PM                   │
│                                             │
│ Remaining: Park → Train                     │
└──────────────────────────────────────────────┘
```

The user should be able to:

- See current GPS location.
- See the full remaining accepted route.
- Zoom out and see all remaining stops.
- See next activity.
- See important deadline/reservation status.
- Mark activity started/completed/skipped.
- Receive warnings.
- Receive proposed replans.
- Accept or reject proposed replans.

---

# Current Location Flow

The browser/device is responsible for collecting GPS.

The server cannot independently know the user's precise live location.

Flow:

```text
Browser GPS
    │
    ├────► update Mapbox current-location marker
    │
    └────► telemetry to backend
                   │
                   ▼
               C++ planner
```

All meaningful planning decisions still happen server-side.

The client is only:

- Collecting location.
- Rendering location.
- Sending observations.

---

# Telemetry / Active Trip WebSocket

Open or subscribe to the active-trip WebSocket when **Go** is pressed.

Use the existing `liveroute.v1` envelope and message kinds. UI names are mapped
to the fixed schema as follows; they are not new wire-level kinds.

Client behavior → Backend envelope:

```text
authenticate                    → authenticate
subscribe to active trip        → subscribe_trip
ordinary GPS                    → telemetry_update / location
confirmed navigation deviation  → telemetry_update / route_deviation
activity started/completed/skip  → trip_command / activity_status_changed
accept proposal                 → trip_command / accept_proposal
reject proposal                 → trip_command / reject_proposal
reconnect recovery              → resynchronize_trip
```

Backend messages remain:

```text
connection_ready
subscription_state
telemetry_status
command_acknowledgement
planner_notification
plan_proposal
resynchronization_state
error
```

HTTP activation plus `subscription_state` represents TripStarted. RouteUpdated
is client-local Mapbox state. Deadline warnings use `planner_notification`.
Next-activity and canonical-plan changes are derived from a newly published
`subscription_state`. Completion of all activities is UI state; the reusable
trip remains active until explicitly deactivated.

Do not send every possible UI state change as telemetry.

Only send information the backend/planner needs.

---

# Location Update Behavior

The blue dot can update frequently on Mapbox without requiring a new route or a new replan.

Normal GPS update:

```text
GPS update
   ↓
move blue dot
   ↓
send telemetry when appropriate
```

Do not request a new Mapbox Directions route on every GPS sample.

Do not trigger a full C++ replan on every GPS sample.

Ordinary location observations remain state-only under the existing contract.
The route-deviation adapter below produces a separate bounded observation after
a confirmed route change; the backend decides whether its ETA consequence is
material enough to trigger C++.

The ordinary foreground browser policy is fixed by `browser_gps` in
`config/v15-contract-policy.yaml` and mirrored byte-for-value in
`frontend/src/live/gps-policy.json`:

- start high-accuracy `watchPosition` only after active-trip subscription, with
  a 2-second permitted browser cache age and 10-second position timeout;
- accept backend-bound samples only with finite/ranged coordinates, a finite
  positive accuracy of at most 50 meters, age at most 10 seconds, and no more
  than 1 second of future clock skew; use the Geolocation timestamp as
  `observed_at_unix_ms`;
- send the first eligible sample immediately, then only after at least 10 meters
  of movement or as a 5-second stationary heartbeat, with an unconditional
  maximum of one location frame per second;
- keep at most one frame awaiting `telemetry_status` and one replaceable newest
  eligible sample; a 10-second missing status closes the stream for ordinary
  reconnect/resynchronization and the location frame is never retried;
- stop and clear collection while hidden, disconnected, offline, deactivated,
  or unsubscribed; require a fresh callback after recovery and never persist or
  replay GPS; and
- show a stale/degraded location indicator after 15 seconds without an eligible
  sample. Permission denial requires user action; temporary unavailable/timeout
  errors send nothing and may recover through the existing foreground watch.

Every usable callback may still move the local Mapbox marker and accuracy
display. Accuracy is intentionally not added to the strict V1 location payload.
V1.5 does not synthesize or transmit separate velocity/heading observations.
The exact timestamp, haversine distance, slot replacement, acknowledgement, and
visibility rules are normative in `plans/LiveRouteV15HTTPContract.md`.

Potential replan triggers:

- Significant route deviation.
- Deadline slack crosses a threshold.
- Activity completed early/late.
- Reservation becomes endangered.
- Destination is no longer feasible.
- Operating hours invalidate an activity.
- User manually changes canonical plan.

---

# Two-Layer Route Deviation

Web V1.5 has two independent rerouting layers:

```text
browser GPS
    ↓
web route-progress adapter + current Mapbox route
    ↓ confirmed off-route
Mapbox Directions reroutes to the same next activity
    ↓ revised ETA/delay
backend validates, coalesces, and applies significance policy
    ↓ material itinerary impact only
C++ evaluates the remaining itinerary with OSRM
    ↓
optional proposal requiring user acceptance
```

The Mapbox reroute never changes activity membership or order. It replaces only
the road/walking route from the current location to the same next activity.

The browser-side adapter must account for reported GPS accuracy and use bounded
debounce/hysteresis before declaring off-route. It requests Directions only on
a confirmed deviation or accepted canonical destination-sequence change, never
on every GPS sample.

The exact baseline is `liveroute-navigation-v1-baseline-1` in
`config/v15-contract-policy.yaml`, mirrored in
`frontend/src/live/route-deviation-policy.json`, with full algorithms in
`plans/LiveRouteV15HTTPContract.md`. It uses accuracy-adjusted distance to the
current-leg polyline and three samples at least one second apart: walking enters
at 20 meters/exits at 10; driving enters at 35/exits at 15; deviation samples
require accuracy at most 25 meters. Two exit samples clear the state.

Confirmed entry requests Directions only to the same next activity. There is one
in-flight request, a five-second timeout, and one two-second retry for network,
timeout, 429, or 5xx failure. Success has a 15-second cooldown; terminal failure
has a 30-second cooldown and keeps the previous route visibly degraded. A
canonical next-activity change cancels old work and bypasses cooldown.

The top-level wire message remains `telemetry_update`, and its existing
`route_deviation` observation remains exactly:

```text
location
distance_from_route_meters
```

V1.5 must not add standard fields to the strict `liveroute.v1` payload. The
browser attaches the following normalized navigation data under the existing
top-level namespaced `extensions` object, using the exact key
`liveroute.navigation_v1`:

```text
policy_version
next_activity_id
navigation_route_id
previous_eta_unix_ms
updated_eta_unix_ms
remaining_duration_seconds
remaining_distance_meters
off_route
```

The extension object requires all listed fields;
`policy_version = liveroute-navigation-v1-baseline-1`; Unix milliseconds and
bounded counts use JavaScript-safe JSON integers; ids are canonical UUIDs;
distances and durations are nonnegative integers; `off_route = true`; and
`updated_eta_unix_ms` must not precede the observation time. Clients that do not
understand the extension ignore it as required by the existing compatibility
policy. A V1.5-aware backend validates
that the next activity is the current canonical next activity, rejects stale
navigation-route identities, and coalesces replaceable updates. It compares the
ETA change with a configured material-delay threshold and relevant
reservation/open-window/deadline boundaries. Only a material result becomes the
existing explicit C++ `RouteDeviationDetected` event.

A valid `liveroute.v1` route-deviation message without this extension retains
completed V1 behavior: it is treated as an already confirmed explicit deviation
and is forwarded under the existing admission rules. The new filtering applies
only when the V1.5 navigation extension is present and valid; this avoids
reinterpreting older clients.

The backend treats the successful reroute as materially replan-worthy when ETA
worsens by at least five minutes or crosses the next activity's scheduled-start,
reservation-plus-grace, open-window-latest-start, or mandatory-deadline-latest-
start boundary. Otherwise it updates navigation state without invoking C++.
Each successful route gets a new UUID and can emit at most one material event.

This baseline deliberately makes no performance/optimality claim. Recorded
simulated and real-device traces may justify a later named policy version, but
they are calibration work rather than an implementation gate. The exact
extension schema and positive/negative compatibility corpus are now under
`schema/websocket/`; the fixture also proves the completed V1 envelope accepts
and ignores the namespaced object.

A future iOS/Android application may obtain off-route/reroute observations from
Mapbox Navigation SDK instead of the web adapter, then send the same normalized
LiveRoute telemetry.

---

# User-Visible Route

The visible route should represent the **canonical accepted plan**.

Example:

```text
Current location
      ↓
Restaurant
      ↓
Park
      ↓
Train
```

Mapbox Directions generates the visible route through those ordered waypoints.

Mapbox GL JS draws the geometry.

When zoomed out, the user should see the route through all remaining accepted stops.

When zoomed in, the UI can emphasize the active leg while retaining the full route as desired.

---

# Replanning Flow

The C++ planner only suggests changes.

Example canonical plan:

```text
Museum → Restaurant → Park → Train
```

The user becomes late.

C++ planner evaluates OSRM matrices and determines:

```text
Proposed:
Museum → Restaurant → Train

Reason:
Skip Park to protect train deadline.
```

The backend publishes the schema-defined `StoredPlanProposal` payload. For
example (with empty segment arrays only to keep the example compact):

```json
{
  "proposal": {
    "proposal_id": "00000000-0000-4000-8000-000000000001",
    "source_runtime_epoch": "7",
    "source_planner_state_version": "19",
    "base_current_plan_id": "00000000-0000-4000-8000-000000000002",
    "source_trip_revision": "12",
    "source_accepted_mutation_sequence": "18",
    "preserved_prefix": [],
    "revised_suffix": [],
    "created_at_unix_ms": 1786291200000
  },
  "notification": "plan_change_suggested",
  "reasons": ["route_deviation"],
  "stats": {
    "candidates_evaluated": "40",
    "candidates_pruned": "12",
    "search_depth": 4,
    "queue_wait_microseconds": 20,
    "provider_microseconds": 3000,
    "planner_microseconds": 700,
    "serialization_microseconds": 30,
    "deadline_hit": false
  },
  "quality": {
    "plan_quality": "complete",
    "routing_quality": "fresh",
    "recovery_state": "current"
  }
}
```

The frontend consumes the exact schema-defined `plan_proposal` payload rather
than a second UI-only proposal contract.

---

# Proposal Rendering

The canonical route remains the authoritative route.

A proposal should be displayed separately.

Recommended visual behavior:

```text
solid route  = canonical accepted plan
dashed route = proposed replan
```

Example proposal UI:

```text
Suggested change

Skip Park

Saves approximately 37 minutes
Projected train arrival: 4:48 PM

[Accept] [Keep Current Plan]
```

The client must not silently switch to the proposed route.

---

# Accepting a Replan

User presses:

```text
Accept
```

Client sends the existing WebSocket `trip_command / accept_proposal`, echoing
the exact proposal identity: `proposal_id`, `source_runtime_epoch`,
`source_planner_state_version`, and `base_current_plan_id`.

Backend validates:

- Proposal belongs to this trip.
- The exact runtime/planner/base-plan identity still matches.
- The stored `source_trip_revision` still matches canonical trip state.
- Proposal has not already been accepted/rejected.

Acceptance is intentionally runtime-first:

1. Backend durably records the decision intent/outbox row and replies with
   `command_acknowledgement.phase = durable_recorded`.
2. Backend delivers the decision to C++.
3. C++ validates and acknowledges the decision against its active state.
4. Only after that acknowledgement, PostgreSQL atomically inserts the accepted
   execution plan, increments `trip_revision`, advances the finalized mutation
   sequence, and marks the proposal accepted.
5. Backend sends the requester a final schema-defined
   `command_acknowledgement.phase = planner_applied` and broadcasts a fresh
   `subscription_state` to all subscribed clients.
6. Frontend requests new Mapbox Directions geometry directly from the newly
   confirmed canonical stop sequence.

The first `durable_recorded` acknowledgement is not permission to switch routes.
Only `planner_applied` plus a refreshed `subscription_state` whose current plan
matches the accepted proposal outcome permits the frontend to switch routes.
`canonical_committed` is reserved for canonical-first user-authored edits and is
not a proposal-acceptance phase.

V1 has no wall-clock proposal expiry. A proposal becomes unusable when its exact
identity is stale, a canonical edit changes its base/revision, a newer proposal
supersedes it, or the trip deactivates. The frontend should describe this as
"suggestion no longer current," not invent a timer.

---

# Rejecting a Replan

User presses:

```text
Keep Current Plan
```

Client sends the existing WebSocket `trip_command / reject_proposal` with the
same exact proposal identity.

Backend:

- Marks proposal rejected.
- Leaves canonical plan unchanged.
- Finalizes the runtime-first rejection through C++ and PostgreSQL.
- Sends the final acknowledgement and refreshed `subscription_state`.
- Keeps the existing Mapbox route.

---

# Canonical Plan / Revision Model

Maintain explicit revisioning.

Example:

```text
trip_revision = 12
```

C++ proposal:

```text
proposal_id = P42
source_trip_revision = 12
source_runtime_epoch = E
source_planner_state_version = V
base_current_plan_id = C
```

If the user manually edits the trip and backend advances to:

```text
trip_revision = 13
```

then proposal `P42` must no longer be allowed to overwrite revision 13.

This protects against stale asynchronous replanning results.

---

# Mapbox Routing After Replan Acceptance

Before acceptance:

```text
Canonical:
Current → Restaurant → Park → Train

Proposed:
Current → Restaurant → Train
```

Frontend may display:

```text
solid:
Current → Restaurant → Park → Train

dashed:
Current → Restaurant → Train
```

After acceptance:

```text
Canonical:
Current → Restaurant → Train
```

After final backend confirmation, the frontend obtains new Mapbox Directions
geometry and replaces the old canonical route.

---

# OSRM vs Mapbox Responsibilities

Keep this separation strict.

## OSRM Table

Used internally for:

- Pairwise travel times.
- Candidate-plan scoring.
- Replanning feasibility.
- C++ planner computation.

Not intended as the primary visible user-navigation route.

---

## Mapbox Directions

Used for:

- Visible route geometry.
- Route through currently accepted ordered stops.
- Planning preview.
- Active trip route display.

Mapbox does not decide the canonical itinerary.

---

## Mapbox GL JS

Used for:

- Drawing basemap.
- POI markers.
- Current user location.
- Accepted route.
- Proposed route.
- Zoom/pan/user interaction.

---

## Mapbox Search

Used for:

- Human-readable place search.
- Ephemeral POI selection and map preview.

Search Box is never the durable place authority in V1.5. Canonical durable
coordinates and optional addresses come only from the separate Mapbox Geocoding
v6 request made with `permanent=true`.

---

# Important ETA Consistency Rule

Mapbox Directions and OSRM may produce slightly different ETA estimates because they are different routing engines/data pipelines.

For V1.5, avoid mixing unexplained ETA values in the UI.

Choose one source for user-facing ETA display.

Recommended:

- Use Mapbox for visible/navigation ETA shown to the user.
- Use OSRM internally for planner scoring/replanning.

The planner may make conservative decisions based on OSRM while the user-facing route display remains Mapbox-backed.

Planner reason codes may explain that a deadline or reservation is endangered,
but the frontend must not label an OSRM duration as a Mapbox ETA. When showing a
clock arrival or "minutes saved" comparison for canonical versus proposed
routes, calculate both displayed values from Mapbox routes requested for those
exact ordered stops and label temporary/unavailable route data explicitly.

If differences become important later, introduce a provider abstraction or use one routing provider consistently.

---

# Authentication and LiveRoute Sessions

The frontend includes real Sign in with Google using Google Identity Services
and OpenID Connect for authentication only. This requires Google OAuth/OIDC
configuration:

- a Google Cloud project and configured OAuth consent screen;
- a Web application client id;
- exact authorized JavaScript origins for local and production frontend URLs;
- `openid`, `email`, and `profile` only; and
- separate future iOS/Android client ids in the same project.

External authentication references: [Google Identity OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect)
and [Google ID-token claims](https://developers.google.com/identity/openid-connect/reference).

The browser obtains a Google ID-token credential and posts it over HTTPS to the
backend. The backend verifies its signature and `iss`, `aud`, `exp`, and nonce,
requires a verified email when email is stored, and keys the external identity
by Google's stable `sub`, never by mutable email. LiveRoute does not retain a
Google access or refresh token because it does not call Google APIs on the
user's behalf.

After verification, the backend creates its own opaque revocable session and
sets only a `Secure`, `HttpOnly`, `SameSite=Lax` cookie. Session identifiers are
stored hashed with expiry/revocation metadata. Mutating HTTP operations also use
origin checks and a CSRF token/header. Logout revokes the LiveRoute session and
clears the cookie.

Browser WebSockets cannot attach a custom authorization header and JavaScript
must not read the HttpOnly session cookie. An authenticated HTTP endpoint mints
a short-lived, single-use 43-character base64url WebSocket ticket. The client
places that ticket in the existing first `authenticate` message; the backend
consumes it once and binds the connection to the session user. Development
tokens remain a non-production test mechanism only.

No Google client secret, session secret, Mapbox server-side Permanent Geocoding
token, or token with secret scopes may appear in frontend source, committed
configuration, logs, or WebSocket payloads.

---

# Normative V1.5 HTTP Surface

The `/api/v1` prefix is the first HTTP API contract version; it does not rename
the V1.5 product milestone or the existing `liveroute.v1` WebSocket protocol.
These methods and paths are fixed. Their exact schemas, status/error bodies,
idempotency headers, and compatibility fixtures are normative in
`schema/http/liveroute-v1.5.openapi.yaml` and its checked corpus. Cross-cutting
authentication, session, compatibility, provider, and retention rules are
normative in `plans/LiveRouteV15HTTPContract.md` and
`config/v15-contract-policy.yaml`.

```text
POST   /api/v1/auth/google/nonce
POST   /api/v1/auth/google
GET    /api/v1/session
POST   /api/v1/auth/logout
POST   /api/v1/auth/ws-ticket

GET    /api/v1/trips
POST   /api/v1/trips
GET    /api/v1/trips/{trip_id}
PATCH  /api/v1/trips/{trip_id}
DELETE /api/v1/trips/{trip_id}

POST   /api/v1/trips/{trip_id}/activities
PATCH  /api/v1/trips/{trip_id}/activities/{activity_id}
DELETE /api/v1/trips/{trip_id}/activities/{activity_id}

POST   /api/v1/places/resolve
POST   /api/v1/places

POST   /api/v1/trips/{trip_id}/activate
POST   /api/v1/trips/{trip_id}/deactivate
```

Mapbox search/directions requests go directly from the browser. Proposal
accept/reject and activity lifecycle operations use the existing WebSocket only
while active.

`POST /api/v1/places` is the only place-resolution persistence step. It accepts
the opaque resolution token and an idempotency key; it must reject an
expired/tampered token or any attempt to change the token-bound permanent
coordinate, address, or timezone. It returns the newly created or
exactly replayed canonical `Place`.

Inactive POST/PATCH/DELETE operations require an idempotency key and expected
trip revision. They reuse the existing canonical validation and PostgreSQL
transaction code but must not activate C++, acquire a runtime lease, or create
an indefinitely pending planner-mirror backlog. Structural editing is disabled
in Live Trip mode for V1.5; active execution changes are
activity lifecycle commands and proposal decisions.

---

# Required Client State Distinctions

The frontend should clearly distinguish:

```text
trip
trip_name
saved_relative_plan
trip_revision
optional_display_schedule

active_trip_state
execution_transition_operation
active_canonical_execution_plan
activated_at

current_location

visible_route_geometry

pending_replan_proposal
proposed_route_geometry

websocket_connection_state
```

Do not overwrite `active_canonical_execution_plan` when a proposal arrives.

Instead:

```text
active_canonical_execution_plan = unchanged
pending_replan_proposal = proposal
```

Only replace active canonical execution state after backend confirms proposal
acceptance. The saved relative plan remains unchanged by an execution-specific
proposal.

---

# Required Backend Durable State

Persist durable data such as:

```text
users
external_identities
sessions
single_use_websocket_tickets
trips
trip_name
trip_execution_state
trip_execution_operations
activities
places
saved_relative_plan
active_absolute_execution_plan
activated_at
trip_revision
accepted/saved plan state
replan_proposals
proposal_status
command/idempotency records
snapshots/recovery data as required
```

Keep active planner state in memory in the C++ service.

The C++ planner hot path should not query the database while evaluating candidate plans.

---

# Primary End-to-End User Flow

```text
1. User signs in with Google; backend creates a LiveRoute session.

2. User opens the Trips page and creates a client-local empty trip.

3. User searches through temporary Mapbox Search Box data, selects a POI, and
   chooses its preview pin.

4. Backend makes one Mapbox Geocoding v6 reverse request with `permanent=true`.
   The UI shows the resulting durable address/pin/coordinate; the user confirms
   it, and the backend persists that result plus a locally derived US IANA
   timezone. Temporary Search Box fields are not stored.

5. User adds at least one activity, relative timing, and constraints.

6. HTTP stores the inactive saved trip and relative user-authored plan directly
   in PostgreSQL as canonical saved state; no C++ approval/runtime is involved.

7. Optional display schedule anchors clock labels; direct Mapbox Directions
   renders the canonical saved order.

8. User presses Go.

9. Backend materializes absolute execution times once, durably enters
   `activating`, bootstraps C++, and marks this user's trip active only after the
   exact operation succeeds. Crash recovery resumes either transition state.

10. Client obtains a one-use WebSocket ticket, authenticates with the existing
    envelope, subscribes, and receives `subscription_state`.

11. Client begins GPS/activity telemetry; Mapbox GL JS displays current location
    and Mapbox Directions renders the accepted remaining route from the stored
    PostgreSQL coordinates without a new POI search.

12. Confirmed browser off-route movement first causes a direct Mapbox reroute to
    the same next activity. Only material ETA consequences reach C++.

13. C++ uses OSRM matrices to evaluate the remaining itinerary. If no change is
    needed:
      canonical plan remains unchanged.

14. If a better/necessary plan exists:
      C++ planner returns a proposal.

15. Backend stores/publishes proposal.

16. Frontend displays proposal separately from canonical plan.

17a. User rejects:
      existing plan remains canonical.

17b. User accepts:
      backend verifies exact proposal identity,
      records and delivers the runtime-first decision,
      C++ acknowledges,
      PostgreSQL commits the accepted execution plan,
      backend sends final acknowledgement/subscription state,
      frontend directly requests replacement Mapbox geometry.

18. User continues the trip.

19. Completed activities are omitted from the rendered remaining route but
    retained in durable execution state/history.

20. User deactivates; backend durably enters `deactivating`, tears down or
    epoch-fences C++, resets execution-only state, and returns the reusable saved
    trip to inactive. Crash recovery resumes this operation idempotently.
```

---

# Core Invariants

Implementation must preserve these invariants:

1. **The user's accepted plan is canonical.**
2. **The C++ planner cannot directly mutate canonical trip state.**
3. **Every replan is a proposal until explicitly accepted.**
4. **A proposal is tied to the canonical revision it was generated from.**
5. **Stale proposals cannot overwrite newer canonical state.**
6. **Mapbox always renders the accepted canonical route as the primary route.**
7. **Proposed routes are visually separate until accepted.**
8. **OSRM is primarily an internal planner travel-time provider.**
9. **Mapbox Directions provides user-visible route geometry.**
10. **Mapbox Search provides POI lookup.**
11. **The browser collects GPS; planning decisions remain server-side.**
12. **GPS movement alone does not automatically mutate the itinerary.**
13. **Do not reroute or fully replan on every GPS update.**
14. **Planner candidate evaluation must use in-memory travel-time matrices, not network calls.**
15. **Frontend state must keep canonical and proposed plans separate.**
16. **Inactive saved trips never keep a C++ runtime or planner lease.**
17. **At most one trip per user is activating, active, or deactivating.**
18. **Saved relative plans and active absolute execution plans are distinct.**
19. **Deactivation preserves user-authored saved state and resets execution-only state.**
20. **Only exact final backend confirmation replaces the active canonical route.**
21. **Temporary Search Box, Maps, and Directions calls are direct browser calls; Permanent Geocoding is backend-only.**
22. **Only the user-confirmed Permanent Geocoding coordinate/address result is persisted; temporary Search Box POI fields are never durable.**
23. **The web client does not claim to use Mapbox Navigation SDK; that is a future native-app adapter.**
24. **Google identity establishes a LiveRoute session; Google credentials are not used as application sessions.**
25. **The inactive Trips list displays trip names only; opening a trip loads its full editable plan.**

---

# Initial Frontend Implementation Order

## Contract foundation status

The cross-cutting V1.5 contract foundation is fixed before handler and frontend
feature implementation begins:

1. `schema/http/liveroute-v1.5.openapi.yaml`, its corpus, manifest, and
   `scripts/check-http-contract.py` define and verify the versioned HTTP surface.
2. `migrations/00004_v15_frontend_foundation.sql` adds the forward-only saved
   trip, immutable place, authentication/session, idempotency, and execution
   transition storage model. Its focused migration contract test is
   `tests/migrations/00004_v15_foundation_contract.sql`.
3. `plans/LiveRouteV15HTTPContract.md` and
   `config/v15-contract-policy.yaml` fix session, CSRF, WebSocket-ticket,
   idempotency, provider, origin, retention, and compatibility behavior.
4. `config/frontend-toolchain.lock` and `docker/frontend/Dockerfile` pin the
   frontend build/test toolchain and dependency policy.
5. `config/timezone-boundaries.lock` pins the offline US
   coordinate-to-IANA-timezone dataset, license, digest, container path, and
   deterministic boundary policy.

These artifacts specify behavior but do not themselves implement the Go HTTP
handlers, the timezone resolver, or the React application. Ordinary browser GPS
accuracy, cadence, coalescing, acknowledgement, foreground, and offline behavior
is fixed in the contract policy. The same policy now fixes the named
same-destination route-deviation baseline, including hysteresis, ETA materiality,
Directions cooldown/retry, and degraded fallback. Trace collection may calibrate
a future policy version but no longer blocks implementation.

## Required integration and failure coverage

Before calling frontend integration complete, tests must prove:

- Google token verification rejects invalid issuer/audience/signature/expiry and
  account identity is keyed by `sub`, not email;
- session cookie, CSRF, logout/revocation, origin checks, and one-use/expired
  WebSocket tickets behave correctly without leaking credentials;
- inactive list responses render name-only summaries; opening a trip returns its
  full plan; unnamed/empty trips are not persisted and named one-activity trips
  are accepted;
- no temporary Search Box suggestion/retrieval field appears in PostgreSQL,
  server logs, analytics, activation payloads, or saved activity labels;
- place resolution performs no chargeable Permanent Geocoding request before
  explicit user selection and performs exactly one per resolution attempt;
  empty/out-of-US/provider-failure, final cancellation, resolution-token
  tampering/expiry, and invalid-timezone paths preserve canonical state;
- a permanent result that differs from the temporary pin is visibly presented
  and cannot be persisted without final confirmation;
- the accepted Place supplies the permanent coordinate, optional address-derived
  display label, and validated US IANA timezone, and every later preview,
  activation, OSRM lookup, and Mapbox route uses that same coordinate without a
  new Search Box lookup;
- provider data changes never rewrite an accepted Place during activation;
  correction creates a replacement Place through a revision-checked trip edit;
- inactive save/edit/list never activates C++, acquires a lease, or accumulates
  planner delivery work;
- activation rejects unscheduled activities and any materialized value outside
  the V1 current-day planning horizon;
- concurrent/retried activation admits one transitioning/active trip per user,
  chooses `activated_at` and materializes identical relative offsets exactly
  once, resumes `activating` after crashes, and never reports active before the
  exact C++ bootstrap succeeds;
- deactivation is idempotent, resumes `deactivating` after crashes, releases or
  fences runtime ownership, invalidates proposals, resets execution-only state,
  and preserves the reusable saved plan;
- HTTP idempotency/revision conflicts cannot duplicate or overwrite edits;
- direct Mapbox route chunking preserves exact stop order/mode across the
  64-activity limit and provider failures do not mutate canonical state;
- deterministic fake-clock/geolocation/WebSocket tests prove the first eligible
  location sends immediately, the one-per-second ceiling, 10-meter movement and
  five-second heartbeat gates, latest-only pending replacement, matching-status
  release, and the 10-second acknowledgement timeout;
- GPS tests reject stale/future/nonfinite/out-of-range/>50-meter samples, never
  synthesize velocity/heading, stop on permission denial, show degraded state
  after 15 seconds, pause while hidden/offline/disconnected, retain no durable
  coordinates, and require a fresh callback rather than replay after recovery;
- ordinary GPS never triggers planning, confirmed off-route movement reroutes to
  the same next activity, immaterial ETA changes do not trigger C++, and material
  changes emit one bounded explicit deviation event;
- proposal identity/staleness and runtime-first accept/reject survive reconnect,
  retry, service crash, and PostgreSQL/C++ unavailability;
- the requester receives final `planner_applied` and every subscriber sees
  the same refreshed `subscription_state` before replacing canonical geometry;
  and
- browser JSON messages pass the repository's canonical schema/manifest corpus,
  including compatibility cases proving the navigation extension is accepted by
  old `liveroute.v1` validators and ignored by clients that do not understand it.

## Ordered slices

1. Validate the pinned contract foundation: HTTP schema/corpus, migration,
   toolchain, provider policy, origin policy, and timezone dataset locks.

2. Generate frontend HTTP types from the normative OpenAPI artifact and make
   generation drift a build failure.

3. Apply the V1.5 forward migration and implement its Go persistence adapters.

4. Implement Google OIDC verification, LiveRoute sessions, CSRF defense,
   logout/revocation, and single-use WebSocket tickets.

5. Implement authenticated inactive-trip HTTP CRUD and durable place resolution.

6. Change runtime lifecycle so save/edit/subscribe do not implicitly activate;
   implement atomic/idempotent activate and deactivate orchestration.

7. Publish final `planner_applied` acknowledgements and refreshed
   `subscription_state` after proposal decision finalization.

8. Define the namespaced navigation extension compatibility corpus from measured
   traces and implement bounded backend significance filtering without changing
   the strict `liveroute.v1` route-deviation payload.

9. Create main app navigation:
   - Trips
   - Planner
   - Live Trip

10. Implement Google sign-in/session restoration and the Trips page backed by
    inactive-trip HTTP CRUD.

11. Integrate Mapbox GL JS with a URL-restricted public token.

12. Integrate temporary Mapbox POI search, one backend Permanent Geocoding
    reverse request after selection, final durable-result confirmation, and the
    offline timezone resolver.

13. Build Planner activity list, relative scheduling, constraints, optional
    display schedule, and deterministic required-field defaults.

14. Save the canonical inactive trip through HTTP.

15. Render canonical route preview directly using Mapbox Directions.

16. Implement **Go** activation, one-use WebSocket authentication, and initial
    `subscription_state` gating.

17. Implement browser geolocation for active trips using the fixed foreground
    GPS policy and fake-clock/geolocation coverage.

18. Implement active-trip Mapbox UI and same-destination Directions rerouting.

19. Send ordinary telemetry with one in-flight frame plus one latest pending
    sample through the existing WebSocket envelope. Add confirmed-deviation
    telemetry using the fixed named baseline and its extension corpus.

20. Display backend planner notifications.

21. Add replan proposal state/UI.

22. Render proposed route separately from canonical route.

23. Implement Accept / Keep Current Plan with the exact proposal identity.

24. Replace the canonical route only after final backend confirmation.

25. Implement deactivation/reset/reuse and inactive-trip return flow.

26. Add responsive/mobile web layout after core behavior works.

---

# Scope Notes

For V1.5:

- Keep automated itinerary replanning server-side; the user's manually authored
  relative plan is constructed in the frontend and becomes canonical on save.
- Do not port the planner to the browser.
- Do not implement local/offline replanning yet.
- Do not make Mapbox optimize/reorder itinerary stops.
- Do not use OSRM as the frontend map.
- Do not update canonical state directly from planner output.
- Do not make every GPS update trigger full routing/replanning.
- Keep the frontend primarily responsible for interaction, rendering, GPS collection, and proposal approval.
- Keep Mapbox Navigation SDK integration for future native iOS/Android apps.

The core product loop is:

```text
PLAN → GO → EXECUTE → OBSERVE → PROPOSE → USER APPROVES → UPDATE CANONICAL PLAN
```

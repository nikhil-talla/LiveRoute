# LiveRoute Frontend + Trip Execution Planning Instructions

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
- Enforcing at most one active trip per user.
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

Use Mapbox directly from the web client for the frontend travel experience. The
Go backend does not proxy ordinary map, search, or Directions requests and does
not receive Mapbox route geometry. The browser uses a least-privilege Mapbox
public token restricted to the exact development and production origins; a
Mapbox secret token must never enter browser code or a committed file.

### Mapbox Search Box API

Use for:

- Restaurant search.
- Attraction search.
- Museum search.
- Hotel search.
- POI autocomplete/selection.

Search results remain ephemeral until the durable-place-source decision below
has been satisfied. Do not persist a Mapbox Search Box provider id, address, or
coordinates merely by copying them through another API.

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
is reserved for future native iOS and Android applications. It is not a web-V1
dependency.

The web client uses GL JS plus Directions and implements the bounded browser
route-progress/deviation adapter specified below. A future native client may
replace that adapter with Mapbox Navigation SDK observations without changing
canonical-plan ownership or the backend/C++ boundary.

External capability reference: [Mapbox Navigation products overview](https://docs.mapbox.com/help/getting-started/navigation/).

### Durable place-source gate

Mapbox Search Box is useful for temporary autocomplete, but its standard terms
do not automatically authorize durable storage of returned position data.
Geocodio supports durable address geocoding, but it is an address geocoder: it
does not provide POI autocomplete or durable POI identity. Sending Mapbox-derived
address/coordinate fields to Geocodio does not, by itself, remove the Mapbox
source-data restriction.

Before implementing durable POI saving, select and document one approved path:

1. obtain Mapbox terms that explicitly permit the required saved POI fields;
2. select a POI/autocomplete provider whose terms permit durable storage; or
3. use Mapbox only for an ephemeral preview, require the user to provide/confirm
   an independently sourced postal address, and let the backend resolve that
   user-provided address through Geocodio.

Path 3 can durably retain only the Geocodio result and user-authored label. It
must not claim that Geocodio verified the Mapbox POI identity. This provider and
terms decision is a frontend-integration gate, not behavior to guess during
implementation.

External source references: [Mapbox Search Box restrictions](https://docs.mapbox.com/api/search/search-box/),
[Geocodio API reference](https://new.geocod.io/docs/), and
[Geocodio POI/autocomplete comparison](https://www.geocod.io/geocodio-vs-tomtom/).

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

- Saved inactive trips.
- The user's single active trip, if one exists.
- Optional planning date/start-time metadata.
- Number of activities.
- Execution state (`inactive` or `active`).

Example backend request:

```http
GET /api/v1/trips
```

User-visible execution states:

```text
INACTIVE
ACTIVE
```

Selecting an inactive trip opens the editable planner. A trip becomes saveable
once it contains at least one valid activity; an empty trip is client-local and
must not be persisted.

The optional planning date/start time is presentation metadata. It can order the
Trips page and anchor preview clock labels, but it does not automatically
activate a trip and is not the execution clock.

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

---

# Relative Saved Time and Activation Materialization

Inactive saved plans use offsets relative to trip activation rather than fixed
execution timestamps. Each scheduled activity has a relative start and end
offset; availability windows, reservations, and deadlines that constrain the
simulation are likewise represented relative to activation. Durations remain
fixed under the existing V1 no-shortening rule.

If a display schedule is present, the frontend adds these offsets to the
display-only anchor to show projected local clock times. If absent, it displays
durations and relative offsets. Changing the display anchor does not change the
relative plan.

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

The exact REST DTO and PostgreSQL migration must therefore distinguish the
saved relative plan from the active absolute execution plan. Do not encode an
unscheduled activity as an `omitted` current-plan segment.

---

# POI Search and Selection

The user should never enter only a raw restaurant name into the canonical trip.

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
Map shows an ephemeral selected marker
        ↓
Approved durable-place resolver produces persistable fields
        ↓
Backend stores structured place data
```

Store only fields whose source terms explicitly permit LiveRoute's durable use.

Application place representation should conceptually include:

```json
{
  "internal_place_id": "...",
  "provider": "approved_durable_provider",
  "provider_place_id": "...",
  "display_name": "Shake Shack",
  "formatted_address": "...",
  "latitude": 42.281,
  "longitude": -83.748
}
```

The backend assigns its own `internal_place_id`. A Mapbox Search Box identifier
must not be placed in `provider_place_id` unless the selected Mapbox terms
explicitly allow that retention.

Activities should reference the application's place ID rather than duplicating place data everywhere.

Example:

```cpp
struct Activity {
    ActivityId id;
    PlaceId place_id;

    // timing, flexibility, reservation, etc.
};
```

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
3. Rejects with `409 Conflict` if this user already has another active trip.
4. Locks the trip revision and materializes the saved relative plan at
   `activated_at` as a new user-authored canonical execution plan.
5. Acquires the PostgreSQL runtime lease and bootstraps C++ from the committed
   absolute execution plan.
6. Marks the trip active only after bootstrap succeeds; a failed activation
   leaves it inactive and returns a retryable failure.
7. Returns the active canonical state and its exact revision/plan identity.

The client then opens `/ws`, sends the existing `authenticate` message first,
sends `subscribe_trip`, and waits for `subscription_state` before admitting live
controls. The client independently asks Mapbox Directions for the visible route;
the backend does not proxy or return Mapbox geometry.

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

Web V1 has two independent rerouting layers:

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

The top-level wire message remains `telemetry_update`. The existing
`route_deviation` observation must be additively extended before this slice with:

```text
next_activity_id
navigation_route_id
previous_eta_unix_ms
updated_eta_unix_ms
remaining_duration_seconds
remaining_distance_meters
off_route
location
distance_from_route_meters
```

The backend validates that the next activity is the current canonical next
activity, rejects stale navigation-route identities, and coalesces replaceable
updates. It compares the ETA change with a configured material-delay threshold
and relevant reservation/open-window/deadline boundaries. Only a material result
becomes the existing explicit C++ `RouteDeviationDetected` event. The exact
threshold, hysteresis, and maximum observation cadence must be selected and
covered by tests before implementing this adapter; they are the remaining
route-deviation product policy, not values to invent in code.

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

The backend creates a proposal:

```json
{
  "proposal_id": "...",
  "source_runtime_epoch": "...",
  "source_planner_state_version": "...",
  "base_current_plan_id": "...",
  "source_trip_revision": 12,
  "preserved_prefix": "...",
  "revised_suffix": "..."
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
   `command_acknowledgement.phase = canonical_committed` and broadcasts a fresh
   `subscription_state` to all subscribed clients.
6. Frontend requests new Mapbox Directions geometry directly from the newly
   confirmed canonical stop sequence.

The first `durable_recorded` acknowledgement is not permission to switch routes.
Only the final committed acknowledgement/state makes the proposal canonical.

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

It is not the durable place authority unless the selected Mapbox terms expressly
permit the stored fields. Canonical durable coordinates come from the approved
durable resolver described above.

---

# Important ETA Consistency Rule

Mapbox Directions and OSRM may produce slightly different ETA estimates because they are different routing engines/data pipelines.

For v1, avoid mixing unexplained ETA values in the UI.

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

No Google client secret, session secret, Mapbox token with secret scopes, or
Geocodio API key may appear in frontend source, committed configuration, logs,
or WebSocket payloads.

---

# Suggested HTTP Endpoints

```text
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

POST   /api/v1/trips/{trip_id}/activate
POST   /api/v1/trips/{trip_id}/deactivate
```

Mapbox search/directions requests go directly from the browser. Proposal
accept/reject and activity lifecycle operations use the existing WebSocket only
while active.

Inactive POST/PATCH/DELETE operations require an idempotency key and expected
trip revision. They reuse the existing canonical validation and PostgreSQL
transaction code but must not activate C++, acquire a runtime lease, or create
an indefinitely pending planner-mirror backlog. Structural editing is disabled
in Live Trip mode for the first frontend version; active execution changes are
activity lifecycle commands and proposal decisions.

---

# Suggested Client State

The frontend should clearly distinguish:

```text
trip
saved_relative_plan
trip_revision
optional_display_schedule

active_trip_state
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

# Suggested Backend State

Persist durable data such as:

```text
users
external_identities
sessions
single_use_websocket_tickets
trips
trip_execution_state
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

3. User searches Mapbox for an ephemeral POI preview.

4. The approved durable-place path resolves persistable place fields.

5. User adds at least one activity, relative timing, and constraints.

6. HTTP stores the inactive saved trip and relative user-authored plan directly
   in PostgreSQL as canonical saved state; no C++ approval/runtime is involved.

7. Optional display schedule anchors clock labels; direct Mapbox Directions
   renders the canonical saved order.

8. User presses Go.

9. Backend materializes absolute execution times at `activated_at`, persists the
   canonical execution plan, bootstraps C++, and marks this user's trip active.

10. Client obtains a one-use WebSocket ticket, authenticates with the existing
    envelope, subscribes, and receives `subscription_state`.

11. Client begins GPS/activity telemetry; Mapbox directly displays current
    location and the accepted remaining route.

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

20. User deactivates; backend tears down C++, resets execution-only state, and
    returns the reusable saved trip to inactive.
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
17. **At most one trip per user is active.**
18. **Saved relative plans and active absolute execution plans are distinct.**
19. **Deactivation preserves user-authored saved state and resets execution-only state.**
20. **Only exact final backend confirmation replaces the active canonical route.**
21. **Mapbox web APIs are called directly from the frontend with a scoped public token.**
22. **Mapbox Search data is not persisted without an approved durable-use path.**
23. **The web client does not claim to use Mapbox Navigation SDK; that is a future native-app adapter.**
24. **Google identity establishes a LiveRoute session; Google credentials are not used as application sessions.**

---

# Initial Frontend Implementation Order

## Remaining preimplementation gates

The product/architecture direction above is fixed. These exact artifacts or
bounded policy values must still be selected before their owning slices begin:

1. Durable POI provider/terms and the exact persistable place fields. The
   Mapbox-to-Geocodio pass-through is not approved by this plan.
2. A versioned HTTP contract (OpenAPI plus request/response compatibility
   corpus) for auth, sessions, inactive-trip CRUD, relative plans, activation,
   deactivation, errors, revisions, and idempotency.
3. Forward PostgreSQL migrations separating saved relative plan identity from
   resettable active execution-plan identity and enforcing one active trip per
   user. Existing applied migrations must not be rewritten.
4. Exact session idle/absolute lifetimes, rotation/revocation policy, CSRF token
   lifetime, WebSocket-ticket lifetime, and signing/encryption key rotation.
5. Exact browser off-route distance/accuracy hysteresis, ETA materiality rule,
   debounce/cadence, and Mapbox failure fallback. These values require recorded
   simulated traces rather than arbitrary constants.
6. Pinned frontend toolchain and dependency policy, including Node/package
   manager versions, React/TypeScript build tooling, generated API/schema types,
   formatting, tests, and container build image.
7. Exact same-site production topology and local development origins so session
   cookies, CSRF, Mapbox URL restrictions, backend CORS, and WebSocket origin
   checks agree.

No other unresolved service-ownership decision is known. Items 1 and 5 are the
remaining external/product choices; items 2-4 and 6-7 are required engineering
contracts/configuration that should be pinned in their implementation slices.

## Required integration and failure coverage

Before calling frontend integration complete, tests must prove:

- Google token verification rejects invalid issuer/audience/signature/expiry and
  account identity is keyed by `sub`, not email;
- session cookie, CSRF, logout/revocation, origin checks, and one-use/expired
  WebSocket tickets behave correctly without leaking credentials;
- empty trips are not persisted and one-activity trips are accepted;
- inactive save/edit/list never activates C++, acquires a lease, or accumulates
  planner delivery work;
- concurrent/retried activation admits one active trip per user, materializes
  identical relative offsets exactly once, and leaves the trip inactive on a
  failed C++ bootstrap;
- deactivation is idempotent, releases runtime ownership, invalidates proposals,
  resets execution-only state, and preserves the reusable saved plan;
- HTTP idempotency/revision conflicts cannot duplicate or overwrite edits;
- direct Mapbox route chunking preserves exact stop order/mode across the
  64-activity limit and provider failures do not mutate canonical state;
- ordinary GPS never triggers planning, confirmed off-route movement reroutes to
  the same next activity, immaterial ETA changes do not trigger C++, and material
  changes emit one bounded explicit deviation event;
- proposal identity/staleness and runtime-first accept/reject survive reconnect,
  retry, service crash, and PostgreSQL/C++ unavailability;
- the requester receives final `canonical_committed` and every subscriber sees
  the same refreshed `subscription_state` before replacing canonical geometry;
  and
- browser JSON messages pass the repository's canonical schema/manifest corpus,
  including compatibility cases for the additive deviation observation.

## Ordered slices

1. Resolve the durable POI/provider-terms gate and pin the selected provider/API
   versions, stored fields, secret boundary, failure mapping, and test fixtures.

2. Extend the normative REST/storage contract for inactive/active state, the
   saved relative plan, optional display anchor, active absolute execution plan,
   reset semantics, one-active-trip enforcement, idempotency, and revisions.

3. Add PostgreSQL forward migrations and persistence tests for that model.

4. Implement Google OIDC verification, LiveRoute sessions, CSRF defense,
   logout/revocation, and single-use WebSocket tickets.

5. Implement authenticated inactive-trip HTTP CRUD and durable place resolution.

6. Change runtime lifecycle so save/edit/subscribe do not implicitly activate;
   implement atomic/idempotent activate and deactivate orchestration.

7. Publish final `canonical_committed` acknowledgements and refreshed
   `subscription_state` after proposal decision finalization.

8. Extend `telemetry_update / route_deviation` with the normalized navigation
   fields and implement bounded backend significance filtering.

9. Create main app navigation:
   - Trips
   - Planner
   - Live Trip

10. Implement Google sign-in/session restoration and the Trips page backed by
    inactive-trip HTTP CRUD.

11. Integrate Mapbox GL JS with a URL-restricted public token.

12. Integrate ephemeral Mapbox POI search plus the approved durable resolver.

13. Build Planner activity list, relative scheduling, constraints, optional
    display schedule, and deterministic required-field defaults.

14. Save the canonical inactive trip through HTTP.

15. Render canonical route preview directly using Mapbox Directions.

16. Implement **Go** activation, one-use WebSocket authentication, and initial
    `subscription_state` gating.

17. Implement browser geolocation for active trips.

18. Implement active-trip Mapbox UI and same-destination Directions rerouting.

19. Send ordinary and confirmed-deviation telemetry through the existing
    WebSocket envelope.

20. Display backend planner notifications.

21. Add replan proposal state/UI.

22. Render proposed route separately from canonical route.

23. Implement Accept / Keep Current Plan with the exact proposal identity.

24. Replace the canonical route only after final backend confirmation.

25. Implement deactivation/reset/reuse and inactive-trip return flow.

26. Add responsive/mobile web layout after core behavior works.

---

# Scope Notes

For the first frontend version:

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

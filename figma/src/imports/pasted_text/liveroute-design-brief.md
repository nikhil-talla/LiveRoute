# LiveRoute frontend design brief

Design a polished responsive web application for LiveRoute, a trip-planning product that helps users create reusable itineraries and adapt them when travel conditions change.

Core message:

> Plans that move with you.
> 

Supporting message:

> Build the plan. Keep the flexibility.
> 

LiveRoute does not silently change the user’s itinerary. It may suggest changes, but the user must explicitly accept them.

## Product structure

The primary application flow is:

```
Public landing → Google sign-in → Trips → Planner → Go → Live Trip
                                                   ↓
                                            Suggested change
                                                   ↓
                                      Accept or keep current plan
```

The authenticated product has three main areas:

- Trips
- Planner
- Live Trip

## Required public pages

### 1. Marketing landing page

Create a public homepage for visitors who are not signed in.

Required sections:

- Header with LiveRoute logo
- Primary CTA: “Sign in with Google”
- Hero:
    - “Plans that move with you”
    - Explain that users can save an itinerary and adapt it when travel changes
- Product explanation:
    1. Plan your day
    2. Start when you’re ready
    3. Get suggestions when conditions change
    4. Decide whether to accept them
- Map/itinerary visual
- Short explanation that the user remains in control
- Secondary CTA to sign in
- Footer with simple product/legal links

Do not invent pricing, team plans, testimonials, enterprise features, or unsupported capabilities.

Avoid claiming:

- Background location tracking
- Voice navigation
- Lane guidance
- Native mobile navigation
- Fully automatic itinerary changes
- Offline replanning

### 2. Sign-in page

Use the existing visual direction:

- LiveRoute logo placeholder
- Large headline: “Build the plan. Keep the flexibility.”
- Supporting copy
- Google sign-in button
- Example itinerary route on the existing map on the right on desktop
- Hide the example card on smaller screens

Include states for:

- Loading Google sign-in
- Google configuration unavailable
- Authentication failed
- Authentication temporarily unavailable
- Retry
- Successful authentication transition

Use user-friendly language. Do not expose tokens, nonce values, provider errors, or backend terminology.

### 3. Authentication/loading/error pages

Design:

- Session restoration/loading
- Temporary connection failure
- Retry state
- Signed-out state
- Generic not-found or invalid-route state

Example error language:

> We couldn’t load your trips.
> 

> Connection interrupted.
> 

> Try again.
> 

## Authenticated application shell

### Desktop

Use a top menu bar containing:

- LiveRoute logo at top left
- My Trips (center left), has option to create trip, taking you to create trip page
- Create Trip (has option to go to live trip mode)
- Live Trip
- User profile picture at top right
- User name
- “Signed in”
- Sign out

The Live Trip navigation item should appear disabled or subdued when there is no active trip.

### Mobile

Use:

- LiveRoute logo placeholder top left
- Compact top ba
- User control
- Horizontally scrollable or compact navigation
- Stacked page content
- Sticky or easily reachable primary actions

Minimum supported viewport: 320px wide.

## Visual direction

The current product direction is calm, editorial, warm, and map-oriented.

Visual qualities:

- mostly rectangular but slightly rounded cards
- Strong typography hierarchy
- Generous whitespace
- Clear map/list relationship
- Subtle shadows and borders
- Avoid overly glossy SaaS styling
- Avoid excessive gradients
- Avoid overly technical dashboards

## My Trips page

This is the authenticated dashboard.

### Header

- Eyebrow: “Your itineraries”
- Heading: “Trips”
- Supporting text: “Open a saved plan, or start shaping a new day.”
- Primary action: “New trip”

### Active trip section

Show separately when the user has an active trip.

Include:

- Eyebrow: “In progress”
- Heading: “Active trip”
- Trip name
- Revision or last-updated detail if useful
- Active status pill
- “Open live view”

### Saved trips section

Show inactive trips as name-focused rows or cards.

Each row should include:

- Trip icon or route marker
- Trip name
- Saved/inactive status
- Optional revision detail
- Chevron/action affordance

The inactive list should not show full activity details.

### Empty state

When there are no saved trips:

- Friendly illustration or route motif
- “No saved trips yet”
- Short explanation
- “Plan your first trip”
- “New trip” CTA

### Trips states

Design:

- Loading
- Empty
- One saved trip
- Multiple saved trips
- Active trip plus saved trips
- API failure
- Retry
- Sign-out in progress
- Sign-out failure

## New trip planner

### Page header

- Eyebrow: “New itinerary”
- Heading: “Plan a new trip”
- Supporting text: “Start with a name and a confirmed destination.”
- Back to Trips

### Trip details

Fields:

- Trip name
- Trip timezone
- Optional display-only date
- Optional display-only time
- Optional display-only timezone

The timezone starts from the user’s stored default but may be changed for this trip.

Make it clear that the optional date/time is for planning and preview only. It does not activate the trip.

### Place search flow

The place-selection interface must visually communicate two confirmation stages.

#### Stage 1: Search

- Search field
- Search suggestions
- Category examples:
    - Restaurants
    - Museums
    - Attractions
    - Hotels
    - Addresses
- Loading state
- No-result state
- Provider error state

#### Stage 2: Temporary selection

After selecting a result, show:

- Temporary place name
- Map pin
- Temporary coordinate
- “Use this location”
- “Choose another result”
- Clear temporary status

The temporary result is not yet saved.

#### Stage 3: Durable result

After the user chooses “Use this location,” show the final resolved result:

- Permanent address when available
- Durable coordinate
- Derived timezone
- Map pin
- “Confirm location”
- “Cancel”
- “Search again”

The final saved label may be an address or a coordinate label. Do not assume that the temporary business name becomes the saved activity name.

Important request-cost behavior to reflect in the design:

- Do not imply reverse geocoding happens on every keystroke.
- Do not imply it happens whenever the map moves.
- Do not imply activation performs another place lookup.
- One durable resolution occurs only after explicit “Use this location.”
- Cancellation must leave no saved place.

### Activity list

Each activity should be a reorderable card or row containing:

- Number/order indicator
- Durable place label
- Coordinate/address detail
- Timezone
- Schedule summary
- Travel mode
- Expand/edit control
- Remove control
- Drag/reorder affordance

Actions:

- Add activity
- Reorder activities
- Remove activity
- Edit activity

The application requires at least one activity before saving.

### Activity editor

The editor must expose these values:

- Schedule:
    - Unscheduled
    - Scheduled
- Start offset
- End offset
- Inbound travel mode:
    - Driving
    - Walking
- Activity class:
    - Flexible
    - Fixed
- Priority rank
- Utility score
- Duration:
    - Minimum
    - Preferred
    - Maximum
- Availability windows
- Reservation start
- Reservation grace period
- Deadline
- Mandatory
- Movable
- Skippable
- Shortening allowed

Default values for a new activity:

- Unscheduled
- Driving
- Flexible
- Priority rank 0
- Utility score 0
- Fixed 60-minute duration
- All-day relative availability
- No reservation
- No deadline
- Movable
- Skippable
- Not mandatory
- Shortening disabled

Make the defaults visible and editable.

### Planner route preview

Use a Mapbox-style map panel showing:

- Activity markers
- Numbered stops
- Ordered route
- Zoom controls
- Full route preview
- Empty-map state
- Provider/configuration error state

The map must preserve the user’s activity order. It must not visually imply that the map provider optimized or reordered the trip.

### Planner actions

Include:

- Save trip
- Save changes
- Go
- Back to Trips
- Delete trip for inactive trips

The “Go” action should be disabled until every activity is scheduled.

### Planner states

Design:

- Empty new trip
- Search in progress
- Temporary result selected
- Durable result awaiting confirmation
- Activity editor expanded
- Activity editor collapsed
- Invalid field
- Unscheduled activity
- Saving
- Save success
- Save failure
- Delete confirmation
- Delete failure
- Map token unavailable
- Provider route unavailable
- Inactive trip loaded
- Active trip loaded

## Saved trip editing

Opening an inactive trip should show the complete editable plan.

Include:

- Trip name
- Trip timezone
- Optional display schedule
- Full activity list
- Place/address information
- Activity controls
- Map preview
- Save changes
- Delete trip
- Go

A saved trip is reusable. Editing it must not activate it or create live runtime state.

## Activation flow

When the user presses “Go”:

1. Validate that all activities are scheduled.
2. Request the browser’s current location.
3. Show a location-permission state if necessary.
4. Show “Finding you…”
5. Show “Starting…”
6. Transition to the active trip after activation succeeds.

Design:

- Location permission prompt explanation
- Location unavailable error
- Activation in progress
- Activation failure
- Active transition
- Retry or return-to-planner path

Do not make activation appear instantaneous if the backend is still preparing the live trip.

## Live Trip page

The live page should prioritize the map and the next action.

### Main layout

Desktop:

```
┌────────────────────────────────────────────┐
│ Header / active trip status                │
├──────────────────────┬─────────────────────┤
│ Remaining itinerary  │                     │
│ Next activity        │       Map            │
│ Status               │                     │
│ Activity controls    │                     │
│ Proposal panel       │                     │
└──────────────────────┴─────────────────────┘
```

Mobile:

- Map near the top
- Current location and connection status
- Next activity card
- Activity controls
- Remaining itinerary
- Proposal panel
- Stop trip action

### Live map

Show:

- Current GPS location as a blue-dot style marker
- Accuracy indicator when available
- Full remaining accepted route
- Active leg emphasis
- Remaining stops
- Numbered activity markers
- Route destination
- Map zoom/pan controls
- Route loading state
- Route failure state
- Degraded route state

The visible route represents the currently accepted plan.

### Live trip summary

Show:

- Trip name
- Connection status
- Current location status
- Next activity
- Planned arrival time
- Remaining duration
- Remaining distance
- Reservation status
- Deadline status
- Operating-window warnings
- Remaining activity sequence

Suggested labels:

- “Live trip connected”
- “Preparing live trip connection…”
- “Live location · ±25 m”
- “Live location is stale”
- “Waiting for a live location…”
- “Location permission is required”
- “Live rerouting is temporarily unavailable; the previous route remains visible.”

### Activity status controls

For the current or relevant activity, include:

- Start activity
- Mark completed
- Skip activity
- Planned/current/completed/skipped status
- Pending state
- Acknowledged state
- Rejected/stale/error state

The UI should not silently replace canonical state before backend confirmation.

### Stop trip

Include:

- “Stop trip”
- Confirmation dialog or clear confirmation state
- Stopping state
- Failure state
- Return to saved inactive trip after success

Stopping a trip preserves the reusable saved itinerary but resets execution-only state.

## Planner notifications

Display backend warnings without exposing implementation details.

Examples:

- Low schedule flexibility
- Critical lateness
- Suggested plan change
- Infeasible schedule
- Reservation at risk
- Deadline at risk
- Activity no longer feasible

Use:

- Inline status banners
- Warning cards
- Appropriate severity colors
- Accessible alert/status semantics

Do not use technical labels such as “C++ planner,” “runtime epoch,” “mutation sequence,” or “outbox.”

## Replan proposal UI

A replan is a suggestion, not an automatic change.

Display:

- Eyebrow: “Suggested change”
- Heading: “Review the proposed plan”
- Why the suggestion was made
- Activities that remain in place
- Activities that would move, be skipped, or change
- Proposed route shown separately from the current route
- Current route remains visually canonical
- “Accept suggestion”
- “Keep current plan”

Use clear visual distinction:

- Current plan: solid, authoritative route
- Proposed plan: dashed, alternate, or muted route
- Pending proposal: highlighted card
- Accepted proposal: confirmation state
- Rejected proposal: dismissed/retained current-plan state
- Stale proposal: explain that the plan has changed and the suggestion is no longer current

Suggested user-facing language:

> Your current plan remains unchanged until you accept this suggestion.
> 

> This suggestion keeps 3 activities in place and changes the remaining route.
> 

Do not imply the planner has already changed the itinerary.

## Route deviation states

When the browser detects a confirmed deviation:

- Show a subtle “Updating route…” state
- Keep the previous route visible during the request
- Show the replacement route only after success
- Show a degraded warning after failure
- Never change activity membership or order because of a map reroute
- The reroute must continue to the same next activity

Avoid noisy UI on every GPS update.

## Connection and GPS states

Design:

- Connecting
- Connected
- Reconnecting
- Disconnected
- GPS permission denied
- GPS unavailable
- GPS stale
- Browser tab hidden
- Offline
- Telemetry acknowledgement pending
- Telemetry timeout
- Recovery complete

Do not expose raw WebSocket, gRPC, PostgreSQL, or provider error messages.

## Required reusable components

Create a Figma component library with variants for:

- Logo/brand mark
- Sidebar
- Mobile header
- Primary button
- Secondary button
- Destructive button
- Text input
- Search input
- Select/dropdown
- Time input
- Date input
- Checkbox
- Toggle
- Status pill
- Activity card
- Activity editor
- Trip row
- Empty state
- Map placeholder
- Map marker
- Current-location marker
- Route legend
- Alert/banner
- Toast
- Modal/dialog
- Confirmation dialog
- Loading spinner
- Skeleton loader
- Error state
- Proposal card
- Route comparison
- Connection status
- GPS status
- Schedule timeline
- Reservation/deadline warning

Every component should have variants for:

- Default
- Hover
- Focus
- Disabled
- Loading
- Error
- Success
- Selected
- Read-only
- Mobile

## Required design frames

At minimum, create these Figma frames:

### Public

- Marketing landing page desktop
- Marketing landing page mobile
- Sign-in desktop
- Sign-in mobile
- Authentication loading
- Authentication error

### Trips

- Trips with no trips
- Trips with saved trips
- Trips with active and saved trips
- Trips loading
- Trips error

### Planner

- Empty new planner
- Search suggestions
- Temporary place selected
- Durable place confirmation
- New planner with activities
- Expanded activity editor
- Saved inactive trip
- Unsaved changes
- Save error
- Delete confirmation
- Map unavailable

### Live Trip

- Active trip connected
- Active trip connecting
- Active trip reconnecting
- GPS permission required
- GPS stale
- Active trip with warnings
- Active trip with activity controls
- Planner proposal pending
- Proposed route comparison
- Proposal accepted
- Proposal rejected
- Proposal stale
- Route degraded
- Stop-trip confirmation
- Deactivation in progress
- Deactivation failure

### Responsive

Create desktop and mobile versions for the primary Trips, Planner, and Live Trip screens.

Recommended desktop frame: 1440 × 1024.

Recommended mobile frame: 390 × 844.

Also verify layouts at approximately:

- 1280px
- 1024px
- 860px
- 768px
- 390px
- 320px

## Interaction prototype requirements

Prototype these flows:

1. Landing page → Google sign-in
2. Sign-in → Trips
3. Trips → New trip
4. Search → temporary place selection
5. Temporary place → durable confirmation
6. Add activity → edit activity
7. Reorder activities
8. Save trip
9. Open saved trip
10. Go → location permission → active trip
11. Active trip → mark activity started/completed/skipped
12. GPS stale/reconnect state
13. Planner notification
14. Proposal received
15. Compare proposed route
16. Accept suggestion
17. Keep current plan
18. Stop trip → saved inactive trip
19. Sign out

## Accessibility requirements

Design for these if possible, but not high priority:

- Keyboard navigation
- Visible focus states
- Screen-reader labels
- 4.5:1 minimum text contrast where applicable
- Large touch targets
- Status messages announced appropriately
- Do not rely on color alone
- Accessible map alternatives through the itinerary list
- Reduced-motion behavior
- Clear error text next to invalid controls
- Descriptive button labels

## Language and terminology

Use these user-facing terms:

- Trip
- Saved trip
- Active trip
- Activity
- Stop
- Current plan
- Suggested change
- Keep current plan
- Accept suggestion
- Live location
- Route
- Planned arrival
- Reservation
- Deadline
- Availability
- Save changes
- Go
- Stop trip

Avoid exposing:

- Canonical plan
- Runtime epoch
- Planner state version
- Mutation sequence
- Outbox
- Lease
- PostgreSQL
- C++
- OSRM
- WebSocket
- Protobuf
- Durable command
- Runtime-first
- Mirror finalization

## Important product constraints

- The user’s current plan is authoritative.
- The planner only suggests changes.
- Suggestions require explicit acceptance.
- Saving an inactive trip does not activate live execution.
- A trip must contain at least one activity before saving.
- The user’s activity order must be preserved.
- Mapbox must not reorder or optimize activities.
- The accepted place coordinate is reused for future routing.
- Temporary search data must not appear in saved trip data.
- Permanent place resolution happens only after explicit user action.
- Do not design repeated automatic reverse-geocoding requests.
- Do not design automatic itinerary changes.
- Do not design background location tracking.
- Do not design native turn-by-turn navigation.
- Do not imply that a route failure changes the saved plan.
- The current route remains visible while a replacement route is loading or failing.

## Figma handoff deliverables

The design agent should provide:

- Complete sitemap
- User-flow diagram
- Desktop and mobile page designs
- Component library
- Color and typography tokens
- Spacing and radius tokens
- All interaction states
- Prototype links between key frames
- Responsive behavior notes
- Accessibility annotations
- Empty/loading/error/success state coverage
- Developer handoff notes
- Map layer and route visual specifications
- Copy deck for all visible UI text

The final design should feel like a calm, trustworthy planning tool that gives users flexibility without taking control away from them.
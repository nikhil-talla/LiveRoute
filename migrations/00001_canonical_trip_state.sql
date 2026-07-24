-- +goose Up
-- +goose StatementBegin
CREATE TABLE users (
  id uuid PRIMARY KEY,
  display_name text NOT NULL,
  default_time_zone_name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE development_auth_tokens (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(token_sha256) = 32),
  expires_at timestamptz NULL,
  revoked_at timestamptz NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE trips (
  id uuid PRIMARY KEY,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  default_time_zone_name text NOT NULL,
  trip_revision bigint NOT NULL DEFAULT 0 CHECK (trip_revision >= 0),
  next_mutation_sequence bigint NOT NULL DEFAULT 1 CHECK (next_mutation_sequence >= 1),
  finalized_mutation_sequence bigint NOT NULL DEFAULT 0 CHECK (finalized_mutation_sequence >= 0),
  current_plan_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE trip_activities (
  id uuid PRIMARY KEY,
  trip_id uuid NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  ordinal integer NOT NULL CHECK (ordinal >= 0),
  place_id text NOT NULL,
  display_name text NOT NULL,
  latitude double precision NOT NULL,
  longitude double precision NOT NULL,
  time_zone_name text NOT NULL,
  inbound_travel_mode text NOT NULL CHECK (inbound_travel_mode IN ('walking', 'driving')),
  activity_class text NOT NULL CHECK (activity_class IN ('fixed', 'flexible')),
  activity_state text NOT NULL CHECK (activity_state IN ('planned', 'started', 'completed', 'skipped')),
  activity_delay_seconds integer NOT NULL DEFAULT 0 CHECK (activity_delay_seconds >= 0),
  found_closed_at timestamptz NULL,
  priority_rank integer NOT NULL,
  utility_score integer NOT NULL,
  reservation_start timestamptz NULL,
  reservation_grace_seconds integer NOT NULL DEFAULT 0 CHECK (reservation_grace_seconds >= 0),
  mandatory_deadline timestamptz NULL,
  min_duration_seconds integer NOT NULL CHECK (min_duration_seconds >= 0),
  preferred_duration_seconds integer NOT NULL CHECK (preferred_duration_seconds >= 0),
  max_duration_seconds integer NOT NULL CHECK (max_duration_seconds >= 0),
  mandatory boolean NOT NULL,
  can_shorten boolean NOT NULL,
  can_move boolean NOT NULL,
  can_skip boolean NOT NULL,
  UNIQUE (trip_id, ordinal),
  UNIQUE (trip_id, id),
  CHECK (min_duration_seconds <= preferred_duration_seconds AND preferred_duration_seconds <= max_duration_seconds)
);

CREATE TABLE activity_open_windows (
  trip_id uuid NOT NULL,
  activity_id uuid NOT NULL,
  window_index integer NOT NULL CHECK (window_index >= 0),
  opens_at timestamptz NOT NULL,
  closes_at timestamptz NOT NULL,
  PRIMARY KEY (activity_id, window_index),
  FOREIGN KEY (trip_id, activity_id) REFERENCES trip_activities(trip_id, id) ON DELETE CASCADE,
  CHECK (opens_at < closes_at)
);

CREATE TABLE trip_travel_delays (
  trip_id uuid NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  from_activity_id uuid NOT NULL,
  to_activity_id uuid NOT NULL,
  additional_seconds integer NOT NULL CHECK (additional_seconds >= 0),
  observed_at timestamptz NOT NULL,
  PRIMARY KEY (trip_id, from_activity_id, to_activity_id),
  FOREIGN KEY (trip_id, from_activity_id) REFERENCES trip_activities(trip_id, id) ON DELETE CASCADE,
  FOREIGN KEY (trip_id, to_activity_id) REFERENCES trip_activities(trip_id, id) ON DELETE CASCADE
);

CREATE TABLE itinerary_plans (
  id uuid PRIMARY KEY,
  trip_id uuid NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  plan_revision bigint NOT NULL CHECK (plan_revision > 0),
  origin text NOT NULL CHECK (origin IN ('user_authored', 'accepted_engine_proposal')),
  authored_by_user_id uuid NOT NULL REFERENCES users(id),
  source_proposal_id uuid NULL,
  schema_version integer NOT NULL CHECK (schema_version = 1),
  payload bytea NOT NULL,
  payload_size_bytes integer NOT NULL CHECK (payload_size_bytes >= 0),
  checksum_sha256 bytea NOT NULL CHECK (octet_length(checksum_sha256) = 32),
  created_at timestamptz NOT NULL,
  UNIQUE (trip_id, id),
  UNIQUE (trip_id, plan_revision),
  CHECK ((origin = 'user_authored' AND source_proposal_id IS NULL) OR (origin = 'accepted_engine_proposal' AND source_proposal_id IS NOT NULL))
);

ALTER TABLE trips ADD CONSTRAINT trips_current_plan_fk
  FOREIGN KEY (id, current_plan_id) REFERENCES itinerary_plans(trip_id, id)
  DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX trips_owner_user_id_idx ON trips(owner_user_id);
CREATE INDEX itinerary_plans_trip_revision_idx ON itinerary_plans(trip_id, plan_revision DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE trips DROP CONSTRAINT trips_current_plan_fk;
DROP TABLE itinerary_plans;
DROP TABLE trip_travel_delays;
DROP TABLE activity_open_windows;
DROP TABLE trip_activities;
DROP TABLE trips;
DROP TABLE development_auth_tokens;
DROP TABLE users;
-- +goose StatementEnd

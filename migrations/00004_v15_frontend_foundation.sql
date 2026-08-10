-- +goose Up
-- +goose StatementBegin
CREATE TABLE external_identities (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider text NOT NULL CHECK (provider = 'google'),
  issuer text NOT NULL CHECK (issuer = 'https://accounts.google.com'),
  subject text NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),
  email text NULL CHECK (email IS NULL OR length(email) BETWEEN 3 AND 320),
  email_verified boolean NOT NULL DEFAULT false,
  display_name text NULL CHECK (display_name IS NULL OR length(display_name) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  last_authenticated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (provider, issuer, subject),
  CHECK (email IS NULL OR email_verified)
);

CREATE TABLE oidc_login_nonces (
  id uuid PRIMARY KEY,
  nonce_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(nonce_sha256) = 32),
  browser_binding_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(browser_binding_sha256) = 32),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz NULL,
  CHECK (expires_at > created_at),
  CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);

CREATE TABLE user_sessions (
  id uuid PRIMARY KEY,
  session_family_id uuid NOT NULL,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(token_sha256) = 32),
  csrf_key_id text NOT NULL CHECK (length(csrf_key_id) BETWEEN 1 AND 64),
  created_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  idle_expires_at timestamptz NOT NULL,
  absolute_expires_at timestamptz NOT NULL,
  rotate_after timestamptz NOT NULL,
  replaced_by_session_id uuid NULL,
  predecessor_valid_until timestamptz NULL,
  revoked_at timestamptz NULL,
  revocation_reason text NULL CHECK (revocation_reason IS NULL OR revocation_reason IN ('logout', 'rotated', 'administrator', 'security_event', 'user_deleted')),
  UNIQUE (session_family_id, id),
  UNIQUE (user_id, id),
  UNIQUE (session_family_id, user_id, id),
  CHECK (created_at <= last_seen_at),
  CHECK (last_seen_at < idle_expires_at),
  CHECK (idle_expires_at <= absolute_expires_at),
  CHECK (created_at < rotate_after AND rotate_after <= absolute_expires_at),
  CHECK ((replaced_by_session_id IS NULL) = (predecessor_valid_until IS NULL)),
  CHECK (predecessor_valid_until IS NULL OR predecessor_valid_until <= absolute_expires_at),
  CHECK ((revoked_at IS NULL) = (revocation_reason IS NULL)),
  FOREIGN KEY (session_family_id, user_id, replaced_by_session_id)
    REFERENCES user_sessions(session_family_id, user_id, id)
    DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE websocket_auth_tickets (
  id uuid PRIMARY KEY,
  session_id uuid NOT NULL,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(token_sha256) = 32),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz NULL,
  CHECK (expires_at > created_at),
  CHECK (consumed_at IS NULL OR consumed_at >= created_at),
  FOREIGN KEY (user_id, session_id) REFERENCES user_sessions(user_id, id) ON DELETE CASCADE
);

CREATE TABLE http_idempotency_records (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  -- Preserve the response for an idempotent DELETE replay after the trip row is
  -- gone. Resource identity remains in resource_id and the stored response.
  trip_id uuid NULL REFERENCES trips(id) ON DELETE SET NULL,
  idempotency_key uuid NOT NULL,
  http_method text NOT NULL CHECK (http_method IN ('POST', 'PATCH', 'DELETE')),
  normalized_path text NOT NULL CHECK (length(normalized_path) BETWEEN 1 AND 500 AND normalized_path LIKE '/api/v1/%'),
  operation_kind text NOT NULL CHECK (operation_kind IN (
    'create_trip', 'update_trip', 'delete_trip',
    'add_activity', 'replace_activity', 'delete_activity',
    'resolve_place', 'create_place', 'activate_trip', 'deactivate_trip'
  )),
  request_digest_algorithm text NOT NULL CHECK (request_digest_algorithm IN ('rfc8785-sha256-v1', 'rfc8785-hmac-sha256-v1')),
  request_digest_key_id text NULL CHECK (request_digest_key_id IS NULL OR length(request_digest_key_id) BETWEEN 1 AND 64),
  request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
  state text NOT NULL CHECK (state IN ('in_progress', 'completed')),
  response_status integer NULL CHECK (response_status IS NULL OR response_status BETWEEN 200 AND 599),
  response_content_type text NULL CHECK (response_content_type IS NULL OR response_content_type IN ('application/json', 'application/problem+json')),
  response_body jsonb NULL,
  response_etag text NULL,
  resource_id uuid NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  completed_at timestamptz NULL,
  retain_until timestamptz NOT NULL,
  UNIQUE (user_id, http_method, normalized_path, idempotency_key),
  UNIQUE (user_id, id),
  UNIQUE (user_id, trip_id, id),
  CHECK (retain_until > created_at),
  CHECK ((state = 'completed') = (response_status IS NOT NULL AND completed_at IS NOT NULL)),
  CHECK (completed_at IS NULL OR completed_at >= created_at),
  CHECK (
    (operation_kind = 'resolve_place') =
      (request_digest_algorithm = 'rfc8785-hmac-sha256-v1'
        AND request_digest_key_id IS NOT NULL)
  ),
  CHECK (request_digest_algorithm = 'rfc8785-hmac-sha256-v1' OR request_digest_key_id IS NULL)
);

CREATE TABLE place_resolution_attempts (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  idempotency_record_id uuid NOT NULL UNIQUE,
  provider text NOT NULL CHECK (provider = 'mapbox_geocoding_v6_permanent'),
  state text NOT NULL CHECK (state IN ('pending', 'resolved', 'failed', 'consumed')),
  provider_request_started_at timestamptz NULL,
  resolution_token_sha256 bytea NULL UNIQUE CHECK (resolution_token_sha256 IS NULL OR octet_length(resolution_token_sha256) = 32),
  latitude double precision NULL CHECK (latitude IS NULL OR latitude BETWEEN -90 AND 90),
  longitude double precision NULL CHECK (longitude IS NULL OR longitude BETWEEN -180 AND 180),
  formatted_address text NULL CHECK (formatted_address IS NULL OR length(formatted_address) BETWEEN 1 AND 500),
  display_name text NULL CHECK (display_name IS NULL OR length(display_name) BETWEEN 1 AND 500),
  time_zone_name text NULL CHECK (time_zone_name IS NULL OR length(time_zone_name) BETWEEN 1 AND 64),
  failure_code text NULL CHECK (failure_code IS NULL OR failure_code IN ('PROVIDER_UNAVAILABLE', 'RESOURCE_EXHAUSTED', 'PLACE_NOT_RESOLVED', 'INVALID_ARGUMENT', 'INTERNAL')),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz NULL,
  UNIQUE (user_id, id),
  FOREIGN KEY (user_id, idempotency_record_id)
    REFERENCES http_idempotency_records(user_id, id) ON DELETE CASCADE,
  CHECK (expires_at > created_at),
  CHECK (
    (state = 'pending'
      AND resolution_token_sha256 IS NULL
      AND latitude IS NULL AND longitude IS NULL
      AND display_name IS NULL AND time_zone_name IS NULL
      AND failure_code IS NULL AND consumed_at IS NULL)
    OR (state = 'resolved'
      AND resolution_token_sha256 IS NOT NULL
      AND latitude IS NOT NULL AND longitude IS NOT NULL
      AND display_name IS NOT NULL AND time_zone_name IS NOT NULL
      AND failure_code IS NULL AND consumed_at IS NULL)
    OR (state = 'failed'
      AND resolution_token_sha256 IS NULL
      AND latitude IS NULL AND longitude IS NULL
      AND display_name IS NULL AND time_zone_name IS NULL
      AND failure_code IS NOT NULL AND consumed_at IS NULL)
    OR (state = 'consumed'
      AND resolution_token_sha256 IS NOT NULL
      AND latitude IS NOT NULL AND longitude IS NOT NULL
      AND display_name IS NOT NULL AND time_zone_name IS NOT NULL
      AND failure_code IS NULL AND consumed_at IS NOT NULL)
  )
);

CREATE TABLE places (
  id uuid PRIMARY KEY,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source_resolution_id uuid NOT NULL UNIQUE,
  latitude double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
  longitude double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
  formatted_address text NULL CHECK (formatted_address IS NULL OR length(formatted_address) BETWEEN 1 AND 500),
  display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 500),
  time_zone_name text NOT NULL CHECK (length(time_zone_name) BETWEEN 1 AND 64),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (owner_user_id, id),
  FOREIGN KEY (owner_user_id, source_resolution_id)
    REFERENCES place_resolution_attempts(user_id, id)
    DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE saved_trip_plans (
  id uuid PRIMARY KEY,
  trip_id uuid NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  saved_plan_revision bigint NOT NULL CHECK (saved_plan_revision > 0),
  authored_by_user_id uuid NOT NULL REFERENCES users(id),
  display_local_date date NULL,
  display_local_time time without time zone NULL,
  display_time_zone_name text NULL CHECK (display_time_zone_name IS NULL OR length(display_time_zone_name) BETWEEN 1 AND 64),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (trip_id, id),
  UNIQUE (trip_id, saved_plan_revision),
  UNIQUE (owner_user_id, id),
  CHECK (authored_by_user_id = owner_user_id),
  CHECK (
    (display_local_date IS NULL AND display_local_time IS NULL AND display_time_zone_name IS NULL)
    OR (display_local_date IS NOT NULL AND display_local_time IS NOT NULL AND display_time_zone_name IS NOT NULL)
  )
);

CREATE TABLE saved_trip_activities (
  saved_plan_id uuid NOT NULL,
  trip_id uuid NOT NULL,
  owner_user_id uuid NOT NULL,
  activity_id uuid NOT NULL,
  place_id uuid NOT NULL,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 63),
  schedule_state text NOT NULL CHECK (schedule_state IN ('scheduled', 'unscheduled')),
  start_offset_ms bigint NULL CHECK (start_offset_ms IS NULL OR start_offset_ms BETWEEN 0 AND 86399999),
  end_offset_ms bigint NULL CHECK (end_offset_ms IS NULL OR end_offset_ms BETWEEN 1 AND 86400000),
  inbound_travel_mode text NOT NULL CHECK (inbound_travel_mode IN ('walking', 'driving')),
  activity_class text NOT NULL CHECK (activity_class IN ('fixed', 'flexible')),
  priority_rank integer NOT NULL,
  utility_score integer NOT NULL,
  reservation_start_offset_ms bigint NULL CHECK (reservation_start_offset_ms IS NULL OR reservation_start_offset_ms BETWEEN 0 AND 86400000),
  reservation_grace_seconds bigint NOT NULL CHECK (reservation_grace_seconds BETWEEN 0 AND 4294967295),
  mandatory_deadline_offset_ms bigint NULL CHECK (mandatory_deadline_offset_ms IS NULL OR mandatory_deadline_offset_ms BETWEEN 0 AND 86400000),
  min_duration_seconds integer NOT NULL CHECK (min_duration_seconds BETWEEN 0 AND 86400),
  preferred_duration_seconds integer NOT NULL CHECK (preferred_duration_seconds BETWEEN 0 AND 86400),
  max_duration_seconds integer NOT NULL CHECK (max_duration_seconds BETWEEN 0 AND 86400),
  mandatory boolean NOT NULL,
  can_shorten boolean NOT NULL CHECK (NOT can_shorten),
  can_move boolean NOT NULL,
  can_skip boolean NOT NULL,
  PRIMARY KEY (saved_plan_id, activity_id),
  UNIQUE (saved_plan_id, ordinal),
  FOREIGN KEY (trip_id, saved_plan_id) REFERENCES saved_trip_plans(trip_id, id) ON DELETE CASCADE,
  FOREIGN KEY (owner_user_id, saved_plan_id) REFERENCES saved_trip_plans(owner_user_id, id) ON DELETE CASCADE,
  FOREIGN KEY (owner_user_id, place_id) REFERENCES places(owner_user_id, id),
  CHECK (min_duration_seconds <= preferred_duration_seconds AND preferred_duration_seconds <= max_duration_seconds),
  CHECK (
    (schedule_state = 'scheduled' AND start_offset_ms IS NOT NULL AND end_offset_ms IS NOT NULL AND start_offset_ms < end_offset_ms)
    OR (schedule_state = 'unscheduled' AND start_offset_ms IS NULL AND end_offset_ms IS NULL)
  )
);

CREATE TABLE saved_activity_open_windows (
  saved_plan_id uuid NOT NULL,
  activity_id uuid NOT NULL,
  window_index integer NOT NULL CHECK (window_index BETWEEN 0 AND 31),
  opens_offset_ms bigint NOT NULL CHECK (opens_offset_ms BETWEEN 0 AND 86399999),
  closes_offset_ms bigint NOT NULL CHECK (closes_offset_ms BETWEEN 1 AND 86400000),
  PRIMARY KEY (saved_plan_id, activity_id, window_index),
  FOREIGN KEY (saved_plan_id, activity_id)
    REFERENCES saved_trip_activities(saved_plan_id, activity_id)
    ON DELETE CASCADE,
  CHECK (opens_offset_ms < closes_offset_ms)
);

CREATE TABLE trip_execution_operations (
  id uuid PRIMARY KEY,
  trip_id uuid NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  idempotency_record_id uuid NOT NULL UNIQUE,
  kind text NOT NULL CHECK (kind IN ('activate', 'deactivate')),
  state text NOT NULL CHECK (state IN ('pending', 'succeeded', 'failed')),
  last_step text NOT NULL CHECK (last_step IN ('recorded', 'lease_acquired', 'planner_bootstrapped', 'runtime_fenced', 'lease_released', 'completed', 'failed')),
  source_trip_revision bigint NOT NULL CHECK (source_trip_revision >= 0),
  target_execution_plan_id uuid NULL,
  resulting_trip_revision bigint NULL CHECK (resulting_trip_revision IS NULL OR resulting_trip_revision >= 0),
  safe_error_code text NULL CHECK (safe_error_code IS NULL OR length(safe_error_code) BETWEEN 1 AND 64),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  completed_at timestamptz NULL,
  UNIQUE (trip_id, id),
  FOREIGN KEY (owner_user_id, trip_id, idempotency_record_id)
    REFERENCES http_idempotency_records(user_id, trip_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (trip_id, target_execution_plan_id) REFERENCES itinerary_plans(trip_id, id),
  CHECK ((kind = 'activate') = (target_execution_plan_id IS NOT NULL)),
  CHECK ((state = 'pending') = (completed_at IS NULL)),
  CHECK ((state = 'failed') = (safe_error_code IS NOT NULL)),
  CHECK (completed_at IS NULL OR completed_at >= created_at)
);

ALTER TABLE trips
  ALTER COLUMN current_plan_id DROP NOT NULL,
  ADD COLUMN trip_name text NULL,
  ADD COLUMN saved_plan_id uuid NULL,
  ADD COLUMN execution_state text NOT NULL DEFAULT 'inactive'
    CHECK (execution_state IN ('inactive', 'activating', 'active', 'deactivating')),
  ADD COLUMN active_execution_plan_id uuid NULL,
  ADD COLUMN activated_at timestamptz NULL,
  ADD COLUMN transition_operation_id uuid NULL;

UPDATE trips
SET trip_name = 'Trip ' || left(id::text, 8)
WHERE trip_name IS NULL;

ALTER TABLE trips
  ALTER COLUMN trip_name SET NOT NULL,
  ADD CONSTRAINT trips_name_check CHECK (octet_length(btrim(trip_name)) BETWEEN 1 AND 120),
  ADD CONSTRAINT trips_owner_id_unique UNIQUE (owner_user_id, id),
  ADD CONSTRAINT trips_saved_plan_fk
    FOREIGN KEY (id, saved_plan_id) REFERENCES saved_trip_plans(trip_id, id)
    DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT trips_active_execution_plan_fk
    FOREIGN KEY (id, active_execution_plan_id) REFERENCES itinerary_plans(trip_id, id)
    DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT trips_transition_operation_fk
    FOREIGN KEY (id, transition_operation_id) REFERENCES trip_execution_operations(trip_id, id)
    DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT trips_v15_or_legacy_plan_check CHECK (
    saved_plan_id IS NOT NULL OR current_plan_id IS NOT NULL
  ),
  ADD CONSTRAINT trips_execution_lifecycle_check CHECK (
    (execution_state = 'inactive'
      AND active_execution_plan_id IS NULL
      AND activated_at IS NULL
      AND transition_operation_id IS NULL)
    OR (execution_state = 'activating'
      AND current_plan_id IS NOT NULL
      AND active_execution_plan_id = current_plan_id
      AND activated_at IS NOT NULL
      AND transition_operation_id IS NOT NULL)
    OR (execution_state = 'active'
      AND current_plan_id IS NOT NULL
      AND active_execution_plan_id = current_plan_id
      AND activated_at IS NOT NULL
      AND transition_operation_id IS NULL)
    OR (execution_state = 'deactivating'
      AND current_plan_id IS NOT NULL
      AND active_execution_plan_id = current_plan_id
      AND activated_at IS NOT NULL
      AND transition_operation_id IS NOT NULL)
  );

-- Existing liveroute.v1 create-trip messages have no trip-name field. Preserve
-- that completed wire contract until its adapter is retired; V1.5 HTTP creation
-- always supplies the user-selected name explicitly.
CREATE FUNCTION liveroute_fill_legacy_trip_name() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.trip_name IS NULL THEN
    NEW.trip_name := 'Trip ' || left(NEW.id::text, 8);
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER trips_fill_legacy_trip_name
  BEFORE INSERT ON trips
  FOR EACH ROW EXECUTE FUNCTION liveroute_fill_legacy_trip_name();

ALTER TABLE saved_trip_plans
  ADD CONSTRAINT saved_trip_plans_owner_trip_fk
  FOREIGN KEY (owner_user_id, trip_id) REFERENCES trips(owner_user_id, id)
  ON DELETE CASCADE;

ALTER TABLE trip_execution_operations
  ADD CONSTRAINT trip_execution_operations_owner_trip_fk
  FOREIGN KEY (owner_user_id, trip_id) REFERENCES trips(owner_user_id, id)
  ON DELETE CASCADE;

CREATE UNIQUE INDEX trips_one_executing_per_user_idx
  ON trips(owner_user_id)
  WHERE execution_state IN ('activating', 'active', 'deactivating');

CREATE UNIQUE INDEX trip_execution_operations_one_pending_idx
  ON trip_execution_operations(trip_id)
  WHERE state = 'pending';

CREATE INDEX external_identities_user_idx ON external_identities(user_id);
CREATE INDEX oidc_login_nonces_expiry_idx ON oidc_login_nonces(expires_at) WHERE consumed_at IS NULL;
CREATE INDEX user_sessions_user_live_idx ON user_sessions(user_id, idle_expires_at) WHERE revoked_at IS NULL;
CREATE INDEX user_sessions_family_idx ON user_sessions(session_family_id);
CREATE INDEX websocket_auth_tickets_expiry_idx ON websocket_auth_tickets(expires_at) WHERE consumed_at IS NULL;
CREATE INDEX http_idempotency_retention_idx ON http_idempotency_records(retain_until) WHERE state = 'completed';
CREATE INDEX place_resolution_attempts_expiry_idx ON place_resolution_attempts(expires_at) WHERE state IN ('pending', 'resolved', 'failed');
CREATE INDEX places_owner_idx ON places(owner_user_id, created_at DESC);
CREATE INDEX saved_trip_plans_trip_revision_idx ON saved_trip_plans(trip_id, saved_plan_revision DESC);
CREATE INDEX trip_execution_operations_recovery_idx ON trip_execution_operations(updated_at, trip_id) WHERE state = 'pending';

CREATE FUNCTION liveroute_reject_immutable_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME USING ERRCODE = 'check_violation';
END;
$$;

CREATE TRIGGER places_reject_update
  BEFORE UPDATE ON places
  FOR EACH ROW EXECUTE FUNCTION liveroute_reject_immutable_update();

CREATE TRIGGER saved_trip_plans_reject_update
  BEFORE UPDATE ON saved_trip_plans
  FOR EACH ROW EXECUTE FUNCTION liveroute_reject_immutable_update();

CREATE TRIGGER saved_trip_activities_reject_update
  BEFORE UPDATE ON saved_trip_activities
  FOR EACH ROW EXECUTE FUNCTION liveroute_reject_immutable_update();

CREATE TRIGGER saved_activity_open_windows_reject_update
  BEFORE UPDATE ON saved_activity_open_windows
  FOR EACH ROW EXECUTE FUNCTION liveroute_reject_immutable_update();

CREATE FUNCTION liveroute_assert_saved_plan_nonempty() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  checked_plan_id uuid;
  activity_count bigint;
  minimum_ordinal integer;
  maximum_ordinal integer;
BEGIN
  IF TG_TABLE_NAME = 'saved_trip_plans' THEN
    checked_plan_id := NEW.id;
  ELSE
    checked_plan_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.saved_plan_id ELSE NEW.saved_plan_id END;
  END IF;

  IF NOT EXISTS (SELECT 1 FROM saved_trip_plans WHERE id = checked_plan_id) THEN
    RETURN NULL;
  END IF;

  SELECT count(*), min(ordinal), max(ordinal)
  INTO activity_count, minimum_ordinal, maximum_ordinal
  FROM saved_trip_activities
  WHERE saved_plan_id = checked_plan_id;

  IF activity_count = 0 THEN
    RAISE EXCEPTION 'saved trip plan % must contain at least one activity', checked_plan_id
      USING ERRCODE = 'check_violation';
  END IF;
  IF minimum_ordinal <> 0 OR maximum_ordinal <> activity_count - 1 THEN
    RAISE EXCEPTION 'saved trip plan % activity ordinals must be contiguous from zero', checked_plan_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER saved_trip_plans_nonempty_on_insert
  AFTER INSERT ON saved_trip_plans
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION liveroute_assert_saved_plan_nonempty();

CREATE CONSTRAINT TRIGGER saved_trip_plans_nonempty_on_activity_delete
  AFTER DELETE ON saved_trip_activities
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION liveroute_assert_saved_plan_nonempty();

CREATE CONSTRAINT TRIGGER saved_trip_plans_contiguous_on_activity_insert
  AFTER INSERT ON saved_trip_activities
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION liveroute_assert_saved_plan_nonempty();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM trips WHERE current_plan_id IS NULL) THEN
    RAISE EXCEPTION 'cannot roll back V1.5 foundation while a trip has no V1 current plan';
  END IF;
END;
$$;

DROP TRIGGER saved_trip_plans_contiguous_on_activity_insert ON saved_trip_activities;
DROP TRIGGER saved_trip_plans_nonempty_on_activity_delete ON saved_trip_activities;
DROP TRIGGER saved_trip_plans_nonempty_on_insert ON saved_trip_plans;
DROP FUNCTION liveroute_assert_saved_plan_nonempty();
DROP TRIGGER saved_activity_open_windows_reject_update ON saved_activity_open_windows;
DROP TRIGGER saved_trip_activities_reject_update ON saved_trip_activities;
DROP TRIGGER saved_trip_plans_reject_update ON saved_trip_plans;
DROP TRIGGER places_reject_update ON places;
DROP FUNCTION liveroute_reject_immutable_update();
DROP TRIGGER trips_fill_legacy_trip_name ON trips;
DROP FUNCTION liveroute_fill_legacy_trip_name();

DROP INDEX trip_execution_operations_recovery_idx;
DROP INDEX saved_trip_plans_trip_revision_idx;
DROP INDEX places_owner_idx;
DROP INDEX place_resolution_attempts_expiry_idx;
DROP INDEX http_idempotency_retention_idx;
DROP INDEX websocket_auth_tickets_expiry_idx;
DROP INDEX user_sessions_family_idx;
DROP INDEX user_sessions_user_live_idx;
DROP INDEX oidc_login_nonces_expiry_idx;
DROP INDEX external_identities_user_idx;
DROP INDEX trip_execution_operations_one_pending_idx;
DROP INDEX trips_one_executing_per_user_idx;

ALTER TABLE trip_execution_operations
  DROP CONSTRAINT trip_execution_operations_owner_trip_fk;
ALTER TABLE saved_trip_plans
  DROP CONSTRAINT saved_trip_plans_owner_trip_fk;

ALTER TABLE trips
  DROP CONSTRAINT trips_execution_lifecycle_check,
  DROP CONSTRAINT trips_v15_or_legacy_plan_check,
  DROP CONSTRAINT trips_transition_operation_fk,
  DROP CONSTRAINT trips_active_execution_plan_fk,
  DROP CONSTRAINT trips_saved_plan_fk,
  DROP CONSTRAINT trips_owner_id_unique,
  DROP CONSTRAINT trips_name_check,
  DROP COLUMN transition_operation_id,
  DROP COLUMN activated_at,
  DROP COLUMN active_execution_plan_id,
  DROP COLUMN execution_state,
  DROP COLUMN saved_plan_id,
  DROP COLUMN trip_name;

ALTER TABLE trips ALTER COLUMN current_plan_id SET NOT NULL;

DROP TABLE trip_execution_operations;
DROP TABLE saved_activity_open_windows;
DROP TABLE saved_trip_activities;
DROP TABLE saved_trip_plans;
DROP TABLE places;
DROP TABLE place_resolution_attempts;
DROP TABLE http_idempotency_records;
DROP TABLE websocket_auth_tickets;
DROP TABLE user_sessions;
DROP TABLE oidc_login_nonces;
DROP TABLE external_identities;
-- +goose StatementEnd

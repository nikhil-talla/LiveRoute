-- +goose Up
-- +goose StatementBegin
CREATE TABLE plan_proposals (
  id uuid PRIMARY KEY,
  trip_id uuid NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  base_current_plan_id uuid NOT NULL,
  source_runtime_epoch bigint NOT NULL CHECK (source_runtime_epoch > 0),
  source_planner_state_version bigint NOT NULL CHECK (source_planner_state_version >= 0),
  source_trip_revision bigint NOT NULL CHECK (source_trip_revision >= 1),
  source_accepted_mutation_sequence bigint NOT NULL CHECK (source_accepted_mutation_sequence >= 1),
  schema_version integer NOT NULL CHECK (schema_version = 1),
  payload bytea NOT NULL,
  payload_size_bytes integer NOT NULL CHECK (payload_size_bytes >= 0),
  checksum_sha256 bytea NOT NULL CHECK (octet_length(checksum_sha256) = 32),
  state text NOT NULL CHECK (state IN ('pending', 'accepted', 'rejected', 'stale', 'superseded')),
  decision_message_id uuid NULL,
  resulting_current_plan_id uuid NULL,
  created_at timestamptz NOT NULL,
  decided_at timestamptz NULL,
  UNIQUE (trip_id, id),
  CHECK ((state = 'pending') = (decided_at IS NULL)),
  CHECK ((state = 'accepted') = (resulting_current_plan_id IS NOT NULL)),
  FOREIGN KEY (trip_id, base_current_plan_id) REFERENCES itinerary_plans(trip_id, id),
  FOREIGN KEY (trip_id, resulting_current_plan_id) REFERENCES itinerary_plans(trip_id, id)
);

ALTER TABLE itinerary_plans ADD CONSTRAINT itinerary_plans_source_proposal_fk
  FOREIGN KEY (trip_id, source_proposal_id) REFERENCES plan_proposals(trip_id, id)
  DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE command_intents (
  id uuid PRIMARY KEY,
  trip_id uuid NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  message_id uuid NOT NULL,
  event_id uuid NOT NULL,
  mutation_sequence bigint NOT NULL CHECK (mutation_sequence > 0),
  expected_trip_revision bigint NOT NULL CHECK (expected_trip_revision >= 0),
  command_kind text NOT NULL CHECK (command_kind IN ('create_trip', 'activity_status_changed', 'activity_delayed', 'trip_edited', 'reservation_changed', 'mandatory_deadline_changed', 'operating_hours_changed', 'place_found_closed', 'travel_delay', 'replace_current_plan', 'accept_proposal', 'reject_proposal')),
  application_order text NOT NULL CHECK (application_order IN ('canonical_first', 'runtime_first')),
  command_expires_at timestamptz NULL,
  digest_algorithm text NOT NULL CHECK (digest_algorithm = 'rfc8785-sha256-v1'),
  payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
  command_payload jsonb NOT NULL,
  state text NOT NULL CHECK (state IN ('pending', 'applied', 'rejected', 'expired')),
  outcome_status text NULL CHECK (outcome_status IS NULL OR outcome_status IN (
    'accepted', 'accepted_degraded', 'rejected_invalid', 'rejected_stale',
    'rejected_conflict', 'rejected_expired', 'rejected_unauthenticated',
    'rejected_unauthorized', 'rejected_rate_limited', 'failed_internal',
    'failed_unavailable'
  )),
  outcome_payload jsonb NULL,
  resulting_trip_revision bigint NULL,
  resulting_planner_state_version bigint NULL,
  planned_current_plan_id uuid NULL,
  planned_current_plan_payload bytea NULL,
  planned_current_plan_checksum_sha256 bytea NULL CHECK (planned_current_plan_checksum_sha256 IS NULL OR octet_length(planned_current_plan_checksum_sha256) = 32),
  runtime_sync_state text NOT NULL CHECK (runtime_sync_state IN ('not_required', 'pending', 'synced', 'paused_internal')),
  recorded_at timestamptz NOT NULL,
  finalized_at timestamptz NULL,
  UNIQUE (trip_id, message_id),
  UNIQUE (trip_id, event_id),
  UNIQUE (trip_id, mutation_sequence),
  CHECK (
    (command_kind = 'accept_proposal'
      AND planned_current_plan_id IS NOT NULL
      AND planned_current_plan_payload IS NOT NULL
      AND planned_current_plan_checksum_sha256 IS NOT NULL)
    OR (command_kind <> 'accept_proposal'
      AND planned_current_plan_id IS NULL
      AND planned_current_plan_payload IS NULL
      AND planned_current_plan_checksum_sha256 IS NULL)
  ),
  CHECK (
    (application_order = 'canonical_first') = (
      command_kind IN ('create_trip', 'trip_edited', 'replace_current_plan')
    )
  )
);

CREATE TABLE planner_outbox (
  id uuid PRIMARY KEY,
  command_intent_id uuid NOT NULL UNIQUE REFERENCES command_intents(id) ON DELETE CASCADE,
  trip_id uuid NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  mutation_sequence bigint NOT NULL,
  event_schema_version integer NOT NULL CHECK (event_schema_version = 1),
  event_payload jsonb NOT NULL,
  delivery_state text NOT NULL CHECK (delivery_state IN ('pending', 'paused_internal', 'accepted', 'terminal_rejected')),
  attempt_count bigint NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  last_attempt_at timestamptz NULL,
  last_status text NULL,
  claim_owner uuid NULL,
  claim_expires_at timestamptz NULL,
  finalization_confirmed_at timestamptz NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (trip_id, mutation_sequence)
);

CREATE TABLE planner_snapshots (
  id uuid PRIMARY KEY,
  trip_id uuid NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  snapshot_schema_version integer NOT NULL CHECK (snapshot_schema_version = 1),
  source_runtime_epoch bigint NOT NULL CHECK (source_runtime_epoch > 0),
  source_planner_state_version bigint NOT NULL CHECK (source_planner_state_version >= 0),
  trip_revision bigint NOT NULL CHECK (trip_revision >= 0),
  covered_finalized_mutation_sequence bigint NOT NULL CHECK (covered_finalized_mutation_sequence >= 0),
  payload_size_bytes integer NOT NULL CHECK (payload_size_bytes >= 0),
  checksum_sha256 bytea NOT NULL CHECK (octet_length(checksum_sha256) = 32),
  payload bytea NOT NULL,
  created_at timestamptz NOT NULL,
  invalidated_at timestamptz NULL,
  invalidation_reason text NULL,
  UNIQUE (trip_id, source_runtime_epoch, source_planner_state_version, covered_finalized_mutation_sequence)
);

CREATE TABLE trip_runtime_leases (
  trip_id uuid PRIMARY KEY REFERENCES trips(id) ON DELETE CASCADE,
  holder_id uuid NOT NULL,
  runtime_epoch bigint NOT NULL CHECK (runtime_epoch > 0),
  lease_expires_at timestamptz NOT NULL,
  renewed_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX plan_proposals_one_pending_idx ON plan_proposals(trip_id) WHERE state = 'pending';
CREATE INDEX plan_proposals_trip_created_idx ON plan_proposals(trip_id, created_at DESC);
CREATE INDEX planner_outbox_pending_idx ON planner_outbox(next_attempt_at, trip_id) WHERE delivery_state = 'pending';
CREATE INDEX command_intents_trip_message_idx ON command_intents(trip_id, message_id);
CREATE INDEX planner_snapshots_latest_valid_idx ON planner_snapshots(trip_id, covered_finalized_mutation_sequence DESC) WHERE invalidated_at IS NULL;
CREATE INDEX trip_runtime_leases_expiring_idx ON trip_runtime_leases(lease_expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE trip_runtime_leases;
DROP TABLE planner_snapshots;
DROP TABLE planner_outbox;
DROP TABLE command_intents;
ALTER TABLE itinerary_plans DROP CONSTRAINT itinerary_plans_source_proposal_fk;
DROP TABLE plan_proposals;
-- +goose StatementEnd

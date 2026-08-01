BEGIN;

INSERT INTO users (id, display_name, default_time_zone_name)
VALUES (
  '91919191-9191-9191-9191-919191919191',
  'Migration 3 backfill user',
  'UTC'
);

INSERT INTO trips (
  id, owner_user_id, default_time_zone_name, trip_revision, current_plan_id
) VALUES (
  '92929292-9292-9292-9292-929292929292',
  '91919191-9191-9191-9191-919191919191',
  'UTC', 1,
  '93939393-9393-9393-9393-939393939393'
);

INSERT INTO itinerary_plans (
  id, trip_id, plan_revision, origin, authored_by_user_id, schema_version,
  payload, payload_size_bytes, checksum_sha256, created_at
) VALUES (
  '93939393-9393-9393-9393-939393939393',
  '92929292-9292-9292-9292-929292929292',
  1, 'user_authored',
  '91919191-9191-9191-9191-919191919191',
  1, convert_to('{}', 'UTF8'), 2,
  decode(repeat('00', 32), 'hex'), clock_timestamp()
);

INSERT INTO command_intents (
  id, trip_id, message_id, event_id, mutation_sequence,
  expected_trip_revision, command_kind, application_order,
  digest_algorithm, payload_digest, command_payload, state, outcome_status,
  resulting_trip_revision, runtime_sync_state, recorded_at, finalized_at
) VALUES (
  '94949494-9494-9494-9494-949494949494',
  '92929292-9292-9292-9292-929292929292',
  '95959595-9595-9595-9595-959595959595',
  '96969696-9696-9696-9696-969696969696',
  1, 0, 'create_trip', 'canonical_first', 'rfc8785-sha256-v1',
  decode(repeat('00', 32), 'hex'), '{}'::jsonb, 'applied', 'OK', 1,
  'not_required', clock_timestamp(), clock_timestamp()
);

COMMIT;

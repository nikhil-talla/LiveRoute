BEGIN;

DO $$
DECLARE
  user_id uuid := '11111111-1111-1111-1111-111111111111';
  trip_id uuid := '22222222-2222-2222-2222-222222222222';
  plan_id uuid := '33333333-3333-3333-3333-333333333333';
  proposal_id uuid := '44444444-4444-4444-4444-444444444444';
BEGIN
  INSERT INTO users (id, display_name, default_time_zone_name)
  VALUES (user_id, 'Migration test user', 'UTC');

  -- The trip/current-plan pair is intentionally inserted in dependency order
  -- that requires the contract's deferred composite foreign key.
  INSERT INTO trips (
    id, owner_user_id, default_time_zone_name, trip_revision, current_plan_id
  ) VALUES (trip_id, user_id, 'UTC', 1, plan_id);

  INSERT INTO itinerary_plans (
    id, trip_id, plan_revision, origin, authored_by_user_id, schema_version,
    payload, payload_size_bytes, checksum_sha256, created_at
  ) VALUES (
    plan_id, trip_id, 1, 'user_authored', user_id, 1,
    convert_to('{}', 'UTF8'), 2, decode(repeat('00', 32), 'hex'), clock_timestamp()
  );

  INSERT INTO command_intents (
    id, trip_id, message_id, event_id, mutation_sequence, expected_trip_revision,
    command_kind, application_order, digest_algorithm, payload_digest,
    command_payload, state, runtime_sync_state, recorded_at
  ) VALUES (
    '55555555-5555-5555-5555-555555555555', trip_id,
    '66666666-6666-6666-6666-666666666666',
    '77777777-7777-7777-7777-777777777777', 1, 1,
    'create_trip', 'canonical_first', 'rfc8785-sha256-v1',
    decode(repeat('00', 32), 'hex'), '{}'::jsonb, 'pending', 'not_required',
    clock_timestamp()
  );

  BEGIN
    INSERT INTO command_intents (
      id, trip_id, message_id, event_id, mutation_sequence, expected_trip_revision,
      command_kind, application_order, digest_algorithm, payload_digest,
      command_payload, state, runtime_sync_state, recorded_at
    ) VALUES (
      '88888888-8888-8888-8888-888888888888', trip_id,
      '99999999-9999-9999-9999-999999999999',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 2, 1,
      'create_trip', 'runtime_first', 'rfc8785-sha256-v1',
      decode(repeat('00', 32), 'hex'), '{}'::jsonb, 'pending', 'not_required',
      clock_timestamp()
    );
    RAISE EXCEPTION 'expected canonical-first command constraint to reject row';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  BEGIN
    INSERT INTO command_intents (
      id, trip_id, message_id, event_id, mutation_sequence, expected_trip_revision,
      command_kind, application_order, digest_algorithm, payload_digest,
      command_payload, state, runtime_sync_state, recorded_at
    ) VALUES (
      'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', trip_id,
      'cccccccc-cccc-cccc-cccc-cccccccccccc',
      'dddddddd-dddd-dddd-dddd-dddddddddddd', 3, 1,
      'accept_proposal', 'runtime_first', 'rfc8785-sha256-v1',
      decode(repeat('00', 32), 'hex'), '{}'::jsonb, 'pending', 'pending',
      clock_timestamp()
    );
    RAISE EXCEPTION 'expected accept-proposal materialization constraint to reject row';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  BEGIN
    INSERT INTO command_intents (
      id, trip_id, message_id, event_id, mutation_sequence, expected_trip_revision,
      command_kind, application_order, digest_algorithm, payload_digest,
      command_payload, state, outcome_status, runtime_sync_state, recorded_at
    ) VALUES (
      'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee', trip_id,
      'ffffffff-ffff-ffff-ffff-ffffffffffff',
      '12121212-1212-1212-1212-121212121212', 4, 1,
      'activity_status_changed', 'runtime_first', 'rfc8785-sha256-v1',
      decode(repeat('00', 32), 'hex'), '{}'::jsonb, 'pending', 'not_a_status',
      'not_required', clock_timestamp()
    );
    RAISE EXCEPTION 'expected outcome-status constraint to reject row';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  INSERT INTO plan_proposals (
    id, trip_id, base_current_plan_id, source_runtime_epoch,
    source_planner_state_version, source_trip_revision,
    source_accepted_mutation_sequence, schema_version, payload,
    payload_size_bytes, checksum_sha256, state, created_at
  ) VALUES (
    proposal_id, trip_id, plan_id, 1, 0, 1, 1, 1,
    convert_to('{}', 'UTF8'), 2, decode(repeat('00', 32), 'hex'), 'pending',
    clock_timestamp()
  );

  BEGIN
    INSERT INTO plan_proposals (
      id, trip_id, base_current_plan_id, source_runtime_epoch,
      source_planner_state_version, source_trip_revision,
      source_accepted_mutation_sequence, schema_version, payload,
      payload_size_bytes, checksum_sha256, state, created_at, decided_at
    ) VALUES (
      '14141414-1414-1414-1414-141414141414', trip_id, plan_id, 1, 0, 1, 1, 1,
      convert_to('{}', 'UTF8'), 2, decode(repeat('00', 32), 'hex'), 'accepted',
      clock_timestamp(), clock_timestamp()
    );
    RAISE EXCEPTION 'expected accepted proposal without resulting plan to be rejected';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  BEGIN
    INSERT INTO plan_proposals (
      id, trip_id, base_current_plan_id, source_runtime_epoch,
      source_planner_state_version, source_trip_revision,
      source_accepted_mutation_sequence, schema_version, payload,
      payload_size_bytes, checksum_sha256, state, resulting_current_plan_id,
      created_at, decided_at
    ) VALUES (
      '15151515-1515-1515-1515-151515151515', trip_id, plan_id, 1, 0, 1, 1, 1,
      convert_to('{}', 'UTF8'), 2, decode(repeat('00', 32), 'hex'), 'rejected',
      plan_id, clock_timestamp(), clock_timestamp()
    );
    RAISE EXCEPTION 'expected non-accepted proposal with resulting plan to be rejected';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  BEGIN
    INSERT INTO plan_proposals (
      id, trip_id, base_current_plan_id, source_runtime_epoch,
      source_planner_state_version, source_trip_revision,
      source_accepted_mutation_sequence, schema_version, payload,
      payload_size_bytes, checksum_sha256, state, created_at
    ) VALUES (
      '13131313-1313-1313-1313-131313131313', trip_id, plan_id, 1, 0, 1, 1, 1,
      convert_to('{}', 'UTF8'), 2, decode(repeat('00', 32), 'hex'), 'pending',
      clock_timestamp()
    );
    RAISE EXCEPTION 'expected one-pending-proposal index to reject row';
  EXCEPTION WHEN unique_violation THEN
    NULL;
  END;
END;
$$;

SET CONSTRAINTS ALL IMMEDIATE;
ROLLBACK;

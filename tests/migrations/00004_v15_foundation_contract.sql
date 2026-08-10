BEGIN;

DO $$
DECLARE
  owner_id uuid := '01010101-0101-4101-8101-010101010101';
  trip_id uuid := '02020202-0202-4202-8202-020202020202';
  saved_plan_id uuid := '03030303-0303-4303-8303-030303030303';
  activity_id uuid := '04040404-0404-4404-8404-040404040404';
  resolution_id uuid := '05050505-0505-4505-8505-050505050505';
  place_id uuid := '06060606-0606-4606-8606-060606060606';
  resolve_idempotency_id uuid := '07070707-0707-4707-8707-070707070707';
  execution_plan_id uuid := '08080808-0808-4808-8808-080808080808';
  delete_idempotency_id uuid := '16161616-1616-4616-8616-161616161616';
  legacy_trip_id uuid := '18181818-1818-4818-8818-181818181818';
  legacy_plan_id uuid := '19191919-1919-4919-8919-191919191919';
BEGIN
  INSERT INTO users (id, display_name, default_time_zone_name)
  VALUES (owner_id, 'V1.5 migration user', 'America/New_York');

  INSERT INTO http_idempotency_records (
    id, user_id, idempotency_key, http_method, normalized_path,
    operation_kind, request_digest_algorithm, request_digest_key_id,
    request_digest, state,
    response_status, response_content_type, response_body,
    created_at, completed_at, retain_until
  ) VALUES (
    resolve_idempotency_id, owner_id,
    '07171717-0717-4717-8717-071717171717', 'POST',
    '/api/v1/places/resolve', 'resolve_place',
    'rfc8785-hmac-sha256-v1', 'resolution-hmac-2026q3',
    decode(repeat('01', 32), 'hex'),
    'completed', 200, 'application/json', '{}'::jsonb,
    clock_timestamp(), clock_timestamp(), clock_timestamp() + interval '30 days'
  );

  INSERT INTO place_resolution_attempts (
    id, user_id, idempotency_record_id, provider, state,
    provider_request_started_at, resolution_token_sha256,
    latitude, longitude, formatted_address, display_name, time_zone_name,
    created_at, expires_at, consumed_at
  ) VALUES (
    resolution_id, owner_id, resolve_idempotency_id,
    'mapbox_geocoding_v6_permanent', 'consumed', clock_timestamp(),
    decode(repeat('02', 32), 'hex'), 41.824, -71.4128,
    '1 Example Street, Providence, Rhode Island 02903, United States',
    '1 Example Street, Providence, Rhode Island 02903, United States',
    'America/New_York', clock_timestamp(), clock_timestamp() + interval '10 minutes',
    clock_timestamp()
  );

  INSERT INTO places (
    id, owner_user_id, source_resolution_id, latitude, longitude,
    formatted_address, display_name, time_zone_name
  ) VALUES (
    place_id, owner_id, resolution_id, 41.824, -71.4128,
    '1 Example Street, Providence, Rhode Island 02903, United States',
    '1 Example Street, Providence, Rhode Island 02903, United States',
    'America/New_York'
  );

  -- The saved-plan pointer and immutable plan are inserted in dependency order
  -- that exercises their deferred composite foreign key. An inactive V1.5 trip
  -- intentionally has no absolute V1 current plan before activation.
  INSERT INTO trips (
    id, owner_user_id, default_time_zone_name, trip_revision,
    current_plan_id, trip_name, saved_plan_id
  ) VALUES (
    trip_id, owner_id, 'America/New_York', 1, NULL,
    'Saturday in Providence', saved_plan_id
  );

  INSERT INTO saved_trip_plans (
    id, trip_id, owner_user_id, saved_plan_revision, authored_by_user_id
  ) VALUES (saved_plan_id, trip_id, owner_id, 1, owner_id);

  INSERT INTO saved_trip_activities (
    saved_plan_id, trip_id, owner_user_id, activity_id, place_id, ordinal,
    schedule_state, inbound_travel_mode, activity_class, priority_rank,
    utility_score, reservation_grace_seconds, min_duration_seconds,
    preferred_duration_seconds, max_duration_seconds, mandatory,
    can_shorten, can_move, can_skip
  ) VALUES (
    saved_plan_id, trip_id, owner_id, activity_id, place_id, 0,
    'unscheduled', 'walking', 'flexible', 10, 100, 0,
    3600, 3600, 3600, false, false, true, true
  );

  SET CONSTRAINTS ALL IMMEDIATE;

  IF NOT EXISTS (
    SELECT 1 FROM trips AS stored_trip
    WHERE stored_trip.id = trip_id AND stored_trip.execution_state = 'inactive'
      AND stored_trip.current_plan_id IS NULL
      AND stored_trip.saved_plan_id IS NOT NULL
  ) THEN
    RAISE EXCEPTION 'inactive V1.5 saved trip was not represented correctly';
  END IF;

  BEGIN
    UPDATE places SET latitude = 41.825 WHERE id = place_id;
    RAISE EXCEPTION 'expected immutable Place update to be rejected';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  BEGIN
    INSERT INTO saved_trip_activities (
      saved_plan_id, trip_id, owner_user_id, activity_id, place_id, ordinal,
      schedule_state, inbound_travel_mode, activity_class, priority_rank,
      utility_score, reservation_grace_seconds, min_duration_seconds,
      preferred_duration_seconds, max_duration_seconds, mandatory,
      can_shorten, can_move, can_skip
    ) VALUES (
      saved_plan_id, trip_id, owner_id,
      '09090909-0909-4909-8909-090909090909', place_id, 1,
      'unscheduled', 'walking', 'flexible', 20, 50, 0,
      1800, 3600, 3600, false, true, true, true
    );
    RAISE EXCEPTION 'expected can_shorten=true to be rejected';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  BEGIN
    INSERT INTO saved_trip_activities (
      saved_plan_id, trip_id, owner_user_id, activity_id, place_id, ordinal,
      schedule_state, start_offset_ms, inbound_travel_mode, activity_class,
      priority_rank, utility_score, reservation_grace_seconds,
      min_duration_seconds, preferred_duration_seconds, max_duration_seconds,
      mandatory, can_shorten, can_move, can_skip
    ) VALUES (
      saved_plan_id, trip_id, owner_id,
      '10101010-1010-4010-8010-101010101010', place_id, 1,
      'unscheduled', 0, 'walking', 'flexible', 20, 50, 0,
      3600, 3600, 3600, false, false, true, true
    );
    RAISE EXCEPTION 'expected unscheduled activity with offset to be rejected';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  SET CONSTRAINTS ALL DEFERRED;
  BEGIN
    INSERT INTO saved_trip_plans (
      id, trip_id, owner_user_id, saved_plan_revision, authored_by_user_id
    ) VALUES (
      '11111111-1111-4111-8111-111111111111', trip_id, owner_id, 2, owner_id
    );
    SET CONSTRAINTS ALL IMMEDIATE;
    RAISE EXCEPTION 'expected empty saved plan to be rejected';
  EXCEPTION WHEN check_violation THEN
    SET CONSTRAINTS ALL DEFERRED;
  END;

  BEGIN
    INSERT INTO saved_trip_plans (
      id, trip_id, owner_user_id, saved_plan_revision, authored_by_user_id
    ) VALUES (
      '21212121-2121-4121-8121-212121212121', trip_id, owner_id, 2, owner_id
    );
    INSERT INTO saved_trip_activities (
      saved_plan_id, trip_id, owner_user_id, activity_id, place_id, ordinal,
      schedule_state, inbound_travel_mode, activity_class, priority_rank,
      utility_score, reservation_grace_seconds, min_duration_seconds,
      preferred_duration_seconds, max_duration_seconds, mandatory,
      can_shorten, can_move, can_skip
    ) VALUES (
      '21212121-2121-4121-8121-212121212121', trip_id, owner_id,
      '22222222-2222-4222-8222-222222222222', place_id, 1,
      'unscheduled', 'walking', 'flexible', 10, 100, 0,
      3600, 3600, 3600, false, false, true, true
    );
    SET CONSTRAINTS ALL IMMEDIATE;
    RAISE EXCEPTION 'expected noncontiguous saved-plan ordinals to be rejected';
  EXCEPTION WHEN check_violation THEN
    SET CONSTRAINTS ALL DEFERRED;
  END;

  INSERT INTO itinerary_plans (
    id, trip_id, plan_revision, origin, authored_by_user_id, schema_version,
    payload, payload_size_bytes, checksum_sha256, created_at
  ) VALUES (
    execution_plan_id, trip_id, 1, 'user_authored', owner_id, 1,
    convert_to('{}', 'UTF8'), 2, decode(repeat('03', 32), 'hex'), clock_timestamp()
  );

  UPDATE trips
  SET current_plan_id = execution_plan_id,
      active_execution_plan_id = execution_plan_id,
      activated_at = clock_timestamp(),
      execution_state = 'active'
  WHERE id = trip_id;

  BEGIN
    INSERT INTO trips (
      id, owner_user_id, default_time_zone_name, trip_revision,
      current_plan_id, trip_name, execution_state,
      active_execution_plan_id, activated_at
    ) VALUES (
      '12121212-1212-4212-8212-121212121212', owner_id,
      'America/New_York', 1,
      '13131313-1313-4313-8313-131313131313', 'Second active trip',
      'active', '13131313-1313-4313-8313-131313131313', clock_timestamp()
    );
    RAISE EXCEPTION 'expected one-executing-trip-per-user index to reject row';
  EXCEPTION WHEN unique_violation THEN
    NULL;
  END;

  BEGIN
    INSERT INTO trips (
      id, owner_user_id, default_time_zone_name, trip_revision,
      current_plan_id, trip_name
    ) VALUES (
      '14141414-1414-4414-8414-141414141414', owner_id,
      'America/New_York', 1, NULL, 'No plan of either kind'
    );
    RAISE EXCEPTION 'expected trip without saved or current plan to be rejected';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  BEGIN
    INSERT INTO trips (
      id, owner_user_id, default_time_zone_name, trip_revision,
      current_plan_id, trip_name
    ) VALUES (
      '15151515-1515-4515-8515-151515151515', owner_id,
      'America/New_York', 1, execution_plan_id, repeat('é', 61)
    );
    RAISE EXCEPTION 'expected trip name over 120 UTF-8 bytes to be rejected';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;

  SET CONSTRAINTS ALL DEFERRED;
  INSERT INTO trips (
    id, owner_user_id, default_time_zone_name, trip_revision, current_plan_id
  ) VALUES (
    legacy_trip_id, owner_id, 'America/New_York', 1, legacy_plan_id
  );
  INSERT INTO itinerary_plans (
    id, trip_id, plan_revision, origin, authored_by_user_id, schema_version,
    payload, payload_size_bytes, checksum_sha256, created_at
  ) VALUES (
    legacy_plan_id, legacy_trip_id, 1, 'user_authored', owner_id, 1,
    convert_to('{}', 'UTF8'), 2, decode(repeat('05', 32), 'hex'), clock_timestamp()
  );
  SET CONSTRAINTS ALL IMMEDIATE;
  IF NOT EXISTS (
    SELECT 1 FROM trips
    WHERE id = legacy_trip_id AND trip_name = 'Trip 18181818'
  ) THEN
    RAISE EXCEPTION 'legacy V1 trip creation did not receive a compatible name';
  END IF;

  INSERT INTO http_idempotency_records (
    id, user_id, trip_id, idempotency_key, http_method, normalized_path,
    operation_kind, request_digest_algorithm, request_digest, state,
    response_status, created_at, completed_at, retain_until, resource_id
  ) VALUES (
    delete_idempotency_id, owner_id, trip_id,
    '17171717-1717-4717-8717-171717171717', 'DELETE',
    '/api/v1/trips/02020202-0202-4202-8202-020202020202', 'delete_trip',
    'rfc8785-sha256-v1', decode(repeat('04', 32), 'hex'), 'completed', 204,
    clock_timestamp(), clock_timestamp(), clock_timestamp() + interval '30 days',
    trip_id
  );

  DELETE FROM trips WHERE id = trip_id;
  IF NOT EXISTS (
    SELECT 1 FROM http_idempotency_records AS record
    WHERE record.id = delete_idempotency_id AND record.trip_id IS NULL
      AND record.resource_id = '02020202-0202-4202-8202-020202020202'
      AND record.response_status = 204
  ) THEN
    RAISE EXCEPTION 'trip deletion discarded its replayable idempotency result';
  END IF;
END;
$$;

ROLLBACK;

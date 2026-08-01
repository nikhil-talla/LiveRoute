-- +goose Up
-- +goose StatementBegin
ALTER TABLE command_intents
  ADD COLUMN resulting_current_plan_id uuid NULL;

-- Every canonical-first command creates exactly one immutable plan at its
-- resulting trip revision. Backfill by the unique durable plan revision; do
-- not inspect the intentionally opaque JSON outbox payload.
UPDATE command_intents AS intent
SET resulting_current_plan_id = plan.id
FROM itinerary_plans AS plan
WHERE intent.application_order = 'canonical_first'
  AND plan.trip_id = intent.trip_id
  AND plan.plan_revision = intent.resulting_trip_revision;

ALTER TABLE command_intents
  ADD CONSTRAINT command_intents_canonical_result_plan_check CHECK (
    (application_order = 'canonical_first') =
      (resulting_current_plan_id IS NOT NULL)
  ),
  ADD CONSTRAINT command_intents_resulting_current_plan_fk
    FOREIGN KEY (trip_id, resulting_current_plan_id)
    REFERENCES itinerary_plans(trip_id, id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE command_intents
  DROP CONSTRAINT command_intents_resulting_current_plan_fk,
  DROP CONSTRAINT command_intents_canonical_result_plan_check,
  DROP COLUMN resulting_current_plan_id;
-- +goose StatementEnd

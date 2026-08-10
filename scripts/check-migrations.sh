#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
image_digest=$(awk '
  /^  postgres:$/ { in_postgres = 1; next }
  in_postgres && /^    digest: / { print $2; exit }
  in_postgres && /^  [^ ]/ { exit }
' "$repo_root/config/tool-images.lock")

if [[ ! $image_digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "missing pinned postgres digest in config/tool-images.lock" >&2
  exit 1
fi

container_name="liveroute-migration-check-$$"
postgres_image="postgres@$image_digest"

cleanup() {
  docker rm --force "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

migration_section() {
  local migration=$1
  local section=$2
  awk -v section="$section" '
    $0 == "-- +goose " section { active = 1; next }
    /^-- \+goose (Up|Down)$/ { active = 0 }
    active && $0 !~ /^-- \+goose Statement/ { print }
  ' "$migration"
}

apply_migration_section() {
  migration_section "$1" "$2" |
    docker exec --interactive "$container_name" \
      psql --set ON_ERROR_STOP=1 --username=liveroute --dbname=liveroute
}

docker run --detach --name "$container_name" \
  --env POSTGRES_USER=liveroute \
  --env POSTGRES_PASSWORD=integration-test-only \
  --env POSTGRES_DB=liveroute \
  "$postgres_image" >/dev/null

for _ in $(seq 1 30); do
  if docker exec "$container_name" pg_isready --username=liveroute --dbname=liveroute >/dev/null; then
    break
  fi
  sleep 1
done
docker exec "$container_name" pg_isready --username=liveroute --dbname=liveroute >/dev/null

apply_migration_section "$repo_root/migrations/00001_canonical_trip_state.sql" Up
apply_migration_section "$repo_root/migrations/00002_delivery_recovery.sql" Up
docker exec --interactive "$container_name" \
  psql --set ON_ERROR_STOP=1 --username=liveroute --dbname=liveroute \
  < "$repo_root/tests/migrations/00003_backfill_fixture.sql"
apply_migration_section "$repo_root/migrations/00003_canonical_intent_plan_identity.sql" Up
backfilled_plan_id=$(docker exec "$container_name" \
  psql --tuples-only --no-align --username=liveroute --dbname=liveroute \
  --command "SELECT resulting_current_plan_id FROM command_intents WHERE id = '94949494-9494-9494-9494-949494949494';")
if [[ $backfilled_plan_id != 93939393-9393-9393-9393-939393939393 ]]; then
  echo "migration 3 failed to backfill canonical result-plan identity" >&2
  exit 1
fi
docker exec --interactive "$container_name" \
  psql --set ON_ERROR_STOP=1 --username=liveroute --dbname=liveroute \
  < "$repo_root/tests/migrations/schema_contract.sql"
apply_migration_section "$repo_root/migrations/00004_v15_frontend_foundation.sql" Up
backfilled_trip_name=$(docker exec "$container_name" \
  psql --tuples-only --no-align --username=liveroute --dbname=liveroute \
  --command "SELECT trip_name FROM trips WHERE id = '92929292-9292-9292-9292-929292929292';")
if [[ $backfilled_trip_name != "Trip 92929292" ]]; then
  echo "migration 4 failed to backfill a deterministic trip name" >&2
  exit 1
fi
docker exec --interactive "$container_name" \
  psql --set ON_ERROR_STOP=1 --username=liveroute --dbname=liveroute \
  < "$repo_root/tests/migrations/00004_v15_foundation_contract.sql"
apply_migration_section "$repo_root/migrations/00004_v15_frontend_foundation.sql" Down
apply_migration_section "$repo_root/migrations/00003_canonical_intent_plan_identity.sql" Down
apply_migration_section "$repo_root/migrations/00002_delivery_recovery.sql" Down
apply_migration_section "$repo_root/migrations/00001_canonical_trip_state.sql" Down

remaining_tables=$(docker exec "$container_name" \
  psql --tuples-only --no-align --username=liveroute --dbname=liveroute \
  --command "SELECT count(*) FROM pg_tables WHERE schemaname = 'public';")
if [[ $remaining_tables != 0 ]]; then
  echo "expected migration rollback to remove all public tables, found $remaining_tables" >&2
  exit 1
fi

echo "PostgreSQL migration contract checks passed."

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
  < "$repo_root/tests/migrations/schema_contract.sql"
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

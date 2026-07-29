#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
postgres_digest=$(awk '
  /^  postgres:$/ { active = 1; next }
  active && /^    digest: / { print $2; exit }
  active && /^  [^ ]/ { exit }
' "$repo_root/config/tool-images.lock")
builder_digest=$(awk '
  /^  backend_builder:$/ { active = 1; next }
  active && /^    digest: / { print $2; exit }
  active && /^  [^ ]/ { exit }
' "$repo_root/config/tool-images.lock")
runtime_digest=$(awk '
  /^  backend_runtime:$/ { active = 1; next }
  active && /^    digest: / { print $2; exit }
  active && /^  [^ ]/ { exit }
' "$repo_root/config/tool-images.lock")

if [[ ! $postgres_digest =~ ^sha256:[0-9a-f]{64}$ ]] ||
   [[ ! $builder_digest =~ ^sha256:[0-9a-f]{64}$ ]] ||
   [[ ! $runtime_digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "missing pinned backend/PostgreSQL image digest" >&2
  exit 1
fi
grep --fixed-strings --quiet \
  "golang@$builder_digest" "$repo_root/docker/backend/Dockerfile"
grep --fixed-strings --quiet \
  "debian@$runtime_digest" "$repo_root/docker/backend/Dockerfile"

suffix=$$
network_name="liveroute-backend-test-$suffix"
postgres_name="liveroute-backend-postgres-$suffix"
test_image="liveroute-backend-test:$suffix"
runtime_image="liveroute-backend-runtime:$suffix"
database_url="postgres://liveroute:integration-test-only@postgres:5432/liveroute?sslmode=disable"

cleanup() {
  docker rm --force "$postgres_name" >/dev/null 2>&1 || true
  docker network rm "$network_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker build \
  --file "$repo_root/docker/backend/Dockerfile" \
  --target test \
  --tag "$test_image" \
  "$repo_root"
docker build \
  --file "$repo_root/docker/backend/Dockerfile" \
  --tag "$runtime_image" \
  "$repo_root"

docker network create "$network_name" >/dev/null
docker run --detach --name "$postgres_name" \
  --network "$network_name" \
  --network-alias postgres \
  --env POSTGRES_USER=liveroute \
  --env POSTGRES_PASSWORD=integration-test-only \
  --env POSTGRES_DB=liveroute \
  "postgres@$postgres_digest" >/dev/null

for _ in $(seq 1 30); do
  if docker exec "$postgres_name" \
      pg_isready --username=liveroute --dbname=liveroute >/dev/null; then
    break
  fi
  sleep 1
done
docker exec "$postgres_name" \
  pg_isready --username=liveroute --dbname=liveroute >/dev/null

docker run --rm --network "$network_name" \
  --env LIVEROUTE_DATABASE_URL="$database_url" \
  "$runtime_image" up

migration_version=$(docker exec "$postgres_name" \
  psql --tuples-only --no-align \
    --username=liveroute --dbname=liveroute \
    --command "SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1;")
if [[ $migration_version != 2 ]]; then
  echo "expected Goose migration version 2, found $migration_version" >&2
  exit 1
fi

docker run --rm --network "$network_name" \
  --env LIVEROUTE_TEST_DATABASE_URL="$database_url" \
  "$test_image" \
  go test -race -count=1 ./...

echo "Backend migration, runtime-lease, outbox, command, proposal, decision, and accepted-mutation checks passed."

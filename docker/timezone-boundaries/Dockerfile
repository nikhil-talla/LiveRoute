FROM debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS boundary-builder

RUN apt-get update \
    && apt-get install --no-install-recommends --yes \
      ca-certificates \
      curl \
      unzip \
    && rm -rf /var/lib/apt/lists/*

COPY config/timezone-boundaries.lock /tmp/liveroute-timezone-boundaries.lock

RUN set -eu; \
    release="$(awk '/^  release: / { print $2; exit }' /tmp/liveroute-timezone-boundaries.lock)"; \
    artifact_url="$(awk '/^  url: / { print $2; exit }' /tmp/liveroute-timezone-boundaries.lock)"; \
    expected_archive_sha256="$(awk '/^  sha256: / { print $2; exit }' /tmp/liveroute-timezone-boundaries.lock)"; \
    expected_archive_size="$(awk '/^  size_bytes: / { print $2; exit }' /tmp/liveroute-timezone-boundaries.lock)"; \
    archive_path="$(awk '/^  build_archive_path: / { print $2; exit }' /tmp/liveroute-timezone-boundaries.lock)"; \
    member_name="$(awk '/^  extracted_geojson_name: / { print $2; exit }' /tmp/liveroute-timezone-boundaries.lock)"; \
    expected_member_sha256="$(awk '/^  extracted_geojson_sha256: / { print $2; exit }' /tmp/liveroute-timezone-boundaries.lock)"; \
    expected_member_size="$(awk '/^  extracted_geojson_size_bytes: / { print $2; exit }' /tmp/liveroute-timezone-boundaries.lock)"; \
    install_path="$(awk '/^  container_geojson_path: / { print $2; exit }' /tmp/liveroute-timezone-boundaries.lock)"; \
    test "$release" = "2026c"; \
    test -n "$artifact_url"; \
    test "${#expected_archive_sha256}" -eq 64; \
    test "${#expected_member_sha256}" -eq 64; \
    curl --fail --location --silent --show-error \
      --proto '=https' --tlsv1.2 \
      "$artifact_url" \
      --output "$archive_path"; \
    test "$(stat --format='%s' "$archive_path")" = "$expected_archive_size"; \
    test "$(sha256sum "$archive_path" | awk '{ print $1 }')" = "$expected_archive_sha256"; \
    test "$(unzip -Z1 "$archive_path")" = "$member_name"; \
    mkdir --parents "$(dirname "$install_path")"; \
    unzip -p "$archive_path" "$member_name" > "$install_path"; \
    test "$(stat --format='%s' "$install_path")" = "$expected_member_size"; \
    test "$(sha256sum "$install_path" | awk '{ print $1 }')" = "$expected_member_sha256"

FROM debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

COPY --from=boundary-builder /opt/liveroute/share/timezone-boundaries \
  /opt/liveroute/share/timezone-boundaries
COPY config/timezone-boundaries.lock \
  /opt/liveroute/share/timezone-boundaries/timezone-boundaries.lock
COPY --chmod=0555 docker/timezone-boundaries/verify-assets.sh \
  /usr/local/bin/liveroute-verify-timezone-boundaries
COPY --chmod=0555 docker/timezone-boundaries/install-assets.sh \
  /usr/local/bin/liveroute-install-timezone-boundaries

ENTRYPOINT ["/usr/local/bin/liveroute-verify-timezone-boundaries"]

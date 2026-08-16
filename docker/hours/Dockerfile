FROM debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS tzdata-builder

RUN apt-get update \
    && apt-get install --no-install-recommends --yes \
      ca-certificates \
      curl \
    && rm -rf /var/lib/apt/lists/*

COPY config/tzdata.lock /tmp/liveroute-tzdata.lock

RUN set -eu; \
    release="$(awk '/^release: / { print $2; exit }' /tmp/liveroute-tzdata.lock)"; \
    artifact_url="$(awk '/^  url: / { print $2; exit }' /tmp/liveroute-tzdata.lock)"; \
    expected_sha512="$(awk '/^  sha512: / { print $2; exit }' /tmp/liveroute-tzdata.lock)"; \
    test "$release" = "2026c"; \
    test -n "$artifact_url"; \
    test "${#expected_sha512}" -eq 128; \
    curl --fail --location --silent --show-error \
      --proto '=https' --tlsv1.2 \
      "$artifact_url" \
      --output /tmp/tzdata.tar.gz; \
    actual_sha512="$(sha512sum /tmp/tzdata.tar.gz | awk '{ print $1 }')"; \
    test "$actual_sha512" = "$expected_sha512"; \
    mkdir --parents /tmp/tzdata-source \
      "/opt/liveroute/share/tzdata/$release/zoneinfo"; \
    tar --extract --gzip --file /tmp/tzdata.tar.gz \
      --directory /tmp/tzdata-source; \
    /usr/sbin/zic -b fat \
      -d "/opt/liveroute/share/tzdata/$release/zoneinfo" \
      /tmp/tzdata-source/africa \
      /tmp/tzdata-source/antarctica \
      /tmp/tzdata-source/asia \
      /tmp/tzdata-source/australasia \
      /tmp/tzdata-source/europe \
      /tmp/tzdata-source/northamerica \
      /tmp/tzdata-source/southamerica \
      /tmp/tzdata-source/etcetera \
      /tmp/tzdata-source/factory \
      /tmp/tzdata-source/backward; \
    cp /tmp/tzdata-source/iso3166.tab \
      /tmp/tzdata-source/zone.tab \
      /tmp/tzdata-source/zone1970.tab \
      "/opt/liveroute/share/tzdata/$release/zoneinfo/"; \
    echo "$release" > "/opt/liveroute/share/tzdata/$release/release"; \
    TZDIR="/opt/liveroute/share/tzdata/$release/zoneinfo" \
      /usr/bin/zdump America/New_York >/dev/null

FROM debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

COPY --from=tzdata-builder /opt/liveroute/share/tzdata \
  /opt/liveroute/share/tzdata
COPY config/tzdata.lock /opt/liveroute/share/tzdata/tzdata.lock
COPY config/hours-seed.lock /opt/liveroute/share/hours/seed.lock
COPY data/hours/liveroute-v1-hours-seed.json \
  /opt/liveroute/share/hours/liveroute-v1-hours-seed.json
COPY --chmod=0555 docker/hours/verify-assets.sh \
  /usr/local/bin/liveroute-verify-hours-assets
COPY --chmod=0555 docker/hours/install-tzdata.sh \
  /usr/local/bin/liveroute-install-tzdata

ENV TZDIR=/opt/liveroute/share/tzdata/2026c/zoneinfo

ENTRYPOINT ["/usr/local/bin/liveroute-verify-hours-assets"]

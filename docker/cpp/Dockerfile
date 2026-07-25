# The base digest is resolved from debian:bookworm-slim during the first V1
# skeleton gate. Package versions are pinned in the forthcoming builder lock.
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
    curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 \
      "$artifact_url" --output /tmp/tzdata.tar.gz; \
    test "$(sha512sum /tmp/tzdata.tar.gz | awk '{ print $1 }')" = "$expected_sha512"; \
    mkdir --parents /tmp/tzdata-source \
      "/opt/liveroute/share/tzdata/$release/zoneinfo"; \
    tar --extract --gzip --file /tmp/tzdata.tar.gz --directory /tmp/tzdata-source; \
    /usr/sbin/zic -b fat -d "/opt/liveroute/share/tzdata/$release/zoneinfo" \
      /tmp/tzdata-source/africa /tmp/tzdata-source/antarctica \
      /tmp/tzdata-source/asia /tmp/tzdata-source/australasia \
      /tmp/tzdata-source/europe /tmp/tzdata-source/northamerica \
      /tmp/tzdata-source/southamerica /tmp/tzdata-source/etcetera \
      /tmp/tzdata-source/factory /tmp/tzdata-source/backward; \
    cp /tmp/tzdata-source/zone1970.tab \
      "/opt/liveroute/share/tzdata/$release/zoneinfo/"; \
    echo "$release" > "/opt/liveroute/share/tzdata/$release/release"

FROM debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

RUN apt-get update \
    && apt-get install --no-install-recommends --yes \
      build-essential \
      cmake \
      ninja-build \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
COPY CMakeLists.txt ./
COPY include ./include
COPY src ./src
COPY tests ./tests
COPY data ./data
COPY bench ./bench
COPY --from=tzdata-builder /opt/liveroute/share/tzdata \
  /opt/liveroute/share/tzdata

RUN cmake -S . -B /build -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo \
      -DLIVEROUTE_ENABLE_PINNED_TZDATA_TESTS=ON \
    && cmake --build /build \
    && ctest --test-dir /build --output-on-failure

ENTRYPOINT ["/build/liveroute_planner_service"]

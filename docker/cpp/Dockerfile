# Pinned tzdata is built separately, then copied into the exact Protobuf 31.1 /
# gRPC C++ 1.78.1 tool and runtime image used for generated bindings.
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

FROM --platform=linux/amd64 bmigeri/devcon-cpp@sha256:369f7744d6f9632b1c8142981f01c7b4c98db51b0096686dbd25f9ebb9eaa6f4

USER root
RUN mkdir --parents /var/lib/apt/lists/partial \
    && apt-get update \
    && apt-get install --no-install-recommends --yes \
      libcurl4-openssl-dev=8.5.0-2ubuntu10.11 \
    && rm -rf /var/lib/apt/lists/*
USER developer

WORKDIR /workspace
COPY CMakeLists.txt ./
COPY include ./include
COPY src ./src
COPY tests ./tests
COPY data ./data
COPY bench ./bench
COPY gen ./gen
COPY --from=tzdata-builder /opt/liveroute/share/tzdata \
  /opt/liveroute/share/tzdata

RUN cmake -S . -B /workspace/build -G Ninja \
      -DCMAKE_BUILD_TYPE=RelWithDebInfo \
      -DCMAKE_PREFIX_PATH=/opt/grpc \
      -DLIVEROUTE_ENABLE_GRPC=ON \
      -DLIVEROUTE_ENABLE_OSRM=ON \
      -DLIVEROUTE_ENABLE_PINNED_TZDATA_TESTS=ON \
    && cmake --build /workspace/build \
    && ctest --test-dir /workspace/build --output-on-failure \
    && /workspace/build/liveroute_proto_serialization_bench \
    && /workspace/build/liveroute_sharded_runtime_bench

ENTRYPOINT ["/workspace/build/liveroute_planner_service"]

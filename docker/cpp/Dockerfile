# The base digest is resolved from debian:bookworm-slim during the first V1
# skeleton gate. Package versions are pinned in the forthcoming builder lock.
FROM debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

RUN apt-get update \
    && apt-get install --no-install-recommends --yes \
      build-essential \
      cmake \
      ninja-build \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
COPY CMakeLists.txt ./
COPY src ./src
COPY tests ./tests
COPY bench ./bench

RUN cmake -S . -B /build -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo \
    && cmake --build /build \
    && ctest --test-dir /build --output-on-failure

ENTRYPOINT ["/build/liveroute_planner_service"]

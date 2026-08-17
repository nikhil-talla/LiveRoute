FROM --platform=linux/amd64 golang@sha256:3f6236bd765f898a2a3c2946112b04097814c4529d44534674700cd07b9c6b4c AS build

WORKDIR /workspace/backend
RUN test "$(go version)" = "go version go1.26.5 linux/amd64"
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend ./
COPY schema /workspace/schema
COPY config /workspace/config
RUN go test ./...
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w" \
    -o /out/liveroute-migrate \
    ./cmd/liveroute-migrate
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w" \
    -o /out/liveroute-admin \
    ./cmd/liveroute-admin
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w" \
    -o /out/liveroute-backend \
    ./cmd/liveroute-backend

FROM build AS test

FROM --platform=linux/amd64 debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

RUN groupadd --system --gid 10001 liveroute \
    && useradd --system --uid 10001 --gid liveroute \
      --no-create-home --home-dir /nonexistent liveroute
WORKDIR /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/liveroute-migrate /usr/local/bin/liveroute-migrate
COPY --from=build /out/liveroute-admin /usr/local/bin/liveroute-admin
COPY --from=build /out/liveroute-backend /usr/local/bin/liveroute-backend
COPY migrations ./migrations
COPY schema ./schema
COPY config ./config
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/liveroute-migrate"]
CMD ["up"]

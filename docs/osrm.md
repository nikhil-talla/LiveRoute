# Local Routing Service Setup

LiveRoute uses a checksum-locked Rhode Island map extract and a fixed OSRM
v26.5.0 image. Separate car and walking configurations are prepared and served
only inside the local Docker Compose network. OSRM is the service that supplies
road travel times; it is not exposed directly to the host.

## Install the locked source artifact

The required source metadata is recorded in `config/osrm-dataset.lock`:

- file: `rhode-island-260701.osm.pbf`
- source date: 2026-07-01
- size: 51,668,445 bytes
- SHA-256: `375eb017159102cff19032d5e679a061726d4c9c69851871cf03a1a893ee4b40`

Place it at:

```text
data/osrm/source/rhode-island-260701.osm.pbf
```

If it is not already present, download the exact locked URL:

```bash
mkdir -p data/osrm/source
curl --fail --location \
  --output data/osrm/source/rhode-island-260701.osm.pbf \
  https://download.geofabrik.de/north-america/us/rhode-island-260701.osm.pbf
```

Verify both size and digest before preprocessing:

```bash
test "$(stat --format=%s data/osrm/source/rhode-island-260701.osm.pbf)" = 51668445
printf '%s  %s\n' \
  375eb017159102cff19032d5e679a061726d4c9c69851871cf03a1a893ee4b40 \
  data/osrm/source/rhode-island-260701.osm.pbf | sha256sum --check --strict
```

Downloaded and generated files under `data/osrm/` are ignored by Git.

## Prepare and run OSRM

```bash
docker compose --profile osrm up --build --wait osrm-car osrm-foot
docker compose --profile osrm ps
```

The one-shot preparation containers verify the source file and routing
configuration, then create separate outputs under:

```text
data/osrm/generated/car/
data/osrm/generated/foot/
```

Later starts skip preparation only when the build stamp and required generated
files match. Each routing configuration uses one preparation thread and one
serving thread so local measurements are repeatable.

The car and walking endpoints are private (`http://osrm-car:5000` and
`http://osrm-foot:5000`). They are intentionally not published to the host.
Readiness performs a small fixed Table request and checks response dimensions.

To stop only the local OSRM services:

```bash
docker compose --profile osrm stop osrm-car osrm-foot
```

## Failure diagnosis

- A preparation container exiting immediately usually means the filename, size,
  or SHA-256 does not match the lock.
- `docker compose --profile osrm logs osrm-car-prepare osrm-foot-prepare`
  shows preprocessing failures.
- `docker compose --profile osrm logs osrm-car osrm-foot` shows serving or
  readiness failures.
- A dataset/profile/tool version change requires an explicit lock and cache
  namespace change; do not reuse generated artifacts under a new identity.

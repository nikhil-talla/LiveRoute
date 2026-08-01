FROM --platform=linux/amd64 ghcr.io/project-osrm/osrm-backend@sha256:34c4aa0a41daa8024b3c0bf1fe2ee42fc8b411d07a135ecc29f829390aed31ea

COPY config/osrm/profiles /opt/liveroute/profiles
COPY config/osrm/profiles.sha256 /opt/liveroute/profiles.sha256
COPY --chmod=0555 docker/osrm/prepare.sh /usr/local/bin/liveroute-prepare-osrm
COPY --chmod=0555 docker/osrm/healthcheck.sh /usr/local/bin/liveroute-osrm-healthcheck

RUN cd /opt/liveroute/profiles \
    && sha256sum --check /opt/liveroute/profiles.sha256

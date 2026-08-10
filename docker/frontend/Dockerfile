FROM --platform=linux/amd64 node@sha256:d45d78e7929b46875bbd4e29bea672d5bc48186c6c3588306521c815e78352d6 AS toolchain

RUN test "$(node --version)" = "v24.18.0" \
    && test "$(npm --version)" = "11.16.0"

WORKDIR /workspace/frontend

RUN chown node:node /workspace/frontend

COPY --chown=node:node frontend/package.json frontend/package-lock.json ./

USER node

RUN npm ci --ignore-scripts

USER root

COPY --chown=node:node frontend ./
COPY --chown=node:node schema/http /workspace/schema/http

USER node

FROM toolchain AS test

CMD ["npm", "run", "check"]

FROM toolchain AS development

EXPOSE 5173

CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]

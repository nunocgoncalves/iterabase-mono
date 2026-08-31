# syntax=docker/dockerfile:1
# The HOR-381 sandbox isolation test image. Builds the harness (for the real
# launcher at dist/launcher.js), then runs the probe + runner as ROOT — the
# runner needs root plus the container runtime's default capabilities to format
# and mount fresh ext4/XFS images, chown the sandboxes, and setpriv-spawn
# per-session children. Production likewise renders the trusted supervisor as
# root, retaining runtime-default capabilities plus explicit SETUID/SETGID.
#
# This is the HOR-381 isolation gate (bullets 1-5). Bullets 6-7 (RWO local-path
# cross-pod ownership + Pod Security admission) are HOR-245's gate.
# Run: make harness-isolation-test

FROM node:24-bookworm-slim AS builder
WORKDIR /src
COPY package.json package-lock.json tsconfig.json ./
RUN npm ci
COPY src ./src
RUN npm run build

FROM node:24-bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends e2fsprogs xfsprogs util-linux \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /src/dist ./dist
COPY isolation/probe.mjs isolation/probe-state.mjs isolation/run.mjs ./
# Runs as root (default): the runner chowns sandboxes + setpriv-spawns as
# per-session UIDs. No node_modules needed — launcher + probe use only Node
# built-ins.
ENTRYPOINT ["node", "/app/run.mjs"]

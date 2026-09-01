# Base image for every Run (SPEC §4). One shared image, no per-Agent builds.
# Deliberately absent: age — decryption is the control plane's job.
FROM node:22-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git iptables jq tar util-linux zstd \
    && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code @openai/codex skills \
    && npm cache clean --force

# Unprivileged user the entrypoint drops to via setpriv (SPEC §6).
RUN useradd --create-home --shell /bin/sh agent

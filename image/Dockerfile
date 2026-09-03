# Base image for every Run (SPEC §4). One shared image, no per-Agent builds.
# Deliberately absent: age — decryption is the control plane's job.
FROM golang:1.27-trixie AS golang

# Official Node LTS on Debian trixie. The CLIs need Node >= 22 and trixie's
# own apt package is frozen at 20 for the life of the release.
FROM node:24-trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl git iptables jq pkg-config python3 \
        tar util-linux zstd \
    && rm -rf /var/lib/apt/lists/*

# The Go toolchain is self-contained; copy it rather than trust a third repo.
COPY --from=golang /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH

# Unpinned: each dated image build picks up the current CLIs; the date tag
# itself is the pin. A Run must never self-update mid-flight, hence
# DISABLE_AUTOUPDATER.
RUN npm install -g @anthropic-ai/claude-code @openai/codex skills \
    && npm cache clean --force
ENV DISABLE_AUTOUPDATER=1

# Unprivileged user the entrypoint drops to via setpriv (SPEC §6).
RUN useradd --create-home --shell /bin/sh agent

# yarn through corepack (bundled with Node 24), so a repo's packageManager
# pin is honoured. The cache belongs to agent: that is who runs yarn.
ENV COREPACK_HOME=/usr/local/share/corepack
RUN corepack enable && corepack install -g yarn@stable \
    && chown -R agent:agent "$COREPACK_HOME"

# Build fails here if any tool is broken; in CI this also exercises the arm64
# binaries under QEMU.
RUN claude --version && codex --version && skills --version \
    && go version && yarn --version && gcc --version

# The entrypoint is baked, not mounted: the control plane cannot write files
# to a remote Runner, it only has the Docker API (SPEC §6).
COPY --chmod=644 stream.jq /usr/local/share/stream.jq
COPY --chmod=755 dsecrets.sh /usr/local/bin/dsecrets
COPY --chmod=755 entrypoint.sh /usr/local/bin/entrypoint
ENTRYPOINT ["/usr/local/bin/entrypoint"]

# The control plane (SPEC §1): one static Go binary. Built by the sandbox
# (sandbox/compose.yaml) and by the Coolify deploy (ticket 14).
FROM golang:1.27-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /control-plane ./cmd/control-plane

# Not distroless: a shell in the container is worth a few megabytes when a
# deploy misbehaves. ca-certificates is for the bucket over HTTPS.
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /control-plane /usr/local/bin/control-plane
ENTRYPOINT ["control-plane"]
CMD ["-config", "/etc/autonomous-agents/control-plane.yaml"]

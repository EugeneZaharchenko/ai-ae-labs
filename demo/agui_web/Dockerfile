# Dockerfile for demo/agui_web — the AG-UI bridge, the ADK agent service, and
# the embedded chat page in a single image.
#
# Why both binaries in one image: the bridge is useless without an agent behind
# it, and the lab's agent already exists (week2/.../labs4). Shipping two images
# for a demo would mean two ports, two `docker run` lines, and a network to wire
# between them. One image with a start script is the smaller total complexity,
# and it matches how `task week2:day4:serve` runs the agent on a laptop.
#
# Build context is the REPOSITORY ROOT: the ADK service imports
# week2/internal/refund, and the bridge imports the root module's go.mod.
# Go/ADK pins: October 2026.
#
#   docker build -f demo/agui_web/Dockerfile -t ai-ae-agui:demo .
#
# Dockerfile.dockerignore keeps the rest of the repository — including apps/.env
# and its live credentials — out of the context.

FROM --platform=$BUILDPLATFORM golang:1.27.1 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

# Module files first, then only the packages the two binaries import. A narrow
# COPY is the point of the split: editing an unrelated lab cannot invalidate the
# module download or the compile below.
COPY go.mod go.sum ./
COPY internal/ ./internal/
COPY week2/internal/refund/ ./week2/internal/refund/
COPY week2/Day4_Agent_as_Service_Deploy/labs4/ ./week2/Day4_Agent_as_Service_Deploy/labs4/
COPY demo/agui_web/ ./demo/agui_web/

# Two binaries, one compile pass. CGO_ENABLED=0 is what makes `scratch` possible.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" \
        -o /out/agui-web ./demo/agui_web \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" \
        -o /out/adk-service ./week2/Day4_Agent_as_Service_Deploy/labs4

FROM scratch
COPY --from=build /out/agui-web /agui-web
COPY --from=build /out/adk-service /adk-service

# CA bundle: not needed for the local ADK hop, but the agent and the bridge both
# make plain HTTP calls, and a future tool reaching an HTTPS endpoint would fail
# with "x509: certificate signed by unknown authority" without this.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# No zoneinfo: unlike demo/6_adk_telegram_bot, nothing here calls
# time.LoadLocation. The refund graph is pure string and map work, so the
# database would be dead weight.

# The ADK service is started BY THE BRIDGE, not by a shell script: `scratch` has
# no /bin/sh, so there is nothing to run a script with. main.go execs the child
# itself when AGENT_BIN is set, forwards SIGTERM to it, and waits for its drain.
# On a laptop AGENT_BIN is unset and the agent runs separately via
# `task week2:day4:serve`, so the same binary serves both modes.
#
# Both listeners bind every interface, which is what a published port needs:
#   :8080  the ADK agent service (adkrest: /api/, /health, /readyz)
#   :8081  the AG-UI bridge + chat page (/agui, /, /healthz)
# The bridge reaches the agent over loopback inside the container.
ENV BRIDGE_PORT=8081 \
    AGENT_URL=http://127.0.0.1:8080 \
    AGENT_BIN=/adk-service
EXPOSE 8081

USER 65532:65532
STOPSIGNAL SIGTERM

ENTRYPOINT ["/agui-web"]

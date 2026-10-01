# Dockerfile for demo/6_adk_telegram_bot — a static, non-root, FROM scratch image.
#
# Build context is the REPOSITORY ROOT, not this folder: the bot imports
# internal/modelcfg, internal/adkenv and internal/labrun from the root module.
# Go/ADK pins: September 2026.
#
#   docker build -f demo/6_adk_telegram_bot/Dockerfile -t ai-ae-telegram-bot:demo6 .
#
# Dockerfile.dockerignore keeps the rest of the repository out of the context —
# including apps/.env, which holds real credentials and must never enter a layer.

FROM --platform=$BUILDPLATFORM golang:1.27.1 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
# Only the module files and the packages this binary imports. A narrower COPY is
# the whole point of the split: editing an unrelated lab cannot invalidate the
# module download or the compile step below.
COPY go.mod go.sum ./
COPY internal/adkenv/ ./internal/adkenv/
COPY internal/labrun/ ./internal/labrun/
COPY internal/modelcfg/ ./internal/modelcfg/
COPY demo/6_adk_telegram_bot/ ./demo/6_adk_telegram_bot/
# CGO_ENABLED=0 is what makes `scratch` possible at all; the cache mounts keep a
# rebuild fast without baking the module cache or the build cache into a layer.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" \
    -o /out/telegram-bot ./demo/6_adk_telegram_bot

# scratch: no shell, no package manager, no libc, nothing to patch. The bot makes
# HTTPS calls to api.telegram.org and listens on no port, so that is all it needs.
FROM scratch
COPY --from=build /out/telegram-bot /telegram-bot

# CA bundle. Without it every api.telegram.org call fails with
# "x509: certificate signed by unknown authority".
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Zoneinfo, because the agent's only tool calls time.LoadLocation("Europe/Kyiv").
# scratch has no /usr/share/zoneinfo, so this line is load-bearing, not hygiene.
#
# Measured on a minimal scratch image that only calls LoadLocation: without it,
# the call fails with "unknown time zone Europe/Kyiv" and every current_time
# request is broken. With it, Europe/Kyiv resolves to EEST.
#
# The alternative is `_ "time/tzdata"` in main.go, which compiles the database
# into the binary. Measured: +384 KiB of binary and +98 KiB of image, against
# +184 KiB here. Copying costs about twice as much and keeps the Go source free
# of a container concern, which is why it is the choice; either works, and the
# test suite passes with both. The data is arch-independent, so it stays correct
# when TARGETARCH differs from BUILDPLATFORM.
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo

# No apps/.env inside the image, deliberately: adkenv.Load tolerates the missing
# file and falls back to the process environment, which is how configuration
# arrives — `--env-file apps/.env`, or one -e per variable. Keep WORKDIR off any
# path where a stray apps/.env could shadow the variables you passed.
WORKDIR /app

# Not root. 65532 is the conventional nonroot UID; with scratch there is no
# /etc/passwd, so only the numeric form works.
USER 65532:65532

# Default zone for the process itself. The agent still resolves a caller-named
# zone through the zoneinfo copy above.
ENV TZ=Europe/Kyiv

# No EXPOSE: the bot long-polls Telegram and serves no port. SIGTERM is what
# main.go's signal.NotifyContext waits for, so Ctrl-C and `docker stop` both
# unwind the poll loop cleanly.
STOPSIGNAL SIGTERM
ENTRYPOINT ["/telegram-bot"]

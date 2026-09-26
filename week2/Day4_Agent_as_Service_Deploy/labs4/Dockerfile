# Build context: ai-ae-labs repository root. Go/ADK pins: September 2026.
FROM --platform=$BUILDPLATFORM golang:1.27.1 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
COPY week2/internal/refund/ ./week2/internal/refund/
COPY week2/Day4_Agent_as_Service_Deploy/labs4/ ./week2/Day4_Agent_as_Service_Deploy/labs4/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" \
    -o /out/adk-service ./week2/Day4_Agent_as_Service_Deploy/labs4

FROM scratch
COPY --from=build /out/adk-service /adk-service
# Kept for HTTPS tool extensions; the default graph makes no outbound calls.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
ENV PORT=8080
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/adk-service"]

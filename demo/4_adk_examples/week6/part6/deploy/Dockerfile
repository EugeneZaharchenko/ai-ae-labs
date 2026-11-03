# Build context: the examples module root (examples/ here, ai-ae-labs/examples/ for learners).
#   docker build -f week6/part6/deploy/Dockerfile -t <registry>/refund-harness:0.1.0 .
# Same shape as the Week 2 service image: static binary, FROM scratch, non-root, :8080.
FROM --platform=$BUILDPLATFORM golang:1.27.1 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/ ./internal/
COPY week6/part6/cmd/ ./week6/part6/cmd/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" \
    -o /out/refund-harness ./week6/part6/cmd

FROM scratch
COPY --from=build /out/refund-harness /refund-harness
# check_egress makes HTTPS requests; without CA roots every probe would fail
# for the wrong reason, and the sandbox demo would prove nothing.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
ENV PORT=8080
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/refund-harness"]

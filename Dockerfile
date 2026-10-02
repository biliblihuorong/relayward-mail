# Build stage: static binary without CGO.
FROM golang:1.26-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/relayward ./cmd/relayward
RUN mkdir /out/data

# Run stage: distroless, non-root, no shell.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/relayward /relayward
# Pre-create /data owned by nonroot so a fresh named volume is writable.
COPY --from=build --chown=65532:65532 /out/data /data
USER nonroot:nonroot
EXPOSE 587 8080 8081
VOLUME ["/data"]
# The healthcheck subcommand GETs /healthz (distroless has no curl/wget).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/relayward", "healthcheck", "-url", "http://127.0.0.1:8081/healthz"]
ENTRYPOINT ["/relayward"]
CMD ["serve", "-config", "/etc/relayward/config.yaml"]

# syntax=docker/dockerfile:1
# Static build: modernc.org/sqlite is a pure-Go SQLite, so CGO is disabled
# and the resulting binary has no libc dependency.
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM debian:12-slim AS runtime
# curl is only used by container health checks.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN adduser --system --uid 10001 appuser && mkdir -p /data && chown appuser /data
USER appuser
WORKDIR /app
COPY --from=build /out/server /app/server
ENV API_PORT=8080 \
    DB_PATH=/data/ventilation.db
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD curl -fsS http://127.0.0.1:${API_PORT}/api/healthz || exit 1
ENTRYPOINT ["/app/server"]

# --- Build stage ---
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

# Cache dependencies
COPY server/go.mod server/go.sum ./server/
RUN cd server && go mod download

# Copy server source
COPY server/ ./server/

# Build binaries
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o bin/server ./cmd/server
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" -o bin/multica ./cmd/multica
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/migrate ./cmd/migrate
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/backfill_task_usage_hourly ./cmd/backfill_task_usage_hourly
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/backfill_codex_usage_cache ./cmd/backfill_codex_usage_cache

# --- Runtime stage ---
FROM alpine:3.21

ARG DWS_VERSION=v1.0.50
ARG DWS_RELEASE_BASE=https://github.com/DingTalk-Real-AI/dingtalk-workspace-cli/releases/download

RUN apk add --no-cache ca-certificates tzdata coreutils curl nodejs npm \
    && npm install -g @e2b/cli@2.13.0 \
    && npm cache clean --force

RUN set -eux; \
    mkdir -p /tmp/dws; \
    base="${DWS_RELEASE_BASE}/${DWS_VERSION}"; \
    curl -fsSLo /tmp/dws/checksums.txt "${base}/checksums.txt"; \
    curl -fsSLo /tmp/dws/dws-linux-amd64.tar.gz "${base}/dws-linux-amd64.tar.gz"; \
    cd /tmp/dws; \
    grep '  dws-linux-amd64.tar.gz$' checksums.txt | sha256sum -c -; \
    tar -xzf dws-linux-amd64.tar.gz; \
    install -m 0755 dws /usr/local/bin/dws; \
    rm -rf /tmp/dws; \
    dws version

WORKDIR /app

COPY --from=builder /src/server/bin/server .
COPY --from=builder /src/server/bin/multica .
COPY --from=builder /src/server/bin/migrate .
COPY --from=builder /src/server/bin/backfill_task_usage_hourly .
COPY --from=builder /src/server/bin/backfill_codex_usage_cache .
COPY server/migrations/ ./migrations/
COPY docker/entrypoint.sh .
RUN sed -i 's/\r$//' entrypoint.sh && chmod +x entrypoint.sh

EXPOSE 8080

ENTRYPOINT ["./entrypoint.sh"]

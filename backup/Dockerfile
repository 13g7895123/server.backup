# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.23-bookworm AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 純靜態 binary，不需 libc
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/agent     ./cmd/agent
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/dashboard ./cmd/dashboard

# ── Agent image ────────────────────────────────────────────────────────────────
# 需要 pg_dump / mysqldump / tar 等工具
FROM debian:bookworm-slim AS agent

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tar \
    gzip \
    postgresql-client \
    default-mysql-client \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/agent /app/agent
COPY migrations/ /app/migrations/

ENV HOST_PREFIX=/host
ENV NAS_BASE=/mnt/nas/backups

EXPOSE 9090
ENTRYPOINT ["/app/agent"]

# ── Dashboard image ────────────────────────────────────────────────────────────
# 同樣需要備份工具（單一二進位同時跑 API + 排程器）
FROM debian:bookworm-slim AS dashboard

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tar \
    gzip \
    postgresql-client \
    default-mysql-client \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/dashboard /app/dashboard
COPY migrations/ /app/migrations/

ENV HOST_PREFIX=/host
ENV NAS_BASE=/mnt/nas/backups

EXPOSE 8080
ENTRYPOINT ["/app/dashboard"]

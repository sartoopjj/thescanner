# thescanner-server — multi-stage build.
#   docker compose build && docker compose up -d
# Config lives in ./data/config.json (mounted at /data inside).

FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git ca-certificates tzdata
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=docker
ARG COMMIT=unknown
ARG DATE=unknown

RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w \
      -X github.com/sartoopjj/thescanner/internal/version.Version=${VERSION} \
      -X github.com/sartoopjj/thescanner/internal/version.Commit=${COMMIT} \
      -X github.com/sartoopjj/thescanner/internal/version.Date=${DATE}" \
    -o /thescanner-server ./cmd/server

FROM alpine:3.21
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
RUN adduser -D -u 1000 -h /data thescanner
COPY --from=builder /thescanner-server /usr/local/bin/thescanner-server

VOLUME /data
EXPOSE 5300/udp 5300/tcp 8053/tcp

USER thescanner
WORKDIR /data

ENTRYPOINT ["thescanner-server"]
CMD ["-config", "/data/config.json", "-data-dir", "/data"]

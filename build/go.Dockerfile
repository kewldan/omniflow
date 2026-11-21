# syntax=docker/dockerfile:1
#
# RUNTIME selects the final base image:
#   static   — distroless, for processes that only need the binary (api, web).
#   postgres — Alpine with the PostgreSQL client, for processes that shell out
#              to pg_dump and pg_restore (bot, worker).
ARG RUNTIME=static

FROM golang:1.27.4-alpine AS build
ARG TARGET
# VERSION is stamped into main.version, which the bot announces on start and
# the diagnostics bundle and update check compare against. Local builds stay
# "dev".
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/app ./cmd/${TARGET}

FROM gcr.io/distroless/static-debian13:nonroot AS runtime-static
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]

# pg_dump and pg_restore must match the server major version (PostgreSQL 19,
# as in compose.yaml). Alpine ships no postgresql19-client package, so the two
# tools and libpq are copied from the official image built on the same Alpine
# release, and their remaining shared libraries come from apk. Basing the
# runtime on the postgres image itself would also inherit its VOLUME and
# STOPSIGNAL. Keep both tags on the same Alpine release.
FROM postgres:19.0-alpine3.24 AS pgclient

FROM alpine:3.24.4 AS runtime-postgres
RUN apk add --no-cache ca-certificates tzdata zstd-libs lz4-libs libssl3 libcrypto3 zlib \
        krb5-libs libldap \
    && adduser -D -u 65532 -h /home/nonroot nonroot \
    && mkdir -p /var/lib/omniflow/backups \
    && chown -R nonroot:nonroot /var/lib/omniflow
COPY --from=pgclient /usr/local/lib/libpq.so.5* /usr/local/lib/
COPY --from=pgclient /usr/local/bin/pg_dump /usr/local/bin/pg_restore /usr/local/bin/
RUN pg_dump --version && pg_restore --version
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]

FROM runtime-${RUNTIME}

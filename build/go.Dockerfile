# syntax=docker/dockerfile:1
#
# RUNTIME selects the final base image:
#   static   — distroless, for processes that only need the binary (api, web).
#   postgres — Alpine with the PostgreSQL client, for processes that shell out
#              to pg_dump and pg_restore (bot, worker).
ARG RUNTIME=static

FROM golang:1.26.5-alpine AS build
ARG TARGET
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${TARGET}

FROM gcr.io/distroless/static-debian13:nonroot AS runtime-static
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]

FROM alpine:3.22.2 AS runtime-postgres
# pg_dump and pg_restore must match the server major version, so the client is
# pinned to the same PostgreSQL 18 the compose stack runs.
RUN apk add --no-cache postgresql18-client ca-certificates tzdata \
    && adduser -D -u 65532 -h /home/nonroot nonroot \
    && mkdir -p /var/lib/omniflow/backups \
    && chown -R nonroot:nonroot /var/lib/omniflow
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]

FROM runtime-${RUNTIME}

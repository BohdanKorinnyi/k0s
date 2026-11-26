# syntax=docker/dockerfile:1

# ---- backend ----------------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X dockpanel/internal/appinfo.Version=${VERSION}" \
      -o /out/dockpanel ./cmd/dockpanel

# ---- runtime ----------------------------------------------------------------
FROM alpine:3

# openssh-client: ssh:// hosts are reached by running `ssh`.
RUN apk add --no-cache ca-certificates openssh-client

COPY --from=build /out/dockpanel /usr/local/bin/dockpanel

# Saved hosts, registry logins and remembered SSH host keys.
VOLUME /data
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/api/session || exit 1

ENTRYPOINT ["dockpanel"]

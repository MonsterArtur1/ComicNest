# ComicNest container image.
#
# Multi-stage: compile a static Go binary (no cgo), then copy it into
# distroless/static (CA certificates for ComicVine over HTTPS, no shell).
# Volumes: /comics (library, read-only), /config (config.yaml), /data (SQLite +
# cover cache). Starts as root; with PUID/PGID set (NAS convention) the app
# chowns /config and /data to that user and drops privileges before opening
# anything. Alternatively run with --user and pre-owned directories.
#
#   docker build -t comicnest .
#   docker run -p 8080:8080 -e PUID=1026 -e PGID=100 \
#     -v /path/to/comics:/comics:ro -v ./config:/config -v ./data:/data comicnest

ARG GO_VERSION=1.25

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/comicnest ./cmd/comicnest

FROM gcr.io/distroless/static:latest
LABEL org.opencontainers.image.title="ComicNest" \
      org.opencontainers.image.description="Personal comic library server with web reader and OPDS catalog" \
      org.opencontainers.image.source="https://github.com/MonsterArtur1/ComicNest"

COPY --from=build /out/comicnest /comicnest
COPY config_example.yaml /config_example.yaml

# Defaults for the container layout; config.yaml (created on first start in
# /config) holds everything else: accounts, ComicVine key, OPDS, page size.
ENV COMICNEST_CONFIG=/config/config.yaml \
    COMICNEST_LISTEN=0.0.0.0 \
    COMICNEST_LIBRARY=/comics \
    COMICNEST_DATA_DIR=/data

VOLUME ["/config", "/data"]
EXPOSE 8080
# No USER here on purpose: PUID/PGID handling needs root at start. Set
# PUID/PGID (recommended) or `docker run --user` to avoid running as root.
ENTRYPOINT ["/comicnest"]

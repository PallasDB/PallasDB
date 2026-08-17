# syntax=docker/dockerfile:1

# ---- build ------------------------------------------------------------------
# Cross-compilation is done by Go, not by emulation: BUILDPLATFORM keeps the
# compiler running natively while TARGETOS/TARGETARCH select the output.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH

# Set by the release pipeline; these land in `pallasdb version`.
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

WORKDIR /src

# The scratch stage has no shell, so anything it needs must be built here:
# a CA bundle, and a /data directory already owned by the nonroot uid. Without
# the latter, Docker materialises the VOLUME as an empty root-owned directory
# and the server dies with "open /data/meta0: permission denied".
RUN apk add --no-cache ca-certificates \
 && mkdir -p /data \
 && chown 65532:65532 /data

# Dependency layer, invalidated only by go.mod/go.sum.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath \
      -ldflags="-s -w \
        -X main.version=${VERSION} \
        -X main.commit=${COMMIT} \
        -X main.date=${DATE}" \
      -o /out/pallasdb ./cmd/pallasdb

# ---- runtime ----------------------------------------------------------------
# scratch, not alpine: the binary is static (CGO_ENABLED=0) and PallasDB needs
# no shell, package manager, or libc at runtime. Nothing to patch, nothing to
# exec into.
FROM scratch AS runtime

# CA roots so `pallasdb cluster join`/TLS dialing can verify peers.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/pallasdb /usr/local/bin/pallasdb

# Carry the pre-owned /data across; a VOLUME is initialised from the image's
# copy of the path, so this is what makes the default `docker run` writable.
COPY --from=build --chown=65532:65532 /data /data

# uid/gid 65532 (nonroot). /data above is owned by this uid.
USER 65532:65532

# /data is the default --data-dir. Declaring it means `docker run` with no -v
# still gets a writable location instead of failing on the image layer.
VOLUME ["/data"]

EXPOSE 50051 7001 7946

ENTRYPOINT ["/usr/local/bin/pallasdb"]
CMD ["serve", "grpc", "--addr", ":50051", "--data-dir", "/data"]

LABEL org.opencontainers.image.title="PallasDB" \
      org.opencontainers.image.description="LSM-tree key-value database with SQL, gRPC, and Raft replication" \
      org.opencontainers.image.source="https://github.com/teddymalhan/pallasdb" \
      org.opencontainers.image.licenses="Apache-2.0"

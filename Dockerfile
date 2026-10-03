# One static binary on a distroless base: no shell, no package manager, nothing
# to patch but the binary itself. The blueprint submodule is not needed to
# build, so `git clone` without --recurse-submodules is enough.
#
# The build stage runs on the builder's own architecture and cross-compiles
# for the target: the binary is pure Go, so an arm64 image needs no emulation.
# Only the final stage is per-platform, and it only copies a file.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src

# Dependencies first, so a source-only change does not refetch them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG BUILD_DATE
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -trimpath \
      -ldflags="-s -w \
        -X github.com/remarqable/tmpio/internal/version.Version=${VERSION} \
        -X github.com/remarqable/tmpio/internal/version.Date=${BUILD_DATE}" \
      -o /out/tmpio ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tmpio /usr/local/bin/tmpio
USER nonroot:nonroot
EXPOSE 8000
ENV PORT=8000
# The binary answers its own health check; the image has no curl to do it.
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/tmpio", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/tmpio"]

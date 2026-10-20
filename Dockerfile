# The stagd CLI as an image, for `stagd relay` above all: in a Kubernetes
# cluster (deploy/kubernetes/stagd-relay.yaml), or on a Docker host with
# the Docker socket.
#
#   docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=v0.1.0 .

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/stagd ./cmd/stagd

# Nothing but the binary, CA certificates and time zones, as a user without
# privileges (65532, which deploy/kubernetes/stagd-relay.yaml expects). On a
# Docker host the relay needs the socket: run it as a user that can use it.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/stagd /stagd

USER 65532:65532

ENTRYPOINT ["/stagd"]
CMD ["--help"]

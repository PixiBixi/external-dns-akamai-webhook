FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies change far less often than the code, so they get their own layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Set by buildx, so one builder cross-compiles every target without qemu.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# Fed from the commit timestamp in CI so two builds of one commit are identical.
ARG SOURCE_DATE_EPOCH=0

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/external-dns-akamai-webhook . \
 && touch -d "@${SOURCE_DATE_EPOCH}" /out/external-dns-akamai-webhook

# Static binary, no shell, no package manager, nothing to exploit in the image.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/external-dns-akamai-webhook /external-dns-akamai-webhook

# Only health and metrics. The webhook API stays on localhost.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/external-dns-akamai-webhook"]

FROM --platform=$BUILDPLATFORM golang:1.26.3-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./

RUN go mod download

COPY cmd ./cmd
COPY pkg ./pkg

# BuildKit sets these per --platform; a default here would override it and
# ship an amd64 binary in the arm64 image (#39).
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT}" -o /out/reolinkproxy ./cmd/reolinkproxy

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        gstreamer1.0-tools \
        gstreamer1.0-plugins-base \
        gstreamer1.0-plugins-bad \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --system --uid 65532 --home-dir /nonexistent --shell /usr/sbin/nologin reolinkproxy

COPY --from=build /out/reolinkproxy /usr/local/bin/reolinkproxy

# Fail the build when the binary does not match the image platform. Running it
# is no check: on an amd64 runner an x86-64 binary executes natively inside the
# QEMU-built arm64 image, so only the ELF e_machine byte reveals it (#39).
RUN case "$(dpkg --print-architecture):$(od -An -tx1 -j18 -N1 /usr/local/bin/reolinkproxy | tr -d ' ')" in \
        amd64:3e|arm64:b7) ;; \
        *) echo "reolinkproxy binary does not match the image architecture" >&2; exit 1 ;; \
    esac

USER 65532:65532

EXPOSE 8554/tcp
EXPOSE 8000/udp
EXPOSE 8001/udp
EXPOSE 8002/tcp
EXPOSE 3702/udp

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["/usr/local/bin/reolinkproxy", "healthcheck", "--rtsp-only"]

ENTRYPOINT ["/usr/local/bin/reolinkproxy"]

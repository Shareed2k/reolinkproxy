FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY pkg ./pkg

ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/reolinkproxy ./cmd/reolinkproxy

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/reolinkproxy /reolinkproxy

EXPOSE 8554/tcp
EXPOSE 8000/udp
EXPOSE 8001/udp
EXPOSE 8002/tcp

ENTRYPOINT ["/reolinkproxy"]

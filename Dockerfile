# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w \
        -X github.com/ryczek02/ticketbench-contracts/pkg/version.Version=$VERSION \
        -X github.com/ryczek02/ticketbench-contracts/pkg/version.Commit=$COMMIT \
        -X github.com/ryczek02/ticketbench-contracts/pkg/version.Date=$DATE" \
      -o /out/tbench ./cmd/tbench

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/ryczek02/ticketbench-cli" \
      org.opencontainers.image.description="ticketbench CLI" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/tbench /usr/local/bin/tbench
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/tbench"]

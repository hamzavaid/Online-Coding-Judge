# syntax=docker/dockerfile:1

FROM golang:1.26.7-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS api
COPY --from=build --chown=65532:65532 /out/api /api
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/api"]

# The worker carries only an updated Docker client; its daemon belongs to the dedicated node.
FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS worker
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates docker-cli
COPY --from=build --chown=65532:65532 /out/worker /worker
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/worker"]

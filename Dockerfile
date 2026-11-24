# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS build

RUN apk add --no-cache ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -buildvcs=false \
    -ldflags="-s -w -buildid=" \
    -o /out/mailcontroller ./cmd/mailcontroller

# Prepare a writable data directory and /tmp for the non-root runtime user.
RUN mkdir -p /out/data /out/tmp \
    && chown 65532:65532 /out/data \
    && chmod 1777 /out/tmp

FROM scratch

ARG VERSION=dev
LABEL org.opencontainers.image.title="MailController" \
      org.opencontainers.image.description="Self-hosted Gmail mailbox controller" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mailcontroller /mailcontroller
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/tmp /tmp

ENV MC_DB_PATH=/data/mc.db

VOLUME ["/data"]
EXPOSE 8080
USER 65532:65532

ENTRYPOINT ["/mailcontroller"]

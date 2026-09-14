# Build a static, CGO-free binary (the SQLite driver is pure Go), then ship it
# on a small image with CA certificates for the HTTPS calls to Telegram and
# to the panel.
FROM golang:1.27-alpine AS build
WORKDIR /src
# proxy.golang.org answers 403 for some modules this depends on; the chain
# only falls through to `direct` on 404/410, so name a working mirror first.
ENV CGO_ENABLED=0 GOPROXY=https://goproxy.io,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/rapidobot ./cmd/rapidobot

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 rapidobot \
 && mkdir -p /data \
 && chown rapidobot /data
COPY --from=build /out/rapidobot /usr/local/bin/rapidobot
USER rapidobot
ENV DB_PATH=/data/rapidobot.db
VOLUME ["/data"]
ENTRYPOINT ["rapidobot"]

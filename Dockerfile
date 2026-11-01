FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git curl make libcap-setcap

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS TARGETARCH VERSION=dev-build

RUN make assets && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -ldflags="-s -w -X 'github.com/Tanq16/kamui/cmd.AppVersion=${VERSION}'" \
      -o /app/kamui . && \
    setcap cap_net_bind_service=+ep /app/kamui

FROM alpine:3.24.3

WORKDIR /app
COPY --from=builder --chown=10001:10001 /app/kamui .

RUN apk add --no-cache ca-certificates tzdata libcap-getcap && \
    getcap /app/kamui | grep -q cap_net_bind_service && \
    apk del libcap-getcap && \
    addgroup -g 10001 -S app && \
    adduser -u 10001 -S -G app app && \
    mkdir -p /data && chown 10001:10001 /data
VOLUME ["/data"]

USER 10001:10001
EXPOSE 53/udp 53/tcp 8053
ENTRYPOINT ["./kamui"]
CMD ["serve", "--data-dir", "/data"]

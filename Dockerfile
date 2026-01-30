FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETARCH
ARG TARGETOS=linux

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o /rpcgofer ./cmd/rpcgofer

FROM --platform=$TARGETPLATFORM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /rpcgofer /app/rpcgofer

EXPOSE 8545 8546

ENTRYPOINT ["/app/rpcgofer"]
CMD ["-config", "/app/config.json"]

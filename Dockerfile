FROM golang:1.25-alpine AS builder
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -ldflags="-w -s -X main.version=${VERSION}" \
    -o /bin/avm .


FROM alpine:3.21
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1001 synx

COPY --from=builder /bin/avm /bin/avm

USER synx

ENTRYPOINT ["/bin/avm"]

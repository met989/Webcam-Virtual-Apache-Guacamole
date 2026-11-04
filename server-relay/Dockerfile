# Multi-stage build: compiles Go binary in a clean Alpine environment
FROM golang:alpine AS builder
WORKDIR /app

# Install SSL CA certificates
RUN apk add --no-cache ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o relay .

# Lightweight final image (< 15 MB)
FROM alpine:latest
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app/relay /app/relay
COPY ./static /app/static

ENV PORT=9090
ENV STATIC_DIR=/app/static

EXPOSE 9090

ENTRYPOINT ["/app/relay"]

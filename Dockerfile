# Build stage
FROM golang:1.24.3-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o shrinkray ./cmd/shrinkray

# Runtime stage - linuxserver/ffmpeg already has s6-overlay + hardware accel
FROM linuxserver/ffmpeg:latest

# Copy the binary
COPY --from=builder /app/shrinkray /usr/local/bin/shrinkray

# Copy s6-overlay service definition
COPY root/ /

# Restore s6-overlay entrypoint (linuxserver/ffmpeg overrides it)
ENTRYPOINT ["/init"]

EXPOSE 8080
VOLUME /config /media

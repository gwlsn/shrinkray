# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o shrinkray ./cmd/shrinkray

# Runtime stage
FROM ghcr.io/linuxserver/baseimage-alpine:3.19

# Install ffmpeg
RUN apk add --no-cache ffmpeg

# Copy the binary
COPY --from=builder /app/shrinkray /app/shrinkray

# Create directories
RUN mkdir -p /config /media

# Environment variables
ENV PUID=1000 \
    PGID=1000 \
    TZ=UTC \
    CONFIG_PATH=/config/shrinkray.yaml \
    MEDIA_PATH=/media

# Expose port
EXPOSE 8080

# Volume mounts
VOLUME /config /media

# Health check
HEALTHCHECK --interval=30s --timeout=3s \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/api/stats || exit 1

# Run the application
ENTRYPOINT ["/app/shrinkray", "-config", "/config/shrinkray.yaml", "-media", "/media"]

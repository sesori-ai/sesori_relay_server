# Multi-stage build for Go relay server
# Stage 1: Builder
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go.mod and go.sum first for better layer caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build relay server
RUN CGO_ENABLED=0 go build -o /relay ./cmd/relay/

# Stage 2: Runtime
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates

# Create non-root user
RUN addgroup -g 1000 relay && \
    adduser -D -u 1000 -G relay relay

# Copy binary from builder
COPY --from=builder /relay /usr/local/bin/relay

# Set ownership
RUN chown relay:relay /usr/local/bin/relay

# Switch to non-root user
USER relay

# Expose port
EXPOSE 8080

# Set entrypoint and default command
ENTRYPOINT ["relay"]
CMD ["--addr", ":8080"]

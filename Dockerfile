# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /src

# Install build dependencies
RUN apk add --no-cache git

# The bluesky-social/indigo dependency requires go 1.25+. GOTOOLCHAIN=auto
# lets the Go 1.23 base image download the required 1.25 toolchain
# automatically during `go mod download`. This is the standard Go
# forward-compatibility mechanism (see go.dev/doc/toolchain).
ENV GOTOOLCHAIN=auto

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary. -trimpath strips /workspace/... paths from the
# binary so builds are reproducible across machines; -buildvcs=false
# avoids embedding git metadata that differs between CI and local
# builds.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -o /hypergoat ./cmd/hypergoat

# Runtime stage
FROM alpine:3.21

# Install runtime dependencies.
RUN apk add --no-cache ca-certificates tzdata wget

# Create a non-root user and group for the runtime container.
# Running as UID 1000 (non-zero) applies the principle of least
# privilege: even if an attacker compromises the process they
# cannot write to /etc or trivially escalate inside the container.
RUN addgroup -S -g 1000 hypergoat \
    && adduser -S -u 1000 -G hypergoat -h /app hypergoat \
    && mkdir -p /app/data /app/static \
    && chown -R hypergoat:hypergoat /app

WORKDIR /app

# Copy the statically-linked binary from the builder stage.
COPY --from=builder --chown=hypergoat:hypergoat /hypergoat /app/hypergoat

# Drop privileges before running the process.
USER hypergoat

# Note: we intentionally do NOT declare VOLUME /app/data here.
# Railway bans the Dockerfile VOLUME keyword and expects operators
# Expose port
EXPOSE 8080

# Set environment defaults. DATABASE_URL must be provided at runtime
# (e.g., via Railway env vars or docker-compose).
ENV HOST=0.0.0.0
ENV PORT=8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run the server
ENTRYPOINT ["/app/hypergoat"]

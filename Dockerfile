FROM golang:1.21-alpine AS builder
WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY main.go ./

# Build the application with static linking
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -a -ldflags '-extldflags "-static"' -o provisioner .

# Final stage - use distroless for smaller, more secure image
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Copy CA certificates and binary from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/provisioner .

# Set default config path
ENV CONFIG_PATH=/config/config.json

USER nonroot:nonroot

ENTRYPOINT ["/app/provisioner"]

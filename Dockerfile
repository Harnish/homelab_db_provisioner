FROM golang:1.21-alpine AS builder
WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY *.go ./

# Build the application with static linking
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -a -ldflags '-extldflags "-static"' -o provisioner .

# Final stage - alpine provides pg_dump and mysqldump for backups
FROM alpine:3.19

RUN apk add --no-cache postgresql16-client mariadb-client ca-certificates tzdata && \
    addgroup -S nonroot && adduser -S -G nonroot nonroot

WORKDIR /app

COPY --from=builder /app/provisioner .

ENV CONFIG_PATH=/config/config.json

USER nonroot:nonroot

ENTRYPOINT ["/app/provisioner"]

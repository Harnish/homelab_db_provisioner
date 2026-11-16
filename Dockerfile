FROM golang:1.27-alpine AS builder
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
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -a -ldflags "-X main.version=${VERSION} -extldflags \"-static\"" -o provisioner .

# Final stage - alpine provides pg_dump, mysqldump and mongodump for backups
FROM alpine:3.24

RUN apk add --no-cache postgresql18-client mariadb-client mongodb-tools ca-certificates tzdata && \
    addgroup -S nonroot && adduser -S -G nonroot nonroot

WORKDIR /app

COPY --from=builder /app/provisioner .

ENV CONFIG_PATH=/config/config.json

USER nonroot:nonroot

ENTRYPOINT ["/app/provisioner"]

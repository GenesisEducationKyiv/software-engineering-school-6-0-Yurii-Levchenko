# Multi-stage build: compile in Go image, run in minimal Alpine image
# Stage 1: Build
FROM golang:1.24-alpine AS builder

# Allow Go to auto-download the toolchain version required by go.mod
ENV GOTOOLCHAIN=auto

WORKDIR /app

# Copy dependency files first (Docker caches this layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server main.go

# Stage 2: Run
FROM alpine:3.19

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /app/server .
# Copy migrations (needed at runtime)
COPY --from=builder /app/migrations ./migrations

EXPOSE 8080

CMD ["./server"]

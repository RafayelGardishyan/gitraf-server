FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install git for go-git
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY *.go ./
COPY templates/ ./templates/

# Build
RUN CGO_ENABLED=0 GOOS=linux go build -o gitraf-server .

# Final image
FROM alpine:latest

RUN apk add --no-cache ca-certificates git

WORKDIR /app

COPY --from=builder /app/gitraf-server .
COPY --from=builder /app/templates ./templates

EXPOSE 8080

ENTRYPOINT ["/app/gitraf-server"]
CMD ["--templates", "/app/templates"]

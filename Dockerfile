# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

# Bust cache to ensure fresh build
ARG CACHEBUST=1

COPY . .

# Reference the ARG to invalidate the cache for this and all subsequent layers
RUN echo "Build ${CACHEBUST}" > /dev/null

RUN CGO_ENABLED=0 GOOS=linux go build -o /oak ./cmd/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -o /oak-notify ./cmd/notify

# Runtime stage
FROM alpine:3.19

WORKDIR /app

RUN apk --no-cache add ca-certificates tzdata

COPY --from=builder /oak /oak
COPY --from=builder /oak-notify /oak-notify
COPY config.toml /app/config.toml

RUN mkdir -p /app/logs

EXPOSE 8081

CMD ["/oak"]

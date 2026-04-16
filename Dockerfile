# Build stage
FROM golang:1.21-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -ldflags="-s -w" -o iverbs .

# Final stage
FROM alpine:3.19

RUN apk add --no-cache exiftool inotify-tools ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/iverbs .
COPY static/ static/
COPY templates/ templates/

# Create directories for mounts
RUN mkdir -p /watch/sources /data/db

ENV PORT=8080
ENV WATCH_DIRS="/watch/sources"
ENV WATCHDOG_DELAY_MS=300
ENV DB_PATH="/data/db/iverbs.db"

EXPOSE 8080

CMD ["./iverbs"]
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o iverbs .

FROM alpine:3.19
RUN apk add --no-cache exiftool inotify-tools sqlite
WORKDIR /app
COPY --from=builder /app/iverbs .
COPY templates/ ./templates/
COPY static/ ./static/
RUN mkdir -p /data/cache /data/logs /data/state /data/db
VOLUME ["/data"]
EXPOSE 5000
CMD ["./iverbs"]
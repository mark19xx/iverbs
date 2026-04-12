FROM golang:1.21-alpine AS builder
RUN apk add --no-cache gcc musl-dev sqlite-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -ldflags '-extldflags "-static"' -o iverbs .

FROM alpine:3.19
RUN apk add --no-cache exiftool inotify-tools
WORKDIR /app
COPY --from=builder /app/iverbs .
COPY templates/ ./templates/
COPY static/ ./static/
RUN mkdir -p /data/cache /data/logs /data/state /data/db
VOLUME ["/data"]
EXPOSE 5000
CMD ["./iverbs"]
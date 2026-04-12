FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download && go mod tidy
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o iverbs .

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
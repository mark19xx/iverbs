FROM alpine:3.19

RUN apk add --no-cache \
    python3 \
    py3-pip \
    exiftool \
    inotify-tools \
    && rm -rf /var/cache/apk/*

WORKDIR /app

COPY requirements.txt .
RUN pip3 install --no-cache-dir --break-system-packages -r requirements.txt

COPY app.py .
COPY templates/ ./templates/
COPY static/ ./static/

RUN mkdir -p /data/cache /data/logs /data/state
VOLUME ["/data"]

EXPOSE 5000

CMD ["python3", "app.py"]
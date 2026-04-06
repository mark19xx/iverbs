FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# Install system dependencies
RUN apt-get update && apt-get install -y \
    exiftool \
    inotify-tools \
    rsync \
    python3 \
    python3-pip \
    sqlite3 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Python dependencies
COPY requirements.txt .
RUN pip3 install --no-cache-dir --break-system-packages -r requirements.txt

# Scripts
COPY scripts/restore_exif.sh /usr/local/bin/restore_exif
RUN chmod +x /usr/local/bin/restore_exif

# Web application
COPY app.py .
COPY templates/ ./templates/
COPY static/ ./static/

# Database and log directory
RUN mkdir -p /data/db /data/logs /data/cache/thumbs
VOLUME ["/data"]

EXPOSE 5000

CMD ["python3", "app.py"]

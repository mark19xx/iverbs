# IVERBS - Image & Video EXIF Restore, Backup, Sorter

**Version 0.3.3**

IVERBS is a lightweight, self-hosted web application designed to restore missing EXIF metadata in image and video files by extracting dates from filenames. It includes a real-time watchdog that monitors specified directories and automatically fixes new files.

## Features

- **Multi-source support**: Monitor multiple directories via tabs.
- **Folder tree navigation**: Browse subdirectories (non-recursive view).
- **File listing with pagination**: 20 items per page, intelligent pagination.
- **EXIF status caching**: SQLite + in-memory cache for fast lookups.
- **Batch operations**:
  - Fix Page: fixes files on the current page (respects "Missing only" filter).
  - Fix Folder: fixes all files recursively in selected subfolder.
  - Fix All: fixes all files in the current source tab.
  - All batch operations show progress percentage.
- **Per-file actions**:
  - Fix: extract date from filename (does not overwrite existing EXIF).
  - Edit: manually set EXIF date via prompt (overwrites).
- **Watchdog**: Automatically monitors directories for new/changed files and fixes them if EXIF is missing. Handles file deletion by cleaning cache.
- **Offline-ready**: All assets are bundled; no CDN dependencies.
- **Dark theme**: Responsive, mobile-friendly UI.

## Requirements

- Docker (or Go 1.21+ with exiftool installed locally)
- exiftool (included in Docker image)

## Quick Start with Docker

1. Clone the repository.
2. Prepare your media directories (e.g., `./photos` and `./videos`).
3. Run with Docker Compose:

```yaml
# docker-compose.yml
version: '3.8'
services:
  iverbs:
    image: ghcr.io/yourusername/iverbs:0.3.3
    ports:
      - "8080:8080"
    volumes:
      - /path/to/photos:/watch/sources/photos
      - /path/to/videos:/watch/sources/videos
      - ./data:/data
    environment:
      - WATCH_DIRS=/watch/sources/photos,/watch/sources/videos
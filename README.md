# IVERBS – Image & Video EXIF Restore (Go Backend)

**Version 0.3.2** – Stable, low‑resource, always‑on watchdog, SQLite cache, three fix modes.

---

## 📌 Overview

IVERBS is a web‑based tool that restores missing EXIF metadata (DateTimeOriginal, CreateDate, ModifyDate) from filenames of images and videos. It supports three fix modes (page, folder, full), folder tree browsing, pagination, a “Missing only” filter, manual date editing, and an always‑active filesystem watchdog. The 0.3.2 version is written in **Go** with an always‑on watchdog (no on/off toggle), and the processing delay is configurable via environment variable.

---

## ✨ Main Features

### 1. EXIF Restoration from Filename
Recognises a wide range of patterns (extensible):
- Android / Google Pixel: `IMG_YYYYMMDD_HHMMSS.jpg`, `PXL_...`
- WhatsApp (app): `IMG-YYYYMMDD-WAxxxx.jpg`
- WhatsApp (desktop): `WhatsApp Image YYYY-MM-DD at HH.MM.SS.jpeg`
- Viber: `IMG-YYYYMMDD-Vxxxx.jpg`, `viber_image_YYYY-MM-DD_HH-MM-SS.jpg`
- Telegram: `photo_YYYY-MM-DD_HH-MM-SS.jpg`
- Facebook, Messenger, Instagram, Snapchat: Unix timestamps (10 or 13 digits)
- TikTok: `ssstiktok_1712223851.mp4`, `TikTok_YYYY-MM-DD_video.mp4`
- macOS/Windows screenshots, DJI drones, Samsung Motion Photo, `VideoCapture_...`
- Simple `YYYYMMDD_HHMMSS` (with optional `_1` or `(1)` suffix)
- Any date/time pattern inside the filename (generalised regex)

The extracted date is written into the EXIF tags `DateTimeOriginal`, `CreateDate` and `ModifyDate`.

### 2. Web‑based File Browser
- **Folder tree** on the left (click to navigate). If a folder has no subfolders, “no subfolders” is shown (not an error).
- **File list** with columns: Name (click to open image/video), Estimated date, EXIF? (✓/✗), Action
- **Pagination** – 20 files per page, intelligent page buttons (<<, <, 1,2,3, >, >>)
- **Missing only** filter – show only files without EXIF data

### 3. Three Fix Modes (with percentage progress)
- **Fix Page** – batch‑fix all visible files on the current page (respects “Missing only”)
- **Fix Folder** – batch‑fix all files inside the selected subfolder (recursive)
- **Fix All** – batch‑fix all files inside the current tab (whole source directory)

Each button shows a progress percentage while running.

### 4. Manual Editing
- **Fix** button – automatically writes the date extracted from the filename (does not overwrite existing EXIF)
- **Edit** button – opens a prompt to manually enter a date (YYYY-MM-DD); fetches current EXIF date if present

### 5. Watchdog (Always‑On Background Monitoring)
- Uses `fsnotify` to watch all directories listed in `WATCH_DIRS` (recursively)
- Detects `CREATE`, `WRITE`, `REMOVE` events
- Rate‑limited processing delay configurable via `WATCHDOG_DELAY_MS` (default 300 ms)
- Removes deleted files from the cache automatically
- No on/off toggle – the watchdog is always active

### 6. SQLite Cache & Background Refresh
- Caches EXIF presence (`has_exif`) in an SQLite database
- Memory cache for fast lookups (`sync.RWMutex`)
- Background goroutine walks the filesystem once an hour to clean up stale entries
- Cache survives container restarts

### 7. Resource Efficiency
- **CPU:** Idle < 5 %, during batch fix < 50 % (one core)
- **Memory:** ≈30‑50 MB
- **Binary size:** ≈20 MB (static, Alpine‑based)

---

## 🐳 Installation (Docker)

### Using Docker Compose

Create a `docker-compose.yaml` file:

```yaml
services:
  iverbs:
    image: ghcr.io/mark19xx/iverbs:0.3.2   # or local build
    container_name: iverbs
    restart: unless-stopped
    ports:
      - "5000:5000"
    volumes:
      - /path/to/your/media:/watch/sources/photos:rw
      - /path/to/your/media2:/watch/sources/videos:rw
      - /path/to/persistent/data:/data:rw
    environment:
      - WATCH_DIRS=/watch/sources/photos,/watch/sources/videos
      - WATCHDOG_DELAY_MS=300   # optional, default 300ms
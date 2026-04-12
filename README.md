# IVERBS – Image & Video EXIF Restore (Go Backend)

**Version 0.3.0** – Stable, low‑resource, efficient watchdog, SQLite cache, persistent state.

---

## 📌 Overview

IVERBS is a web‑based tool that restores missing EXIF metadata (DateTimeOriginal, CreateDate, ModifyDate) from filenames of images and videos. It supports batch fixing, folder tree browsing, pagination, a “Missing only” filter, manual date editing, and a filesystem watchdog. The 0.3.0 version is written entirely in **Go**, resulting in much lower CPU and memory usage than the previous Python implementation (≈30‑50 MB RAM, 1‑5 % CPU idle).

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
- **Folder tree** on the left (click to navigate)
- **File list** with columns: Name (click to open image/video), Estimated date, EXIF? (✓/✗), Action
- **Pagination** – 20 files per page, intelligent page buttons (<<, <, 1,2,3, >, >>)
- **Missing only** filter – show only files without EXIF data
- **Write button** – batch‑fix all visible files on the current page (overwrites if “Missing only” is OFF)

### 3. Manual Editing
- **Fix** button – automatically writes the date extracted from the filename (does not overwrite existing EXIF)
- **Edit** button – opens a prompt to manually enter a date (YYYY-MM-DD); fetches current EXIF date if present

### 4. Watchdog (Background Monitoring)
- Uses `fsnotify` to watch the source directories recursively
- Detects `CREATE`, `WRITE`, `REMOVE` events
- Rate‑limited processing (0.3 s between files)
- Removes deleted files from the cache automatically
- Per‑source toggle (checkbox in the “Folders” header) – state is persisted across container restarts

### 5. SQLite Cache & Background Refresh
- Caches EXIF presence (`has_exif`) in an SQLite database
- Memory cache for fast lookups (`sync.RWMutex`)
- Background goroutine walks the filesystem once an hour to clean up stale entries (low priority, `time.Sleep(0.1)` per file)
- Cache survives container restarts

### 6. Resource Efficiency
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
    image: ghcr.io/mark19xx/iverbs:0.3.0   # or local build
    container_name: iverbs
    restart: unless-stopped
    ports:
      - "5000:5000"
    volumes:
      - /path/to/your/media:/watch/sources:rw
      - /path/to/persistent/data:/data:rw
    environment:
      - WATCH_DIRS=/watch/sources
```

Then run:

```bash
docker compose up -d
```

### Building from source

```bash
git clone https://github.com/mark19xx/iverbs.git
cd iverbs
docker build -t iverbs:0.3.0 .
docker run -d --name iverbs -p 5000:5000 -v /path/to/media:/watch/sources:rw -v /path/to/data:/data:rw -e WATCH_DIRS=/watch/sources iverbs:0.3.0
```

### Environment Variables

| Variable       | Default       | Description |
|----------------|---------------|-------------|
| `WATCH_DIRS`   | `/home/user`  | Comma‑separated list of directories to watch (e.g., `/watch/sources`) |
| `DB_PATH`      | `/data/db/iverbs.db` | Path to SQLite database (inside container) |
| `DATA_DIR`     | `/data/cache` | Not used directly – the container already creates `/data/db`, `/data/state`, etc. |

**Volumes:**
- `/watch/sources` – your media folders (bind mount)
- `/data` – persistent data (database, watchdog state, logs, cache)

---

## 🌐 Web Interface Usage

1. Open your browser at `http://<NAS_IP>:5000`
2. **Tabs** appear for each directory listed in `WATCH_DIRS`
3. **Folders** panel shows subdirectories – click to navigate
4. **File list** shows up to 20 files per page
5. **Missing only** – toggle to show only files without EXIF
6. **Write** – batch‑fix all files on the current page
   - If “Missing only” is ON → only files without EXIF are fixed (no overwrite)
   - If “Missing only” is OFF → all files are fixed (overwrite existing EXIF)
7. **Fix** (per file) – writes the estimated date from the filename (no overwrite)
8. **Edit** (per file) – manually set a date (YYYY-MM-DD)
9. **Watch** toggle (in “Folders” header) – turns the watchdog on/off for that source; state persists
10. **Click on file name** – opens the original image/video in a new tab (served via `/api/image/`)

---

## 🔧 Technical Details

- **Go 1.21+** with standard library `net/http`, `html/template`, `os/exec`
- **`github.com/fsnotify/fsnotify`** – file system watcher
- **`github.com/mattn/go-sqlite3`** – SQLite driver (CGO enabled, static linking)
- **External command:** `exiftool` (must be installed in the container – Alpine package)
- **Frontend:** plain HTML/CSS/JS (no frameworks), responsive design, Bootstrap removed

### Watchdog Persistence
- State files are stored in `/data/state/watchdog_<source_idx>.state`
- On container start, the watchdog is automatically restarted for sources that were previously enabled

### Cache Cleanup
- A background goroutine runs every hour and removes entries for files that no longer exist on disk
- Deleted files are also removed immediately when the watchdog sees a `REMOVE` event

---

## 📋 Planned Future Versions

- **0.4 – Backup module** – one‑way sync (rsync/rclone) to multiple destinations, optional scheduling
- **0.5 – Sorter module** – move/copy files based on MIME type or format (jpg vs png, mp4 vs mkv)
- **0.6 – Rename to MERBS** – support for audio files and other media, improved backup/sorter integration

---

## 🧪 Testing

The application has been tested on:
- Ubuntu 24.04 / Nobara 42 (Docker)
- NAS (Lenovo) with Portainer
- Mobile browsers (Kiwi, Lemur, Firefox)

Performance: handling ~800 files with background cache refresh uses < 10 % CPU after initial load.

---

## 📄 License

MIT – free to use, modify, and distribute.

---

## 🙏 Acknowledgements

- `exiftool` by Phil Harvey
- `fsnotify` and `go-sqlite3` open source projects
- All testers and contributors

---

**IVERBS – because your memories deserve the right timestamp.** 🚀
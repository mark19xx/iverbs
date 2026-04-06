#!/bin/bash
# restore_exif.sh - Extract date from filename and write to EXIF metadata

WATCH_DIR="."
ALL_MODE=false
OVERWRITE=false
MAX_DATE=$(date +%Y-%m-%d)
MIN_DATE="1970-01-01"
RSYNC_DEST=""
SKIP_LOG="/data/logs/skip_files.log"

date_to_timestamp() { date -d "$1" +%s 2>/dev/null; }
is_valid_date() { local ts=$1; [[ $ts -ge 0 && $ts -le 1893456000 ]]; }
format_exif_date() { date -d "@$1" +"%Y:%m:%d %H:%M:%S" 2>/dev/null; }

extract_date_from_filename() {
    local filename=$(basename "$1")
    local name_noext="${filename%.*}"
    local year month day hour min sec ts

    # Android / Google Pixel (with optional suffix)
    if [[ "$filename" =~ ^(IMG|PXL)_([0-9]{4})([0-9]{2})([0-9]{2})_([0-9]{2})([0-9]{2})([0-9]{2})(_[0-9]+|\([0-9]+\))?\..+$ ]]; then
        year=${BASH_REMATCH[2]}; month=${BASH_REMATCH[3]}; day=${BASH_REMATCH[4]}; hour=${BASH_REMATCH[5]}; min=${BASH_REMATCH[6]}; sec=${BASH_REMATCH[7]}
        ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # WhatsApp app: IMG-YYYYMMDD-WAxxxx
    if [[ "$filename" =~ ^IMG-([0-9]{4})([0-9]{2})([0-9]{2})-WA[0-9]+\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; ts=$(date -d "$year-$month-$day 00:00:00" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # WhatsApp desktop: "WhatsApp Image YYYY-MM-DD at HH.MM.SS.jpeg"
    if [[ "$filename" =~ ^WhatsApp\ (Image|Video)\ ([0-9]{4})-([0-9]{2})-([0-9]{2})\ at\ ([0-9]{2})\.([0-9]{2})\.([0-9]{2})\..+$ ]]; then
        year=${BASH_REMATCH[2]}; month=${BASH_REMATCH[3]}; day=${BASH_REMATCH[4]}; hour=${BASH_REMATCH[5]}; min=${BASH_REMATCH[6]}; sec=${BASH_REMATCH[7]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # Viber: IMG-YYYYMMDD-Vxxxx
    if [[ "$filename" =~ ^IMG-([0-9]{4})([0-9]{2})([0-9]{2})-V[0-9]+\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; ts=$(date -d "$year-$month-$day 00:00:00" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # Viber: viber_image_YYYY-MM-DD_HH-MM-SS.jpg
    if [[ "$filename" =~ ^viber_image_([0-9]{4})-([0-9]{2})-([0-9]{2})_([0-9]{2})-([0-9]{2})-([0-9]{2})\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; hour=${BASH_REMATCH[4]}; min=${BASH_REMATCH[5]}; sec=${BASH_REMATCH[6]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # Telegram: photo_YYYY-MM-DD_HH-MM-SS.jpg
    if [[ "$filename" =~ ^(photo|video)_([0-9]{4})-([0-9]{2})-([0-9]{2})_([0-9]{2})-([0-9]{2})-([0-9]{2})\..+$ ]]; then
        year=${BASH_REMATCH[2]}; month=${BASH_REMATCH[3]}; day=${BASH_REMATCH[4]}; hour=${BASH_REMATCH[5]}; min=${BASH_REMATCH[6]}; sec=${BASH_REMATCH[7]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # Facebook / Messenger / Instagram / Snapchat / TikTok with Unix timestamp
    for prefix in FB_IMG received_ IG_IMG Snapchat ssstiktok; do
        if [[ "$filename" =~ ^${prefix}_([0-9]{10,13})\..+$ ]]; then
            local ts_ms=${BASH_REMATCH[1]}
            if [[ ${#ts_ms} -eq 13 ]]; then ts=$(( ts_ms / 1000 )); else ts=$ts_ms; fi
            is_valid_date "$ts" && echo "$ts" && return 0
        fi
    done
    # Snapchat with datetime
    if [[ "$filename" =~ ^Snapchat-([0-9]{4})([0-9]{2})([0-9]{2})([0-9]{2})([0-9]{2})([0-9]{2})\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; hour=${BASH_REMATCH[4]}; min=${BASH_REMATCH[5]}; sec=${BASH_REMATCH[6]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # TikTok: TikTok_YYYY-MM-DD_video.mp4
    if [[ "$filename" =~ ^TikTok_([0-9]{4})-([0-9]{2})-([0-9]{2})_video\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; ts=$(date -d "$year-$month-$day 00:00:00" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # macOS screenshot (Hungarian)
    if [[ "$filename" =~ ^Képernyő(fotó|felvétel)\ ([0-9]{4})-([0-9]{2})-([0-9]{2})\ -\ ([0-9]{2})\.([0-9]{2})\.([0-9]{2})\..+$ ]]; then
        year=${BASH_REMATCH[2]}; month=${BASH_REMATCH[3]}; day=${BASH_REMATCH[4]}; hour=${BASH_REMATCH[5]}; min=${BASH_REMATCH[6]}; sec=${BASH_REMATCH[7]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # Windows Snipping Tool
    if [[ "$filename" =~ ^Képernyőkép\ ([0-9]{4})-([0-9]{2})-([0-9]{2})\ ([0-9]{2})([0-9]{2})([0-9]{2})\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; hour=${BASH_REMATCH[4]}; min=${BASH_REMATCH[5]}; sec=${BASH_REMATCH[6]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # Android/iOS screenshot
    if [[ "$filename" =~ ^Screenshot_([0-9]{4})([0-9]{2})([0-9]{2})-([0-9]{2})([0-9]{2})([0-9]{2})_.*\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; hour=${BASH_REMATCH[4]}; min=${BASH_REMATCH[5]}; sec=${BASH_REMATCH[6]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # DJI drone
    if [[ "$filename" =~ ^DJI_([0-9]{4})([0-9]{2})([0-9]{2})([0-9]{2})([0-9]{2})([0-9]{2})_[0-9]+_V\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; hour=${BASH_REMATCH[4]}; min=${BASH_REMATCH[5]}; sec=${BASH_REMATCH[6]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # Samsung Motion Photo
    if [[ "$filename" =~ ^([0-9]{4})([0-9]{2})([0-9]{2})_([0-9]{2})([0-9]{2})([0-9]{2})_MotionPhoto\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; hour=${BASH_REMATCH[4]}; min=${BASH_REMATCH[5]}; sec=${BASH_REMATCH[6]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # Plain YYYYMMDD_HHMMSS (with optional suffix)
    if [[ "$filename" =~ ^([0-9]{4})([0-9]{2})([0-9]{2})_([0-9]{2})([0-9]{2})([0-9]{2})(_[0-9]+|\([0-9]+\))?\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; hour=${BASH_REMATCH[4]}; min=${BASH_REMATCH[5]}; sec=${BASH_REMATCH[6]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # VideoCapture_YYYYMMDD-HHMMSS
    if [[ "$filename" =~ ^VideoCapture_([0-9]{4})([0-9]{2})([0-9]{2})-([0-9]{2})([0-9]{2})([0-9]{2})\..+$ ]]; then
        year=${BASH_REMATCH[1]}; month=${BASH_REMATCH[2]}; day=${BASH_REMATCH[3]}; hour=${BASH_REMATCH[4]}; min=${BASH_REMATCH[5]}; sec=${BASH_REMATCH[6]}; ts=$(date -d "$year-$month-$day $hour:$min:$sec" +%s 2>/dev/null) && echo "$ts" && return 0
    fi
    # Numeric filename (Unix timestamp)
    if [[ "$name_noext" =~ ^[0-9]{10,13}$ ]]; then
        if [[ ${#name_noext} -eq 13 ]]; then ts=$(( name_noext / 1000 )); else ts=$name_noext; fi
        is_valid_date "$ts" && echo "$ts" && return 0
    fi
    return 1
}

update_exif() {
    local file="$1"
    local date_ts="$2"
    local exif_date=$(format_exif_date "$date_ts")
    [[ -z "$exif_date" ]] && { echo "ERROR: Cannot format date for $file"; return 1; }
    [[ ! -w "$file" ]] && { echo "ERROR: File not writable: $file"; return 1; }

    if [[ "$OVERWRITE" == "false" ]]; then
        local existing=$(exiftool -s3 -DateTimeOriginal "$file" 2>/dev/null)
        if [[ -n "$existing" && "$existing" != "0000:00:00 00:00:00" ]]; then
            echo "SKIP: EXIF already exists ($existing) – $file"
            return 0
        fi
    fi

    if exiftool -overwrite_original \
        -DateTimeOriginal="$exif_date" \
        -CreateDate="$exif_date" \
        -ModifyDate="$exif_date" \
        "$file" >/dev/null 2>&1; then
        echo "OK: $file -> $exif_date"
        [[ -n "$RSYNC_DEST" ]] && rsync -avz "$file" "$RSYNC_DEST" >/dev/null 2>&1 &
        return 0
    else
        echo "ERROR: exiftool failed – $file"
        return 1
    fi
}

process_file() {
    local file="$1"
    [[ ! "$file" =~ \.(jpg|jpeg|png|mp4|mov|avi|jpe|jfif)$ ]] && return 0

    local date_ts
    date_ts=$(extract_date_from_filename "$file")
    if [[ $? -ne 0 ]]; then
        echo "SKIP: Cannot extract date from filename – $file"
        echo "$file" >> "$SKIP_LOG"
        return 0
    fi

    local max_ts=$(date_to_timestamp "$MAX_DATE")
    local min_ts=$(date_to_timestamp "$MIN_DATE")
    if [[ $date_ts -gt $max_ts ]]; then
        echo "SKIP: Extracted date newer than $MAX_DATE – $file"
        return 0
    fi
    if [[ $date_ts -lt $min_ts ]]; then
        echo "SKIP: Extracted date older than $MIN_DATE – $file"
        return 0
    fi

    update_exif "$file" "$date_ts"
}

show_help() {
    cat <<EOF
Usage: $0 [OPTIONS]
  --all                  Process all existing files (default: watch mode)
  --overwrite            Overwrite existing EXIF dates
  --watch-dir DIR        Directory to watch (default: .)
  --max-date YYYY-MM-DD  Maximum date (default: today)
  --min-date YYYY-MM-DD  Minimum date (default: 1970-01-01)
  --rsync-dest DEST      Rsync destination after successful write
  --help                 Show this help
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --all) ALL_MODE=true; shift ;;
        --overwrite) OVERWRITE=true; shift ;;
        --watch-dir) WATCH_DIR="$2"; shift 2 ;;
        --max-date) MAX_DATE="$2"; shift 2 ;;
        --min-date) MIN_DATE="$2"; shift 2 ;;
        --rsync-dest) RSYNC_DEST="$2"; shift 2 ;;
        --help) show_help; exit 0 ;;
        *) echo "Unknown option: $1"; show_help; exit 1 ;;
    esac
done

for cmd in exiftool inotifywait date rsync; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "ERROR: $cmd not found"
        exit 1
    fi
done

cd "$WATCH_DIR" || { echo "ERROR: Cannot cd to $WATCH_DIR"; exit 1; }
mkdir -p "$(dirname "$SKIP_LOG")"
[[ "$ALL_MODE" == "true" ]] && > "$SKIP_LOG"

echo "=== EXIF Restore Started ==="
echo "Directory: $WATCH_DIR | Max: $MAX_DATE | Min: $MIN_DATE | Overwrite: $OVERWRITE"

if [[ "$ALL_MODE" == "true" ]]; then
    find . -type f -print0 | while IFS= read -r -d '' file; do
        process_file "$file"
    done
else
    inotifywait -m -e close_write -e moved_to --format '%f' . | while read -r file; do
        process_file "$file"
    done
fi

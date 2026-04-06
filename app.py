import os
import sqlite3
import subprocess
import signal
import re
import threading
import time
from datetime import datetime
from flask import Flask, render_template, request, jsonify, send_file
from PIL import Image
import hashlib
from watchdog.observers import Observer
from watchdog.events import FileSystemEventHandler

app = Flask(__name__)

PHOTOS_DIR = os.environ.get('WATCH_DIR', '/watch/sources')
DB_PATH = os.environ.get('DB_PATH', '/data/db/iverbs.db')
RSYNC_DEST = os.environ.get('RSYNC_DEST', '')
PID_FILE = '/data/logs/watchdog.pid'
SKIP_LOG = '/data/logs/skip_files.log'
THUMB_CACHE = '/data/cache/thumbs'

os.makedirs(THUMB_CACHE, exist_ok=True)
os.makedirs(os.path.dirname(DB_PATH), exist_ok=True)

WATCH_NAME = os.path.basename(PHOTOS_DIR.rstrip('/'))

# Global observer reference
observer = None

def init_db():
    conn = sqlite3.connect(DB_PATH)
    c = conn.cursor()
    c.execute('''CREATE TABLE IF NOT EXISTS manual_fixes
                 (file_path TEXT PRIMARY KEY, fixed_date TEXT, fixed_at TIMESTAMP)''')
    c.execute('''CREATE TABLE IF NOT EXISTS skip_cache
                 (file_path TEXT PRIMARY KEY, skip_reason TEXT, last_seen TIMESTAMP)''')
    conn.commit()
    conn.close()

init_db()

def get_skip_files_from_log():
    if not os.path.exists(SKIP_LOG):
        return []
    with open(SKIP_LOG, 'r') as f:
        return [line.strip() for line in f if line.strip()]

def remove_skip_from_log(file_path):
    files = get_skip_files_from_log()
    if file_path in files:
        files.remove(file_path)
        with open(SKIP_LOG, 'w') as f:
            for line in files:
                f.write(line + '\n')

def get_exif_datetimeoriginal(file_path):
    try:
        result = subprocess.run(
            ['exiftool', '-s3', '-DateTimeOriginal', file_path],
            capture_output=True, text=True, timeout=5
        )
        if result.returncode == 0 and result.stdout.strip():
            return result.stdout.strip()
    except Exception:
        pass
    return None

def extract_date_from_filename(file_path):
    filename = os.path.basename(file_path)
    match = re.search(r'\b(\d{10})(?:\d{3})?\b', filename)
    if match:
        ts = int(match.group(1))
        if len(match.group(0)) == 13:
            ts //= 1000
        try:
            return datetime.fromtimestamp(ts).strftime('%Y-%m-%d %H:%M:%S')
        except:
            pass

    patterns = [
        (r'(\d{4})(\d{2})(\d{2})[ _-]?(\d{2})(\d{2})(\d{2})', '%Y-%m-%d %H:%M:%S'),
        (r'(\d{4})-(\d{2})-(\d{2})[ _-]?(\d{2})\.(\d{2})\.(\d{2})', '%Y-%m-%d %H:%M:%S'),
        (r'(\d{4})-(\d{2})-(\d{2})[ _-]?(\d{2})-(\d{2})-(\d{2})', '%Y-%m-%d %H:%M:%S'),
        (r'(\d{4})(\d{2})(\d{2})', '%Y-%m-%d'),
    ]
    for pattern, fmt in patterns:
        match = re.search(pattern, filename)
        if match:
            groups = match.groups()
            if len(groups) == 6:
                y, m, d, H, M, S = map(int, groups)
                try:
                    dt = datetime(y, m, d, H, M, S)
                    return dt.strftime('%Y-%m-%d %H:%M:%S')
                except:
                    continue
            elif len(groups) == 3:
                y, m, d = map(int, groups)
                try:
                    dt = datetime(y, m, d)
                    return dt.strftime('%Y-%m-%d 00:00:00')
                except:
                    continue
    return None

def generate_thumbnail(file_path, size=(200, 200)):
    mtime = os.path.getmtime(file_path)
    cache_key = hashlib.md5(f"{file_path}_{mtime}".encode()).hexdigest()
    thumb_path = os.path.join(THUMB_CACHE, cache_key + ".jpg")
    if os.path.exists(thumb_path):
        return thumb_path
    try:
        with Image.open(file_path) as img:
            img.thumbnail(size, Image.Resampling.LANCZOS)
            if img.mode in ('RGBA', 'P'):
                img = img.convert('RGB')
            img.save(thumb_path, 'JPEG', quality=85)
        return thumb_path
    except Exception as e:
        app.logger.error(f"Thumbnail failed: {e}")
        return None

def list_files_recursively(root):
    result = []
    for dirpath, dirnames, filenames in os.walk(root):
        rel_dir = os.path.relpath(dirpath, root)
        if rel_dir == '.':
            rel_dir = ''
        for f in filenames:
            if f.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
                full_path = os.path.join(dirpath, f)
                rel_path = os.path.join(rel_dir, f) if rel_dir else f
                estimated = extract_date_from_filename(full_path)
                result.append({
                    'name': f,
                    'path': rel_path,
                    'full_path': full_path,
                    'size': os.path.getsize(full_path),
                    'mtime': datetime.fromtimestamp(os.path.getmtime(full_path)).isoformat(),
                    'datetimeoriginal': get_exif_datetimeoriginal(full_path),
                    'estimated': estimated
                })
    return result

def process_new_file(file_path):
    """Process a single file (called by watchdog)."""
    rel_path = os.path.relpath(file_path, PHOTOS_DIR)
    estimated = extract_date_from_filename(file_path)
    if not estimated:
        # Add to skip log if not recognized
        with open(SKIP_LOG, 'a') as f:
            f.write(rel_path + '\n')
        return
    # Check existing EXIF
    existing = get_exif_datetimeoriginal(file_path)
    if existing and existing != '0000:00:00 00:00:00':
        return  # skip if already has EXIF
    # Apply date
    cmd = ['exiftool', '-overwrite_original',
           f'-DateTimeOriginal={estimated}',
           f'-CreateDate={estimated}',
           f'-ModifyDate={estimated}',
           file_path]
    subprocess.run(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    # Remove from skip log if it was there
    remove_skip_from_log(rel_path)

class MediaFileHandler(FileSystemEventHandler):
    def on_created(self, event):
        if not event.is_directory:
            if event.src_path.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
                process_new_file(event.src_path)
    def on_moved(self, event):
        if not event.is_directory:
            if event.dest_path.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
                process_new_file(event.dest_path)

def start_watchdog():
    global observer
    if observer is not None:
        return
    event_handler = MediaFileHandler()
    observer = Observer()
    observer.schedule(event_handler, PHOTOS_DIR, recursive=True)
    observer.start()
    # Write PID file
    with open(PID_FILE, 'w') as f:
        f.write(str(os.getpid()))

def stop_watchdog():
    global observer
    if observer is not None:
        observer.stop()
        observer.join()
        observer = None
    if os.path.exists(PID_FILE):
        os.remove(PID_FILE)

@app.route('/')
def index():
    return render_template('index.html', watch_name=WATCH_NAME)

@app.route('/api/config')
def api_config():
    return jsonify({'watch_name': WATCH_NAME, 'watch_dir': PHOTOS_DIR})

@app.route('/api/browse')
def api_browse():
    files = list_files_recursively(PHOTOS_DIR)
    return jsonify(files)

@app.route('/api/thumb/<path:file_path>')
def api_thumb(file_path):
    full = os.path.join(PHOTOS_DIR, file_path)
    if not os.path.exists(full):
        return '', 404
    thumb = generate_thumbnail(full)
    if thumb and os.path.exists(thumb):
        return send_file(thumb, mimetype='image/jpeg')
    return '', 404

@app.route('/api/skip_files')
def api_skip_files():
    files = get_skip_files_from_log()
    return jsonify(files)

@app.route('/api/fix_file', methods=['POST'])
def api_fix_file():
    data = request.json
    file_path = data.get('file')
    new_date = data.get('date')
    if not file_path or not new_date:
        return jsonify({'error': 'Missing parameters'}), 400
    full = os.path.join(PHOTOS_DIR, file_path)
    if not os.path.exists(full):
        return jsonify({'error': 'File not found'}), 404
    cmd = ['exiftool', '-overwrite_original',
           f'-DateTimeOriginal={new_date} 00:00:00',
           f'-CreateDate={new_date} 00:00:00',
           f'-ModifyDate={new_date} 00:00:00',
           full]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode == 0:
        remove_skip_from_log(file_path)
        conn = sqlite3.connect(DB_PATH)
        c = conn.cursor()
        c.execute('REPLACE INTO manual_fixes VALUES (?, ?, ?)',
                  (file_path, new_date, datetime.now().isoformat()))
        conn.commit()
        conn.close()
        return jsonify({'success': True})
    else:
        return jsonify({'error': result.stderr}), 500

@app.route('/api/autofix_single', methods=['POST'])
def api_autofix_single():
    data = request.json
    file_path = data.get('file')
    if not file_path:
        return jsonify({'error': 'Missing file parameter'}), 400
    full = os.path.join(PHOTOS_DIR, file_path)
    if not os.path.exists(full):
        return jsonify({'error': 'File not found'}), 404
    estimated_date = extract_date_from_filename(full)
    if not estimated_date:
        return jsonify({'error': 'Could not extract date from filename'}), 400
    cmd = ['exiftool', '-overwrite_original',
           f'-DateTimeOriginal={estimated_date}',
           f'-CreateDate={estimated_date}',
           f'-ModifyDate={estimated_date}',
           full]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode == 0:
        remove_skip_from_log(file_path)
        conn = sqlite3.connect(DB_PATH)
        c = conn.cursor()
        c.execute('REPLACE INTO manual_fixes VALUES (?, ?, ?)',
                  (file_path, estimated_date.split()[0], datetime.now().isoformat()))
        conn.commit()
        conn.close()
        return jsonify({'success': True, 'estimated_date': estimated_date})
    else:
        return jsonify({'error': result.stderr}), 500

@app.route('/api/batch_fix', methods=['POST'])
def api_batch_fix():
    data = request.json
    files = data.get('files', [])
    new_date = data.get('date')
    if not files or not new_date:
        return jsonify({'error': 'Missing parameters'}), 400
    results = []
    for file_path in files:
        full = os.path.join(PHOTOS_DIR, file_path)
        if not os.path.exists(full):
            results.append({'file': file_path, 'error': 'Not found'})
            continue
        cmd = ['exiftool', '-overwrite_original',
               f'-DateTimeOriginal={new_date} 00:00:00',
               f'-CreateDate={new_date} 00:00:00',
               f'-ModifyDate={new_date} 00:00:00',
               full]
        r = subprocess.run(cmd, capture_output=True, text=True)
        if r.returncode == 0:
            remove_skip_from_log(file_path)
            conn = sqlite3.connect(DB_PATH)
            c = conn.cursor()
            c.execute('REPLACE INTO manual_fixes VALUES (?, ?, ?)',
                      (file_path, new_date, datetime.now().isoformat()))
            conn.commit()
            conn.close()
            results.append({'file': file_path, 'success': True})
        else:
            results.append({'file': file_path, 'error': r.stderr})
    return jsonify({'results': results})

@app.route('/api/start_watchdog', methods=['POST'])
def api_start_watchdog():
    global observer
    if observer is not None:
        return jsonify({'message': 'Watchdog already running'}), 200
    start_watchdog()
    return jsonify({'success': True})

@app.route('/api/stop_watchdog', methods=['POST'])
def api_stop_watchdog():
    global observer
    if observer is None:
        return jsonify({'message': 'No watchdog running'}), 200
    stop_watchdog()
    return jsonify({'success': True})

@app.route('/api/run_rsync', methods=['POST'])
def api_run_rsync():
    if not RSYNC_DEST:
        return jsonify({'error': 'No RSYNC_DEST configured'}), 400
    cmd = ['rsync', '-avz', '--progress', PHOTOS_DIR + '/', RSYNC_DEST]
    subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return jsonify({'message': 'Rsync started in background'})

@app.route('/api/autofix_python', methods=['POST'])
def api_autofix_python():
    data = request.json
    overwrite = data.get('overwrite', False)
    max_date = data.get('max_date', '')
    min_date = data.get('min_date', '')

    max_ts = datetime.strptime(max_date, '%Y-%m-%d').timestamp() if max_date else None
    min_ts = datetime.strptime(min_date, '%Y-%m-%d').timestamp() if min_date else None

    results = []
    for dirpath, dirnames, filenames in os.walk(PHOTOS_DIR):
        for f in filenames:
            if not f.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
                continue
            full_path = os.path.join(dirpath, f)
            rel_path = os.path.relpath(full_path, PHOTOS_DIR)

            if not overwrite:
                existing = get_exif_datetimeoriginal(full_path)
                if existing and existing != '0000:00:00 00:00:00':
                    results.append({'file': rel_path, 'status': 'skipped (EXIF exists)'})
                    continue

            estimated = extract_date_from_filename(full_path)
            if not estimated:
                results.append({'file': rel_path, 'status': 'skipped (no date in filename)'})
                continue

            est_date = estimated.split()[0]
            est_ts = datetime.strptime(est_date, '%Y-%m-%d').timestamp()
            if max_ts and est_ts > max_ts:
                results.append({'file': rel_path, 'status': f'skipped (date > {max_date})'})
                continue
            if min_ts and est_ts < min_ts:
                results.append({'file': rel_path, 'status': f'skipped (date < {min_date})'})
                continue

            cmd = ['exiftool', '-overwrite_original',
                   f'-DateTimeOriginal={estimated}',
                   f'-CreateDate={estimated}',
                   f'-ModifyDate={estimated}',
                   full_path]
            ret = subprocess.run(cmd, capture_output=True, text=True)
            if ret.returncode == 0:
                remove_skip_from_log(rel_path)
                results.append({'file': rel_path, 'status': 'fixed', 'date': estimated})
            else:
                results.append({'file': rel_path, 'status': 'error', 'message': ret.stderr})

    return jsonify({'results': results})

@app.route('/api/watchdog_status', methods=['GET'])
def api_watchdog_status():
    global observer
    running = observer is not None and observer.is_alive()
    return jsonify({'running': running})

if __name__ == '__main__':
    # Ensure watchdog is not started automatically; wait for API call
    app.run(host='0.0.0.0', port=5000, debug=False)

import os
import json
import time
import threading
import queue
from datetime import datetime
from flask import Flask, render_template, request, jsonify
from watchdog.observers import Observer
from watchdog.events import FileSystemEventHandler

app = Flask(__name__)

# Konfiguráció a környezeti változókból
WATCH_SOURCES = os.environ.get('WATCH_DIRS', '/home/user').split(',')
DATA_DIR = os.environ.get('DATA_PATH', '/data/cache')
CACHE_FILE = os.path.join(DATA_DIR, 'exif_cache.json')
PID_FILE = '/data/logs/watchdog.pid'
os.makedirs(DATA_DIR, exist_ok=True)
os.makedirs('/data/logs', exist_ok=True)

# Memória cache: { 'full_path': has_exif (bool) }
exif_cache = {}
cache_lock = threading.Lock()

# Rate limiting queue a watchdog számára
watchdog_queue = queue.Queue()
watchdog_worker_running = False

def load_cache():
    global exif_cache
    if os.path.exists(CACHE_FILE):
        with open(CACHE_FILE, 'r') as f:
            data = json.load(f)
        with cache_lock:
            exif_cache = data
    else:
        with cache_lock:
            exif_cache = {}

def save_cache():
    with cache_lock:
        data = exif_cache.copy()
    with open(CACHE_FILE, 'w') as f:
        json.dump(data, f)

def check_exif(file_path):
    try:
        import subprocess
        result = subprocess.run(
            ['exiftool', '-s3', '-DateTimeOriginal', file_path],
            capture_output=True, text=True, timeout=2
        )
        has = result.returncode == 0 and result.stdout.strip() not in ('', '0000:00:00 00:00:00')
        with cache_lock:
            exif_cache[file_path] = has
        return has
    except Exception:
        with cache_lock:
            exif_cache[file_path] = False
        return False

def get_tree(root_path):
    tree = []
    try:
        for entry in os.scandir(root_path):
            if entry.is_dir() and not entry.name.startswith('.'):
                tree.append(entry.name)
    except Exception:
        pass
    return sorted(tree)

def get_files_in_dir(dir_path, limit=50, offset=0, missing_only=False):
    files = []
    all_files = []
    try:
        for entry in os.scandir(dir_path):
            if entry.is_file() and entry.name.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
                full = entry.path
                with cache_lock:
                    has_exif = exif_cache.get(full, False)
                if missing_only and has_exif:
                    continue
                all_files.append({
                    'name': entry.name,
                    'path': full,
                    'has_exif': has_exif
                })
    except Exception:
        pass
    total = len(all_files)
    files = all_files[offset:offset+limit]
    return files, total

def extract_date_from_filename(file_path):
    import re
    filename = os.path.basename(file_path)
    m = re.search(r'\b(\d{10})(?:\d{3})?\b', filename)
    if m:
        ts = int(m.group(1))
        if len(m.group(0)) == 13:
            ts //= 1000
        try:
            return datetime.fromtimestamp(ts).strftime('%Y-%m-%d %H:%M:%S')
        except:
            pass
    m = re.search(r'(\d{4})(\d{2})(\d{2})[ _-]?(\d{2})(\d{2})(\d{2})', filename)
    if m:
        y, mo, d, H, M, S = map(int, m.groups())
        try:
            dt = datetime(y, mo, d, H, M, S)
            return dt.strftime('%Y-%m-%d %H:%M:%S')
        except:
            pass
    m = re.search(r'(\d{4})-(\d{2})-(\d{2})[ _-]?(\d{2})\.(\d{2})\.(\d{2})', filename)
    if m:
        y, mo, d, H, M, S = map(int, m.groups())
        try:
            dt = datetime(y, mo, d, H, M, S)
            return dt.strftime('%Y-%m-%d %H:%M:%S')
        except:
            pass
    m = re.search(r'(\d{4})(\d{2})(\d{2})', filename)
    if m:
        y, mo, d = map(int, m.groups())
        try:
            dt = datetime(y, mo, d)
            return dt.strftime('%Y-%m-%d 00:00:00')
        except:
            pass
    return None

def fix_file(file_path, overwrite=False, dry_run=False):
    estimated = extract_date_from_filename(file_path)
    if not estimated:
        return {'error': 'No date in filename'}
    if not overwrite:
        with cache_lock:
            has = exif_cache.get(file_path, False)
        if not has:
            has = check_exif(file_path)
        if has:
            return {'skipped': 'EXIF already exists'}
    if dry_run:
        return {'estimated': estimated}
    import subprocess
    cmd = ['exiftool', '-overwrite_original',
           f'-DateTimeOriginal={estimated}',
           f'-CreateDate={estimated}',
           f'-ModifyDate={estimated}',
           file_path]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode == 0:
        with cache_lock:
            exif_cache[file_path] = True
        save_cache()
        return {'success': True, 'date': estimated}
    else:
        return {'error': result.stderr}

class RateLimitedHandler(FileSystemEventHandler):
    def on_created(self, event):
        if not event.is_directory and event.src_path.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
            watchdog_queue.put(event.src_path)
    def on_moved(self, event):
        if not event.is_directory and event.dest_path.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
            watchdog_queue.put(event.dest_path)

def watchdog_worker():
    global watchdog_worker_running
    while watchdog_worker_running:
        try:
            file_path = watchdog_queue.get(timeout=1)
            time.sleep(0.3)
            if not check_exif(file_path):
                fix_file(file_path, overwrite=False, dry_run=False)
        except queue.Empty:
            continue
        except Exception as e:
            app.logger.error(f"Watchdog worker error: {e}")

observer = None

def start_watchdog():
    global observer, watchdog_worker_running
    if observer is not None:
        return
    watchdog_worker_running = True
    threading.Thread(target=watchdog_worker, daemon=True).start()
    event_handler = RateLimitedHandler()
    observer = Observer()
    for source in WATCH_SOURCES:
        src_path = source.strip()
        if os.path.exists(src_path):
            observer.schedule(event_handler, src_path, recursive=True)
    observer.start()
    with open(PID_FILE, 'w') as f:
        f.write(str(os.getpid()))

def stop_watchdog():
    global observer, watchdog_worker_running
    if observer is not None:
        observer.stop()
        observer.join()
        observer = None
    watchdog_worker_running = False
    if os.path.exists(PID_FILE):
        os.remove(PID_FILE)

@app.route('/')
def index():
    return render_template('index.html')

@app.route('/api/sources')
def api_sources():
    sources = [os.path.basename(p.rstrip('/')) for p in WATCH_SOURCES]
    return jsonify(sources)

@app.route('/api/tree/<int:source_idx>')
def api_tree(source_idx):
    if source_idx >= len(WATCH_SOURCES):
        return jsonify([])
    root = WATCH_SOURCES[source_idx].strip()
    tree = get_tree(root)
    return jsonify(tree)

@app.route('/api/browse')
def api_browse():
    source_idx = int(request.args.get('source', 0))
    subpath = request.args.get('path', '')
    limit = int(request.args.get('limit', 50))
    offset = int(request.args.get('offset', 0))
    missing_only = request.args.get('missing_only', 'false').lower() == 'true'
    if source_idx >= len(WATCH_SOURCES):
        return jsonify({'error': 'Invalid source'}), 400
    root = WATCH_SOURCES[source_idx].strip()
    full_path = os.path.join(root, subpath) if subpath else root
    if not os.path.exists(full_path) or not os.path.isdir(full_path):
        return jsonify({'error': 'Invalid path'}), 400
    files, total = get_files_in_dir(full_path, limit, offset, missing_only)
    for f in files:
        f['estimated'] = extract_date_from_filename(f['path'])
    return jsonify({'files': files, 'total': total})

@app.route('/api/fix', methods=['POST'])
def api_fix():
    data = request.json
    file_path = data.get('file')
    overwrite = data.get('overwrite', False)
    dry_run = data.get('dry_run', False)
    if not file_path or not os.path.exists(file_path):
        return jsonify({'error': 'File not found'}), 400
    result = fix_file(file_path, overwrite, dry_run)
    return jsonify(result)

@app.route('/api/batch_fix', methods=['POST'])
def api_batch_fix():
    data = request.json
    files = data.get('files', [])
    overwrite = data.get('overwrite', False)
    results = []
    for f in files:
        res = fix_file(f, overwrite, dry_run=False)
        results.append({'file': f, 'result': res})
    return jsonify({'results': results})

@app.route('/api/refresh_cache', methods=['POST'])
def api_refresh_cache():
    load_cache()
    return jsonify({'success': True})

@app.route('/api/watchdog_status', methods=['GET'])
def api_watchdog_status():
    running = observer is not None and observer.is_alive()
    return jsonify({'running': running})

@app.route('/api/start_watchdog', methods=['POST'])
def api_start_watchdog():
    start_watchdog()
    return jsonify({'success': True})

@app.route('/api/stop_watchdog', methods=['POST'])
def api_stop_watchdog():
    stop_watchdog()
    return jsonify({'success': True})

if __name__ == '__main__':
    load_cache()
    app.run(host='0.0.0.0', port=5000, debug=False)
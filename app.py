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

WATCH_SOURCES = os.environ.get('WATCH_DIRS', '/home/user').split(',')
DATA_DIR = os.environ.get('DATA_PATH', '/data/cache')
STATE_DIR = '/data/state'
CACHE_FILE = os.path.join(DATA_DIR, 'exif_cache.json')
os.makedirs(DATA_DIR, exist_ok=True)
os.makedirs(STATE_DIR, exist_ok=True)

exif_cache = {}
cache_lock = threading.Lock()

# Task manager for progress polling
tasks = {}
tasks_lock = threading.Lock()
task_counter = 0

# Watchdog per source
observers = {}
watchdog_queues = {}
watchdog_workers = {}
watchdog_states = {}  # source_idx -> bool (enabled)

def load_watchdog_states():
    """Betölti a watchdog állapotokat a fájlokból."""
    global watchdog_states
    for i in range(len(WATCH_SOURCES)):
        state_file = os.path.join(STATE_DIR, f'watchdog_{i}.state')
        if os.path.exists(state_file):
            with open(state_file, 'r') as f:
                watchdog_states[i] = f.read().strip().lower() == 'true'
        else:
            watchdog_states[i] = False

def save_watchdog_state(source_idx):
    """Ment egy watchdog állapotot fájlba."""
    state_file = os.path.join(STATE_DIR, f'watchdog_{source_idx}.state')
    with open(state_file, 'w') as f:
        f.write('true' if watchdog_states.get(source_idx, False) else 'false')

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
        return has, result.stdout.strip()
    except Exception:
        with cache_lock:
            exif_cache[file_path] = False
        return False, None

def get_tree(root_path):
    tree = []
    try:
        for entry in os.scandir(root_path):
            if entry.is_dir() and not entry.name.startswith('.'):
                tree.append(entry.name)
    except Exception:
        pass
    return sorted(tree)

def get_files_in_dir(dir_path, limit=20, offset=0, missing_only=False):
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
                _, exif_date = check_exif(full)
                all_files.append({
                    'name': entry.name,
                    'path': full,
                    'has_exif': has_exif,
                    'exif_date': exif_date if exif_date else None
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
        has, _ = check_exif(file_path)
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

def batch_fix_task(task_id, files, overwrite):
    """Háttérszál a batch fix feldolgozására."""
    total = len(files)
    with tasks_lock:
        tasks[task_id] = {'total': total, 'processed': 0, 'status': 'running'}
    for i, file_path in enumerate(files):
        if os.path.exists(file_path):
            fix_file(file_path, overwrite=overwrite, dry_run=False)
        with tasks_lock:
            tasks[task_id]['processed'] = i + 1
    with tasks_lock:
        tasks[task_id]['status'] = 'completed'

# Watchdog worker
def watchdog_worker_func(source_idx, root_path):
    q = watchdog_queues[source_idx]
    while True:
        try:
            file_path = q.get(timeout=1)
            time.sleep(0.3)
            has, _ = check_exif(file_path)
            if not has:
                fix_file(file_path, overwrite=False)
        except queue.Empty:
            # check if should stop
            if not watchdog_states.get(source_idx, False):
                break
        except Exception as e:
            app.logger.error(f"Watchdog worker error for {root_path}: {e}")
    # clean up
    if source_idx in watchdog_queues:
        del watchdog_queues[source_idx]
    if source_idx in watchdog_workers:
        del watchdog_workers[source_idx]

class RateLimitedHandler(FileSystemEventHandler):
    def __init__(self, source_idx):
        self.source_idx = source_idx
    def on_created(self, event):
        if not event.is_directory and event.src_path.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
            if self.source_idx in watchdog_queues:
                watchdog_queues[self.source_idx].put(event.src_path)
    def on_moved(self, event):
        if not event.is_directory and event.dest_path.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
            if self.source_idx in watchdog_queues:
                watchdog_queues[self.source_idx].put(event.dest_path)

def start_watchdog_for_source(source_idx):
    if source_idx in observers and observers[source_idx] is not None:
        return True  # already running
    root_path = WATCH_SOURCES[source_idx].strip()
    if not os.path.exists(root_path):
        return False
    q = queue.Queue()
    watchdog_queues[source_idx] = q
    event_handler = RateLimitedHandler(source_idx)
    observer = Observer()
    observer.schedule(event_handler, root_path, recursive=True)
    observer.start()
    observers[source_idx] = observer
    worker_thread = threading.Thread(target=watchdog_worker_func, args=(source_idx, root_path), daemon=True)
    worker_thread.start()
    watchdog_workers[source_idx] = worker_thread
    watchdog_states[source_idx] = True
    save_watchdog_state(source_idx)
    return True

def stop_watchdog_for_source(source_idx):
    if source_idx not in observers or observers[source_idx] is None:
        return False
    # Stop observer
    observers[source_idx].stop()
    observers[source_idx].join()
    observers[source_idx] = None
    # Signal worker to stop
    watchdog_states[source_idx] = False
    # Optionally put a dummy item to wake up queue
    if source_idx in watchdog_queues:
        try:
            watchdog_queues[source_idx].put(None, timeout=0.1)
        except:
            pass
    # Wait a bit for worker to exit
    time.sleep(0.5)
    # Clean up
    if source_idx in watchdog_workers:
        del watchdog_workers[source_idx]
    if source_idx in watchdog_queues:
        del watchdog_queues[source_idx]
    save_watchdog_state(source_idx)
    return True

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
    limit = int(request.args.get('limit', 20))
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
    new_date = data.get('date')
    if new_date:
        # Manual edit: set specific date
        if not os.path.exists(file_path):
            return jsonify({'error': 'File not found'}), 400
        estimated = new_date + ' 00:00:00'
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
            return jsonify({'success': True})
        else:
            return jsonify({'error': result.stderr}), 500
    else:
        # Auto fix based on filename
        overwrite = data.get('overwrite', False)
        dry_run = data.get('dry_run', False)
        if not os.path.exists(file_path):
            return jsonify({'error': 'File not found'}), 400
        result = fix_file(file_path, overwrite, dry_run)
        return jsonify(result)

@app.route('/api/batch_fix', methods=['POST'])
def api_batch_fix():
    global task_counter
    data = request.json
    files = data.get('files', [])
    overwrite = data.get('overwrite', False)
    if not files:
        return jsonify({'error': 'No files'}), 400
    with tasks_lock:
        task_counter += 1
        task_id = task_counter
    # Start background thread
    thread = threading.Thread(target=batch_fix_task, args=(task_id, files, overwrite))
    thread.daemon = True
    thread.start()
    return jsonify({'task_id': task_id})

@app.route('/api/task/<int:task_id>/progress')
def api_task_progress(task_id):
    with tasks_lock:
        task = tasks.get(task_id)
        if not task:
            return jsonify({'error': 'Task not found'}), 404
        return jsonify({
            'total': task['total'],
            'processed': task['processed'],
            'status': task['status']
        })

@app.route('/api/refresh_cache', methods=['POST'])
def api_refresh_cache():
    load_cache()
    return jsonify({'success': True})

@app.route('/api/watchdog_status', methods=['GET'])
def api_watchdog_status():
    source_idx = int(request.args.get('source', 0))
    running = watchdog_states.get(source_idx, False)
    return jsonify({'running': running})

@app.route('/api/start_watchdog', methods=['POST'])
def api_start_watchdog():
    data = request.json
    source_idx = data.get('source', 0)
    if start_watchdog_for_source(source_idx):
        return jsonify({'success': True})
    else:
        return jsonify({'error': 'Could not start watchdog'}), 500

@app.route('/api/stop_watchdog', methods=['POST'])
def api_stop_watchdog():
    data = request.json
    source_idx = data.get('source', 0)
    if stop_watchdog_for_source(source_idx):
        return jsonify({'success': True})
    else:
        return jsonify({'error': 'Could not stop watchdog'}), 500

if __name__ == '__main__':
    load_cache()
    load_watchdog_states()
    # Auto-start watchdog for previously enabled sources
    for idx, enabled in watchdog_states.items():
        if enabled:
            start_watchdog_for_source(idx)
    app.run(host='0.0.0.0', port=5000, debug=False)
import os
import sqlite3
import time
import threading
import queue
from datetime import datetime
from flask import Flask, render_template, request, jsonify, send_file
from watchdog.observers import Observer
from watchdog.events import FileSystemEventHandler

app = Flask(__name__)

WATCH_SOURCES = os.environ.get('WATCH_DIRS', '/home/user').split(',')
DATA_DIR = os.environ.get('DATA_PATH', '/data/cache')
STATE_DIR = '/data/state'
DB_PATH = '/data/db/iverbs.db'
os.makedirs(DATA_DIR, exist_ok=True)
os.makedirs(STATE_DIR, exist_ok=True)
os.makedirs('/data/db', exist_ok=True)

def init_db():
    conn = sqlite3.connect(DB_PATH)
    c = conn.cursor()
    c.execute('''CREATE TABLE IF NOT EXISTS exif_cache
                 (file_path TEXT PRIMARY KEY, has_exif INTEGER, last_checked TIMESTAMP)''')
    conn.commit()
    conn.close()
init_db()

exif_cache = {}
cache_lock = threading.Lock()

tasks = {}
tasks_lock = threading.Lock()
task_counter = 0

observers = {}
watchdog_queues = {}
watchdog_workers = {}
watchdog_states = {}

background_refresh_running = False
background_refresh_thread = None

def load_cache_from_db():
    global exif_cache
    conn = sqlite3.connect(DB_PATH)
    c = conn.cursor()
    c.execute('SELECT file_path, has_exif FROM exif_cache')
    rows = c.fetchall()
    with cache_lock:
        exif_cache = {row[0]: bool(row[1]) for row in rows}
    conn.close()
    app.logger.info(f"Cache loaded from DB: {len(exif_cache)} entries")

def save_cache_to_db(file_path, has_exif):
    conn = sqlite3.connect(DB_PATH)
    c = conn.cursor()
    c.execute('REPLACE INTO exif_cache VALUES (?, ?, ?)',
              (file_path, 1 if has_exif else 0, datetime.now().isoformat()))
    conn.commit()
    conn.close()

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
        save_cache_to_db(file_path, has)
        return has, result.stdout.strip() if has else None
    except Exception:
        with cache_lock:
            exif_cache[file_path] = False
        save_cache_to_db(file_path, False)
        return False, None

def get_exif_from_cache(file_path):
    with cache_lock:
        return exif_cache.get(file_path, False)

def background_cache_refresh():
    global background_refresh_running
    app.logger.info("Background cache refresh started")
    visited = set()
    while background_refresh_running:
        for source_path in WATCH_SOURCES:
            source_path = source_path.strip()
            if not os.path.exists(source_path):
                continue
            for dirpath, dirnames, filenames in os.walk(source_path):
                for f in filenames:
                    if f.lower().endswith(('.jpg', '.jpeg', '.png', '.mp4', '.mov', '.avi', '.jpe', '.jfif')):
                        full = os.path.join(dirpath, f)
                        if full in visited:
                            continue
                        visited.add(full)
                        if not get_exif_from_cache(full):
                            check_exif(full)
                        time.sleep(0.1)  # Módosítva: 0.1 másodperc (régen 0.05)
        for _ in range(600):
            if not background_refresh_running:
                break
            time.sleep(1)
        visited.clear()
    app.logger.info("Background cache refresh stopped")

def start_background_refresh():
    global background_refresh_running, background_refresh_thread
    if background_refresh_running:
        return
    background_refresh_running = True
    background_refresh_thread = threading.Thread(target=background_cache_refresh, daemon=True)
    background_refresh_thread.start()

def stop_background_refresh():
    global background_refresh_running
    background_refresh_running = False
    if background_refresh_thread:
        background_refresh_thread.join(timeout=5)

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
                has_exif = get_exif_from_cache(full)
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

def fix_file(file_path, overwrite=False):
    estimated = extract_date_from_filename(file_path)
    if not estimated:
        return {'error': 'No date in filename'}
    if not overwrite:
        has = get_exif_from_cache(file_path)
        if has:
            return {'skipped': 'EXIF already exists'}
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
        save_cache_to_db(file_path, True)
        return {'success': True, 'date': estimated}
    else:
        return {'error': result.stderr}

def batch_fix_task(task_id, files, overwrite):
    total = len(files)
    with tasks_lock:
        tasks[task_id] = {'total': total, 'processed': 0, 'status': 'running'}
    for i, file_path in enumerate(files):
        if os.path.exists(file_path):
            fix_file(file_path, overwrite=overwrite)
        with tasks_lock:
            tasks[task_id]['processed'] = i + 1
    with tasks_lock:
        tasks[task_id]['status'] = 'completed'

def watchdog_worker_func(source_idx, root_path):
    q = watchdog_queues[source_idx]
    while True:
        try:
            file_path = q.get(timeout=1)
            time.sleep(0.3)
            has = get_exif_from_cache(file_path)
            if not has:
                fix_file(file_path, overwrite=False)
        except queue.Empty:
            if not watchdog_states.get(source_idx, False):
                break
        except Exception as e:
            app.logger.error(f"Watchdog worker error for {root_path}: {e}")
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
        return True
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
    observers[source_idx].stop()
    observers[source_idx].join()
    observers[source_idx] = None
    watchdog_states[source_idx] = False
    if source_idx in watchdog_queues:
        try:
            watchdog_queues[source_idx].put(None, timeout=0.1)
        except:
            pass
    time.sleep(0.5)
    if source_idx in watchdog_workers:
        del watchdog_workers[source_idx]
    if source_idx in watchdog_queues:
        del watchdog_queues[source_idx]
    save_watchdog_state(source_idx)
    return True

def load_watchdog_states():
    global watchdog_states
    for i in range(len(WATCH_SOURCES)):
        state_file = os.path.join(STATE_DIR, f'watchdog_{i}.state')
        if os.path.exists(state_file):
            with open(state_file, 'r') as f:
                watchdog_states[i] = f.read().strip().lower() == 'true'
        else:
            watchdog_states[i] = False

def save_watchdog_state(source_idx):
    state_file = os.path.join(STATE_DIR, f'watchdog_{source_idx}.state')
    with open(state_file, 'w') as f:
        f.write('true' if watchdog_states.get(source_idx, False) else 'false')

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
        rel = os.path.relpath(f['path'], start=root)
        f['rel_path'] = rel
    return jsonify({'files': files, 'total': total})

@app.route('/api/image/<int:source_idx>/<path:rel_path>')
def api_image(source_idx, rel_path):
    if source_idx >= len(WATCH_SOURCES):
        return '', 403
    root = WATCH_SOURCES[source_idx].strip()
    full = os.path.join(root, rel_path)
    if not os.path.exists(full) or not os.path.isfile(full):
        return '', 404
    return send_file(full, conditional=True)

@app.route('/api/exif', methods=['GET'])
def api_exif():
    file_path = request.args.get('file')
    if not file_path or not os.path.exists(file_path):
        return jsonify({'error': 'File not found'}), 404
    has, exif_date = check_exif(file_path)
    return jsonify({'exif_date': exif_date})

@app.route('/api/fix', methods=['POST'])
def api_fix():
    data = request.json
    file_path = data.get('file')
    new_date = data.get('date')
    if new_date:
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
            save_cache_to_db(file_path, True)
            return jsonify({'success': True})
        else:
            return jsonify({'error': result.stderr}), 500
    else:
        overwrite = data.get('overwrite', False)
        if not os.path.exists(file_path):
            return jsonify({'error': 'File not found'}), 400
        result = fix_file(file_path, overwrite)
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
    load_cache_from_db()
    load_watchdog_states()
    for idx, enabled in watchdog_states.items():
        if enabled:
            start_watchdog_for_source(idx)
    start_background_refresh()
    app.run(host='0.0.0.0', port=5000, debug=False)
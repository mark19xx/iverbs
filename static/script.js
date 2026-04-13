let currentSource = 0;
let currentPath = '';
let currentPage = 1;
let pageSize = 20;
let totalFiles = 0;
let missingOnly = false;

let watchEnabled = [];
let pollingInterval = null;
let currentTaskId = null;
let sseConnection = null;

// Watchdog állapotok betöltése – most már várjuk meg
async function loadWatchdogStates() {
    try {
        const res = await fetch('/api/watchdog_states');
        if (res.ok) {
            const states = await res.json();
            watchEnabled = states;
        } else {
            console.warn('Watchdog states endpoint returned error, using defaults');
            watchEnabled = [];
        }
    } catch (err) {
        console.warn('Could not load watchdog states, using defaults', err);
        watchEnabled = [];
    }
    // Ha nem sikerült a betöltés, inicializáljunk üres tömböt (a tabok számát majd később állítjuk be)
}

async function loadTabs() {
    try {
        const res = await fetch('/api/sources');
        if (!res.ok) throw new Error('Failed to load sources');
        const sources = await res.json();
        const tabsDiv = document.getElementById('tabs');
        if (!tabsDiv) return;
        tabsDiv.innerHTML = '';
        sources.forEach((src, idx) => {
            const tab = document.createElement('div');
            tab.className = 'tab' + (idx === currentSource ? ' active' : '');
            tab.textContent = src;
            tab.onclick = () => switchSource(idx);
            tabsDiv.appendChild(tab);
        });
        // Biztosítsuk, hogy a watchEnabled tömb mérete megegyezzen a források számával
        if (watchEnabled.length !== sources.length) {
            watchEnabled = new Array(sources.length).fill(false);
        }
        updateWatchToggleUI();
    } catch (err) {
        console.error('Error loading tabs:', err);
        const tabsDiv = document.getElementById('tabs');
        if (tabsDiv) tabsDiv.innerHTML = '<div style="color:red;">Error loading tabs</div>';
    }
}

function switchSource(idx) {
    if (idx === currentSource) return;
    currentSource = idx;
    currentPath = '';
    currentPage = 1;
    loadTree();
    loadFiles();
    highlightActiveTab();
    updateWatchToggleUI();
}

function highlightActiveTab() {
    const tabs = document.querySelectorAll('.tab');
    tabs.forEach((tab, i) => {
        if (i === currentSource) tab.classList.add('active');
        else tab.classList.remove('active');
    });
}

async function loadTree() {
    try {
        const res = await fetch(`/api/tree/${currentSource}`);
        if (!res.ok) throw new Error('Tree API error');
        const dirs = await res.json();
        const treeDiv = document.getElementById('tree');
        if (!treeDiv) return;
        treeDiv.innerHTML = '';
        dirs.forEach(dir => {
            const div = document.createElement('div');
            div.className = 'tree-item';
            if (currentPath === dir) div.classList.add('selected');
            div.textContent = `📁 ${dir}`;
            div.onclick = () => {
                currentPath = dir;
                currentPage = 1;
                loadFiles();
                highlightSelectedTreeItem();
            };
            treeDiv.appendChild(div);
        });
        const rootDiv = document.createElement('div');
        rootDiv.className = 'tree-item';
        if (currentPath === '') rootDiv.classList.add('selected');
        rootDiv.textContent = '📁 ..';
        rootDiv.onclick = () => {
            currentPath = '';
            currentPage = 1;
            loadFiles();
            highlightSelectedTreeItem();
        };
        treeDiv.prepend(rootDiv);
        highlightSelectedTreeItem();
    } catch (err) {
        console.error('Error loading tree:', err);
        const treeDiv = document.getElementById('tree');
        if (treeDiv) treeDiv.innerHTML = '<div style="color:red;">Error loading folders</div>';
    }
}

function highlightSelectedTreeItem() {
    const items = document.querySelectorAll('.tree-item');
    items.forEach(item => {
        if (item.textContent.includes(currentPath) && currentPath !== '') {
            if (item.textContent === `📁 ${currentPath}`) item.classList.add('selected');
            else item.classList.remove('selected');
        } else if (currentPath === '' && item.textContent === '📁 ..') {
            item.classList.add('selected');
        } else {
            item.classList.remove('selected');
        }
    });
}

async function loadFiles() {
    try {
        const url = `/api/browse?source=${currentSource}&path=${encodeURIComponent(currentPath)}&limit=${pageSize}&offset=${(currentPage-1)*pageSize}&missing_only=${missingOnly}`;
        const res = await fetch(url);
        if (!res.ok) throw new Error('Browse API error');
        const data = await res.json();
        totalFiles = data.total;
        const files = data.files;
        const tbody = document.querySelector('#fileTable tbody');
        if (!tbody) return;
        tbody.innerHTML = '';
        files.forEach(f => {
            const row = tbody.insertRow();
            const nameCell = row.insertCell(0);
            const nameLink = document.createElement('a');
            nameLink.href = `/api/image/${currentSource}/${encodeURIComponent(f.rel_path)}`;
            nameLink.target = '_blank';
            nameLink.textContent = f.name;
            nameLink.style.color = '#20c997';
            nameLink.style.textDecoration = 'none';
            nameCell.appendChild(nameLink);
            row.insertCell(1).textContent = f.estimated || '?';
            const exifStatus = f.has_exif ? '✓' : '✗';
            row.insertCell(2).textContent = exifStatus;
            const actionCell = row.insertCell(3);
            const fixBtn = document.createElement('button');
            fixBtn.textContent = 'Fix';
            fixBtn.className = 'btn-small';
            fixBtn.onclick = () => fixSingleFile(f.path);
            actionCell.appendChild(fixBtn);
            const editBtn = document.createElement('button');
            editBtn.textContent = 'Edit';
            editBtn.className = 'btn-small';
            editBtn.style.marginLeft = '5px';
            editBtn.onclick = () => editFileDate(f.path);
            actionCell.appendChild(editBtn);
        });
        renderPagination();
    } catch (err) {
        console.error('Error loading files:', err);
        const tbody = document.querySelector('#fileTable tbody');
        if (tbody) tbody.innerHTML = '<tr><td colspan="4">Error loading files</td><tr>';
    }
}

async function editFileDate(filePath) {
    try {
        const exifRes = await fetch(`/api/exif?file=${encodeURIComponent(filePath)}`);
        const exifData = await exifRes.json();
        const currentExif = exifData.exif_date ? exifData.exif_date.split(' ')[0] : '';
        const newDate = prompt(`Enter date (YYYY-MM-DD) for ${filePath.split('/').pop()}`, currentExif);
        if (!newDate) return;
        const res = await fetch('/api/fix', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({file: filePath, date: newDate})
        });
        const data = await res.json();
        if (data.success) {
            alert('Date updated!');
            loadFiles();
        } else {
            alert('Error: ' + (data.error || 'Unknown'));
        }
    } catch (err) {
        alert('Error: ' + err.message);
    }
}

async function fixSingleFile(filePath) {
    try {
        const res = await fetch('/api/fix', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({file: filePath, overwrite: false})
        });
        const data = await res.json();
        if (data.success) {
            alert('Fixed!');
            loadFiles();
        } else {
            alert('Error: ' + (data.error || 'Unknown'));
        }
    } catch (err) {
        alert('Error: ' + err.message);
    }
}

async function startBatchFix() {
    try {
        const url = `/api/browse?source=${currentSource}&path=${encodeURIComponent(currentPath)}&limit=${pageSize}&offset=${(currentPage-1)*pageSize}&missing_only=${missingOnly}`;
        const res = await fetch(url);
        if (!res.ok) throw new Error('Failed to fetch files');
        const data = await res.json();
        const files = data.files.map(f => f.path);
        if (files.length === 0) {
            alert('No files to process.');
            return;
        }
        const overwrite = !missingOnly;
        const batchRes = await fetch('/api/batch_fix', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({files: files, overwrite: overwrite})
        });
        const batchData = await batchRes.json();
        if (batchData.task_id) {
            currentTaskId = batchData.task_id;
            const writeBtn = document.getElementById('autoFixBtn');
            if (writeBtn) writeBtn.disabled = true;
            if (pollingInterval) clearInterval(pollingInterval);
            pollingInterval = setInterval(async () => {
                try {
                    const progRes = await fetch(`/api/task/${currentTaskId}/progress`);
                    if (!progRes.ok) throw new Error('Progress error');
                    const prog = await progRes.json();
                    const percent = Math.round((prog.processed / prog.total) * 100);
                    if (writeBtn) writeBtn.textContent = `${percent}%`;
                    if (prog.status === 'completed') {
                        clearInterval(pollingInterval);
                        if (writeBtn) {
                            writeBtn.disabled = false;
                            writeBtn.textContent = 'Write';
                        }
                        currentTaskId = null;
                        loadFiles();
                        alert('Batch write completed.');
                    }
                } catch (err) {
                    clearInterval(pollingInterval);
                    if (writeBtn) {
                        writeBtn.disabled = false;
                        writeBtn.textContent = 'Write';
                    }
                    currentTaskId = null;
                    console.error('Progress polling error', err);
                }
            }, 1000);
        } else {
            alert('Failed to start batch fix.');
        }
    } catch (err) {
        alert('Error: ' + err.message);
    }
}

function renderPagination() {
    const totalPages = Math.ceil(totalFiles / pageSize);
    const paginationDiv = document.getElementById('pagination');
    if (!paginationDiv) return;
    paginationDiv.innerHTML = '';

    if (totalPages === 0) return;

    function addButton(label, page, isActive = false, disabled = false) {
        const btn = document.createElement('button');
        btn.textContent = label;
        if (disabled) btn.disabled = true;
        if (isActive) btn.classList.add('active');
        btn.onclick = () => {
            if (page !== currentPage) {
                currentPage = page;
                loadFiles();
            }
        };
        paginationDiv.appendChild(btn);
    }

    addButton('<<', 1, false, currentPage === 1);
    addButton('<', currentPage - 1, false, currentPage === 1);

    let pages = [];
    if (totalPages <= 5) {
        for (let i = 1; i <= totalPages; i++) pages.push(i);
    } else {
        pages.push(1);
        if (currentPage > 3) pages.push('...');
        for (let i = Math.max(2, currentPage - 1); i <= Math.min(totalPages - 1, currentPage + 1); i++) {
            if (!pages.includes(i)) pages.push(i);
        }
        if (currentPage < totalPages - 2) pages.push('...');
        pages.push(totalPages);
    }

    for (let p of pages) {
        if (p === '...') {
            const span = document.createElement('span');
            span.textContent = '...';
            span.style.margin = '0 4px';
            span.style.color = '#888';
            paginationDiv.appendChild(span);
        } else {
            addButton(p.toString(), p, p === currentPage, false);
        }
    }

    addButton('>', currentPage + 1, false, currentPage === totalPages);
    addButton('>>', totalPages, false, currentPage === totalPages);
}

function updateWatchToggleUI() {
    const toggle = document.getElementById('watchToggle');
    if (toggle && watchEnabled.length > currentSource) {
        toggle.checked = watchEnabled[currentSource] || false;
    }
}

async function toggleWatchdog() {
    const toggle = document.getElementById('watchToggle');
    if (!toggle) return;
    const newState = toggle.checked;
    const endpoint = newState ? '/api/start_watchdog' : '/api/stop_watchdog';
    try {
        const res = await fetch(endpoint, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({source: currentSource})
        });
        const data = await res.json();
        if (!data.success) throw new Error(data.error || 'Failed');
        watchEnabled[currentSource] = newState;
    } catch (err) {
        alert('Failed to toggle watchdog: ' + err.message);
        toggle.checked = !newState;
    }
}

function connectSSE() {
    if (sseConnection) {
        sseConnection.close();
    }
    try {
        sseConnection = new EventSource('/api/watchdog/events');
        sseConnection.onmessage = (event) => {
            try {
                const data = JSON.parse(event.data);
                if (data.source >= 0 && data.source < watchEnabled.length) {
                    watchEnabled[data.source] = data.running;
                    if (data.source === currentSource) {
                        const toggle = document.getElementById('watchToggle');
                        if (toggle && toggle.checked !== data.running) {
                            toggle.checked = data.running;
                        }
                    }
                }
            } catch (e) {
                console.error('SSE parse error', e);
            }
        };
        sseConnection.onerror = (err) => {
            console.error('SSE error, reconnecting in 5s', err);
            if (sseConnection) sseConnection.close();
            setTimeout(connectSSE, 5000);
        };
    } catch (err) {
        console.error('SSE init error', err);
        setTimeout(connectSSE, 5000);
    }
}

window.addEventListener('beforeunload', () => {
    if (sseConnection) sseConnection.close();
});

document.getElementById('missingOnly')?.addEventListener('change', (e) => {
    missingOnly = e.target.checked;
    currentPage = 1;
    loadFiles();
});
document.getElementById('autoFixBtn')?.addEventListener('click', startBatchFix);
document.getElementById('watchToggle')?.addEventListener('change', toggleWatchdog);

// --- Inicializálás: watchdog állapotok betöltése, majd tabok ---
(async function init() {
    await loadWatchdogStates();   // először betöltjük a watchdog állapotokat
    await loadTabs();             // majd a tabokat (a watchEnabled már feltöltődött)
    loadTree();
    loadFiles();
    connectSSE();
})();
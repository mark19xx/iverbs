let currentSource = 0;
let currentPath = '';
let currentPage = 1;
let pageSize = 20;
let totalFiles = 0;
let missingOnly = false;

// Watch állapotok tabonként
let watchEnabled = [];
let pollingInterval = null;
let currentTaskId = null;

async function loadTabs() {
    const res = await fetch('/api/sources');
    const sources = await res.json();
    const tabsDiv = document.getElementById('tabs');
    tabsDiv.innerHTML = '';
    sources.forEach((src, idx) => {
        const tab = document.createElement('div');
        tab.className = 'tab' + (idx === currentSource ? ' active' : '');
        tab.textContent = src;
        tab.onclick = () => switchSource(idx);
        tabsDiv.appendChild(tab);
    });
    watchEnabled = new Array(sources.length).fill(false);
    updateWatchToggleUI();
}

function switchSource(idx) {
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
    const res = await fetch(`/api/tree/${currentSource}`);
    const dirs = await res.json();
    const treeDiv = document.getElementById('tree');
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
    const url = `/api/browse?source=${currentSource}&path=${encodeURIComponent(currentPath)}&limit=${pageSize}&offset=${(currentPage-1)*pageSize}&missing_only=${missingOnly}`;
    const res = await fetch(url);
    const data = await res.json();
    totalFiles = data.total;
    const files = data.files;
    const tbody = document.querySelector('#fileTable tbody');
    tbody.innerHTML = '';
    files.forEach(f => {
        const row = tbody.insertRow();
        row.insertCell(0).textContent = f.name;
        row.insertCell(1).textContent = f.estimated || '?';
        const exifStatus = f.has_exif ? '✓' : '✗';
        row.insertCell(2).textContent = exifStatus;
        const actionCell = row.insertCell(3);
        const estimatedDate = f.estimated ? f.estimated.split(' ')[0] : null;
        const exifDate = f.exif_date ? f.exif_date.split(' ')[0] : null;
        const isMatch = (f.has_exif && estimatedDate && exifDate && estimatedDate === exifDate);
        if (isMatch) {
            const editBtn = document.createElement('button');
            editBtn.textContent = 'Edit';
            editBtn.className = 'btn-small';
            editBtn.onclick = () => editFileDate(f.path, f.estimated);
            actionCell.appendChild(editBtn);
        } else {
            const fixBtn = document.createElement('button');
            fixBtn.textContent = 'Fix';
            fixBtn.className = 'btn-small';
            fixBtn.onclick = () => fixSingleFile(f.path);
            actionCell.appendChild(fixBtn);
        }
    });
    renderPagination();
}

async function editFileDate(filePath, estimated) {
    const newDate = prompt(`Enter date (YYYY-MM-DD) for ${filePath.split('/').pop()}`, estimated ? estimated.split(' ')[0] : '');
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
}

async function fixSingleFile(filePath) {
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
}

async function startBatchFix() {
    const url = `/api/browse?source=${currentSource}&path=${encodeURIComponent(currentPath)}&limit=${pageSize}&offset=${(currentPage-1)*pageSize}&missing_only=${missingOnly}`;
    const res = await fetch(url);
    const data = await res.json();
    const files = data.files.map(f => f.path);
    if (files.length === 0) {
        alert('No files to process.');
        return;
    }
    const overwrite = !missingOnly;  // if missingOnly is false, overwrite true
    const batchRes = await fetch('/api/batch_fix', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({files: files, overwrite: overwrite})
    });
    const batchData = await batchRes.json();
    if (batchData.task_id) {
        currentTaskId = batchData.task_id;
        const writeBtn = document.getElementById('autoFixBtn');
        writeBtn.disabled = true;
        // Start polling
        if (pollingInterval) clearInterval(pollingInterval);
        pollingInterval = setInterval(async () => {
            const progRes = await fetch(`/api/task/${currentTaskId}/progress`);
            if (!progRes.ok) {
                clearInterval(pollingInterval);
                writeBtn.disabled = false;
                writeBtn.textContent = 'Write';
                currentTaskId = null;
                loadFiles();
                return;
            }
            const prog = await progRes.json();
            const percent = Math.round((prog.processed / prog.total) * 100);
            writeBtn.textContent = `${percent}%`;
            if (prog.status === 'completed') {
                clearInterval(pollingInterval);
                writeBtn.disabled = false;
                writeBtn.textContent = 'Write';
                currentTaskId = null;
                loadFiles();
                alert('Batch write completed.');
            }
        }, 1000);
    } else {
        alert('Failed to start batch fix.');
    }
}

function renderPagination() {
    const totalPages = Math.ceil(totalFiles / pageSize);
    const paginationDiv = document.getElementById('pagination');
    paginationDiv.innerHTML = '';
    const first = document.createElement('button');
    first.textContent = '<<';
    first.disabled = (currentPage === 1);
    first.onclick = () => { if (currentPage > 1) { currentPage = 1; loadFiles(); } };
    paginationDiv.appendChild(first);
    const prev = document.createElement('button');
    prev.textContent = '<';
    prev.disabled = (currentPage === 1);
    prev.onclick = () => { if (currentPage > 1) { currentPage--; loadFiles(); } };
    paginationDiv.appendChild(prev);
    for (let i = 1; i <= totalPages; i++) {
        const btn = document.createElement('button');
        btn.textContent = i;
        if (i === currentPage) btn.classList.add('active');
        btn.onclick = () => { currentPage = i; loadFiles(); };
        paginationDiv.appendChild(btn);
    }
    const next = document.createElement('button');
    next.textContent = '>';
    next.disabled = (currentPage === totalPages);
    next.onclick = () => { if (currentPage < totalPages) { currentPage++; loadFiles(); } };
    paginationDiv.appendChild(next);
    const last = document.createElement('button');
    last.textContent = '>>';
    last.disabled = (currentPage === totalPages);
    last.onclick = () => { if (currentPage < totalPages) { currentPage = totalPages; loadFiles(); } };
    paginationDiv.appendChild(last);
}

async function updateWatchdogStatus() {
    const res = await fetch(`/api/watchdog_status?source=${currentSource}`);
    const data = await res.json();
    const toggle = document.getElementById('watchToggle');
    toggle.checked = data.running;
    watchEnabled[currentSource] = data.running;
}

async function toggleWatchdog() {
    const toggle = document.getElementById('watchToggle');
    const newState = toggle.checked;
    const endpoint = newState ? '/api/start_watchdog' : '/api/stop_watchdog';
    const res = await fetch(endpoint, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({source: currentSource})
    });
    const data = await res.json();
    if (!data.success) {
        alert('Failed to toggle watchdog');
        toggle.checked = !newState;
    } else {
        watchEnabled[currentSource] = newState;
    }
}

function updateWatchToggleUI() {
    const toggle = document.getElementById('watchToggle');
    if (toggle) {
        toggle.checked = watchEnabled[currentSource] || false;
    }
}

document.getElementById('missingOnly').addEventListener('change', (e) => {
    missingOnly = e.target.checked;
    currentPage = 1;
    loadFiles();
});
document.getElementById('autoFixBtn').addEventListener('click', startBatchFix);
document.getElementById('refreshCache').addEventListener('click', async () => {
    await fetch('/api/refresh_cache', {method: 'POST'});
    alert('Cache refreshed');
    loadFiles();
});
document.getElementById('watchToggle').addEventListener('change', toggleWatchdog);

loadTabs();
loadTree();
loadFiles();
updateWatchdogStatus();
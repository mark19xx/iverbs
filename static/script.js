let currentSource = 0;
let currentPath = '';
let currentPage = 1;
let pageSize = 50;
let totalFiles = 0;
let missingOnly = false;
let overwrite = false;

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
}

function switchSource(idx) {
    currentSource = idx;
    currentPath = '';
    currentPage = 1;
    loadTree();
    loadFiles();
    highlightActiveTab();
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
        div.textContent = `📁 ${dir}`;
        div.onclick = () => {
            currentPath = dir;
            currentPage = 1;
            loadFiles();
        };
        treeDiv.appendChild(div);
    });
    const rootDiv = document.createElement('div');
    rootDiv.className = 'tree-item';
    rootDiv.textContent = '📁 ..';
    rootDiv.onclick = () => {
        currentPath = '';
        currentPage = 1;
        loadFiles();
    };
    treeDiv.prepend(rootDiv);
}

async function loadFiles() {
    const url = `/api/browse?source=${currentSource}&path=${encodeURIComponent(currentPath)}&limit=${pageSize}&offset=${(currentPage-1)*pageSize}&missing_only=${missingOnly}`;
    const res = await fetch(url);
    const data = await res.json();
    totalFiles = data.total;
    const files = data.files;
    const tbody = document.getElementById('fileTable');
    tbody.innerHTML = '';
    files.forEach(f => {
        const row = tbody.insertRow();
        row.insertCell(0).textContent = f.name;
        row.insertCell(1).textContent = f.estimated || '?';
        row.insertCell(2).textContent = f.has_exif ? '✓' : '✗';
        const btn = document.createElement('button');
        btn.textContent = 'Fix';
        btn.className = 'btn-small';
        btn.onclick = () => fixSingleFile(f.path);
        const cell = row.insertCell(3);
        cell.appendChild(btn);
    });
    renderPagination();
}

function renderPagination() {
    const totalPages = Math.ceil(totalFiles / pageSize);
    const paginationDiv = document.getElementById('pagination');
    paginationDiv.innerHTML = '';
    if (totalPages <= 1) return;
    const first = document.createElement('button');
    first.textContent = '<<';
    first.onclick = () => { if (currentPage > 1) { currentPage = 1; loadFiles(); } };
    paginationDiv.appendChild(first);
    const prev = document.createElement('button');
    prev.textContent = '<';
    prev.onclick = () => { if (currentPage > 1) { currentPage--; loadFiles(); } };
    paginationDiv.appendChild(prev);
    for (let i = Math.max(1, currentPage-2); i <= Math.min(totalPages, currentPage+2); i++) {
        const btn = document.createElement('button');
        btn.textContent = i;
        if (i === currentPage) btn.classList.add('active');
        btn.onclick = () => { currentPage = i; loadFiles(); };
        paginationDiv.appendChild(btn);
    }
    const next = document.createElement('button');
    next.textContent = '>';
    next.onclick = () => { if (currentPage < totalPages) { currentPage++; loadFiles(); } };
    paginationDiv.appendChild(next);
    const last = document.createElement('button');
    last.textContent = '>>';
    last.onclick = () => { if (currentPage < totalPages) { currentPage = totalPages; loadFiles(); } };
    paginationDiv.appendChild(last);
}

async function fixSingleFile(filePath) {
    const res = await fetch('/api/fix', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({file: filePath, overwrite: overwrite})
    });
    const data = await res.json();
    logToConsole(data);
    loadFiles();
}

async function batchFix() {
    const url = `/api/browse?source=${currentSource}&path=${encodeURIComponent(currentPath)}&limit=${pageSize}&offset=${(currentPage-1)*pageSize}&missing_only=${missingOnly}`;
    const res = await fetch(url);
    const data = await res.json();
    const files = data.files.map(f => f.path);
    if (files.length === 0) return;
    const fixRes = await fetch('/api/batch_fix', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({files: files, overwrite: overwrite})
    });
    const result = await fixRes.json();
    logToConsole(result);
    loadFiles();
}

function logToConsole(obj) {
    const pre = document.getElementById('consoleOutput');
    pre.textContent = JSON.stringify(obj, null, 2) + '\n' + pre.textContent;
}

document.getElementById('missingOnly').addEventListener('change', (e) => {
    missingOnly = e.target.checked;
    currentPage = 1;
    loadFiles();
});
document.getElementById('overwriteFix').addEventListener('change', (e) => {
    overwrite = e.target.checked;
});
document.getElementById('autoFixBtn').addEventListener('click', batchFix);
document.getElementById('clearConsole').addEventListener('click', () => {
    document.getElementById('consoleOutput').textContent = '';
});
document.getElementById('startWatchdog').addEventListener('click', async () => {
    const res = await fetch('/api/start_watchdog', {method: 'POST'});
    const data = await res.json();
    logToConsole(data);
});
document.getElementById('stopWatchdog').addEventListener('click', async () => {
    const res = await fetch('/api/stop_watchdog', {method: 'POST'});
    const data = await res.json();
    logToConsole(data);
});
document.getElementById('refreshCache').addEventListener('click', async () => {
    const res = await fetch('/api/refresh_cache', {method: 'POST'});
    const data = await res.json();
    logToConsole(data);
    loadFiles();
});

loadTabs();
loadTree();
loadFiles();
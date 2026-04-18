let currentSource = 0;
let currentPath = '';
let currentPage = 1;
let pageSize = 20;
let totalFiles = 0;
let missingOnly = false;

let pollingInterval = null;
let currentTaskId = null;

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
    } catch (err) {
        console.error('Error loading tabs:', err);
        const tabsDiv = document.getElementById('tabs');
        if (tabsDiv) tabsDiv.innerHTML = '';
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
        let url = `/api/tree/${currentSource}`;
        if (currentPath) {
            url += `?path=${encodeURIComponent(currentPath)}`;
        }
        const res = await fetch(url);
        if (!res.ok) throw new Error('Tree API error');
        const dirs = await res.json();
        const treeDiv = document.getElementById('tree');
        const treeHeader = document.getElementById('treeHeader');
        if (!treeDiv) return;

        if (currentPath === '') {
            const sources = await (await fetch('/api/sources')).json();
            treeHeader.textContent = `📁 ${sources[currentSource]}`;
        } else {
            const parts = currentPath.split('/');
            treeHeader.textContent = `📁 ${parts[parts.length-1]}`;
        }

        treeDiv.innerHTML = '';

        if (dirs.length > 0) {
            dirs.forEach(dir => {
                const div = document.createElement('div');
                div.className = 'tree-item';
                div.textContent = `📁 ${dir}`;
                div.onclick = () => {
                    currentPath = currentPath ? `${currentPath}/${dir}` : dir;
                    currentPage = 1;
                    loadTree();
                    loadFiles();
                };
                treeDiv.appendChild(div);
            });
        }

        if (currentPath !== '') {
            const parentDiv = document.createElement('div');
            parentDiv.className = 'tree-item';
            parentDiv.textContent = '📁 ..';
            parentDiv.onclick = () => {
                const parts = currentPath.split('/');
                parts.pop();
                currentPath = parts.join('/');
                currentPage = 1;
                loadTree();
                loadFiles();
            };
            treeDiv.prepend(parentDiv);
        }
    } catch (err) {
        console.error('Error loading tree:', err);
        // Nem jelenítünk meg semmit a felhasználónak
        const treeDiv = document.getElementById('tree');
        if (treeDiv) treeDiv.innerHTML = '';
    }
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
        if (tbody) tbody.innerHTML = '';
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

async function startBatchFix(files, overwrite, buttonElement) {
    if (files.length === 0) {
        alert('No files to process.');
        return;
    }
    const batchRes = await fetch('/api/batch_fix', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({files: files, overwrite: overwrite})
    });
    const batchData = await batchRes.json();
    if (batchData.task_id) {
        currentTaskId = batchData.task_id;
        if (buttonElement) buttonElement.disabled = true;
        if (pollingInterval) clearInterval(pollingInterval);
        pollingInterval = setInterval(async () => {
            try {
                const progRes = await fetch(`/api/task/${currentTaskId}/progress`);
                if (!progRes.ok) throw new Error('Progress error');
                const prog = await progRes.json();
                const percent = Math.round((prog.processed / prog.total) * 100);
                if (buttonElement) buttonElement.textContent = `${percent}%`;
                if (prog.status === 'completed') {
                    clearInterval(pollingInterval);
                    if (buttonElement) {
                        buttonElement.disabled = false;
                        buttonElement.textContent = buttonElement.id === 'fixPageBtn' ? 'Fix Page' : (buttonElement.id === 'fixFolderBtn' ? 'Fix Folder' : 'Fix All');
                    }
                    currentTaskId = null;
                    loadFiles();
                    alert('Batch fix completed.');
                }
            } catch (err) {
                clearInterval(pollingInterval);
                if (buttonElement) {
                    buttonElement.disabled = false;
                    buttonElement.textContent = buttonElement.id === 'fixPageBtn' ? 'Fix Page' : (buttonElement.id === 'fixFolderBtn' ? 'Fix Folder' : 'Fix All');
                }
                currentTaskId = null;
                console.error('Progress polling error', err);
            }
        }, 1000);
    } else {
        alert('Failed to start batch fix.');
    }
}

async function fixPage() {
    const url = `/api/browse?source=${currentSource}&path=${encodeURIComponent(currentPath)}&limit=${pageSize}&offset=${(currentPage-1)*pageSize}&missing_only=${missingOnly}`;
    const res = await fetch(url);
    const data = await res.json();
    const files = data.files.map(f => f.path);
    const overwrite = !missingOnly;
    const btn = document.getElementById('fixPageBtn');
    startBatchFix(files, overwrite, btn);
}

async function fixFolder() {
    if (currentPath === '') {
        alert('Please select a folder first.');
        return;
    }
    const sources = await (await fetch('/api/sources')).json();
    const root = sources[currentSource];
    const fullPath = root + '/' + currentPath;
    const res = await fetch('/api/batch_fix_path', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({path: fullPath, overwrite: !missingOnly})
    });
    const data = await res.json();
    if (data.task_id && data.task_id !== -1) {
        currentTaskId = data.task_id;
        const btn = document.getElementById('fixFolderBtn');
        btn.disabled = true;
        if (pollingInterval) clearInterval(pollingInterval);
        pollingInterval = setInterval(async () => {
            try {
                const progRes = await fetch(`/api/task/${currentTaskId}/progress`);
                if (!progRes.ok) throw new Error('Progress error');
                const prog = await progRes.json();
                const percent = Math.round((prog.processed / prog.total) * 100);
                btn.textContent = `${percent}%`;
                if (prog.status === 'completed') {
                    clearInterval(pollingInterval);
                    btn.disabled = false;
                    btn.textContent = 'Fix Folder';
                    currentTaskId = null;
                    loadFiles();
                    alert('Folder fix completed.');
                }
            } catch (err) {
                clearInterval(pollingInterval);
                btn.disabled = false;
                btn.textContent = 'Fix Folder';
                currentTaskId = null;
                console.error('Progress polling error', err);
            }
        }, 1000);
    } else if (data.message) {
        alert(data.message);
    } else {
        alert('Failed to start folder fix.');
    }
}

async function fixAll() {
    const sources = await (await fetch('/api/sources')).json();
    const root = sources[currentSource];
    const res = await fetch('/api/batch_fix_path', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({path: root, overwrite: !missingOnly})
    });
    const data = await res.json();
    if (data.task_id && data.task_id !== -1) {
        currentTaskId = data.task_id;
        const btn = document.getElementById('fixAllBtn');
        btn.disabled = true;
        if (pollingInterval) clearInterval(pollingInterval);
        pollingInterval = setInterval(async () => {
            try {
                const progRes = await fetch(`/api/task/${currentTaskId}/progress`);
                if (!progRes.ok) throw new Error('Progress error');
                const prog = await progRes.json();
                const percent = Math.round((prog.processed / prog.total) * 100);
                btn.textContent = `${percent}%`;
                if (prog.status === 'completed') {
                    clearInterval(pollingInterval);
                    btn.disabled = false;
                    btn.textContent = 'Fix All';
                    currentTaskId = null;
                    loadFiles();
                    alert('Full fix completed.');
                }
            } catch (err) {
                clearInterval(pollingInterval);
                btn.disabled = false;
                btn.textContent = 'Fix All';
                currentTaskId = null;
                console.error('Progress polling error', err);
            }
        }, 1000);
    } else if (data.message) {
        alert(data.message);
    } else {
        alert('Failed to start full fix.');
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

document.getElementById('missingOnly')?.addEventListener('change', (e) => {
    missingOnly = e.target.checked;
    currentPage = 1;
    loadFiles();
});
document.getElementById('fixPageBtn')?.addEventListener('click', fixPage);
document.getElementById('fixFolderBtn')?.addEventListener('click', fixFolder);
document.getElementById('fixAllBtn')?.addEventListener('click', fixAll);

loadTabs();
loadTree();
loadFiles();
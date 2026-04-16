(function() {
    const API_BASE = '/api';

    // Template adatok kiolvasása meta tag-ekből
    const versionMeta = document.querySelector('meta[name="iverbs-version"]');
    const sourcesMeta = document.querySelector('meta[name="iverbs-sources"]');
    
    const VERSION = versionMeta ? versionMeta.content : '0.3.2';
    let sources = [];
    try {
        sources = JSON.parse(sourcesMeta.content);
    } catch(e) {
        console.error('Invalid sources JSON', e);
    }

    // Opcionális: verzió kiírása a konzolra
    console.log(`IVERBS ${VERSION} loaded`);

    let currentSource = 0;           // index
    let currentPath = '';            // relatív útvonal a source gyökerétől
    let currentPage = 1;
    const limit = 20;
    let missingOnly = false;

    // DOM elemek
    const tabsEl = document.getElementById('tabs');
    const folderTreeEl = document.getElementById('folder-tree');
    const fileListBody = document.getElementById('file-list-body');
    const paginationEl = document.getElementById('pagination');
    const missingOnlyCheck = document.getElementById('missing-only');
    const fixPageBtn = document.getElementById('fix-page-btn');
    const fixFolderBtn = document.getElementById('fix-folder-btn');
    const fixAllBtn = document.getElementById('fix-all-btn');

    // Állapot
    let activeFolder = ''; // a kiválasztott mappa (relatív út) a sidebar-on

    // Inicializálás
    function init() {
        renderTabs();
        if (sources.length > 0) {
            selectSource(0);
        }
        missingOnlyCheck.addEventListener('change', () => {
            missingOnly = missingOnlyCheck.checked;
            currentPage = 1;
            loadFiles();
        });
        fixPageBtn.addEventListener('click', onFixPage);
        fixFolderBtn.addEventListener('click', onFixFolder);
        fixAllBtn.addEventListener('click', onFixAll);
    }

    function renderTabs() {
        tabsEl.innerHTML = '';
        sources.forEach((name, idx) => {
            const tab = document.createElement('div');
            tab.className = 'tab' + (idx === currentSource ? ' active' : '');
            tab.textContent = name;
            tab.dataset.index = idx;
            tab.addEventListener('click', () => selectSource(idx));
            tabsEl.appendChild(tab);
        });
    }

    function selectSource(idx) {
        currentSource = idx;
        currentPath = '';
        activeFolder = '';
        currentPage = 1;
        renderTabs();
        loadFolderTree();
        loadFiles();
    }

    async function loadFolderTree() {
        try {
            const resp = await fetch(`${API_BASE}/tree/${currentSource}`);
            const dirs = await resp.json();
            renderFolderTree(dirs);
        } catch (err) {
            console.error('Failed to load folder tree', err);
        }
    }

    function renderFolderTree(dirs) {
        folderTreeEl.innerHTML = '';
        // Gyökér elem (opcionális)
        const rootLi = document.createElement('li');
        rootLi.textContent = '. (root)';
        rootLi.dataset.path = '';
        rootLi.addEventListener('click', () => selectFolder(''));
        if (activeFolder === '') rootLi.classList.add('active');
        folderTreeEl.appendChild(rootLi);

        dirs.forEach(dir => {
            const li = document.createElement('li');
            li.textContent = dir;
            li.dataset.path = dir;
            li.addEventListener('click', () => selectFolder(dir));
            if (activeFolder === dir) li.classList.add('active');
            folderTreeEl.appendChild(li);
        });
    }

    function selectFolder(path) {
        activeFolder = path;
        currentPath = path;
        currentPage = 1;
        // Frissítjük az aktív stílust
        document.querySelectorAll('#folder-tree li').forEach(li => {
            li.classList.remove('active');
            if (li.dataset.path === path) li.classList.add('active');
        });
        loadFiles();
    }

    async function loadFiles() {
        const offset = (currentPage - 1) * limit;
        const url = `${API_BASE}/browse?source=${currentSource}&path=${encodeURIComponent(currentPath)}&limit=${limit}&offset=${offset}&missing_only=${missingOnly}`;
        try {
            const resp = await fetch(url);
            const data = await resp.json();
            renderFileTable(data.files);
            renderPagination(data.total);
        } catch (err) {
            console.error('Failed to load files', err);
        }
    }

    function renderFileTable(files) {
        fileListBody.innerHTML = '';
        if (files.length === 0) {
            fileListBody.innerHTML = '<tr><td colspan="4" style="text-align:center; padding:2rem;">No files found</td></tr>';
            return;
        }
        files.forEach(file => {
            const tr = document.createElement('tr');
            tr.dataset.absPath = file.abs_path || ''; // abs_path a backend-ből jön

            // Név link
            const nameTd = document.createElement('td');
            const link = document.createElement('a');
            link.href = `${API_BASE}/image/${currentSource}/${encodeURI(file.path)}`;
            link.target = '_blank';
            link.textContent = file.name;
            nameTd.appendChild(link);
            tr.appendChild(nameTd);

            // Becsült dátum
            const estTd = document.createElement('td');
            estTd.textContent = file.est_date || '-';
            tr.appendChild(estTd);

            // EXIF?
            const exifTd = document.createElement('td');
            exifTd.textContent = file.has_exif ? '✓' : '✗';
            exifTd.className = file.has_exif ? 'exif-yes' : 'exif-no';
            tr.appendChild(exifTd);

            // Műveletek
            const actionsTd = document.createElement('td');
            const fixBtn = document.createElement('button');
            fixBtn.textContent = 'Fix';
            fixBtn.className = 'btn';
            fixBtn.style.marginRight = '5px';
            fixBtn.addEventListener('click', () => fixSingle(file, false));
            const editBtn = document.createElement('button');
            editBtn.textContent = 'Edit';
            editBtn.className = 'btn';
            editBtn.addEventListener('click', () => editSingle(file));
            actionsTd.appendChild(fixBtn);
            actionsTd.appendChild(editBtn);
            tr.appendChild(actionsTd);

            fileListBody.appendChild(tr);
        });
    }

    function renderPagination(total) {
        const totalPages = Math.ceil(total / limit);
        paginationEl.innerHTML = '';

        if (totalPages <= 1) return;

        const createPageBtn = (page, text = page, disabled = false) => {
            const btn = document.createElement('button');
            btn.textContent = text;
            if (page === currentPage) btn.classList.add('active');
            btn.disabled = disabled;
            if (!disabled) {
                btn.addEventListener('click', () => {
                    currentPage = page;
                    loadFiles();
                });
            }
            return btn;
        };

        // << (első)
        paginationEl.appendChild(createPageBtn(1, '<<', currentPage === 1));
        // < (előző)
        paginationEl.appendChild(createPageBtn(currentPage - 1, '<', currentPage === 1));

        // Oldalszámok (intelligens)
        let startPage = Math.max(1, currentPage - 2);
        let endPage = Math.min(totalPages, currentPage + 2);
        if (endPage - startPage < 4) {
            if (startPage === 1) endPage = Math.min(totalPages, startPage + 4);
            else if (endPage === totalPages) startPage = Math.max(1, endPage - 4);
        }
        if (startPage > 1) {
            paginationEl.appendChild(createPageBtn(1));
            if (startPage > 2) {
                const ellipsis = document.createElement('span');
                ellipsis.textContent = '...';
                ellipsis.style.padding = '0 0.5rem';
                paginationEl.appendChild(ellipsis);
            }
        }
        for (let p = startPage; p <= endPage; p++) {
            paginationEl.appendChild(createPageBtn(p));
        }
        if (endPage < totalPages) {
            if (endPage < totalPages - 1) {
                const ellipsis = document.createElement('span');
                ellipsis.textContent = '...';
                ellipsis.style.padding = '0 0.5rem';
                paginationEl.appendChild(ellipsis);
            }
            paginationEl.appendChild(createPageBtn(totalPages));
        }

        // > (következő)
        paginationEl.appendChild(createPageBtn(currentPage + 1, '>', currentPage === totalPages));
        // >> (utolsó)
        paginationEl.appendChild(createPageBtn(totalPages, '>>', currentPage === totalPages));
    }

    async function fixSingle(file, overwrite = false) {
        try {
            const resp = await fetch(`${API_BASE}/fix`, {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({file: file.AbsPath || file.abs_path, overwrite: overwrite})
            });
            if (!resp.ok) {
                const err = await resp.text();
                alert('Fix failed: ' + err);
            } else {
                loadFiles();
            }
        } catch (err) {
            alert('Error: ' + err);
        }
    }

    async function editSingle(file) {
        let currentDate = '';
        try {
            const resp = await fetch(`${API_BASE}/exif?file=${encodeURIComponent(file.AbsPath || file.abs_path)}`);
            const data = await resp.json();
            if (data.exif_date) {
                const parts = data.exif_date.split(' ')[0].split(':');
                if (parts.length === 3) {
                    currentDate = parts.join('-');
                }
            }
        } catch (e) {}

        const newDate = prompt('Enter date (YYYY-MM-DD):', currentDate);
        if (!newDate) return;
        if (!/^\d{4}-\d{2}-\d{2}$/.test(newDate)) {
            alert('Invalid date format. Use YYYY-MM-DD.');
            return;
        }
        try {
            const resp = await fetch(`${API_BASE}/fix`, {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({file: file.AbsPath || file.abs_path, date: newDate})
            });
            if (!resp.ok) {
                const err = await resp.text();
                alert('Edit failed: ' + err);
            } else {
                loadFiles();
            }
        } catch (err) {
            alert('Error: ' + err);
        }
    }

    async function runBatchWithProgress(endpoint, body, button) {
        const originalText = button.textContent;
        button.disabled = true;
        try {
            const resp = await fetch(endpoint, {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(body)
            });
            if (!resp.ok) throw new Error(await resp.text());
            const data = await resp.json();
            const taskId = data.task_id;

            const poll = async () => {
                const progResp = await fetch(`${API_BASE}/task/${taskId}/progress`);
                const prog = await progResp.json();
                const percent = prog.total > 0 ? Math.round((prog.processed / prog.total) * 100) : 0;
                button.textContent = `${originalText} (${percent}%)`;
                if (prog.status === 'completed') {
                    button.textContent = originalText;
                    button.disabled = false;
                    loadFiles();
                } else {
                    setTimeout(poll, 1000);
                }
            };
            poll();
        } catch (err) {
            alert('Batch operation failed: ' + err);
            button.textContent = originalText;
            button.disabled = false;
        }
    }

    async function onFixPage() {
        const files = [];
        document.querySelectorAll('#file-list-body tr').forEach(row => {
            const absPath = row.dataset.absPath;
            if (absPath) files.push(absPath);
        });
        if (files.length === 0) {
            alert('No files on current page.');
            return;
        }
        runBatchWithProgress(`${API_BASE}/batch_fix`, {files: files, overwrite: !missingOnly}, fixPageBtn);
    }

    async function onFixFolder() {
        if (!activeFolder && activeFolder !== '') {
            alert('Please select a folder.');
            return;
        }
        // A batch_fix_path végpontot hívjuk, átadva a source indexet query paraméterben és a relatív utat a body-ban
        const resp = await fetch(`${API_BASE}/batch_fix_path?source=${currentSource}`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({path: currentPath, overwrite: !missingOnly})
        });
        if (!resp.ok) throw new Error(await resp.text());
        const data = await resp.json();
        pollTaskProgress(data.task_id, fixFolderBtn, 'Fix Folder');
    }

    async function onFixAll() {
        const resp = await fetch(`${API_BASE}/batch_fix_path?source=${currentSource}`, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({path: '', overwrite: !missingOnly})
        });
        if (!resp.ok) throw new Error(await resp.text());
        const data = await resp.json();
        pollTaskProgress(data.task_id, fixAllBtn, 'Fix All');
    }

    function pollTaskProgress(taskId, button, originalText) {
        button.disabled = true;
        const poll = async () => {
            try {
                const resp = await fetch(`${API_BASE}/task/${taskId}/progress`);
                const prog = await resp.json();
                const percent = prog.total > 0 ? Math.round((prog.processed / prog.total) * 100) : 0;
                button.textContent = `${originalText} (${percent}%)`;
                if (prog.status === 'completed') {
                    button.textContent = originalText;
                    button.disabled = false;
                    loadFiles();
                } else {
                    setTimeout(poll, 1000);
                }
            } catch (err) {
                button.textContent = originalText;
                button.disabled = false;
            }
        };
        poll();
    }

    init();
})();
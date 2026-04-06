async function loadFileBrowser() {
    const res = await fetch('/api/browse');
    const files = await res.json();
    const tbody = document.getElementById('fileBrowser');
    if (files.length === 0) {
        tbody.innerHTML = '<tr><td colspan="6">No media files found. </table>';
        return;
    }
    tbody.innerHTML = '';
    files.forEach(file => {
        const exifDate = file.datetimeoriginal ? file.datetimeoriginal.replace(/:/g, '-') : '—';
        const estimated = file.estimated ? file.estimated.split(' ')[0] : '?';
        const row = tbody.insertRow();
        row.classList.add('file-row');
        row.dataset.exifMissing = (exifDate === '—') ? 'true' : 'false';
        const refreshCell = row.insertCell(0);
        refreshCell.innerHTML = '';
        const thumbCell = row.insertCell(1);
        const img = document.createElement('img');
        img.src = `/api/thumb/${encodeURIComponent(file.path)}?t=${Date.now()}`;
        img.classList.add('thumbnail');
        img.onerror = () => { img.src = 'data:image/svg+xml,%3Csvg xmlns="http://www.w3.org/2000/svg" width="50" height="50" viewBox="0 0 24 24" fill="gray"%3E%3Crect width="50" height="50" fill="%23444"/%3E%3C/svg%3E'; };
        thumbCell.appendChild(img);
        row.insertCell(2).innerText = file.name;
        row.insertCell(3).innerText = exifDate;
        row.insertCell(4).innerText = estimated;
        const actionCell = row.insertCell(5);
        const dateInput = document.createElement('input');
        dateInput.type = 'text';
        dateInput.placeholder = 'YYYY-MM-DD';
        dateInput.classList.add('date-input', 'form-control', 'form-control-sm');
        const setBtn = document.createElement('button');
        setBtn.innerText = 'Set';
        setBtn.classList.add('set-btn', 'btn', 'btn-sm', 'btn-primary');
        setBtn.onclick = async () => {
            const newDate = dateInput.value;
            if (!newDate) { alert('Enter a date (YYYY-MM-DD)'); return; }
            const res = await fetch('/api/fix_file', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({file: file.path, date: newDate})
            });
            const data = await res.json();
            if (data.success) {
                alert('Date fixed!');
                loadFileBrowser();
            } else {
                alert('Error: ' + data.error);
            }
        };
        actionCell.appendChild(dateInput);
        actionCell.appendChild(setBtn);
    });
    applyMissingOnlyFilter();
}

function applyMissingOnlyFilter() {
    const missingOnly = document.getElementById('autofixAll').checked;
    const rows = document.querySelectorAll('#fileBrowser .file-row');
    rows.forEach(row => {
        if (missingOnly && row.dataset.exifMissing !== 'true') {
            row.style.display = 'none';
        } else {
            row.style.display = '';
        }
    });
}

document.getElementById('refreshBrowserBtn').addEventListener('click', () => loadFileBrowser());

const watchdogToggle = document.getElementById('watchdogToggle');
async function updateWatchdogStatus() {
    const res = await fetch('/api/watchdog_status');
    const data = await res.json();
    watchdogToggle.checked = data.running;
}
watchdogToggle.addEventListener('change', async (e) => {
    if (e.target.checked) {
        const res = await fetch('/api/start_watchdog', {method: 'POST'});
        const data = await res.json();
        if (!data.success && data.message !== 'Watchdog already running') {
            alert('Failed to start watchdog');
            e.target.checked = false;
        }
    } else {
        const res = await fetch('/api/stop_watchdog', {method: 'POST'});
        const data = await res.json();
        if (!data.success) alert('Failed to stop watchdog');
    }
});

document.getElementById('runRsync').addEventListener('click', async () => {
    const res = await fetch('/api/run_rsync', {method: 'POST'});
    const data = await res.json();
    alert(data.message || data.error);
});

const today = new Date().toISOString().slice(0,10);
document.getElementById('autofixMaxDate').value = today;
document.getElementById('runAutofix').addEventListener('click', async () => {
    const all = document.getElementById('autofixAll').checked;
    const overwrite = document.getElementById('autofixOverwrite').checked;
    const maxDate = document.getElementById('autofixMaxDate').value;
    const minDate = document.getElementById('autofixMinDate').value;
    const outputDiv = document.getElementById('autofixOutput');
    outputDiv.innerText = 'Running AutoFix... please wait.';
    try {
        const res = await fetch('/api/autofix_python', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
                overwrite: overwrite,
                max_date: maxDate,
                min_date: minDate
            })
        });
        const data = await res.json();
        if (data.results) {
            let output = '';
            for (const item of data.results) {
                output += `${item.file}: ${item.status}${item.date ? ' -> ' + item.date : ''}\n`;
            }
            outputDiv.innerText = output;
        } else {
            outputDiv.innerText = 'Error: ' + (data.error || 'Unknown');
        }
    } catch (err) {
        outputDiv.innerText = 'Request failed: ' + err;
    }
    loadFileBrowser();
});
document.getElementById('clearAutofixOutputConsole').addEventListener('click', () => {
    document.getElementById('autofixOutput').innerText = '';
});

document.getElementById('autofixAll').addEventListener('change', () => applyMissingOnlyFilter());

// Animáció a brandre kattintáskor
const brand = document.getElementById('brand');
brand.addEventListener('click', () => {
    brand.classList.add('animate-brand');
    setTimeout(() => {
        brand.classList.remove('animate-brand');
    }, 800);
    alert('IVERBS: Image & Video EXIF Restore and Backup and Sorter');
});

updateWatchdogStatus();
loadFileBrowser();
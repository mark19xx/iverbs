package main

import (
    "database/sql"
    "encoding/json"
    "fmt"
    "html/template"
    "io"
    "log"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/fsnotify/fsnotify"
    _ "github.com/mattn/go-sqlite3"
)

// Konfiguráció
var watchSources []string
var dataDir = "/data/cache"
var stateDir = "/data/state"
var dbPath = "/data/db/iverbs.db"
var version = "0.3.0"

// Cache struktúra
type exifCache struct {
    mu    sync.RWMutex
    cache map[string]bool // filePath -> hasExif
}

var cache = &exifCache{cache: make(map[string]bool)}

// Task manager a batch fix progresshez
type Task struct {
    Total     int
    Processed int
    Status    string // "running", "completed"
}

var tasks = struct {
    mu    sync.Mutex
    m     map[int]*Task
    counter int
}{m: make(map[int]*Task)}

// Watchdog per source
type watchdog struct {
    mu        sync.RWMutex
    watchers  map[int]*fsnotify.Watcher
    queues    map[int]chan string
    states    map[int]bool
    cancel    map[int]chan struct{}
}

var wd = &watchdog{
    watchers: make(map[int]*fsnotify.Watcher),
    queues:   make(map[int]chan string),
    states:   make(map[int]bool),
    cancel:   make(map[int]chan struct{}),
}

// SQLite inicializálás
func initDB() error {
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        return err
    }
    defer db.Close()
    _, err = db.Exec(`CREATE TABLE IF NOT EXISTS exif_cache (
        file_path TEXT PRIMARY KEY,
        has_exif INTEGER,
        last_checked TIMESTAMP
    )`)
    return err
}

// Cache betöltése az adatbázisból
func loadCacheFromDB() error {
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        return err
    }
    defer db.Close()
    rows, err := db.Query("SELECT file_path, has_exif FROM exif_cache")
    if err != nil {
        return err
    }
    defer rows.Close()
    cache.mu.Lock()
    defer cache.mu.Unlock()
    for rows.Next() {
        var path string
        var has int
        if err := rows.Scan(&path, &has); err != nil {
            continue
        }
        cache.cache[path] = has == 1
    }
    log.Printf("Cache loaded from DB: %d entries", len(cache.cache))
    return nil
}

// Cache mentése adatbázisba (egy rekord)
func saveCacheToDB(filePath string, hasExif bool) {
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        log.Printf("DB error: %v", err)
        return
    }
    defer db.Close()
    _, err = db.Exec("REPLACE INTO exif_cache VALUES (?, ?, ?)",
        filePath, boolToInt(hasExif), time.Now().Format(time.RFC3339))
    if err != nil {
        log.Printf("DB error: %v", err)
    }
}

// Törlés az adatbázisból
func deleteFromDB(filePath string) {
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        return
    }
    defer db.Close()
    db.Exec("DELETE FROM exif_cache WHERE file_path = ?", filePath)
}

// Segédfüggvény
func boolToInt(b bool) int {
    if b {
        return 1
    }
    return 0
}

// EXIF ellenőrzés (exiftool hívás)
func checkExif(filePath string) (bool, string) {
    cmd := exec.Command("exiftool", "-s3", "-DateTimeOriginal", filePath)
    out, err := cmd.Output()
    if err != nil {
        cache.mu.Lock()
        cache.cache[filePath] = false
        cache.mu.Unlock()
        saveCacheToDB(filePath, false)
        return false, ""
    }
    dateStr := strings.TrimSpace(string(out))
    has := dateStr != "" && dateStr != "0000:00:00 00:00:00"
    cache.mu.Lock()
    cache.cache[filePath] = has
    cache.mu.Unlock()
    saveCacheToDB(filePath, has)
    return has, dateStr
}

// Gyorsítótárból olvasás
func getExifFromCache(filePath string) bool {
    cache.mu.RLock()
    defer cache.mu.RUnlock()
    return cache.cache[filePath]
}

// Dátum kinyerés a fájlnévből (regex)
func extractDateFromFilename(filePath string) string {
    filename := filepath.Base(filePath)
    // Unix timestamp
    re := regexp.MustCompile(`\b(\d{10})(?:\d{3})?\b`)
    if m := re.FindStringSubmatch(filename); m != nil {
        ts, _ := strconv.ParseInt(m[1], 10, 64)
        if len(m[0]) == 13 {
            ts /= 1000
        }
        t := time.Unix(ts, 0)
        return t.Format("2006-01-02 15:04:05")
    }
    // YYYYMMDD_HHMMSS
    re = regexp.MustCompile(`(\d{4})(\d{2})(\d{2})[ _-]?(\d{2})(\d{2})(\d{2})`)
    if m := re.FindStringSubmatch(filename); m != nil {
        y, _ := strconv.Atoi(m[1])
        mo, _ := strconv.Atoi(m[2])
        d, _ := strconv.Atoi(m[3])
        h, _ := strconv.Atoi(m[4])
        min, _ := strconv.Atoi(m[5])
        s, _ := strconv.Atoi(m[6])
        t := time.Date(y, time.Month(mo), d, h, min, s, 0, time.Local)
        return t.Format("2006-01-02 15:04:05")
    }
    // YYYY-MM-DD HH.MM.SS
    re = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})[ _-]?(\d{2})\.(\d{2})\.(\d{2})`)
    if m := re.FindStringSubmatch(filename); m != nil {
        y, _ := strconv.Atoi(m[1])
        mo, _ := strconv.Atoi(m[2])
        d, _ := strconv.Atoi(m[3])
        h, _ := strconv.Atoi(m[4])
        min, _ := strconv.Atoi(m[5])
        s, _ := strconv.Atoi(m[6])
        t := time.Date(y, time.Month(mo), d, h, min, s, 0, time.Local)
        return t.Format("2006-01-02 15:04:05")
    }
    // Csak dátum YYYYMMDD
    re = regexp.MustCompile(`(\d{4})(\d{2})(\d{2})`)
    if m := re.FindStringSubmatch(filename); m != nil {
        y, _ := strconv.Atoi(m[1])
        mo, _ := strconv.Atoi(m[2])
        d, _ := strconv.Atoi(m[3])
        t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.Local)
        return t.Format("2006-01-02 00:00:00")
    }
    return ""
}

// Fájl javítása (exiftool írás)
func fixFile(filePath string, overwrite bool) map[string]interface{} {
    estimated := extractDateFromFilename(filePath)
    if estimated == "" {
        return map[string]interface{}{"error": "No date in filename"}
    }
    if !overwrite {
        if getExifFromCache(filePath) {
            return map[string]interface{}{"skipped": "EXIF already exists"}
        }
    }
    cmd := exec.Command("exiftool", "-overwrite_original",
        "-DateTimeOriginal="+estimated,
        "-CreateDate="+estimated,
        "-ModifyDate="+estimated,
        filePath)
    if err := cmd.Run(); err != nil {
        return map[string]interface{}{"error": err.Error()}
    }
    cache.mu.Lock()
    cache.cache[filePath] = true
    cache.mu.Unlock()
    saveCacheToDB(filePath, true)
    return map[string]interface{}{"success": true, "date": estimated}
}

// Mappafa (alkönyvtárak)
func getTree(rootPath string) []string {
    entries, err := os.ReadDir(rootPath)
    if err != nil {
        return []string{}
    }
    var dirs []string
    for _, e := range entries {
        if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
            dirs = append(dirs, e.Name())
        }
    }
    return dirs
}

// Fájlok listázása egy mappában (nem rekurzív)
func getFilesInDir(dirPath string, limit, offset int, missingOnly bool) ([]map[string]interface{}, int) {
    entries, err := os.ReadDir(dirPath)
    if err != nil {
        return []map[string]interface{}{}, 0
    }
    var allFiles []map[string]interface{}
    for _, e := range entries {
        if e.IsDir() {
            continue
        }
        name := e.Name()
        ext := strings.ToLower(filepath.Ext(name))
        if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".mp4" && ext != ".mov" && ext != ".avi" && ext != ".jpe" && ext != ".jfif" {
            continue
        }
        full := filepath.Join(dirPath, name)
        hasExif := getExifFromCache(full)
        if missingOnly && hasExif {
            continue
        }
        allFiles = append(allFiles, map[string]interface{}{
            "name":     name,
            "path":     full,
            "has_exif": hasExif,
        })
    }
    total := len(allFiles)
    start := offset
    end := offset + limit
    if start > total {
        start = total
    }
    if end > total {
        end = total
    }
    files := allFiles[start:end]
    // Hozzáadjuk a becsült dátumot és a relatív elérési utat
    for i := range files {
        files[i]["estimated"] = extractDateFromFilename(files[i]["path"].(string))
        // relatív útvonal a gyökérhez (a watch source-hoz képest)
        // ezt majd a browse végpontban számoljuk
    }
    return files, total
}

// Háttérfeltöltő szál (cache refresh)
func backgroundCacheRefresh() {
    log.Println("Background cache refresh started")
    visited := make(map[string]bool)
    for {
        for _, src := range watchSources {
            src = strings.TrimSpace(src)
            if _, err := os.Stat(src); err != nil {
                continue
            }
            filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
                if err != nil {
                    return nil
                }
                if info.IsDir() {
                    return nil
                }
                ext := strings.ToLower(filepath.Ext(path))
                if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".mp4" && ext != ".mov" && ext != ".avi" && ext != ".jpe" && ext != ".jfif" {
                    return nil
                }
                if visited[path] {
                    return nil
                }
                visited[path] = true
                if !getExifFromCache(path) {
                    checkExif(path)
                }
                time.Sleep(100 * time.Millisecond)
                return nil
            })
        }
        // 10 perc szünet
        time.Sleep(10 * time.Minute)
        // visited törlése, hogy újra bejárja (új fájlok miatt)
        visited = make(map[string]bool)
    }
}

// Watchdog munkás szál
func watchdogWorker(sourceIdx int, queue chan string, cancel <-chan struct{}) {
    for {
        select {
        case filePath := <-queue:
            time.Sleep(300 * time.Millisecond) // rate limiting
            // Ellenőrizzük, hogy létezik-e még a fájl (törlés esetén ne dolgozzuk fel)
            if _, err := os.Stat(filePath); err != nil {
                // Fájl nem létezik – töröljük a cache-ből
                cache.mu.Lock()
                delete(cache.cache, filePath)
                cache.mu.Unlock()
                deleteFromDB(filePath)
                continue
            }
            if !getExifFromCache(filePath) {
                fixFile(filePath, false)
            }
        case <-cancel:
            return
        }
    }
}

// Watchdog indítása egy forrásra
func startWatchdogForSource(sourceIdx int) error {
    wd.mu.Lock()
    defer wd.mu.Unlock()
    if _, ok := wd.watchers[sourceIdx]; ok {
        return nil // már fut
    }
    rootPath := strings.TrimSpace(watchSources[sourceIdx])
    if _, err := os.Stat(rootPath); err != nil {
        return err
    }
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        return err
    }
    if err := watcher.Add(rootPath); err != nil {
        watcher.Close()
        return err
    }
    // Rekurzív hozzáadás
    filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return nil
        }
        if info.IsDir() {
            watcher.Add(path)
        }
        return nil
    })
    queue := make(chan string, 100)
    cancel := make(chan struct{})
    go watchdogWorker(sourceIdx, queue, cancel)
    // Eseménykezelő goroutine
    go func() {
        for {
            select {
            case event, ok := <-watcher.Events:
                if !ok {
                    return
                }
                if event.Op&fsnotify.Create == fsnotify.Create || event.Op&fsnotify.Write == fsnotify.Write {
                    // Új vagy módosított fájl
                    ext := strings.ToLower(filepath.Ext(event.Name))
                    if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".mp4" || ext == ".mov" || ext == ".avi" || ext == ".jpe" || ext == ".jfif" {
                        queue <- event.Name
                    }
                } else if event.Op&fsnotify.Remove == fsnotify.Remove {
                    // Törlés: cache-ből eltávolítás
                    cache.mu.Lock()
                    delete(cache.cache, event.Name)
                    cache.mu.Unlock()
                    deleteFromDB(event.Name)
                } else if event.Op&fsnotify.Rename == fsnotify.Rename {
                    // Átnevezés: cache-ből töröljük a régit (új majd Create-kor jön)
                    cache.mu.Lock()
                    delete(cache.cache, event.Name)
                    cache.mu.Unlock()
                    deleteFromDB(event.Name)
                }
            case err, ok := <-watcher.Errors:
                if !ok {
                    return
                }
                log.Printf("Watchdog error: %v", err)
            }
        }
    }()
    wd.watchers[sourceIdx] = watcher
    wd.queues[sourceIdx] = queue
    wd.cancel[sourceIdx] = cancel
    wd.states[sourceIdx] = true
    // Állapot mentése fájlba
    stateFile := filepath.Join(stateDir, fmt.Sprintf("watchdog_%d.state", sourceIdx))
    os.WriteFile(stateFile, []byte("true"), 0644)
    return nil
}

// Watchdog leállítása
func stopWatchdogForSource(sourceIdx int) error {
    wd.mu.Lock()
    defer wd.mu.Unlock()
    watcher, ok := wd.watchers[sourceIdx]
    if !ok {
        return nil
    }
    if cancel, ok := wd.cancel[sourceIdx]; ok {
        close(cancel)
    }
    watcher.Close()
    delete(wd.watchers, sourceIdx)
    delete(wd.queues, sourceIdx)
    delete(wd.cancel, sourceIdx)
    wd.states[sourceIdx] = false
    stateFile := filepath.Join(stateDir, fmt.Sprintf("watchdog_%d.state", sourceIdx))
    os.WriteFile(stateFile, []byte("false"), 0644)
    return nil
}

// Watchdog állapotok betöltése fájlból
func loadWatchdogStates() {
    for i := range watchSources {
        stateFile := filepath.Join(stateDir, fmt.Sprintf("watchdog_%d.state", i))
        data, err := os.ReadFile(stateFile)
        if err != nil {
            wd.states[i] = false
            continue
        }
        wd.states[i] = strings.TrimSpace(string(data)) == "true"
    }
}

// HTTP API

// Sablon (HTML)
var tmpl *template.Template

func initTemplate() {
    tmpl = template.Must(template.ParseFiles("templates/index.html"))
}

// Főoldal
func indexHandler(w http.ResponseWriter, r *http.Request) {
    tmpl.Execute(w, nil)
}

// Források listája
func sourcesHandler(w http.ResponseWriter, r *http.Request) {
    sources := make([]string, len(watchSources))
    for i, s := range watchSources {
        sources[i] = filepath.Base(strings.TrimSpace(s))
    }
    json.NewEncoder(w).Encode(sources)
}

// Mappa fa
func treeHandler(w http.ResponseWriter, r *http.Request) {
    idxStr := r.PathValue("source_idx")
    idx, err := strconv.Atoi(idxStr)
    if err != nil || idx >= len(watchSources) {
        http.Error(w, "Invalid source", http.StatusBadRequest)
        return
    }
    root := strings.TrimSpace(watchSources[idx])
    dirs := getTree(root)
    json.NewEncoder(w).Encode(dirs)
}

// Fájlok böngészése
func browseHandler(w http.ResponseWriter, r *http.Request) {
    sourceIdx, _ := strconv.Atoi(r.URL.Query().Get("source"))
    subpath := r.URL.Query().Get("path")
    limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
    if limit == 0 {
        limit = 20
    }
    offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
    missingOnly := r.URL.Query().Get("missing_only") == "true"
    if sourceIdx >= len(watchSources) {
        http.Error(w, "Invalid source", http.StatusBadRequest)
        return
    }
    root := strings.TrimSpace(watchSources[sourceIdx])
    fullPath := root
    if subpath != "" {
        fullPath = filepath.Join(root, subpath)
    }
    files, total := getFilesInDir(fullPath, limit, offset, missingOnly)
    // Relatív útvonalak és becsült dátumok hozzáadása
    for i := range files {
        rel, _ := filepath.Rel(root, files[i]["path"].(string))
        files[i]["rel_path"] = rel
        files[i]["estimated"] = extractDateFromFilename(files[i]["path"].(string))
    }
    response := map[string]interface{}{
        "files": files,
        "total": total,
    }
    json.NewEncoder(w).Encode(response)
}

// Kép/video szolgáltatás
func imageHandler(w http.ResponseWriter, r *http.Request) {
    sourceIdx, _ := strconv.Atoi(r.PathValue("source_idx"))
    relPath := r.PathValue("rel_path")
    if sourceIdx >= len(watchSources) {
        http.Error(w, "Forbidden", http.StatusForbidden)
        return
    }
    root := strings.TrimSpace(watchSources[sourceIdx])
    full := filepath.Join(root, relPath)
    http.ServeFile(w, r, full)
}

// EXIF lekérdezés egy fájlhoz
func exifHandler(w http.ResponseWriter, r *http.Request) {
    filePath := r.URL.Query().Get("file")
    if filePath == "" {
        http.Error(w, "Missing file", http.StatusBadRequest)
        return
    }
    has, date := checkExif(filePath)
    json.NewEncoder(w).Encode(map[string]interface{}{"exif_date": date})
}

// Egy fájl javítása (automatikus vagy kézi)
func fixHandler(w http.ResponseWriter, r *http.Request) {
    var req struct {
        File      string `json:"file"`
        Date      string `json:"date"`
        Overwrite bool   `json:"overwrite"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request", http.StatusBadRequest)
        return
    }
    if req.Date != "" {
        // Kézi dátum beállítás
        estimated := req.Date + " 00:00:00"
        cmd := exec.Command("exiftool", "-overwrite_original",
            "-DateTimeOriginal="+estimated,
            "-CreateDate="+estimated,
            "-ModifyDate="+estimated,
            req.File)
        if err := cmd.Run(); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        cache.mu.Lock()
        cache.cache[req.File] = true
        cache.mu.Unlock()
        saveCacheToDB(req.File, true)
        json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
        return
    }
    // Automatikus javítás fájlnévből
    result := fixFile(req.File, req.Overwrite)
    json.NewEncoder(w).Encode(result)
}

// Batch javítás (indítás)
func batchFixHandler(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Files     []string `json:"files"`
        Overwrite bool     `json:"overwrite"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request", http.StatusBadRequest)
        return
    }
    tasks.mu.Lock()
    tasks.counter++
    taskID := tasks.counter
    tasks.m[taskID] = &Task{Total: len(req.Files), Processed: 0, Status: "running"}
    tasks.mu.Unlock()
    // Aszinkron feldolgozás
    go func() {
        for i, file := range req.Files {
            fixFile(file, req.Overwrite)
            tasks.mu.Lock()
            if t, ok := tasks.m[taskID]; ok {
                t.Processed = i + 1
            }
            tasks.mu.Unlock()
        }
        tasks.mu.Lock()
        if t, ok := tasks.m[taskID]; ok {
            t.Status = "completed"
        }
        tasks.mu.Unlock()
    }()
    json.NewEncoder(w).Encode(map[string]interface{}{"task_id": taskID})
}

// Feladat állapot lekérdezése
func taskProgressHandler(w http.ResponseWriter, r *http.Request) {
    taskID, _ := strconv.Atoi(r.PathValue("task_id"))
    tasks.mu.Lock()
    task, ok := tasks.m[taskID]
    tasks.mu.Unlock()
    if !ok {
        http.Error(w, "Task not found", http.StatusNotFound)
        return
    }
    json.NewEncoder(w).Encode(map[string]interface{}{
        "total":     task.Total,
        "processed": task.Processed,
        "status":    task.Status,
    })
}

// Watchdog állapot lekérdezése
func watchdogStatusHandler(w http.ResponseWriter, r *http.Request) {
    sourceIdx, _ := strconv.Atoi(r.URL.Query().Get("source"))
    wd.mu.RLock()
    running := wd.states[sourceIdx]
    wd.mu.RUnlock()
    json.NewEncoder(w).Encode(map[string]bool{"running": running})
}

// Watchdog indítása
func startWatchdogHandler(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Source int `json:"source"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request", http.StatusBadRequest)
        return
    }
    if err := startWatchdogForSource(req.Source); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// Watchdog leállítása
func stopWatchdogHandler(w http.ResponseWriter, r *http.Request) {
    var req struct {
        Source int `json:"source"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request", http.StatusBadRequest)
        return
    }
    if err := stopWatchdogForSource(req.Source); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// Statikus fájlok (CSS, JS)
func staticHandler(w http.ResponseWriter, r *http.Request) {
    http.ServeFile(w, r, "static/"+r.PathValue("file"))
}

func main() {
    // Környezeti változók
    watchEnv := os.Getenv("WATCH_DIRS")
    if watchEnv == "" {
        watchEnv = "/home/user"
    }
    watchSources = strings.Split(watchEnv, ",")
    if len(watchSources) == 0 {
        log.Fatal("No watch directories defined")
    }
    // Adatkönyvtárak létrehozása
    os.MkdirAll(dataDir, 0755)
    os.MkdirAll(stateDir, 0755)
    os.MkdirAll(filepath.Dir(dbPath), 0755)

    // Adatbázis és cache inicializálás
    if err := initDB(); err != nil {
        log.Fatal(err)
    }
    if err := loadCacheFromDB(); err != nil {
        log.Fatal(err)
    }

    // Watchdog állapotok betöltése és indítás
    loadWatchdogStates()
    for i, enabled := range wd.states {
        if enabled {
            if err := startWatchdogForSource(i); err != nil {
                log.Printf("Failed to start watchdog for source %d: %v", i, err)
            }
        }
    }

    // Háttérfeltöltő szál indítása
    go backgroundCacheRefresh()

    // Sablon betöltése
    initTemplate()

    // HTTP router
    http.HandleFunc("/", indexHandler)
    http.HandleFunc("GET /api/sources", sourcesHandler)
    http.HandleFunc("GET /api/tree/{source_idx}", treeHandler)
    http.HandleFunc("GET /api/browse", browseHandler)
    http.HandleFunc("GET /api/image/{source_idx}/{rel_path...}", imageHandler)
    http.HandleFunc("GET /api/exif", exifHandler)
    http.HandleFunc("POST /api/fix", fixHandler)
    http.HandleFunc("POST /api/batch_fix", batchFixHandler)
    http.HandleFunc("GET /api/task/{task_id}/progress", taskProgressHandler)
    http.HandleFunc("GET /api/watchdog_status", watchdogStatusHandler)
    http.HandleFunc("POST /api/start_watchdog", startWatchdogHandler)
    http.HandleFunc("POST /api/stop_watchdog", stopWatchdogHandler)
    http.HandleFunc("GET /static/{file}", staticHandler)

    log.Printf("IVERBS %s started on :5000", version)
    log.Fatal(http.ListenAndServe(":5000", nil))
}
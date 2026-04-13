package main

import (
    "database/sql"
    "encoding/json"
    "fmt"
    "html/template"
    "log"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/fsnotify/fsnotify"
    _ "github.com/mattn/go-sqlite3"
)

const version = "0.3.0"

var (
    watchSources   []string
    dbPath         = "/data/db/iverbs.db"
    stateDir       = "/data/state"
    exifCache      = make(map[string]bool)
    cacheMutex     sync.RWMutex
    tasks          = make(map[int]*Task)
    tasksMutex     sync.RWMutex
    taskCounter    = 0
    observers      = make(map[int]*fsnotify.Watcher)
    watchdogQueues = make(map[int]chan string)
    watchdogStates = make(map[int]bool)
    stateMutex     sync.RWMutex
)

// SSE broker
type SSEEvent struct {
    Source  int  `json:"source"`
    Running bool `json:"running"`
}

type SSEBroker struct {
    clients        map[chan SSEEvent]bool
    newClients     chan chan SSEEvent
    closingClients chan chan SSEEvent
    events         chan SSEEvent
    mutex          sync.Mutex
}

func NewSSEBroker() *SSEBroker {
    b := &SSEBroker{
        clients:        make(map[chan SSEEvent]bool),
        newClients:     make(chan chan SSEEvent),
        closingClients: make(chan chan SSEEvent),
        events:         make(chan SSEEvent),
    }
    go b.listen()
    return b
}

func (b *SSEBroker) listen() {
    for {
        select {
        case newCh := <-b.newClients:
            b.mutex.Lock()
            b.clients[newCh] = true
            b.mutex.Unlock()
        case closingCh := <-b.closingClients:
            b.mutex.Lock()
            delete(b.clients, closingCh)
            b.mutex.Unlock()
        case event := <-b.events:
            b.mutex.Lock()
            for ch := range b.clients {
                ch <- event
            }
            b.mutex.Unlock()
        }
    }
}

func (b *SSEBroker) Broadcast(event SSEEvent) {
    b.events <- event
}

var sseBroker = NewSSEBroker()

type Task struct {
    Total     int    `json:"total"`
    Processed int    `json:"processed"`
    Status    string `json:"status"`
}

type FileInfo struct {
    Name      string `json:"name"`
    Path      string `json:"path"`
    HasExif   bool   `json:"has_exif"`
    Estimated string `json:"estimated"`
    RelPath   string `json:"rel_path"`
}

type BrowseResponse struct {
    Files []FileInfo `json:"files"`
    Total int        `json:"total"`
}

func initDB() error {
    os.MkdirAll(filepath.Dir(dbPath), 0755)
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

    cacheMutex.Lock()
    defer cacheMutex.Unlock()
    for rows.Next() {
        var path string
        var has int
        if err := rows.Scan(&path, &has); err != nil {
            continue
        }
        exifCache[path] = has == 1
    }
    log.Printf("Cache loaded from DB: %d entries", len(exifCache))
    return nil
}

func saveCacheToDB(filePath string, hasExif bool) {
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        log.Printf("Error opening DB: %v", err)
        return
    }
    defer db.Close()

    has := 0
    if hasExif {
        has = 1
    }
    _, err = db.Exec("REPLACE INTO exif_cache VALUES (?, ?, ?)",
        filePath, has, time.Now().Format("2006-01-02 15:04:05"))
    if err != nil {
        log.Printf("Error saving to DB: %v", err)
    }
}

func getExifFromCache(filePath string) bool {
    cacheMutex.RLock()
    defer cacheMutex.RUnlock()
    return exifCache[filePath]
}

func setExifCache(filePath string, hasExif bool) {
    cacheMutex.Lock()
    defer cacheMutex.Unlock()
    exifCache[filePath] = hasExif
    go saveCacheToDB(filePath, hasExif)
}

func checkExif(filePath string) (bool, string) {
    cmd := exec.Command("exiftool", "-s3", "-DateTimeOriginal", filePath)
    output, err := cmd.Output()
    if err != nil {
        setExifCache(filePath, false)
        return false, ""
    }
    dateStr := strings.TrimSpace(string(output))
    has := dateStr != "" && dateStr != "0000:00:00 00:00:00"
    setExifCache(filePath, has)
    if has {
        return true, dateStr
    }
    return false, ""
}

func extractDateFromFilename(filePath string) string {
    filename := filepath.Base(filePath)
    // Unix timestamp
    re := regexp.MustCompile(`\b(\d{10})(?:\d{3})?\b`)
    if matches := re.FindStringSubmatch(filename); len(matches) > 1 {
        ts, err := strconv.ParseInt(matches[1], 10, 64)
        if err == nil {
            if len(matches[0]) == 13 {
                ts /= 1000
            }
            t := time.Unix(ts, 0)
            return t.Format("2006-01-02 15:04:05")
        }
    }

    // YYYYMMDD_HHMMSS
    re = regexp.MustCompile(`(\d{4})(\d{2})(\d{2})[ _-]?(\d{2})(\d{2})(\d{2})`)
    if matches := re.FindStringSubmatch(filename); len(matches) == 7 {
        y, _ := strconv.Atoi(matches[1])
        m, _ := strconv.Atoi(matches[2])
        d, _ := strconv.Atoi(matches[3])
        H, _ := strconv.Atoi(matches[4])
        M, _ := strconv.Atoi(matches[5])
        S, _ := strconv.Atoi(matches[6])
        t := time.Date(y, time.Month(m), d, H, M, S, 0, time.Local)
        if t.Year() > 1970 {
            return t.Format("2006-01-02 15:04:05")
        }
    }

    // YYYY-MM-DD HH.MM.SS
    re = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})[ _-]?(\d{2})\.(\d{2})\.(\d{2})`)
    if matches := re.FindStringSubmatch(filename); len(matches) == 7 {
        y, _ := strconv.Atoi(matches[1])
        m, _ := strconv.Atoi(matches[2])
        d, _ := strconv.Atoi(matches[3])
        H, _ := strconv.Atoi(matches[4])
        M, _ := strconv.Atoi(matches[5])
        S, _ := strconv.Atoi(matches[6])
        t := time.Date(y, time.Month(m), d, H, M, S, 0, time.Local)
        if t.Year() > 1970 {
            return t.Format("2006-01-02 15:04:05")
        }
    }

    // YYYYMMDD
    re = regexp.MustCompile(`(\d{4})(\d{2})(\d{2})`)
    if matches := re.FindStringSubmatch(filename); len(matches) == 4 {
        y, _ := strconv.Atoi(matches[1])
        m, _ := strconv.Atoi(matches[2])
        d, _ := strconv.Atoi(matches[3])
        t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.Local)
        if t.Year() > 1970 {
            return t.Format("2006-01-02 15:04:05")
        }
    }

    return ""
}

func fixFile(filePath string, overwrite bool) (map[string]interface{}, error) {
    estimated := extractDateFromFilename(filePath)
    if estimated == "" {
        return map[string]interface{}{"error": "No date in filename"}, nil
    }
    if !overwrite {
        has := getExifFromCache(filePath)
        if has {
            return map[string]interface{}{"skipped": "EXIF already exists"}, nil
        }
    }
    cmd := exec.Command("exiftool", "-overwrite_original",
        "-DateTimeOriginal="+estimated,
        "-CreateDate="+estimated,
        "-ModifyDate="+estimated,
        filePath)
    if err := cmd.Run(); err != nil {
        return nil, fmt.Errorf("exiftool failed: %v", err)
    }
    setExifCache(filePath, true)
    return map[string]interface{}{"success": true, "date": estimated}, nil
}

func getTree(rootPath string) []string {
    entries, err := os.ReadDir(rootPath)
    if err != nil {
        return []string{}
    }
    var dirs []string
    for _, entry := range entries {
        if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
            dirs = append(dirs, entry.Name())
        }
    }
    sort.Strings(dirs)
    return dirs
}

func getFilesInDir(dirPath string, limit, offset int, missingOnly bool) ([]FileInfo, int) {
    entries, err := os.ReadDir(dirPath)
    if err != nil {
        return []FileInfo{}, 0
    }
    var allFiles []FileInfo
    for _, entry := range entries {
        if entry.IsDir() {
            continue
        }
        ext := strings.ToLower(filepath.Ext(entry.Name()))
        if ext != ".jpg" && ext != ".jpeg" && ext != ".png" &&
            ext != ".mp4" && ext != ".mov" && ext != ".avi" &&
            ext != ".jpe" && ext != ".jfif" {
            continue
        }
        fullPath := filepath.Join(dirPath, entry.Name())
        hasExif := getExifFromCache(fullPath)
        if missingOnly && hasExif {
            continue
        }
        allFiles = append(allFiles, FileInfo{
            Name:      entry.Name(),
            Path:      fullPath,
            HasExif:   hasExif,
            Estimated: extractDateFromFilename(fullPath),
        })
    }
    total := len(allFiles)
    start := offset
    if start > total {
        start = total
    }
    end := offset + limit
    if end > total {
        end = total
    }
    return allFiles[start:end], total
}

func batchFixTask(taskID int, files []string, overwrite bool) {
    total := len(files)
    tasksMutex.Lock()
    tasks[taskID] = &Task{Total: total, Processed: 0, Status: "running"}
    tasksMutex.Unlock()

    for i, filePath := range files {
        if _, err := os.Stat(filePath); err == nil {
            fixFile(filePath, overwrite)
        }
        tasksMutex.Lock()
        tasks[taskID].Processed = i + 1
        tasksMutex.Unlock()
    }

    tasksMutex.Lock()
    tasks[taskID].Status = "completed"
    tasksMutex.Unlock()
}

func startWatchdogForSource(sourceIdx int) error {
    stateMutex.Lock()
    defer stateMutex.Unlock()
    if _, exists := observers[sourceIdx]; exists {
        return nil
    }
    rootPath := strings.TrimSpace(watchSources[sourceIdx])
    if _, err := os.Stat(rootPath); os.IsNotExist(err) {
        return fmt.Errorf("path does not exist: %s", rootPath)
    }

    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        return err
    }

    // Add directory recursively
    err = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return nil
        }
        if info.IsDir() {
            return watcher.Add(path)
        }
        return nil
    })
    if err != nil {
        watcher.Close()
        return err
    }

    eventQueue := make(chan string, 100)
    watchdogQueues[sourceIdx] = eventQueue

    go func() {
        for {
            select {
            case event, ok := <-watcher.Events:
                if !ok {
                    return
                }
                ext := strings.ToLower(filepath.Ext(event.Name))
                if ext != ".jpg" && ext != ".jpeg" && ext != ".png" &&
                    ext != ".mp4" && ext != ".mov" && ext != ".avi" &&
                    ext != ".jpe" && ext != ".jfif" {
                    continue
                }
                if event.Op&fsnotify.Create == fsnotify.Create ||
                    event.Op&fsnotify.Write == fsnotify.Write {
                    eventQueue <- event.Name
                } else if event.Op&fsnotify.Remove == fsnotify.Remove {
                    // Delete from cache
                    setExifCache(event.Name, false)
                }
            case err, ok := <-watcher.Errors:
                if !ok {
                    return
                }
                log.Printf("Watchdog error: %v", err)
            }
        }
    }()

    go func() {
        for filePath := range eventQueue {
            time.Sleep(300 * time.Millisecond) // rate limiting
            has, _ := checkExif(filePath)
            if !has {
                fixFile(filePath, false)
            }
        }
    }()

    observers[sourceIdx] = watcher
    watchdogStates[sourceIdx] = true
    saveWatchdogState(sourceIdx)
    sseBroker.Broadcast(SSEEvent{Source: sourceIdx, Running: true})
    return nil
}

func stopWatchdogForSource(sourceIdx int) error {
    stateMutex.Lock()
    defer stateMutex.Unlock()
    watcher, exists := observers[sourceIdx]
    if !exists {
        return nil
    }
    if err := watcher.Close(); err != nil {
        return err
    }
    delete(observers, sourceIdx)
    if queue, ok := watchdogQueues[sourceIdx]; ok {
        close(queue)
        delete(watchdogQueues, sourceIdx)
    }
    watchdogStates[sourceIdx] = false
    saveWatchdogState(sourceIdx)
    sseBroker.Broadcast(SSEEvent{Source: sourceIdx, Running: false})
    return nil
}

func loadWatchdogStates() {
    stateMutex.Lock()
    defer stateMutex.Unlock()
    for i := range watchSources {
        stateFile := filepath.Join(stateDir, fmt.Sprintf("watchdog_%d.state", i))
        data, err := os.ReadFile(stateFile)
        if err != nil {
            watchdogStates[i] = false
            continue
        }
        watchdogStates[i] = strings.TrimSpace(string(data)) == "true"
    }
}

func saveWatchdogState(sourceIdx int) {
    stateFile := filepath.Join(stateDir, fmt.Sprintf("watchdog_%d.state", sourceIdx))
    val := "false"
    if watchdogStates[sourceIdx] {
        val = "true"
    }
    os.WriteFile(stateFile, []byte(val), 0644)
}

func backgroundCacheRefresh() {
    ticker := time.NewTicker(1 * time.Hour)
    for range ticker.C {
        log.Println("Starting periodic cache cleanup")
        cacheMutex.RLock()
        for path := range exifCache {
            if _, err := os.Stat(path); os.IsNotExist(err) {
                cacheMutex.RUnlock()
                setExifCache(path, false)
                cacheMutex.RLock()
            }
        }
        cacheMutex.RUnlock()
        log.Println("Periodic cache cleanup completed")
    }
}

func main() {
    watchSourcesEnv := os.Getenv("WATCH_DIRS")
    if watchSourcesEnv == "" {
        watchSources = []string{"/home/user"}
    } else {
        watchSources = strings.Split(watchSourcesEnv, ",")
    }

    if err := initDB(); err != nil {
        log.Fatalf("Failed to init DB: %v", err)
    }
    if err := loadCacheFromDB(); err != nil {
        log.Printf("Warning: Failed to load cache from DB: %v", err)
    }

    loadWatchdogStates()
    for idx, enabled := range watchdogStates {
        if enabled {
            if err := startWatchdogForSource(idx); err != nil {
                log.Printf("Failed to start watchdog for source %d: %v", idx, err)
            }
        }
    }

    go backgroundCacheRefresh()

    tmpl := template.Must(template.ParseGlob("templates/*.html"))
    http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        sources := make([]string, len(watchSources))
        for i, src := range watchSources {
            sources[i] = filepath.Base(strings.TrimSpace(src))
        }
        tmpl.ExecuteTemplate(w, "index.html", map[string]interface{}{
            "sources": sources,
            "version": version,
        })
    })

    http.HandleFunc("/api/sources", func(w http.ResponseWriter, r *http.Request) {
        sources := make([]string, len(watchSources))
        for i, src := range watchSources {
            sources[i] = filepath.Base(strings.TrimSpace(src))
        }
        json.NewEncoder(w).Encode(sources)
    })

    http.HandleFunc("/api/tree/", func(w http.ResponseWriter, r *http.Request) {
        parts := strings.Split(r.URL.Path, "/")
        if len(parts) < 4 {
            http.Error(w, "Invalid request", http.StatusBadRequest)
            return
        }
        sourceIdx, err := strconv.Atoi(parts[3])
        if err != nil || sourceIdx >= len(watchSources) {
            json.NewEncoder(w).Encode([]string{})
            return
        }
        root := strings.TrimSpace(watchSources[sourceIdx])
        tree := getTree(root)
        json.NewEncoder(w).Encode(tree)
    })

    http.HandleFunc("/api/browse", func(w http.ResponseWriter, r *http.Request) {
        sourceIdx, _ := strconv.Atoi(r.URL.Query().Get("source"))
        subpath := r.URL.Query().Get("path")
        limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
        if limit <= 0 {
            limit = 20
        }
        offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
        missingOnly := r.URL.Query().Get("missing_only") == "true"
        if sourceIdx >= len(watchSources) {
            json.NewEncoder(w).Encode(BrowseResponse{Files: []FileInfo{}, Total: 0})
            return
        }
        root := strings.TrimSpace(watchSources[sourceIdx])
        fullPath := root
        if subpath != "" {
            fullPath = filepath.Join(root, subpath)
        }
        files, total := getFilesInDir(fullPath, limit, offset, missingOnly)
        for i := range files {
            rel, _ := filepath.Rel(root, files[i].Path)
            files[i].RelPath = rel
            files[i].Estimated = extractDateFromFilename(files[i].Path)
        }
        json.NewEncoder(w).Encode(BrowseResponse{Files: files, Total: total})
    })

    http.HandleFunc("/api/image/", func(w http.ResponseWriter, r *http.Request) {
        parts := strings.Split(r.URL.Path, "/")
        if len(parts) < 5 {
            http.Error(w, "Invalid request", http.StatusBadRequest)
            return
        }
        sourceIdx, err := strconv.Atoi(parts[3])
        if err != nil || sourceIdx >= len(watchSources) {
            http.Error(w, "Forbidden", http.StatusForbidden)
            return
        }
        relPath := strings.Join(parts[4:], "/")
        root := strings.TrimSpace(watchSources[sourceIdx])
        fullPath := filepath.Join(root, relPath)
        if !strings.HasPrefix(fullPath, root) {
            http.Error(w, "Forbidden", http.StatusForbidden)
            return
        }
        http.ServeFile(w, r, fullPath)
    })

    http.HandleFunc("/api/exif", func(w http.ResponseWriter, r *http.Request) {
        filePath := r.URL.Query().Get("file")
        if filePath == "" {
            http.Error(w, "Missing file parameter", http.StatusBadRequest)
            return
        }
        _, exifDate := checkExif(filePath)
        json.NewEncoder(w).Encode(map[string]interface{}{"exif_date": exifDate})
    })

    http.HandleFunc("/api/fix", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
            http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
            return
        }
        var data map[string]interface{}
        if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
            http.Error(w, "Invalid JSON", http.StatusBadRequest)
            return
        }
        filePath, ok := data["file"].(string)
        if !ok || filePath == "" {
            http.Error(w, "Missing file parameter", http.StatusBadRequest)
            return
        }
        if newDate, ok := data["date"].(string); ok && newDate != "" {
            estimated := newDate + " 00:00:00"
            cmd := exec.Command("exiftool", "-overwrite_original",
                "-DateTimeOriginal="+estimated,
                "-CreateDate="+estimated,
                "-ModifyDate="+estimated,
                filePath)
            if err := cmd.Run(); err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
                return
            }
            setExifCache(filePath, true)
            json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
            return
        }
        overwrite, _ := data["overwrite"].(bool)
        result, err := fixFile(filePath, overwrite)
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        json.NewEncoder(w).Encode(result)
    })

    http.HandleFunc("/api/batch_fix", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
            http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
            return
        }
        var data map[string]interface{}
        if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
            http.Error(w, "Invalid JSON", http.StatusBadRequest)
            return
        }
        filesInterface, ok := data["files"].([]interface{})
        if !ok {
            http.Error(w, "Missing files parameter", http.StatusBadRequest)
            return
        }
        files := make([]string, len(filesInterface))
        for i, f := range filesInterface {
            files[i], _ = f.(string)
        }
        overwrite, _ := data["overwrite"].(bool)
        tasksMutex.Lock()
        taskCounter++
        taskID := taskCounter
        tasks[taskID] = &Task{Total: len(files), Processed: 0, Status: "running"}
        tasksMutex.Unlock()
        go batchFixTask(taskID, files, overwrite)
        json.NewEncoder(w).Encode(map[string]interface{}{"task_id": taskID})
    })

    http.HandleFunc("/api/task/", func(w http.ResponseWriter, r *http.Request) {
        parts := strings.Split(r.URL.Path, "/")
        if len(parts) < 4 {
            http.Error(w, "Invalid request", http.StatusBadRequest)
            return
        }
        taskID, err := strconv.Atoi(parts[3])
        if err != nil {
            http.Error(w, "Invalid task ID", http.StatusBadRequest)
            return
        }
        tasksMutex.RLock()
        task, exists := tasks[taskID]
        tasksMutex.RUnlock()
        if !exists {
            http.Error(w, "Task not found", http.StatusNotFound)
            return
        }
        json.NewEncoder(w).Encode(task)
    })

    http.HandleFunc("/api/watchdog_status", func(w http.ResponseWriter, r *http.Request) {
        sourceIdx, _ := strconv.Atoi(r.URL.Query().Get("source"))
        stateMutex.RLock()
        running := watchdogStates[sourceIdx]
        stateMutex.RUnlock()
        json.NewEncoder(w).Encode(map[string]interface{}{"running": running})
    })

    http.HandleFunc("/api/watchdog/events", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        w.Header().Set("Cache-Control", "no-cache")
        w.Header().Set("Connection", "keep-alive")
        w.Header().Set("Access-Control-Allow-Origin", "*")

        eventChan := make(chan SSEEvent)
        sseBroker.newClients <- eventChan

        // Küldjük el az aktuális állapotot minden forrásról a csatlakozáskor
        stateMutex.RLock()
        for idx, running := range watchdogStates {
            eventChan <- SSEEvent{Source: idx, Running: running}
        }
        stateMutex.RUnlock()

        flusher, ok := w.(http.Flusher)
        if !ok {
            http.Error(w, "SSE not supported", http.StatusInternalServerError)
            return
        }

        ctx := r.Context()
        for {
            select {
            case event := <-eventChan:
                data, _ := json.Marshal(event)
                fmt.Fprintf(w, "data: %s\n\n", data)
                flusher.Flush()
            case <-ctx.Done():
                sseBroker.closingClients <- eventChan
                return
            }
        }
    })

    http.HandleFunc("/api/start_watchdog", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
            http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
            return
        }
        var data map[string]interface{}
        if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
            http.Error(w, "Invalid JSON", http.StatusBadRequest)
            return
        }
        sourceIdx, ok := data["source"].(float64)
        if !ok {
            http.Error(w, "Missing source parameter", http.StatusBadRequest)
            return
        }
        if err := startWatchdogForSource(int(sourceIdx)); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
    })

    http.HandleFunc("/api/stop_watchdog", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
            http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
            return
        }
        var data map[string]interface{}
        if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
            http.Error(w, "Invalid JSON", http.StatusBadRequest)
            return
        }
        sourceIdx, ok := data["source"].(float64)
        if !ok {
            http.Error(w, "Missing source parameter", http.StatusBadRequest)
            return
        }
        if err := stopWatchdogForSource(int(sourceIdx)); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
    })

    log.Printf("IVERBS %s starting on :5000", version)
    log.Fatal(http.ListenAndServe(":5000", nil))
}
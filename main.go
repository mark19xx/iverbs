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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "github.com/mattn/go-sqlite3"
)

const Version = "0.3.2"

var (
	// Környezeti változók
	watchDirsStr    = getEnv("WATCH_DIRS", "/home/user")
	watchdogDelayMs = getEnvInt("WATCHDOG_DELAY_MS", 300)
	dbPath          = getEnv("DB_PATH", "/data/db/iverbs.db")

	watchDirs []string

	// SQLite kapcsolat
	db *sql.DB

	// Memória cache az EXIF státuszhoz
	exifCache  = make(map[string]bool)
	cacheMutex sync.RWMutex

	// Batch feladatok kezelése
	tasks      = make(map[int]*BatchTask)
	taskMutex  sync.RWMutex
	nextTaskID = 1

	// Watchdog vezérlés
	watcher *fsnotify.Watcher
)

// BatchTask egy háttérben futó kötegelt műveletet reprezentál
type BatchTask struct {
	ID        int
	Total     int
	Processed int
	Status    string // "running", "completed"
	mu        sync.Mutex
}

// Fájl adatstruktúra a listázáshoz
type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`     // relatív útvonal a source gyökerétől
	AbsPath string `json:"abs_path"` // abszolút útvonal (belső használatra, most exportálva)
	EstDate string `json:"est_date"` // becsült dátum a fájlnévből (YYYY:MM:DD HH:MM:SS vagy üres)
	HasExif bool   `json:"has_exif"`
}

// API válasz a böngészéshez
type BrowseResponse struct {
	Files []FileInfo `json:"files"`
	Total int        `json:"total"`
}

// EXIF válasz
type ExifResponse struct {
	ExifDate *string `json:"exif_date"` // null ha nincs
}

// Fix kérés body
type FixRequest struct {
	File      string `json:"file"`
	Date      string `json:"date,omitempty"`
	Overwrite *bool  `json:"overwrite,omitempty"`
}

// Batch fix kérés (több fájl)
type BatchFixFilesRequest struct {
	Files     []string `json:"files"`
	Overwrite bool     `json:"overwrite"`
}

// Batch fix kérés (mappa)
type BatchFixPathRequest struct {
	Path      string `json:"path"`
	Overwrite bool   `json:"overwrite"`
}

// API válasz task ID-val
type TaskResponse struct {
	TaskID int `json:"task_id"`
}

// Task progress válasz
type TaskProgressResponse struct {
	Total     int    `json:"total"`
	Processed int    `json:"processed"`
	Status    string `json:"status"`
}

func main() {
	// Ellenőrizzük, hogy a szükséges programok elérhetők-e
	checkDependencies()

	// Környezet előkészítése
	watchDirs = strings.Split(watchDirsStr, ",")
	for i := range watchDirs {
		watchDirs[i] = strings.TrimSpace(watchDirs[i])
		if watchDirs[i] == "" {
			continue
		}
		// Ellenőrizzük, hogy a könyvtár létezik-e
		if _, err := os.Stat(watchDirs[i]); os.IsNotExist(err) {
			log.Fatalf("Watch directory does not exist: %s", watchDirs[i])
		}
	}

	// SQLite inicializálása
	if err := initDB(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Cache betöltése az adatbázisból
	if err := loadCacheFromDB(); err != nil {
		log.Printf("Warning: failed to load cache from DB: %v", err)
	}

	// Watchdog indítása (mindig aktív)
	if err := startWatchdog(); err != nil {
		log.Fatalf("Failed to start watchdog: %v", err)
	}
	defer watcher.Close()

	// Cache tisztítás óránként
	go cacheCleanupLoop()

	// HTTP szerver útvonalak
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/api/sources", sourcesHandler)
	http.HandleFunc("/api/tree/", treeHandler)
	http.HandleFunc("/api/browse", browseHandler)
	http.HandleFunc("/api/image/", imageHandler)
	http.HandleFunc("/api/exif", exifHandler)
	http.HandleFunc("/api/fix", fixHandler)
	http.HandleFunc("/api/batch_fix", batchFixHandler)
	http.HandleFunc("/api/batch_fix_path", batchFixPathHandler)
	http.HandleFunc("/api/task/", taskProgressHandler)

	port := getEnv("PORT", "8080")
	log.Printf("IVERBS %s starting on port %s", Version, port)
	log.Printf("Watching directories: %v", watchDirs)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func checkDependencies() {
	// Ellenőrizzük, hogy az exiftool elérhető-e
	if _, err := exec.LookPath("exiftool"); err != nil {
		log.Fatal("exiftool not found in PATH")
	}
}

// initDB létrehozza az adatbázist és a táblát, ha nem létezik
func initDB() error {
	// Biztosítjuk, hogy a könyvtár létezik
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return err
	}

	var err error
	db, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS exif_cache (
		file_path TEXT PRIMARY KEY,
		has_exif INTEGER NOT NULL
	)`)
	if err != nil {
		return err
	}

	return nil
}

// loadCacheFromDB betölti az összes rekordot a memória cache-be
func loadCacheFromDB() error {
	rows, err := db.Query("SELECT file_path, has_exif FROM exif_cache")
	if err != nil {
		return err
	}
	defer rows.Close()

	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	for rows.Next() {
		var path string
		var hasExif int
		if err := rows.Scan(&path, &hasExif); err != nil {
			continue
		}
		exifCache[path] = hasExif != 0
	}
	return nil
}

// setExifCache frissíti a memória cache-t és az adatbázist
func setExifCache(filePath string, hasExif bool) {
	cacheMutex.Lock()
	exifCache[filePath] = hasExif
	cacheMutex.Unlock()

	// Adatbázis frissítése (upsert)
	_, err := db.Exec(`INSERT OR REPLACE INTO exif_cache (file_path, has_exif) VALUES (?, ?)`,
		filePath, boolToInt(hasExif))
	if err != nil {
		log.Printf("DB error setting cache for %s: %v", filePath, err)
	}
}

// deleteExifCache törli a bejegyzést
func deleteExifCache(filePath string) {
	cacheMutex.Lock()
	delete(exifCache, filePath)
	cacheMutex.Unlock()

	_, err := db.Exec("DELETE FROM exif_cache WHERE file_path = ?", filePath)
	if err != nil {
		log.Printf("DB error deleting cache for %s: %v", filePath, err)
	}
}

// getExifCache visszaadja a cache-elt értéket és hogy létezik-e
func getExifCache(filePath string) (hasExif bool, found bool) {
	cacheMutex.RLock()
	defer cacheMutex.RUnlock()
	has, ok := exifCache[filePath]
	return has, ok
}

// hasExifWithCache ellenőrzi az EXIF meglétét, cache segítségével
func hasExifWithCache(filePath string) bool {
	if has, found := getExifCache(filePath); found {
		return has
	}
	// Ha nincs cache-elve, lekérjük exiftool-al
	has := checkExifExists(filePath)
	setExifCache(filePath, has)
	return has
}

// checkExifExists meghívja az exiftool-t
func checkExifExists(filePath string) bool {
	cmd := exec.Command("exiftool", "-s3", "-DateTimeOriginal", filePath)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

// getExifDate lekéri a DateTimeOriginal értéket
func getExifDate(filePath string) (string, error) {
	cmd := exec.Command("exiftool", "-s3", "-DateTimeOriginal", filePath)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// extractDateFromFilename próbál dátumot kinyerni a fájlnévből
func extractDateFromFilename(filename string) string {
	base := filepath.Base(filename)
	// Különböző regex minták
	patterns := []struct {
		re     *regexp.Regexp
		layout string
	}{
		// Unix timestamp (10 vagy 13 számjegy)
		{regexp.MustCompile(`(\d{10,13})`), "unix"},
		// YYYYMMDD_HHMMSS
		{regexp.MustCompile(`(\d{8})_(\d{6})`), "20060102_150405"},
		// YYYY-MM-DD HH.MM.SS
		{regexp.MustCompile(`(\d{4}-\d{2}-\d{2})[\s._-](\d{2}\.\d{2}\.\d{2})`), "2006-01-02_15.04.05"},
		// YYYYMMDD
		{regexp.MustCompile(`(\d{8})`), "20060102"},
		// YYYY-MM-DD
		{regexp.MustCompile(`(\d{4}-\d{2}-\d{2})`), "2006-01-02"},
	}

	for _, p := range patterns {
		matches := p.re.FindStringSubmatch(base)
		if len(matches) == 0 {
			continue
		}
		if p.layout == "unix" {
			ts := matches[1]
			var sec int64
			if len(ts) == 13 {
				// milliszekundum
				ms, _ := strconv.ParseInt(ts, 10, 64)
				sec = ms / 1000
			} else {
				sec, _ = strconv.ParseInt(ts, 10, 64)
			}
			t := time.Unix(sec, 0)
			return t.Format("2006:01:02 15:04:05")
		} else if p.layout == "20060102_150405" {
			datePart := matches[1]
			timePart := matches[2]
			t, err := time.Parse("20060102150405", datePart+timePart)
			if err == nil {
				return t.Format("2006:01:02 15:04:05")
			}
		} else if p.layout == "2006-01-02_15.04.05" {
			datePart := matches[1]
			timePart := matches[2]
			t, err := time.Parse("2006-01-0215.04.05", datePart+timePart)
			if err == nil {
				return t.Format("2006:01:02 15:04:05")
			}
		} else if p.layout == "20060102" {
			t, err := time.Parse("20060102", matches[1])
			if err == nil {
				return t.Format("2006:01:02 15:04:05")
			}
		} else if p.layout == "2006-01-02" {
			t, err := time.Parse("2006-01-02", matches[1])
			if err == nil {
				return t.Format("2006:01:02 15:04:05")
			}
		}
	}
	return ""
}

// setExifDate beállítja az EXIF dátumokat a fájlban
func setExifDate(filePath, dateTime string) error {
	// exiftool -overwrite_original -DateTimeOriginal=... -CreateDate=... -ModifyDate=...
	cmd := exec.Command("exiftool", "-overwrite_original",
		"-DateTimeOriginal="+dateTime,
		"-CreateDate="+dateTime,
		"-ModifyDate="+dateTime,
		filePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("exiftool error: %v, output: %s", err, string(output))
	}
	// Frissítsük a cache-t
	setExifCache(filePath, true)
	return nil
}

// fixFileFromFilename megpróbálja a fájlnévből kinyert dátumot beírni, ha nincs EXIF vagy overwrite=true
func fixFileFromFilename(filePath string, overwrite bool) error {
	// Ellenőrizzük, hogy egyáltalán média fájl-e (egyszerű kiterjesztés ellenőrzés)
	ext := strings.ToLower(filepath.Ext(filePath))
	mediaExts := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true, ".tiff": true,
		".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".m4v": true, ".3gp": true,
		".heic": true, ".heif": true, ".webp": true,
	}
	if !mediaExts[ext] {
		return fmt.Errorf("not a supported media file")
	}

	if !overwrite {
		if hasExifWithCache(filePath) {
			return nil // már van EXIF, nincs felülírás
		}
	}

	dateStr := extractDateFromFilename(filepath.Base(filePath))
	if dateStr == "" {
		return fmt.Errorf("could not extract date from filename")
	}

	// Ellenőrizzük, hogy a dátum formátuma megfelelő-e (YYYY:MM:DD HH:MM:SS)
	if _, err := time.Parse("2006:01:02 15:04:05", dateStr); err != nil {
		return fmt.Errorf("invalid date format extracted: %s", dateStr)
	}

	return setExifDate(filePath, dateStr)
}

// cacheCleanupLoop óránként lefut és eltávolítja a nem létező fájlokat a cache-ből
func cacheCleanupLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		cacheMutex.RLock()
		paths := make([]string, 0, len(exifCache))
		for p := range exifCache {
			paths = append(paths, p)
		}
		cacheMutex.RUnlock()

		for _, p := range paths {
			if _, err := os.Stat(p); os.IsNotExist(err) {
				deleteExifCache(p)
			}
		}
	}
}

// --- Watchdog ---
func startWatchdog() error {
	var err error
	watcher, err = fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	// Rekurzívan figyeljük az összes watch könyvtárat
	for _, dir := range watchDirs {
		if err := addWatchRecursive(dir); err != nil {
			log.Printf("Warning: could not watch %s: %v", dir, err)
		}
	}

	go watchdogLoop()
	return nil
}

func addWatchRecursive(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // hiba esetén folytatjuk
		}
		if info.IsDir() {
			if err := watcher.Add(path); err != nil {
				log.Printf("Failed to watch directory %s: %v", path, err)
			}
		}
		return nil
	})
}

func watchdogLoop() {
	delay := time.Duration(watchdogDelayMs) * time.Millisecond
	// Események feldolgozása rate limitinggel
	eventQueue := make(chan fsnotify.Event, 100)
	processTicker := time.NewTicker(delay)
	defer processTicker.Stop()

	// Goroutine az események fogadására
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				eventQueue <- event
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("Watchdog error: %v", err)
			}
		}
	}()

	// Feldolgozás ütemezetten
	for {
		select {
		case <-processTicker.C:
			// Feldolgozzuk az összes várakozó eseményt (de-duplikációval)
			events := make(map[string]fsnotify.Event)
			// Kiolvassuk a queue-t amíg van
			done := false
			for !done {
				select {
				case ev := <-eventQueue:
					events[ev.Name] = ev
				default:
					done = true
				}
			}
			for _, ev := range events {
				handleWatchdogEvent(ev)
			}
		}
	}
}

func handleWatchdogEvent(event fsnotify.Event) {
	// Csak fájlokkal foglalkozunk (CREATE, WRITE, REMOVE)
	if event.Op&fsnotify.Create == fsnotify.Create || event.Op&fsnotify.Write == fsnotify.Write {
		// Ha könyvtár jött létre, adjuk hozzá a figyeléshez
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			watcher.Add(event.Name)
			return
		}
		// Kis késleltetés, hogy a fájl teljesen kiíródjon
		time.Sleep(500 * time.Millisecond)
		// Ellenőrizzük és javítjuk
		if hasExifWithCache(event.Name) {
			return
		}
		// Próbáljuk megjavítani
		if err := fixFileFromFilename(event.Name, false); err != nil {
			log.Printf("Watchdog fix failed for %s: %v", event.Name, err)
		} else {
			log.Printf("Watchdog fixed: %s", event.Name)
		}
	} else if event.Op&fsnotify.Remove == fsnotify.Remove {
		// Törlés a cache-ből
		deleteExifCache(event.Name)
	}
}

// --- HTTP Handlerek ---

func indexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFiles("templates/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Csak a forrás neveket adjuk át (a tabokhoz)
	sourceNames := make([]string, len(watchDirs))
	for i, d := range watchDirs {
		sourceNames[i] = filepath.Base(d)
	}
	data := struct {
		Version string
		Sources []string
	}{
		Version: Version,
		Sources: sourceNames,
	}
	w.Header().Set("Content-Type", "text/html")
	tmpl.Execute(w, data)
}

func sourcesHandler(w http.ResponseWriter, r *http.Request) {
	sourceNames := make([]string, len(watchDirs))
	for i, d := range watchDirs {
		sourceNames[i] = filepath.Base(d)
	}
	json.NewEncoder(w).Encode(sourceNames)
}

func treeHandler(w http.ResponseWriter, r *http.Request) {
	// /api/tree/{source_idx}
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tree/"), "/")
	if len(pathParts) < 1 {
		http.Error(w, "Missing source index", http.StatusBadRequest)
		return
	}
	sourceIdx, err := strconv.Atoi(pathParts[0])
	if err != nil || sourceIdx < 0 || sourceIdx >= len(watchDirs) {
		http.Error(w, "Invalid source index", http.StatusBadRequest)
		return
	}
	root := watchDirs[sourceIdx]

	// Csak a közvetlen alkönyvtárak listája
	entries, err := os.ReadDir(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dirs := []string{}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	json.NewEncoder(w).Encode(dirs)
}

func browseHandler(w http.ResponseWriter, r *http.Request) {
	// Paraméterek: source, path, limit, offset, missing_only
	sourceIdx, _ := strconv.Atoi(r.URL.Query().Get("source"))
	relPath := r.URL.Query().Get("path")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	missingOnly, _ := strconv.ParseBool(r.URL.Query().Get("missing_only"))

	if sourceIdx < 0 || sourceIdx >= len(watchDirs) {
		http.Error(w, "Invalid source", http.StatusBadRequest)
		return
	}
	root := watchDirs[sourceIdx]
	fullPath := filepath.Join(root, relPath)

	// Ellenőrizzük, hogy a fullPath a root alatt van-e
	if !strings.HasPrefix(fullPath, root) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	// Fájlok listázása
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var files []FileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Csak támogatott kiterjesztések?
		absPath := filepath.Join(fullPath, e.Name())
		hasExif := hasExifWithCache(absPath)
		if missingOnly && hasExif {
			continue
		}
		estDate := extractDateFromFilename(e.Name())
		relFilePath, _ := filepath.Rel(root, absPath)
		files = append(files, FileInfo{
			Name:    e.Name(),
			Path:    relFilePath,
			AbsPath: absPath,
			EstDate: estDate,
			HasExif: hasExif,
		})
	}

	total := len(files)

	// Lapozás
	start := offset
	end := offset + limit
	if start > len(files) {
		start = len(files)
	}
	if end > len(files) {
		end = len(files)
	}
	pagedFiles := files[start:end]

	resp := BrowseResponse{
		Files: pagedFiles,
		Total: total,
	}
	json.NewEncoder(w).Encode(resp)
}

func imageHandler(w http.ResponseWriter, r *http.Request) {
	// /api/image/{source_idx}/{rel_path...}
	path := strings.TrimPrefix(r.URL.Path, "/api/image/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	sourceIdx, err := strconv.Atoi(parts[0])
	if err != nil || sourceIdx < 0 || sourceIdx >= len(watchDirs) {
		http.Error(w, "Invalid source", http.StatusBadRequest)
		return
	}
	relPath := parts[1]
	root := watchDirs[sourceIdx]
	absPath := filepath.Join(root, relPath)

	// Biztonsági ellenőrzés
	if !strings.HasPrefix(absPath, root) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	http.ServeFile(w, r, absPath)
}

func exifHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("file")
	if filePath == "" {
		http.Error(w, "Missing file parameter", http.StatusBadRequest)
		return
	}
	// Ellenőrizzük, hogy a fájl valamelyik watch könyvtár alatt van-e
	valid := false
	for _, root := range watchDirs {
		if strings.HasPrefix(filePath, root) {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	date, err := getExifDate(filePath)
	resp := ExifResponse{}
	if err == nil && date != "" {
		resp.ExifDate = &date
	} else {
		resp.ExifDate = nil
	}
	json.NewEncoder(w).Encode(resp)
}

func fixHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req FixRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Ellenőrizzük, hogy a fájl a watch könyvtárak alatt van
	valid := false
	for _, root := range watchDirs {
		if strings.HasPrefix(req.File, root) {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if req.Date != "" {
		// Manuális dátum (YYYY-MM-DD)
		// Átalakítjuk EXIF formátumra
		t, err := time.Parse("2006-01-02", req.Date)
		if err != nil {
			http.Error(w, "Invalid date format, use YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		exifDate := t.Format("2006:01:02 15:04:05")
		if err := setExifDate(req.File, exifDate); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		overwrite := false
		if req.Overwrite != nil {
			overwrite = *req.Overwrite
		}
		if err := fixFileFromFilename(req.File, overwrite); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func batchFixHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req BatchFixFilesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	taskID := createBatchTask(len(req.Files))
	go executeBatchFix(taskID, req.Files, req.Overwrite)

	resp := TaskResponse{TaskID: taskID}
	json.NewEncoder(w).Encode(resp)
}

func batchFixPathHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req BatchFixPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	path := req.Path
	// Ha a path nem abszolút, akkor a source query paraméterből kiegészítjük
	if !filepath.IsAbs(path) {
		sourceIdxStr := r.URL.Query().Get("source")
		if sourceIdxStr == "" {
			http.Error(w, "source query parameter required for relative path", http.StatusBadRequest)
			return
		}
		sourceIdx, err := strconv.Atoi(sourceIdxStr)
		if err != nil || sourceIdx < 0 || sourceIdx >= len(watchDirs) {
			http.Error(w, "invalid source", http.StatusBadRequest)
			return
		}
		path = filepath.Join(watchDirs[sourceIdx], path)
	}

	// Ellenőrizzük, hogy az elérési út valamelyik watch könyvtár alatt van
	valid := false
	for _, root := range watchDirs {
		if strings.HasPrefix(path, root) {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Rekurzívan összegyűjtjük a fájlokat
	var files []string
	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			// Csak média fájlok
			ext := strings.ToLower(filepath.Ext(p))
			mediaExts := map[string]bool{
				".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true, ".tiff": true,
				".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".m4v": true, ".3gp": true,
				".heic": true, ".heif": true, ".webp": true,
			}
			if mediaExts[ext] {
				files = append(files, p)
			}
		}
		return nil
	})

	taskID := createBatchTask(len(files))
	go executeBatchFix(taskID, files, req.Overwrite)

	resp := TaskResponse{TaskID: taskID}
	json.NewEncoder(w).Encode(resp)
}

func taskProgressHandler(w http.ResponseWriter, r *http.Request) {
	// /api/task/{task_id}/progress
	path := strings.TrimPrefix(r.URL.Path, "/api/task/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "progress" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	taskID, err := strconv.Atoi(parts[0])
	if err != nil {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	taskMutex.RLock()
	task, ok := tasks[taskID]
	taskMutex.RUnlock()
	if !ok {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	task.mu.Lock()
	resp := TaskProgressResponse{
		Total:     task.Total,
		Processed: task.Processed,
		Status:    task.Status,
	}
	task.mu.Unlock()

	json.NewEncoder(w).Encode(resp)
}

// --- Batch feladatkezelés ---

func createBatchTask(total int) int {
	taskMutex.Lock()
	defer taskMutex.Unlock()
	id := nextTaskID
	nextTaskID++
	task := &BatchTask{
		ID:     id,
		Total:  total,
		Status: "running",
	}
	tasks[id] = task
	return id
}

func executeBatchFix(taskID int, files []string, overwrite bool) {
	taskMutex.RLock()
	task := tasks[taskID]
	taskMutex.RUnlock()
	if task == nil {
		return
	}

	for i, f := range files {
		// Ellenőrizzük, hogy a fájl létezik-e
		if _, err := os.Stat(f); os.IsNotExist(err) {
			// nem létezik, növeljük a feldolgozott számlálót és folytatjuk
			task.mu.Lock()
			task.Processed = i + 1
			task.mu.Unlock()
			continue
		}
		if err := fixFileFromFilename(f, overwrite); err != nil {
			log.Printf("Batch fix error for %s: %v", f, err)
		}
		task.mu.Lock()
		task.Processed = i + 1
		task.mu.Unlock()
		// Kis szünet, hogy ne terheljük túl
		time.Sleep(50 * time.Millisecond)
	}

	task.mu.Lock()
	task.Status = "completed"
	task.mu.Unlock()

	// Tisztítás: 5 perc után törölhetnénk, de most bent hagyjuk
}

// --- Segédfüggvények ---

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
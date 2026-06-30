package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"embed"

	"gopkg.in/yaml.v3"
)

//go:embed templates
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

const (
	KB = 1024
	MB = 1024 * KB
	GB = 1024 * MB
	TB = 1024 * GB
)

type Config struct {
	Dir  string `yaml:"base-dir"`
	Host string `yaml:"hostname"`
	Port uint16 `yaml:"port"`
}

type FileEntry struct {
	Name        string
	IsDir       bool
	Size        int64
	SizeHuman   string
	NoOfContent string
	Icon        string
}

type Crumb struct {
	Name string
	Path string
}

type Data struct {
	Files  []FileEntry
	Crumbs []Crumb
}

var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func main() {
	config, err := getConfig()
	if err != nil {
		fmt.Printf("Unable to load config: %s\n", err)
		os.Exit(1)
	}

	if config.Host == "" {
		config.Host = "localhost"
	}
	if config.Port == 0 {
		config.Port = 8080
	}
	if config.Dir == "" {
		config.Dir = "."
	}

	serveRoot, err := filepath.Abs(config.Dir)
	if err != nil {
		fmt.Printf("Invalid directory (%s): %s\n", config.Dir, err)
		os.Exit(1)
	}

	tmpl, err := template.ParseFS(templateFiles, "templates/index.html")
	if err != nil {
		fmt.Printf("Unable to parse template: %s\n", err)
		os.Exit(1)
	}

	staticSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		fmt.Printf("Unable to create static sub FS: %s\n", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", homePage(serveRoot, tmpl))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		fmt.Printf("Starting server...\n")
		fmt.Printf("Server running on http://%s:%d/\n", config.Host, config.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Server error: %s\n", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	fmt.Println("Shutting down, waiting for in-flight requests...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		fmt.Printf("Graceful shutdown failed: %s\n", err)
	}
	fmt.Println("Server stopped")
}

func getConfig() (Config, error) {
	configPath := flag.String("c", "./config.yaml", "Location of the config file")
	flag.Parse()

	var config Config
	configData, err := os.ReadFile(*configPath)
	if err != nil {
		return config, err
	}
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return config, fmt.Errorf("parsing config: %w", err)
	}
	return config, nil
}

func iconClass(name string, isDir bool) string {
	if isDir {
		return "dir"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".bmp", ".ico":
		return "img"
	case ".mp4", ".mkv", ".mov", ".avi", ".webm", ".flv":
		return "vid"
	case ".mp3", ".flac", ".wav", ".ogg", ".aac", ".m4a":
		return "aud"
	case ".zip", ".tar", ".gz", ".bz2", ".rar", ".7z", ".xz":
		return "arc"
	case ".pdf", ".doc", ".docx", ".txt", ".md", ".odt", ".rtf":
		return "doc"
	default:
		return "gen"
	}
}

func getFiles(path string) ([]FileEntry, error) {
	dirContent, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	files := make([]FileEntry, 0, len(dirContent))
	for _, f := range dirContent {
		if strings.HasPrefix(f.Name(), ".") {
			continue
		}

		entry := FileEntry{
			Name:  f.Name(),
			IsDir: f.IsDir(),
			Icon:  iconClass(f.Name(), f.IsDir()),
		}

		if f.IsDir() {
			subEntries, err := os.ReadDir(filepath.Join(path, f.Name()))
			if err == nil {
				entry.NoOfContent = fmt.Sprintf("%d items", len(subEntries))
			}
		} else {
			info, err := f.Info()
			if err == nil {
				entry.Size = info.Size()
				entry.SizeHuman = humanSize(info.Size())
			}
		}

		files = append(files, entry)
	}
	return files, nil
}

func buildCrumbs(urlPath string) []Crumb {
	segments := strings.Split(strings.Trim(urlPath, "/"), "/")
	crumbs := make([]Crumb, 0, len(segments))
	for i, seg := range segments {
		if seg == "" {
			continue
		}
		crumbs = append(crumbs, Crumb{
			Name: seg,
			Path: "/" + strings.Join(segments[:i+1], "/") + "/",
		})
	}
	return crumbs
}

func homePage(serveRoot string, tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		requestedPath := filepath.Join(serveRoot, filepath.FromSlash(r.URL.Path))

		rel, err := filepath.Rel(serveRoot, requestedPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		info, err := os.Stat(requestedPath)
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "Not found", http.StatusNotFound)
			} else {
				fmt.Printf("Stat failed (%s): %s\n", requestedPath, err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			}
			return
		}

		if !info.IsDir() {
			http.ServeFile(w, r, requestedPath)
			return
		}

		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}

		files, err := getFiles(requestedPath)
		if err != nil {
			http.Error(w, "Could not read directory", http.StatusInternalServerError)
			return
		}

		data := Data{
			Files:  files,
			Crumbs: buildCrumbs(r.URL.Path),
		}

		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)

		if err := tmpl.Execute(buf, data); err != nil {
			fmt.Printf("Template error: %s\n", err)
			http.Error(w, "Template error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
		_, _ = buf.WriteTo(w)
	}
}

func humanSize(b int64) string {
	switch {
	case b < KB:
		return fmt.Sprintf("%d B", b)
	case b < MB:
		return fmt.Sprintf("%.1f KB", float64(b)/KB)
	case b < GB:
		return fmt.Sprintf("%.1f MB", float64(b)/MB)
	case b < TB:
		return fmt.Sprintf("%.1f GB", float64(b)/TB)
	default:
		return fmt.Sprintf("%.1f TB", float64(b)/TB)
	}
}

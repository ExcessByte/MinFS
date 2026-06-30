package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

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
}

type Data struct {
    Files  []FileEntry
    Crumbs []Crumb
}

type Crumb struct {
    Name string
    Path string
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

	tmpl, err := template.ParseFiles("./templates/index.html")
	if err != nil {
		fmt.Printf("Unable to parse template: %s\n", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", homePage(serveRoot, tmpl))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

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
		return config, fmt.Errorf("Parsing config: %w", err)
	}
	return config, nil
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
		}

		if f.IsDir() {
			subEntries, err := os.ReadDir(filepath.Join(path, f.Name()))
			if err == nil {
				entry.NoOfContent = fmt.Sprintf("%d Items", len(subEntries))
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

func homePage(serveRoot string, tmpl *template.Template) http.HandlerFunc {
	rootWithSep := serveRoot + string(filepath.Separator)

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
		_ = rootWithSep

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
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
crumbs := make([]Crumb, 0, len(segments))
for i, seg := range segments {
    if seg == "" {        // handles root "/" producing an empty segment
        continue
    }
    crumbs = append(crumbs, Crumb{
        Name: seg,
        Path: "/" + strings.Join(segments[:i+1], "/"),
    })
}

data := Data{
    Files:  files,
    Crumbs: crumbs,
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

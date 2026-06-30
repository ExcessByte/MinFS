package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const configFile = "config.yaml"

type Config struct {
	Dir  string `yaml:"dir"`
	Host string `yaml:"host"`
	Port uint16 `yaml:"port"`
}

func main() {
	config, err := getConfig()
	if err != nil {
		log.Fatalf("unable to load config: %v", err)
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
		log.Fatalf("invalid dir %q: %v", config.Dir, err)
	}

	tmpl, err := template.ParseFiles("./templates/index.html")
	if err != nil {
		log.Fatalf("unable to parse template: %v", err)
	}

	http.HandleFunc("/", homePage(serveRoot, tmpl))
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)
	fmt.Printf("Starting server at %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func getConfig() (Config, error) {
	var config Config
	configData, err := os.ReadFile(configFile)
	if err != nil {
		return config, err
	}
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return config, fmt.Errorf("parsing config: %w", err)
	}
	return config, nil
}

func getFiles(path string) ([]os.DirEntry, error) {
	dirContent, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	files := make([]os.DirEntry, 0, len(dirContent))
	for _, f := range dirContent {
		if strings.HasPrefix(f.Name(), ".") {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

func homePage(serveRoot string, tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestedPath := filepath.Join(serveRoot, filepath.FromSlash(r.URL.Path))
		if !strings.HasPrefix(requestedPath, serveRoot+string(filepath.Separator)) &&
			requestedPath != serveRoot {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		info, err := os.Stat(requestedPath)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		if !info.IsDir() {
			http.ServeFile(w, r, requestedPath)
			return
		}

		files, err := getFiles(requestedPath)
		if err != nil {
			http.Error(w, "could not read directory", http.StatusInternalServerError)
			return
		}

		if err := tmpl.Execute(w, files); err != nil {
			log.Printf("template error: %v", err)
		}
	}
}

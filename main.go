package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
)

const HOME_DIR string = "/home/eric"

var tmpl = template.Must(template.ParseFiles("./templates/index.html"))

func main() {
	http.HandleFunc("/", homePage)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func getFiles(path string) ([]os.DirEntry, error) {
    dir_content, err := os.ReadDir(path)
    if err != nil {
        return nil, err
    }
    files := make([]os.DirEntry, 0, len(dir_content))
    for _, f := range dir_content {
        if strings.HasPrefix(f.Name(), ".") {
            continue
        }
        files = append(files, f)
    }
    return files, nil
}

func homePage(w http.ResponseWriter, r *http.Request) {
	path := HOME_DIR + r.URL.Path

	info, err := os.Stat(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if !info.IsDir() {
		http.ServeFile(w, r, path)
		return
	}

	files, err := getFiles(path)
	if err != nil {
		http.Error(w, "could not read directory", http.StatusInternalServerError)
		return
	}

	tmpl.Execute(w, files)
}

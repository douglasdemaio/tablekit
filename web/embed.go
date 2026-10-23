package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var staticFiles embed.FS

func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") ||
			strings.HasSuffix(path, ".svg") || strings.HasSuffix(path, ".png") ||
			strings.HasSuffix(path, ".webp") || strings.HasSuffix(path, ".woff2") {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		fileServer.ServeHTTP(w, r)
	})
}

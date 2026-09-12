package edge

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dist holds the built web application. It is embedded rather than mounted so
// the edge image stays a single static binary with nothing to go missing at
// runtime — a deployment cannot end up serving an API with no UI in front of
// it, or a UI built from a different commit than the API it talks to.
//
// The directory is produced by `npm run build` in web/ and copied in by
// deploy/docker/Dockerfile.edge. The `all:` prefix keeps files Go would
// otherwise skip, such as Vite's hashed assets beginning with an underscore.
//
//go:embed all:dist
var dist embed.FS

// WebHandler serves the built application, or reports plainly that this build
// has none. A `go build` outside the image has an empty dist/ — saying so is
// better than serving a blank page that looks like a broken app.
func WebHandler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return notBuilt()
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return notBuilt()
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The application routes client-side, so a deep link such as
		// /runs/platform/7 is a request the server has no file for and must
		// answer with the application itself. Anything that looks like a
		// built asset is served as a file, and a miss there is a real 404
		// rather than an index.html with the wrong content type.
		if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}

func notBuilt() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(
			"this edge binary was built without the web application\n" +
				"build it with: cd web && npm ci && npm run build\n"))
	})
}

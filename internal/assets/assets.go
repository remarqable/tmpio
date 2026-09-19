// Package assets embeds templates and static files into the binary.
package assets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:views
var viewsFS embed.FS

//go:embed all:static
var staticFS embed.FS

var dev bool

// Version is a short hash of the embedded static files, used to bust caches
// (/static/css/site.css?v=<Version>) so a one-day cache never serves stale CSS.
var Version = computeVersion()

func computeVersion() string {
	h := sha256.New()
	_ = fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}

// SetDev switches to filesystem loading (hot reload) in development.
func SetDev(on bool) { dev = on }

// StaticFS returns the static file system.
func StaticFS() fs.FS {
	if dev {
		return os.DirFS("internal/assets/static")
	}
	sub, _ := fs.Sub(staticFS, "static")
	return sub
}

// Templates parses every page under views/pages together with layouts and
// partials, returning one template set per page keyed by "pages/<name>.html".
func Templates(funcs template.FuncMap) (map[string]*template.Template, error) {
	var root fs.FS
	if dev {
		root = os.DirFS("internal/assets/views")
	} else {
		sub, err := fs.Sub(viewsFS, "views")
		if err != nil {
			return nil, err
		}
		root = sub
	}
	shared := []string{}
	for _, dir := range []string{"layouts", "partials"} {
		entries, err := fs.ReadDir(root, dir)
		if err != nil {
			return nil, fmt.Errorf("views/%s: %w", dir, err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".html") {
				shared = append(shared, dir+"/"+e.Name())
			}
		}
	}
	out := map[string]*template.Template{}
	err := fs.WalkDir(root, "pages", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		t := template.New(filepath.Base(p)).Funcs(funcs)
		for _, s := range shared {
			b, err := fs.ReadFile(root, s)
			if err != nil {
				return err
			}
			if _, err := t.New(s).Parse(string(b)); err != nil {
				return fmt.Errorf("%s: %w", s, err)
			}
		}
		b, err := fs.ReadFile(root, p)
		if err != nil {
			return err
		}
		if _, err := t.New(p).Parse(string(b)); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		out[p] = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

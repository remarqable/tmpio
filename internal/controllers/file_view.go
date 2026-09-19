package controllers

import (
	"encoding/csv"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/models"
)

// maxCSVRows bounds the table viewer; the raw file is always available.
const maxCSVRows = 1000

// serveFile serves a text file: the raw bytes to agents and non-browser
// requests (or ?raw=1), and a read-only viewer inside the shell for browser navigation.
func (d *Deps) serveFile(c *gin.Context, a *addr, e *models.Entry) {
	doc, err := d.Ops.Read(c.Request.Context(), a.Prin, e.Path, 0)
	if err != nil {
		d.fail(c, err)
		return
	}
	if !isNavigation(c) || c.Query("raw") == "1" {
		c.Header("ETag", etag(doc.Entry))
		c.Header("X-Robots-Tag", "noindex, nofollow, noarchive")
		mime := models.FileMIME(e.Path)
		if strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "application/yaml" || mime == "application/xml" || mime == "application/toml" || mime == "application/sql" {
			mime += "; charset=utf-8"
		}
		if c.Query("raw") == "1" {
			c.Header("Content-Disposition", "attachment; filename=\""+e.Name()+"\"")
		}
		c.Data(http.StatusOK, mime, []byte(doc.Source))
		return
	}
	site, err := d.siteModel(c, a)
	if err != nil {
		d.fail(c, err)
		return
	}
	site.Mode = "site"
	site.Entry = e
	site.Title = e.Name()
	site.CurrentDir = parentDir(e.Path)
	site.CurrentHTML = a.Prefix + e.Path
	site.Breadcrumbs = breadcrumbs(a.Prefix, e.Path, false)
	data := gin.H{"Site": site, "Source": doc.Source, "Lang": langOf(e.Path), "Bytes": len(doc.Source), "Lines": strings.Count(doc.Source, "\n") + 1, "Saved": c.Query("saved")}
	ext := path.Ext(e.Path)
	if ext == ".csv" || ext == ".tsv" {
		r := csv.NewReader(strings.NewReader(doc.Source))
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		if ext == ".tsv" {
			r.Comma = '\t'
		}
		var rows [][]string
		truncated := false
		for {
			rec, err := r.Read()
			if err != nil {
				break
			}
			if len(rows) >= maxCSVRows {
				truncated = true
				break
			}
			rows = append(rows, rec)
		}
		if len(rows) > 0 {
			data["Header"], data["Rows"], data["Truncated"] = rows[0], rows[1:], truncated
		}
	}
	c.Header("ETag", etag(e))
	d.render(c, http.StatusOK, "pages/site/file.html", "layout/site", data)
}

// langOf maps a file extension to a code-block language class.
func langOf(p string) string {
	switch path.Ext(p) {
	case ".yml":
		return "yaml"
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".sh":
		return "bash"
	case ".txt", ".log", ".ini":
		return "text"
	default:
		return strings.TrimPrefix(path.Ext(p), ".")
	}
}

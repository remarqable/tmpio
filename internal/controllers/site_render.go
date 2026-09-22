package controllers

import (
	"context"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/render"
)

// Crumb is one breadcrumb.
type Crumb struct {
	Title string
	URL   string
	Last  bool
}

// NavNode is one sidebar tree node.
type NavNode struct {
	Title    string
	URL      string
	Path     string
	Kind     string
	Children []*NavNode
	Active   bool
	Open     bool
	Order    *int

	// Columns the directory listing shows. A directory carries the newest
	// timestamp of anything beneath it, so a folder row still says something.
	Revision int64
	Size     int64
	Updated  time.Time

	// Pages beneath a directory, so a collapsed folder can say how much it is
	// hiding rather than making you open it to find out.
	Count int
}

// RenderedPage is the view model of a rendered document.
type RenderedPage struct {
	Title       string
	Description string
	Tags        []string
	HTML        template.HTML
	TOC         []render.TOCEntry
	Revision    int64
	UpdatedAt   string
	Warnings    []string
	RawURL      string
	JSONURL     string
}

// SiteView is everything the site layout needs.
type SiteView struct {
	Name        string
	Config      *models.SiteConfig
	Prefix      string
	OrgCode     string
	Title       string
	Nav         []models.NavItem
	Sidebar     []*NavNode
	Breadcrumbs []Crumb
	Page        *RenderedPage
	Entry       *models.Entry
	Directory   *models.Entry
	Listing     []*NavNode
	CurrentHTML string
	Query       string
	Search      *models.SearchPage
	IsOwner     bool
	Shared      bool
	ShareBase   string
	Mode        string    // site | ops | admin
	Wide        bool      // operation and admin pages use the full width
	AdminNav    []NavLink // admin sections (Mode == admin)
	CurrentDir  string    // directory the owner tools act on
	Folders     []string  // every directory path, for the move field
}

// siteModel loads config and the sidebar tree for a tenant in one address form.
func (d *Deps) siteModel(c *gin.Context, a *addr) (*SiteView, error) {
	cfg, _, err := d.Ops.SiteConfigFor(c.Request.Context(), a.Tenant.ID)
	if err != nil {
		return nil, err
	}
	sv := &SiteView{Config: cfg, Prefix: a.Prefix, OrgCode: a.Tenant.Code, Name: siteName(cfg, a.Tenant), IsOwner: a.Prin.IsOwnerSession()}
	for _, n := range cfg.Navigation {
		sv.Nav = append(sv.Nav, models.NavItem{Title: n.Title, Path: a.Prefix + n.Path})
	}
	if cfg.Sidebar.Auto {
		entries, err := d.Ops.Tree(c.Request.Context(), a.Tenant.ID, false)
		if err != nil {
			return nil, err
		}
		sv.Sidebar = buildTree(entries, a.Prefix)
	}
	return sv, nil
}

// buildTree turns the flat entry list into an ordered hierarchy: overview
// first, then children by order, then title/path. index.md is folded into its directory.
func buildTree(entries []models.Entry, prefix string) []*NavNode {
	byPath := map[string]*NavNode{}
	root := &NavNode{Path: "/", Kind: models.KindDirectory}
	byPath["/"] = root
	for i := range entries {
		e := &entries[i]
		if e.Kind == models.KindDirectory {
			if e.Path == "/" {
				continue
			}
			n := &NavNode{Title: e.Title, Path: e.Path, Kind: e.Kind, Order: e.OrderIndex, URL: "", Revision: e.CurrentRevision, Updated: e.UpdatedAt}
			if n.Title == "" {
				n.Title = e.Name()
			}
			byPath[e.Path] = n
		}
	}
	for i := range entries {
		e := &entries[i]
		switch e.Kind {
		case models.KindDirectory:
			if e.Path == "/" {
				continue
			}
			parent := parentDir(e.Path)
			if p, ok := byPath[parent]; ok {
				p.Children = append(p.Children, byPath[e.Path])
			}
		case models.KindPage:
			dir := parentDir(e.Path)
			p, ok := byPath[dir]
			if !ok {
				continue
			}
			if e.IsIndexPage() {
				p.Title = e.Title
				p.URL = prefix + e.HTMLPath()
				p.Order = e.OrderIndex
				p.Revision = e.CurrentRevision
				p.Size = e.SizeBytes
				continue
			}
			p.Children = append(p.Children, &NavNode{Title: e.Title, URL: prefix + e.HTMLPath(), Path: e.Path, Kind: e.Kind, Order: e.OrderIndex, Revision: e.CurrentRevision, Size: e.SizeBytes, Updated: e.UpdatedAt})
		case models.KindFile:
			if p, ok := byPath[parentDir(e.Path)]; ok {
				p.Children = append(p.Children, &NavNode{Title: e.Name(), URL: prefix + e.Path, Path: e.Path, Kind: e.Kind, Revision: e.CurrentRevision, Size: e.SizeBytes, Updated: e.UpdatedAt})
			}
		case models.KindAsset:
			if strings.HasSuffix(e.Path, ".pdf") {
				if p, ok := byPath[parentDir(e.Path)]; ok {
					p.Children = append(p.Children, &NavNode{Title: e.Name(), URL: prefix + e.Path, Path: e.Path, Kind: e.Kind, Revision: e.CurrentRevision, Size: e.SizeBytes, Updated: e.UpdatedAt})
				}
			}
		}
	}
	var sortNodes func(n *NavNode)
	sortNodes = func(n *NavNode) {
		sort.SliceStable(n.Children, func(i, j int) bool {
			a, b := n.Children[i], n.Children[j]
			ao, bo := orderKey(a.Order), orderKey(b.Order)
			if ao != bo {
				return ao < bo
			}
			if !strings.EqualFold(a.Title, b.Title) {
				return strings.ToLower(a.Title) < strings.ToLower(b.Title)
			}
			return a.Path < b.Path
		})
		for _, ch := range n.Children {
			sortNodes(ch)
		}
	}
	sortNodes(root)
	prune(root)
	rollUp(root)
	countLeaves(root)
	return root.Children
}

// nodeHasCurrent reports whether this node is the current page or holds it
// somewhere beneath.
func nodeHasCurrent(n *NavNode, current string) bool {
	if current == "" || n == nil {
		return false
	}
	if n.URL == current {
		return true
	}
	for _, ch := range n.Children {
		if nodeHasCurrent(ch, current) {
			return true
		}
	}
	return false
}

// countLeaves gives every directory the number of documents beneath it,
// including those in its subdirectories.
func countLeaves(n *NavNode) int {
	if n.Kind != models.KindDirectory {
		return 1
	}
	total := 0
	for _, ch := range n.Children {
		total += countLeaves(ch)
	}
	n.Count = total
	return total
}

// rollUp gives every directory the newest timestamp beneath it, so a folder
// row in the listing reports when its contents last changed rather than when
// the folder itself was created.
func rollUp(n *NavNode) time.Time {
	newest := n.Updated
	for _, ch := range n.Children {
		if t := rollUp(ch); t.After(newest) {
			newest = t
		}
	}
	n.Updated = newest
	return newest
}

// folderPaths flattens the tree into every directory path, so the move field
// can offer the folders that already exist instead of asking for a guess.
func folderPaths(nodes []*NavNode) []string {
	var out []string
	var walk func(ns []*NavNode)
	walk = func(ns []*NavNode) {
		for _, n := range ns {
			if n.Kind == models.KindDirectory {
				out = append(out, n.Path)
				walk(n.Children)
			}
		}
	}
	walk(nodes)
	sort.Strings(out)
	return out
}

// prune drops directories that contain no pages (for example an assets-only
// folder) so the sidebar only shows navigable destinations.
func prune(n *NavNode) {
	kept := n.Children[:0]
	for _, ch := range n.Children {
		if ch.Kind == models.KindDirectory {
			prune(ch)
			if len(ch.Children) == 0 && ch.URL == "" {
				continue
			}
			if ch.URL == "" {
				ch.URL = dirURL(ch)
			}
		}
		kept = append(kept, ch)
	}
	n.Children = kept
}

// dirURL is the generated-listing URL of a directory node (prefix recovered from a child).
func dirURL(n *NavNode) string {
	child := n.Children[0].URL
	i := strings.Index(child, n.Path+"/")
	if i < 0 {
		return n.Path + "/"
	}
	return child[:i] + n.Path + "/"
}

func orderKey(o *int) int {
	if o == nil {
		return 1 << 30
	}
	return *o
}

func parentDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// listingFor returns the direct children of a directory as nodes.
func (d *Deps) listingFor(ctx context.Context, site *SiteView, dir *models.Entry) []*NavNode {
	var find func(nodes []*NavNode) *NavNode
	find = func(nodes []*NavNode) *NavNode {
		for _, n := range nodes {
			if n.Path == dir.Path {
				return n
			}
			if r := find(n.Children); r != nil {
				return r
			}
		}
		return nil
	}
	if dir.Path == "/" {
		if site.Sidebar != nil {
			return site.Sidebar
		}
	}
	if n := find(site.Sidebar); n != nil {
		return n.Children
	}
	// Sidebar disabled: build a one-level listing directly.
	entries, err := d.Ops.Tree(ctx, dir.TenantID, false)
	if err != nil {
		return nil
	}
	tree := buildTree(entries, site.Prefix)
	if dir.Path == "/" {
		return tree
	}
	if n := find(tree); n != nil {
		return n.Children
	}
	return nil
}

// titled replaces the last crumb's slug-derived title with the real one. The
// slug cannot carry case, so "crm-options" became "Crm options" while the
// page's own title, "CRM options for a two person team", sat unused directly
// below it.
func titled(crumbs []Crumb, title string) []Crumb {
	if title == "" || len(crumbs) == 0 {
		return crumbs
	}
	crumbs[len(crumbs)-1].Title = title
	return crumbs
}

func breadcrumbs(prefix, p string, isDir bool) []Crumb {
	out := []Crumb{{Title: i18n.T("en", "site.home"), URL: prefix + "/"}}
	if p == "/" || p == "/index.md" {
		out[0].Last = true
		return out
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	acc := ""
	for i, s := range segs {
		acc += "/" + s
		last := i == len(segs)-1
		title := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSuffix(s, ".md"), "-", " "), "_", " ")
		if last && !isDir && s == "index.md" {
			continue
		}
		url := prefix + acc + "/"
		if last && !isDir {
			url = prefix + strings.TrimSuffix(acc, ".md")
		}
		out = append(out, Crumb{Title: strings.ToUpper(title[:1]) + title[1:], URL: url, Last: last})
	}
	return out
}

// renderPage renders a document for the given address form.
func (d *Deps) renderPage(c *gin.Context, a *addr, doc *models.Document) (*RenderedPage, error) {
	res, err := d.Ops.RenderPage(c.Request.Context(), a.Tenant.ID, a.Prefix, doc.Entry.Path, doc.Source)
	if err != nil {
		// Stored content always validated at write time; a parser change could
		// still reject it. Show the source escaped rather than failing the page.
		return &RenderedPage{Title: doc.Entry.Title, HTML: template.HTML("<pre>" + template.HTMLEscapeString(doc.Source) + "</pre>"), Revision: doc.Entry.CurrentRevision}, nil
	}
	page := &RenderedPage{Title: res.Title, Description: res.Meta.Description, Tags: res.Meta.Tags, HTML: template.HTML(res.HTML), TOC: res.TOC, Revision: doc.Entry.CurrentRevision, UpdatedAt: doc.Entry.UpdatedAt.UTC().Format("2006-01-02 15:04 UTC")}
	page.RawURL = a.Prefix + doc.Entry.Path
	page.JSONURL = a.Prefix + doc.Entry.JSONPath()
	for _, w := range res.Warnings {
		page.Warnings = append(page.Warnings, w.Message)
	}
	return page, nil
}

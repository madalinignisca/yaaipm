package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/static"
	"github.com/yuin/goldmark"
)

type Engine struct {
	templates        map[string]*template.Template
	AssistantEnabled bool
}

// PageData holds common data passed to every template.
type PageData struct {
	Title            string
	User             *models.User
	Org              *models.Organization
	Orgs             []models.Organization
	CSRFToken        string
	Flash            string
	FlashType        string // "success", "error", "warning"
	CurrentPath      string
	Data             any
	AssistantEnabled bool
	ProjectID        string // set on project pages for the AI assistant
}

func NewEngine(templatesDir string, manifest *static.Manifest) (*Engine, error) {
	e := &Engine{templates: make(map[string]*template.Template)}

	// asset template func: returns content-hashed URL when manifest is available,
	// plain /static/ path otherwise (tests).
	assetFunc := func(logical string) string {
		if manifest != nil {
			return manifest.AssetPath(logical)
		}
		return "/static/" + logical
	}

	md := goldmark.New()

	funcMap := template.FuncMap{
		"asset": assetFunc,
		"markdown": func(src string) template.HTML {
			var buf bytes.Buffer
			if err := md.Convert([]byte(src), &buf); err != nil {
				return template.HTML(template.HTMLEscapeString(src))
			}
			return template.HTML(buf.String())
		},
		"formatDate": func(t time.Time) string {
			return t.Format("Jan 2, 2006")
		},
		"formatDateTime": func(t time.Time) string {
			return t.Format("Jan 2, 2006 3:04 PM")
		},
		"formatDateInput": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Format("2006-01-02")
		},
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"title": strings.Title,
		"hasPrefix": strings.HasPrefix,
		"contains":  strings.Contains,
		"statusColor": func(status string) string {
			switch status {
			case "backlog":
				return "gray"
			case "ready":
				return "blue"
			case "planning", "plan_review":
				return "purple"
			case "implementing":
				return "yellow"
			case "testing":
				return "orange"
			case "review":
				return "indigo"
			case "done":
				return "green"
			case "cancelled":
				return "red"
			default:
				return "gray"
			}
		},
		"priorityColor": func(p string) string {
			switch p {
			case "critical":
				return "red"
			case "high":
				return "orange"
			case "medium":
				return "yellow"
			case "low":
				return "gray"
			default:
				return "gray"
			}
		},
		"derefStr": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
		"eq": func(a, b string) bool { return a == b },
		"dict": func(pairs ...any) map[string]any {
			m := make(map[string]any)
			for i := 0; i+1 < len(pairs); i += 2 {
				key, _ := pairs[i].(string)
				m[key] = pairs[i+1]
			}
			return m
		},
		"strList": func(items ...string) []string {
			return items
		},
		"formatCents": func(cents int64) string {
			return fmt.Sprintf("$%.2f", float64(cents)/100.0)
		},
		"humanize": func(s string) string {
			return strings.ReplaceAll(s, "_", " ")
		},
	}

	layoutFiles, err := filepath.Glob(filepath.Join(templatesDir, "layouts", "*.html"))
	if err != nil {
		return nil, fmt.Errorf("finding layouts: %w", err)
	}

	componentFiles, err := filepath.Glob(filepath.Join(templatesDir, "components", "*.html"))
	if err != nil {
		return nil, fmt.Errorf("finding components: %w", err)
	}

	// Parse page templates (with layout)
	pagePatterns := []string{
		filepath.Join(templatesDir, "pages", "*.html"),
		filepath.Join(templatesDir, "auth", "*.html"),
	}

	for _, pattern := range pagePatterns {
		pages, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, page := range pages {
			files := append(append([]string{page}, layoutFiles...), componentFiles...)
			name := filepath.Base(page)
			t, err := template.New(name).Funcs(funcMap).ParseFiles(files...)
			if err != nil {
				return nil, fmt.Errorf("parsing %s: %w", name, err)
			}
			e.templates[name] = t
		}
	}

	// Parse standalone partials (HTMX fragments, no layout)
	err = filepath.WalkDir(filepath.Join(templatesDir, "components"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		name := "partial:" + filepath.Base(path)
		t, err := template.New(filepath.Base(path)).Funcs(funcMap).ParseFiles(path)
		if err != nil {
			return fmt.Errorf("parsing partial %s: %w", path, err)
		}
		e.templates[name] = t
		return nil
	})
	if err != nil {
		return nil, err
	}

	return e, nil
}

// Render renders a full page (with layout).
func (e *Engine) Render(w http.ResponseWriter, name string, data PageData) error {
	t, ok := e.templates[name]
	if !ok {
		return fmt.Errorf("template %q not found", name)
	}
	data.AssistantEnabled = e.AssistantEnabled
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return t.ExecuteTemplate(w, "base", data)
}

// RenderPartial renders an HTMX partial (no layout).
func (e *Engine) RenderPartial(w http.ResponseWriter, name string, data any) error {
	key := "partial:" + name
	t, ok := e.templates[key]
	if !ok {
		// Fall back to direct template name
		t, ok = e.templates[name]
		if !ok {
			return fmt.Errorf("partial template %q not found", name)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return t.Execute(w, data)
}

// RenderError renders an error page.
func (e *Engine) RenderError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Error</title></head><body><h1>%d</h1><p>%s</p></body></html>`, status, message)
}

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

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/static"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/util"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
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
	Projects         []models.Project   // projects for sidebar (selected org)
	ActiveProject    *models.Project    // current project (nil if not on a project page)
	ActiveTab        string             // "brief", "features", "bugs", "gantt", "costs", "archived", "settings"
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

	// Generate syntax highlighting CSS once at startup.
	chromaStyle := styles.Get("dracula")
	if chromaStyle == nil {
		chromaStyle = styles.Fallback
	}
	chromaFmt := chromahtml.New(chromahtml.WithClasses(true))
	var cssBuf bytes.Buffer
	_ = chromaFmt.WriteCSS(&cssBuf, chromaStyle)
	highlightCSSStr := cssBuf.String()

	md := goldmark.New(goldmark.WithExtensions(
		extension.Table,
		extension.Strikethrough,
		extension.Linkify,
		extension.TaskList,
		extension.DefinitionList,
		extension.Footnote,
		extension.Typographer,
		highlighting.NewHighlighting(
			highlighting.WithStyle("dracula"),
			highlighting.WithFormatOptions(
				chromahtml.WithClasses(true),
			),
			highlighting.WithWrapperRenderer(func(w util.BufWriter, c highlighting.CodeBlockContext, entering bool) {
				lang, _ := c.Language()
				if string(lang) == "mermaid" {
					if entering {
						w.WriteString(`<pre class="mermaid">`)
					} else {
						w.WriteString("</pre>")
					}
					return
				}
				if entering {
					w.WriteString("<pre class=\"chroma\"><code>")
				} else {
					w.WriteString("</code></pre>")
				}
			}),
		),
	))

	funcMap := template.FuncMap{
		"highlightCSS": func() template.HTML {
			return template.HTML("<style>" + highlightCSSStr + "</style>")
		},
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
		"formatCents": func(cents int64, currency ...string) string {
			code := "EUR"
			if len(currency) > 0 && currency[0] != "" {
				code = currency[0]
			}
			return fmt.Sprintf("%s%.2f", currencySymbol(code), float64(cents)/100.0)
		},
		"humanize": func(s string) string {
			return strings.ReplaceAll(s, "_", " ")
		},
		"formatBytes": func(b int64) string {
			const unit = 1024
			if b < unit {
				return fmt.Sprintf("%d B", b)
			}
			div, exp := int64(unit), 0
			for n := b / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
		},
		"fileIcon": func(ct string) template.HTML {
			switch {
			case strings.HasPrefix(ct, "image/"):
				return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"/><circle cx="8.5" cy="8.5" r="1.5"/><polyline points="21 15 16 10 5 21"/></svg>`)
			case strings.HasPrefix(ct, "video/"):
				return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="23 7 16 12 23 17 23 7"/><rect x="1" y="5" width="15" height="14" rx="2" ry="2"/></svg>`)
			case ct == "application/pdf":
				return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#e53e3e" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/></svg>`)
			case strings.Contains(ct, "spreadsheet"), strings.Contains(ct, "excel"), ct == "text/csv":
				return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#38a169" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"/><line x1="3" y1="9" x2="21" y2="9"/><line x1="3" y1="15" x2="21" y2="15"/><line x1="9" y1="3" x2="9" y2="21"/><line x1="15" y1="3" x2="15" y2="21"/></svg>`)
			case strings.Contains(ct, "zip"), strings.Contains(ct, "compressed"), strings.Contains(ct, "archive"):
				return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#d69e2e" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 8v13H3V8"/><path d="M1 3h22v5H1z"/><path d="M10 12h4"/></svg>`)
			case strings.HasPrefix(ct, "text/"), strings.Contains(ct, "json"), strings.Contains(ct, "xml"), strings.Contains(ct, "javascript"):
				return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#4299e1" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>`)
			default:
				return template.HTML(`<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><polyline points="13 2 13 9 20 9"/></svg>`)
			}
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

// currencySymbol returns the display symbol/prefix for a currency code.
func currencySymbol(code string) string {
	switch code {
	case "EUR":
		return "€"
	case "USD", "CAD", "AUD":
		return "$"
	case "GBP":
		return "£"
	case "JPY", "CNY":
		return "¥"
	case "CHF", "SEK", "NOK", "DKK", "PLN", "CZK", "RON", "HUF", "BGN", "HRK":
		return code + " "
	default:
		return code + " "
	}
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

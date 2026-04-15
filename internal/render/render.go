package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	gorillacsrf "filippo.io/csrf/gorilla"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/madalin/forgedesk/internal/diff"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/static"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/util"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type Engine struct {
	templates        map[string]*template.Template
	AssistantEnabled bool
}

// PageData holds common data passed to every template.
type PageData struct {
	Data             any
	ActiveProject    *models.Project
	User             *models.User
	Org              *models.Organization
	FlashType        string
	ActiveTab        string
	CSRFToken        string
	Flash            string
	Title            string
	CurrentPath      string
	ProjectID        string
	Projects         []models.Project
	Orgs             []models.Organization
	AssistantEnabled bool
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

	// HTML sanitizer for markdown output — prevents stored XSS while
	// allowing safe formatting, Chroma syntax highlighting, and Mermaid diagrams.
	sanitizer := bluemonday.UGCPolicy()
	sanitizer.AllowAttrs("class").OnElements("pre", "code", "span", "div")
	sanitizer.AllowAttrs("style").OnElements("span") // Chroma inline styles

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
						_, _ = w.WriteString(`<pre class="mermaid">`)
					} else {
						_, _ = w.WriteString("</pre>")
					}
					return
				}
				if entering {
					_, _ = w.WriteString("<pre class=\"chroma\"><code>")
				} else {
					_, _ = w.WriteString("</code></pre>")
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
			return template.HTML(sanitizer.Sanitize(buf.String()))
		},
		"formatDate": func(t any) string {
			switch v := t.(type) {
			case time.Time:
				return v.Format("Jan 2, 2006")
			case *time.Time:
				if v != nil {
					return v.Format("Jan 2, 2006")
				}
			}
			return ""
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
		"upper":     strings.ToUpper,
		"lower":     strings.ToLower,
		"title":     cases.Title(language.Und).String,
		"hasPrefix": strings.HasPrefix,
		"contains":  strings.Contains,
		"statusColor": func(status string) string {
			const colorGray = "gray"
			switch status {
			case "backlog":
				return colorGray
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
				return colorGray
			}
		},
		"priorityColor": func(p string) string {
			const colorGray = "gray"
			switch p {
			case "critical":
				return "red"
			case "high":
				return "orange"
			case "medium":
				return "yellow"
			case "low":
				return colorGray
			default:
				return colorGray
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
		"truncate": func(s string, maxLen int) string {
			// Strip newlines for single-line previews
			s = strings.ReplaceAll(s, "\n", " ")
			s = strings.TrimSpace(s)
			if len(s) <= maxLen {
				return s
			}
			return s[:maxLen] + "…"
		},
		"toJSON": func(v any) template.JS {
			b, err := json.Marshal(v)
			if err != nil {
				return "[]"
			}
			return template.JS(b)
		},
		// Feature Debate Mode helpers (spec §5 UI rendering).
		// renderDiff takes a cached unified-diff string (stored on
		// feature_debate_rounds.diff_unified) and returns sanitized
		// HTML with diff-add/diff-del/diff-ctx class spans. Every
		// line is HTMLEscaped in diff.RenderHTML; see that package
		// for the safety audit.
		// providerLabel maps an internal provider key (claude / gemini /
		// openai) to its user-facing brand name. Computed server-side so
		// the debate template doesn't have to put {{if}}{{end}} branches
		// inside HTML attribute values — html/template's context-aware
		// escaper silently emits partial output when an attribute value
		// contains conditional pipelines.
		"providerLabel": func(name string) string {
			switch name {
			case "claude":
				return "Claude"
			case "gemini":
				return "Gemini"
			case "openai":
				return "ChatGPT"
			default:
				return name
			}
		},
		"renderDiff": func(unified *string) template.HTML {
			// Nil pointer (no cached diff) and empty string both route
			// through diff.RenderHTML, which handles the empty case by
			// returning just the outer <pre><code></code></pre> wrapper.
			// Keeps the template.HTML construction confined to the
			// already-audited helper rather than minting a new cast
			// here.
			var raw string
			if unified != nil {
				raw = *unified
			}
			return diff.RenderHTML(raw)
		},
		// derefInt / derefString unwrap nullable *int and *string
		// fields for safe template-side display; zero/empty values
		// substitute for nil. Used by debate_sidebar.html for the
		// effort_score / effort_hours / effort_reasoning columns.
		"derefInt": func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		},
		// derefString omitted — the existing derefStr helper (line 187)
		// already covers this case; debate_sidebar.html uses derefStr.
		// relTime renders a nullable *time.Time as "just now" / "5m ago"
		// / "2h ago" / "3d ago" for the sidebar's "last scored"
		// indicator. Returns an em-dash for nil so the template can
		// always substitute it into a sentence fragment.
		"relTime": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			d := time.Since(*t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return fmt.Sprintf("%dm ago", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%dh ago", int(d.Hours()))
			default:
				return fmt.Sprintf("%dd ago", int(d.Hours()/24))
			}
		},
		// mul multiplies two ints; used by the effort-bar-vertical's
		// inline CSS calc() for the score-pointer position
		// (score * 10% per band point).
		"mul": func(a, b int) int { return a * b },
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
		"csrfField": func() template.HTML {
			return ""
		},
		"csrfToken": func() string {
			return ""
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
		pages, globErr := filepath.Glob(pattern)
		if globErr != nil {
			return nil, globErr
		}
		for _, page := range pages {
			files := append(append([]string{page}, layoutFiles...), componentFiles...)
			name := filepath.Base(page)
			t, parseErr := template.New(name).Funcs(funcMap).ParseFiles(files...)
			if parseErr != nil {
				return nil, fmt.Errorf("parsing %s: %w", name, parseErr)
			}
			e.templates[name] = t
		}
	}

	// Standalone partials (HTMX fragments, no layout) are parsed
	// once into a shared template set so each partial can reference
	// sibling components via {{template "other.html" ...}} without
	// O(N^2) re-parsing at startup. Previously each partial was
	// parsed in isolation; the comment partial's {{template
	// "reactions.html" ...}} include then failed at Execute time
	// with "no such template", and since html/template buffers
	// output until success, the partial silently emitted nothing —
	// newly posted comments disappeared until a full page reload
	// re-rendered them. (#37)
	if len(componentFiles) > 0 {
		partialSet, parseErr := template.New("partials").Funcs(funcMap).ParseFiles(componentFiles...)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing component partials: %w", parseErr)
		}
		for _, path := range componentFiles {
			e.templates["partial:"+filepath.Base(path)] = partialSet
		}
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

// Render renders a full page (with layout), injecting CSRF token from the request context.
func (e *Engine) Render(w http.ResponseWriter, r *http.Request, name string, data PageData) error {
	t, ok := e.templates[name]
	if !ok {
		return fmt.Errorf("template %q not found", name)
	}
	data.AssistantEnabled = e.AssistantEnabled
	// filippo.io/csrf/gorilla uses header-based CSRF (Origin /
	// Sec-Fetch-Site); Token/TemplateField are shim no-ops kept for
	// source compatibility. Removing these call sites would also
	// require removing references from every template that emits a
	// hidden field, which is a separate cleanup.
	data.CSRFToken = gorillacsrf.Token(r) //nolint:staticcheck // filippo.io/csrf/gorilla shim — no-op

	// Clone template with request-specific CSRF funcs
	t, err := t.Clone()
	if err != nil {
		return fmt.Errorf("cloning template: %w", err)
	}
	t.Funcs(template.FuncMap{
		"csrfField": func() template.HTML {
			return gorillacsrf.TemplateField(r) //nolint:staticcheck // filippo.io/csrf/gorilla shim — no-op
		},
		"csrfToken": func() string {
			return gorillacsrf.Token(r) //nolint:staticcheck // filippo.io/csrf/gorilla shim — no-op
		},
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return t.ExecuteTemplate(w, "base", data)
}

// RenderPartial renders an HTMX partial (no layout).
// All partial keys share one template set parsed from the components/
// directory (see Engine construction), so we execute by base-name
// rather than relying on the set's default template.
func (e *Engine) RenderPartial(w http.ResponseWriter, name string, data any) error {
	key := "partial:" + name
	t, ok := e.templates[key]
	if !ok {
		// Fall back to direct template name (for tests that register
		// custom templates outside the components/ walk).
		t, ok = e.templates[name]
		if !ok {
			return fmt.Errorf("partial template %q not found", name)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		return t.Execute(w, data)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return t.ExecuteTemplate(w, name, data)
}

// RenderError renders an error page.
func (e *Engine) RenderError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Error</title></head><body><h1>%d</h1><p>%s</p></body></html>`, status, html.EscapeString(message))
}

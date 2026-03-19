// init.js — Mermaid diagram initialization + CSRF + security hooks
// Runs after mermaid.min.js loads but before Alpine.js auto-starts.
if (typeof mermaid !== 'undefined') {
    mermaid.initialize({ startOnLoad: false, theme: 'default' });
    mermaid.run();
}
document.addEventListener('htmx:afterSettle', function() {
    if (typeof mermaid !== 'undefined') {
        mermaid.run();
    }
});

// Inject CSRF token on all HTMX requests
document.addEventListener('htmx:configRequest', function(event) {
    var meta = document.querySelector('meta[name="csrf-token"]');
    if (meta) {
        event.detail.headers['X-CSRF-Token'] = meta.getAttribute('content');
    }
});

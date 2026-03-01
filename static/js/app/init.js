// init.js — Mermaid diagram initialization
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

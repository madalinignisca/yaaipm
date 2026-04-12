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

// apiFetch wraps fetch() for calls to our own mutation endpoints.
// It matches the HTMX request profile so raw fetch() callers behave
// consistently with HTMX-boosted forms:
//
//   * Injects X-CSRF-Token from the meta tag (parallel to the HTMX
//     configRequest listener above). Harmless under the current
//     header-based CSRF scheme (filippo.io/csrf/gorilla uses
//     Sec-Fetch-Site); future-proofing if the middleware ever
//     reverts to a token-based scheme.
//   * Sets credentials: 'same-origin' so the session cookie travels.
//   * Rejects non-2xx responses so callers can't silently treat a
//     403 CSRF failure or 500 as a success. (#33)
//
// Callers receive the parsed JSON body on success, or a rejected
// promise carrying the response status + best-effort error message
// on failure.
window.apiFetch = function(url, options) {
    options = options || {};
    var headers = {};
    // Copy caller-provided headers first so our additions don't
    // clobber caller intent unless we explicitly override.
    if (options.headers) {
        Object.keys(options.headers).forEach(function(k) { headers[k] = options.headers[k]; });
    }
    var meta = document.querySelector('meta[name="csrf-token"]');
    if (meta && !headers['X-CSRF-Token']) {
        headers['X-CSRF-Token'] = meta.getAttribute('content');
    }
    options.headers = headers;
    if (options.credentials === undefined) {
        options.credentials = 'same-origin';
    }
    return fetch(url, options).then(function(response) {
        return response.json().catch(function() {
            // Non-JSON body; synthesize a minimal error payload so
            // the subsequent .ok check still has something to work with.
            return { error: 'Unexpected response (HTTP ' + response.status + ')' };
        }).then(function(body) {
            if (!response.ok) {
                var msg = (body && body.error) ? body.error : ('Request failed: HTTP ' + response.status);
                var err = new Error(msg);
                err.status = response.status;
                err.body = body;
                throw err;
            }
            return body;
        });
    });
};

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

    // Collect caller-provided headers. Supports both plain-object
    // headers and Headers instances (forEach). Per Gemini review
    // on #33.
    var headers = {};
    if (options.headers) {
        if (typeof options.headers.forEach === 'function') {
            options.headers.forEach(function(v, k) { headers[k] = v; });
        } else {
            Object.keys(options.headers).forEach(function(k) { headers[k] = options.headers[k]; });
        }
    }
    var meta = document.querySelector('meta[name="csrf-token"]');
    if (meta && !headers['X-CSRF-Token']) {
        headers['X-CSRF-Token'] = meta.getAttribute('content');
    }

    // Build a fresh options object so we don't mutate the caller's
    // (they may be reusing it for retries / multiple requests).
    var fetchOptions = {};
    Object.keys(options).forEach(function(k) { fetchOptions[k] = options[k]; });
    fetchOptions.headers = headers;
    if (fetchOptions.credentials === undefined) {
        fetchOptions.credentials = 'same-origin';
    }

    return fetch(url, fetchOptions).then(function(response) {
        // 204 No Content (and any other empty success body) must not
        // be funneled through response.json() — that would throw and
        // trick callers into thinking the request failed.
        var bodyPromise = response.status === 204
            ? Promise.resolve({})
            : response.json().catch(function() {
                return { error: 'Unexpected response (HTTP ' + response.status + ')' };
            });
        return bodyPromise.then(function(body) {
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

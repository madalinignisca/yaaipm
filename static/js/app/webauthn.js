// WebAuthn browser API helpers - placeholder for future implementation
(function() {
    'use strict';

    function base64URLToBuffer(base64url) {
        const base64 = base64url.replace(/-/g, '+').replace(/_/g, '/');
        const padding = '='.repeat((4 - base64.length % 4) % 4);
        const binary = atob(base64 + padding);
        return Uint8Array.from(binary, c => c.charCodeAt(0));
    }

    function bufferToBase64URL(buffer) {
        const bytes = new Uint8Array(buffer);
        let binary = '';
        for (let i = 0; i < bytes.byteLength; i++) {
            binary += String.fromCharCode(bytes[i]);
        }
        return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
    }

    window.WebAuthnHelpers = {
        base64URLToBuffer: base64URLToBuffer,
        bufferToBase64URL: bufferToBase64URL
    };
})();

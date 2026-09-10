// No offline caching by design — ComicNest requires a live connection to its
// server. This service worker exists solely so the browser considers the app
// installable; it never intercepts requests.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));
self.addEventListener('fetch', () => {});

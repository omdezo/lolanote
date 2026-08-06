// QomraNote service worker: app-shell caching for the installable PWA.
// Hashed /assets bundles cache forever (cache-first); the shell (/,
// index.html) is network-first with cache fallback so deploys land on the
// next online load but the app still opens offline. API and WS traffic is
// never touched.
// Named off the build when the deploy supplies one (main.tsx registers this
// script as /sw.js?v=<build>). A poisoned shell then cannot outlive the deploy
// that recorded it, and without a build id the name is exactly what it was.
const BUILD = new URL(self.location.href).searchParams.get('v') || 'v1';
const SHELL_CACHE = `qomranote-shell-${BUILD}`;
const ASSET_CACHE = 'qomranote-assets-v1';

/** When the shell was last written, so a stale one can be distrusted. */
const SHELL_STAMP = '/__shell-cached-at';
/** A cached shell older than this is suspect on the next boot: it may be a
 *  captive portal's page that has been serving as this app for a fortnight. */
const MAX_SHELL_AGE_MS = 14 * 24 * 60 * 60 * 1000;

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(SHELL_CACHE).then((cache) => cache.addAll(['/', '/manifest.webmanifest', '/icon.svg'])),
  );
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      const keys = await caches.keys();
      await Promise.all(
        keys.filter((k) => k !== SHELL_CACHE && k !== ASSET_CACHE).map((k) => caches.delete(k)),
      );
      // Drop a shell nobody has refreshed in a fortnight rather than keep
      // serving it: the failure this guards is self-perpetuating, and the one
      // thing that fixes it is being made to go to the network again.
      const cache = await caches.open(SHELL_CACHE);
      const stamp = await cache.match(SHELL_STAMP);
      const at = stamp ? Number(await stamp.text()) : 0;
      if (at && Date.now() - at > MAX_SHELL_AGE_MS) await cache.delete('/');

      await pruneAssets();
      await trimUnderPressure();
    })(),
  );
  self.clients.claim();
});

/**
 * MO9. The asset cache had a FIXED name, so it survived every deploy, and the
 * activate handler deleted only caches that were not it. Vite emits
 * content-hashed bundles: every deploy therefore added a complete new set and
 * retired nothing, forever.
 *
 * On a desktop this is invisible. On a phone the origin quota is a fraction of
 * remaining device storage and browsers evict PER ORIGIN, ALL OR NOTHING under
 * pressure — so what eventually gets thrown away is not just stale JS but
 * `boardCache`, the IndexedDB board mirror sharing the same quota, which is the
 * only reason this app is usable on a location shoot with no signal. Silent,
 * and it presents as "the app forgot my boards".
 *
 * The fix is the standard precache-manifest trick without adding a build
 * plugin: the freshly-cached shell IS the manifest. Anything under /assets/
 * that the current index.html does not reference is from a previous deploy and
 * can go.
 */
async function pruneAssets() {
  const cache = await caches.open(ASSET_CACHE);
  const keys = await cache.keys();
  if (keys.length === 0) return;

  const shell = await caches.open(SHELL_CACHE);
  const doc = await shell.match('/');
  // No shell cached yet means no manifest to diff against. Deleting everything
  // here would be worse than keeping it: the next load would refetch the whole
  // bundle over the connection that just failed to give us a shell.
  if (!doc) return;
  const html = await doc.text();

  const referenced = new Set();
  for (const m of html.matchAll(/(?:src|href)\s*=\s*["']([^"']+)["']/g)) {
    try { referenced.add(new URL(m[1], self.location.origin).pathname); } catch { /* not a URL */ }
  }
  // A shell that mentions no assets at all is a shell we do not understand —
  // an error page, a captive portal, a dev-server document. Pruning against it
  // would delete the entire bundle on the strength of a page we should not have
  // trusted, so this refuses rather than guesses.
  if (![...referenced].some((p) => p.startsWith('/assets/'))) return;

  await Promise.all(keys.map(async (req) => {
    const path = new URL(req.url).pathname;
    if (!path.startsWith('/assets/')) return;
    if (referenced.has(path)) return;
    await cache.delete(req);
  }));
}

/** How full the origin quota may get before assets are dropped to protect the
 *  offline board mirror sharing it. */
const QUOTA_CEILING = 0.8;

/**
 * The second half of the same argument: pruning by deploy is not enough if a
 * single deploy's bundle plus a large board mirror is already near the quota.
 * Assets are recoverable from the network; a board snapshot on a phone with no
 * signal is not — so under pressure the assets are what goes.
 */
async function trimUnderPressure() {
  if (!navigator.storage || !navigator.storage.estimate) return;
  try {
    const { usage = 0, quota = 0 } = await navigator.storage.estimate();
    if (!quota || usage / quota < QUOTA_CEILING) return;
    const cache = await caches.open(ASSET_CACHE);
    for (const req of await cache.keys()) await cache.delete(req);
  } catch { /* estimate is best-effort and not everywhere */ }
}

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== 'GET' || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/ws')) return;

  // Immutable hashed bundles: cache-first.
  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(
      caches.open(ASSET_CACHE).then(async (cache) => {
        const hit = await cache.match(event.request);
        if (hit) return hit;
        const res = await fetch(event.request);
        if (res.ok) cache.put(event.request, res.clone());
        return res;
      }),
    );
    return;
  }

  // Shell navigation: network-first, cached fallback for offline opens.
  if (event.request.mode === 'navigate' || url.pathname === '/') {
    event.respondWith(
      fetch(event.request)
        .then((res) => {
          // The branch that decides whether this app can ever open again used
          // to write whatever came back, unconditionally — no status check, no
          // type check, no redirect check — while the asset branch twelve lines
          // above already guarded on res.ok.
          //
          // Any 500 from the origin, any 502 mid-deploy, or — the mobile case —
          // any captive-portal interstitial returning 200 with somebody else's
          // HTML on hotel, airport or cafe Wi-Fi was written over '/' in the
          // shell cache. From then on the installed PWA opened to the captive
          // portal's login page forever, offline AND online, because the shell
          // is network-first-with-cache-fallback and the cache was poisoned.
          // Only a good load fixed it, and the user had no way to know that.
          //
          // res.type 'basic' means same-origin and not opaque; res.redirected
          // catches the portal, which answers by redirecting somewhere else.
          if (res.ok && res.type === 'basic' && !res.redirected) {
            const copy = res.clone();
            caches.open(SHELL_CACHE).then((cache) => {
              cache.put('/', copy);
              cache.put(SHELL_STAMP, new Response(String(Date.now())));
            });
          }
          return res;
        })
        .catch(() => caches.match('/')),
    );
  }
});

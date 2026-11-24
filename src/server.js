// Local review server: serves a directory as-is, adds the comment overlay to
// HTML pages, stores comments, and tells open pages when files change.

import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { CLIENT_PATH, prepareHtml } from './annotate.js';
import { Store, StoreError, normalizeFile } from './store.js';

const CLIENT_FILE = fileURLToPath(new URL('./client/client.js', import.meta.url));
const API = '/__inliner/api';
const EVENTS = '/__inliner/events';
const LOCAL_HOSTS = new Set(['localhost', '127.0.0.1', '[::1]']);
const MAX_BODY = 1024 * 1024;

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.htm': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.map': 'application/json; charset=utf-8',
  '.txt': 'text/plain; charset=utf-8',
  '.md': 'text/plain; charset=utf-8',
  '.csv': 'text/csv; charset=utf-8',
  '.xml': 'application/xml; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.avif': 'image/avif',
  '.ico': 'image/x-icon',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ttf': 'font/ttf',
  '.otf': 'font/otf',
  '.mp4': 'video/mp4',
  '.webm': 'video/webm',
  '.mp3': 'audio/mpeg',
  '.wav': 'audio/wav',
  '.pdf': 'application/pdf',
  '.wasm': 'application/wasm',
};

const isHtml = (p) => /\.html?$/i.test(p);
const escapeHtml = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const encodePath = (rel) => rel.split('/').map(encodeURIComponent).join('/');

class HttpError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

/**
 * @param {{ root: string, port?: number, strictPort?: boolean, pollMs?: number }} opts
 * @returns {Promise<{ url: string, port: number, root: string, store: Store, close: () => Promise<void> }>}
 */
export async function startServer({ root, port = 4600, strictPort = false, pollMs = 400 }) {
  root = fs.realpathSync(path.resolve(root));
  const store = new Store(root);

  // --- change detection -----------------------------------------------------
  // Poll the files pages have actually loaded. Unlike fs.watch this behaves the
  // same on every platform and survives editors that save by rename.
  const clients = new Set();
  const watched = new Map(); // abs path -> { rel, sig }
  const signature = (abs) => {
    try {
      const st = fs.statSync(abs);
      return `${st.mtimeMs}:${st.size}`;
    } catch {
      return null;
    }
  };
  const watch = (abs, rel) => {
    if (watched.size < 5000) watched.set(abs, { rel, sig: signature(abs) });
  };
  let storeSig = signature(store.file);

  const broadcast = (event, data) => {
    const msg = `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
    for (const res of clients) res.write(msg);
  };

  let ticks = 0;
  const timer = setInterval(() => {
    if (!clients.size) return;
    for (const [abs, entry] of watched) {
      const sig = signature(abs);
      if (sig === null || sig === entry.sig) continue; // deleted files: wait for them to come back
      entry.sig = sig;
      broadcast('reload', { path: entry.rel });
    }
    const sig = signature(store.file);
    if (sig !== storeSig) {
      storeSig = sig;
      broadcast('comments', {});
    }
    if (++ticks % 60 === 0) for (const res of clients) res.write(': keepalive\n\n');
  }, pollMs);
  timer.unref();

  // --- helpers --------------------------------------------------------------
  const send = (res, status, body, headers = {}) => {
    res.writeHead(status, { 'Cache-Control': 'no-store', ...headers });
    res.end(body);
  };
  const sendJson = (res, status, data) => send(res, status, JSON.stringify(data), { 'Content-Type': MIME['.json'] });

  const readJson = (req) =>
    new Promise((resolve, reject) => {
      let size = 0;
      const chunks = [];
      req.on('data', (chunk) => {
        size += chunk.length;
        if (size > MAX_BODY) {
          reject(new HttpError(413, 'Request body too large.'));
          req.destroy();
          return;
        }
        chunks.push(chunk);
      });
      req.on('end', () => {
        try {
          resolve(chunks.length ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : {});
        } catch {
          reject(new HttpError(400, 'Request body is not valid JSON.'));
        }
      });
      req.on('error', reject);
    });

  const requireFile = (value) => {
    const file = normalizeFile(value);
    if (!file) throw new HttpError(400, 'Missing or invalid "file".');
    return file;
  };

  // --- API ------------------------------------------------------------------
  async function handleApi(req, res, url) {
    const route = url.pathname.slice(API.length);
    const { method } = req;

    if (method !== 'GET') {
      // Only pages served by this server may change comments.
      const origin = req.headers.origin;
      if (origin && origin !== `http://${req.headers.host}`) throw new HttpError(403, 'Cross-origin request refused.');
    }

    if (route === '/comments' && method === 'GET') {
      const file = requireFile(url.searchParams.get('file'));
      return sendJson(res, 200, { file, comments: store.list({ file }) });
    }
    if (route === '/comments' && method === 'POST') {
      const body = await readJson(req);
      return sendJson(res, 201, store.add({ ...body, file: requireFile(body.file) }));
    }
    if (route === '/feedback' && method === 'GET') {
      const file = url.searchParams.get('file');
      const text = store.feedback({ file: file ? requireFile(file) : undefined });
      return send(res, 200, text, { 'Content-Type': 'text/markdown; charset=utf-8' });
    }
    if (route === '/anchors' && method === 'POST') {
      const body = await readJson(req);
      const changed = store.syncAnchors(requireFile(body.file), body.updates);
      // Line bookkeeping is not news for other tabs: don't announce it.
      if (changed) storeSig = signature(store.file);
      return sendJson(res, 200, { changed });
    }

    const match = /^\/comments\/(\d+)$/.exec(route);
    if (match) {
      const id = Number(match[1]);
      if (method === 'PATCH') {
        const body = await readJson(req);
        const comment = store.update(id, { body: body.body, status: body.status });
        if (!comment) throw new HttpError(404, `No comment #${id}.`);
        return sendJson(res, 200, comment);
      }
      if (method === 'DELETE') {
        if (!store.remove([id]).length) throw new HttpError(404, `No comment #${id}.`);
        return sendJson(res, 200, { ok: true });
      }
    }
    throw new HttpError(404, 'Unknown API route.');
  }

  function handleEvents(req, res) {
    res.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-store',
      Connection: 'keep-alive',
    });
    res.write('retry: 1000\n\n');
    clients.add(res);
    req.on('close', () => clients.delete(res));
  }

  // --- static files ---------------------------------------------------------
  function resolveUrlPath(pathname) {
    let decoded;
    try {
      decoded = decodeURIComponent(pathname);
    } catch {
      throw new HttpError(400, 'Malformed URL.');
    }
    const segments = decoded.split('/').filter(Boolean);
    // Dot-prefixed entries (.git, .env, .inliner, and of course "..") are never served.
    if (decoded.includes('\0') || segments.some((s) => s.startsWith('.'))) throw new HttpError(404, 'Not found.');
    const abs = path.join(root, ...segments);
    let real;
    try {
      real = fs.realpathSync(abs);
    } catch {
      throw new HttpError(404, 'Not found.');
    }
    if (real !== root && !real.startsWith(root + path.sep)) throw new HttpError(404, 'Not found.');
    return { abs, rel: segments.join('/') };
  }

  function sendPage(res, abs, rel) {
    watch(abs, rel);
    send(res, 200, prepareHtml(fs.readFileSync(abs, 'utf8'), rel), { 'Content-Type': MIME['.html'] });
  }

  function sendListing(res, abs, rel) {
    const openCounts = new Map();
    for (const c of store.list()) if (c.status === 'open') openCounts.set(c.file, (openCounts.get(c.file) ?? 0) + 1);

    const entries = fs
      .readdirSync(abs, { withFileTypes: true })
      .filter((e) => !e.name.startsWith('.') && e.name !== 'node_modules')
      .map((e) => {
        let dir = e.isDirectory();
        if (e.isSymbolicLink()) {
          try {
            dir = fs.statSync(path.join(abs, e.name)).isDirectory();
          } catch {
            return null;
          }
        }
        return { name: e.name, dir };
      })
      .filter((e) => e && (e.dir || isHtml(e.name)))
      .sort((a, b) => Number(b.dir) - Number(a.dir) || a.name.localeCompare(b.name));

    const rows = entries.map((e) => {
      const childRel = rel ? `${rel}/${e.name}` : e.name;
      const count = e.dir
        ? [...openCounts].reduce((n, [file, c]) => (file.startsWith(`${childRel}/`) ? n + c : n), 0)
        : (openCounts.get(childRel) ?? 0);
      const badge = count ? `<span class="count">${count}</span>` : '';
      return `<li><a href="/${encodePath(childRel)}${e.dir ? '/' : ''}">${escapeHtml(e.name)}${e.dir ? '/' : ''}</a>${badge}</li>`;
    });
    if (rel) rows.unshift(`<li><a href="/${encodePath(path.posix.dirname(rel)).replace(/^\.$/, '')}">../</a></li>`);

    const title = `/${rel}`;
    send(
      res,
      200,
      `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>inliner — ${escapeHtml(title)}</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 15px/1.6 system-ui, -apple-system, "Segoe UI", "Hiragino Sans", "Noto Sans JP", sans-serif; max-width: 720px; margin: 48px auto; padding: 0 20px; }
  h1 { font-size: 15px; font-weight: 600; margin: 0 0 4px; }
  p { color: GrayText; margin: 0 0 20px; font-size: 13px; }
  ul { list-style: none; padding: 0; margin: 0; border-top: 1px solid color-mix(in srgb, CanvasText 12%, transparent); }
  li { display: flex; align-items: center; gap: 10px; padding: 8px 4px; border-bottom: 1px solid color-mix(in srgb, CanvasText 12%, transparent); }
  a { color: LinkText; text-decoration: none; overflow-wrap: anywhere; }
  a:hover { text-decoration: underline; }
  .count { margin-left: auto; background: #e8930c; color: #fff; border-radius: 999px; padding: 0 8px; font-size: 12px; font-weight: 600; flex: none; }
  .empty { color: GrayText; padding: 16px 4px; }
</style>
<h1>inliner <span style="font-weight:400">${escapeHtml(title)}</span></h1>
<p>${escapeHtml(root)}</p>
${rows.length ? `<ul>\n${rows.join('\n')}\n</ul>` : ''}
${entries.length ? '' : '<div class="empty">No HTML files here.</div>'}
</html>
`,
      { 'Content-Type': MIME['.html'] },
    );
  }

  function handleStatic(req, res, url) {
    if (req.method !== 'GET' && req.method !== 'HEAD') throw new HttpError(405, 'Method not allowed.');
    const { abs, rel } = resolveUrlPath(url.pathname);
    const stat = fs.statSync(abs);

    if (stat.isDirectory()) {
      if (!url.pathname.endsWith('/')) return send(res, 302, '', { Location: `${url.pathname}/${url.search}` });
      const index = path.join(abs, 'index.html');
      if (fs.existsSync(index)) return sendPage(res, index, rel ? `${rel}/index.html` : 'index.html');
      return sendListing(res, abs, rel);
    }
    if (isHtml(abs)) return sendPage(res, abs, rel);

    watch(abs, rel);
    res.writeHead(200, {
      'Content-Type': MIME[path.extname(abs).toLowerCase()] ?? 'application/octet-stream',
      'Content-Length': stat.size,
      'Cache-Control': 'no-store',
    });
    if (req.method === 'HEAD') return res.end();
    fs.createReadStream(abs).on('error', () => res.destroy()).pipe(res);
  }

  // --- server ---------------------------------------------------------------
  async function handle(req, res) {
    // Refuse anything not addressed to localhost: blocks DNS-rebinding pages
    // from reading local files through this server.
    const hostname = (req.headers.host ?? '').replace(/:\d+$/, '').toLowerCase();
    if (!LOCAL_HOSTS.has(hostname)) throw new HttpError(403, 'Forbidden host.');

    const url = new URL(req.url, 'http://localhost');
    if (url.pathname === CLIENT_PATH) {
      return send(res, 200, fs.readFileSync(CLIENT_FILE), { 'Content-Type': MIME['.js'] });
    }
    if (url.pathname === EVENTS) return handleEvents(req, res);
    if (url.pathname === API || url.pathname.startsWith(`${API}/`)) return handleApi(req, res, url);
    return handleStatic(req, res, url);
  }

  const server = http.createServer((req, res) => {
    handle(req, res).catch((err) => {
      const status = err instanceof HttpError || err instanceof StoreError ? err.status : err?.code === 'ENOENT' ? 404 : 500;
      if (status === 500) console.error(`inliner: ${req.method} ${req.url}: ${err?.stack ?? err}`);
      if (res.headersSent) return res.destroy();
      sendJson(res, status, { error: status === 500 ? 'Internal error.' : err.message });
    });
  });

  const listen = (p) =>
    new Promise((resolve, reject) => {
      const onError = (err) => {
        server.off('listening', onListening);
        reject(err);
      };
      const onListening = () => {
        server.off('error', onError);
        resolve();
      };
      server.once('error', onError);
      server.once('listening', onListening);
      server.listen(p, '127.0.0.1');
    });

  for (let attempt = 0; ; attempt++) {
    try {
      await listen(port === 0 ? 0 : port + attempt);
      break;
    } catch (err) {
      if (err.code !== 'EADDRINUSE' || strictPort || port === 0 || attempt >= 20) {
        clearInterval(timer);
        throw err.code === 'EADDRINUSE' ? new Error(`Port ${port + attempt} is already in use.`) : err;
      }
    }
  }
  const actualPort = server.address().port;

  return {
    url: `http://localhost:${actualPort}`,
    port: actualPort,
    root,
    store,
    close: () =>
      new Promise((resolve) => {
        clearInterval(timer);
        for (const res of clients) res.end();
        server.close(() => resolve());
        server.closeAllConnections();
      }),
  };
}

import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { startServer } from '../src/server.js';

const PAGE = '<!doctype html>\n<html>\n<body>\n<h1>Title</h1>\n<p>Hello world</p>\n</body>\n</html>\n';

async function setup(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'inliner-server-'));
  fs.writeFileSync(path.join(root, 'page.html'), PAGE);
  fs.writeFileSync(path.join(root, 'style.css'), 'body { color: red }');
  fs.writeFileSync(path.join(root, '.env'), 'SECRET=1');
  fs.mkdirSync(path.join(root, 'docs'));
  fs.writeFileSync(path.join(root, 'docs', 'index.html'), '<p>docs</p>');
  fs.mkdirSync(path.join(root, 'empty'));
  fs.writeFileSync(path.join(path.dirname(root), 'outside.txt'), 'outside');
  const server = await startServer({ root, port: 0, pollMs: 30 });
  t.after(() => server.close());
  const json = (method, route, body, headers = {}) =>
    fetch(`${server.url}/__inliner/api${route}`, {
      method,
      headers: { ...(body ? { 'Content-Type': 'application/json' } : {}), ...headers },
      body: body ? JSON.stringify(body) : undefined,
    });
  return { root, server, json };
}

const comment = { file: 'page.html', kind: 'text', body: 'Shorter', anchor: { quote: 'Hello', line: 5, lineTag: 'p' } };

test('serves HTML with line marks and the overlay script, other files untouched', async (t) => {
  const { server } = await setup(t);
  const html = await (await fetch(`${server.url}/page.html`)).text();
  assert.match(html, /<p data-inliner-line="5">Hello world<\/p>/);
  assert.match(html, /<script src="\/__inliner\/client\.js" data-inliner-file="page\.html" defer><\/script>\n<\/body>/);

  const css = await fetch(`${server.url}/style.css`);
  assert.equal(css.headers.get('content-type'), 'text/css; charset=utf-8');
  assert.equal(await css.text(), 'body { color: red }');

  const client = await fetch(`${server.url}/__inliner/client.js`);
  assert.match(client.headers.get('content-type'), /javascript/);
  assert.match(await client.text(), /inliner overlay/);
});

test('directories: index.html, listing with open-comment counts, redirect', async (t) => {
  const { server, json } = await setup(t);
  await json('POST', '/comments', comment);

  const index = await (await fetch(`${server.url}/docs/`)).text();
  assert.match(index, /data-inliner-file="docs\/index\.html"/);

  const listing = await (await fetch(`${server.url}/`)).text();
  assert.match(listing, /<a href="\/page\.html">page\.html<\/a><span class="count">1<\/span>/);
  assert.match(listing, /<a href="\/docs\/">docs\/<\/a>/);
  assert.doesNotMatch(listing, /style\.css|\.env|\.inliner/);

  const redirect = await fetch(`${server.url}/docs`, { redirect: 'manual' });
  assert.equal(redirect.status, 302);
  assert.equal(redirect.headers.get('location'), '/docs/');
  assert.match(await (await fetch(`${server.url}/empty/`)).text(), /No HTML files here/);
});

test('never serves dotfiles or anything outside the root', async (t) => {
  const { server, json } = await setup(t);
  await json('POST', '/comments', comment);
  for (const url of ['/.env', '/.inliner/comments.json', '/nope.html', '/%2e%2e/outside.txt', '/docs/%2e%2e/%2e%2e/outside.txt']) {
    assert.equal((await fetch(server.url + url)).status, 404, url);
  }
  // A raw request, because fetch() normalises ".." away before sending.
  const status = await new Promise((resolve, reject) => {
    http.get({ host: '127.0.0.1', port: server.port, path: '/../outside.txt', headers: { Host: 'localhost' } }, (res) => resolve(res.statusCode)).on('error', reject);
  });
  assert.equal(status, 404);
});

test('refuses requests for another host and cross-origin writes', async (t) => {
  const { server, json } = await setup(t);
  const status = await new Promise((resolve, reject) => {
    http.get({ host: '127.0.0.1', port: server.port, path: '/page.html', headers: { Host: 'evil.example' } }, (res) => resolve(res.statusCode)).on('error', reject);
  });
  assert.equal(status, 403);
  assert.equal((await json('POST', '/comments', comment, { Origin: 'https://evil.example' })).status, 403);
  assert.equal((await json('POST', '/comments', comment, { Origin: server.url })).status, 201);
});

test('comment API: create, list, edit, resolve, feedback, delete', async (t) => {
  const { server, root, json } = await setup(t);

  const created = await json('POST', '/comments', comment);
  assert.equal(created.status, 201);
  assert.equal((await created.json()).id, 1);
  assert.equal((await json('POST', '/comments', { ...comment, body: '' })).status, 400);
  assert.equal((await json('POST', '/comments', { ...comment, file: '../x.html' })).status, 400);
  assert.equal((await fetch(`${server.url}/__inliner/api/comments`, { method: 'POST', body: '{', headers: { 'Content-Type': 'application/json' } })).status, 400);

  const listed = await (await json('GET', '/comments?file=page.html')).json();
  assert.deepEqual(listed.comments.map((c) => c.body), ['Shorter']);
  assert.deepEqual((await (await json('GET', '/comments?file=docs/index.html')).json()).comments, []);

  assert.equal((await (await json('PATCH', '/comments/1', { body: 'Much shorter' })).json()).body, 'Much shorter');
  const feedback = await (await json('GET', '/feedback?file=page.html')).text();
  assert.match(feedback, /### #1 · `page\.html:5` · text/);
  assert.match(feedback, /> Much shorter/);
  assert.equal(fs.readFileSync(path.join(root, '.inliner', 'feedback.md'), 'utf8'), await (await json('GET', '/feedback')).text());

  assert.equal((await (await json('POST', '/anchors', { file: 'page.html', updates: [{ id: 1, anchored: false }] })).json()).changed, true);
  assert.equal((await (await json('PATCH', '/comments/1', { status: 'resolved' })).json()).status, 'resolved');
  assert.equal((await json('PATCH', '/comments/1', { status: 'bogus' })).status, 400);
  assert.equal((await json('PATCH', '/comments/9', { body: 'x' })).status, 404);

  assert.equal((await json('DELETE', '/comments/1')).status, 200);
  assert.equal((await json('DELETE', '/comments/1')).status, 404);
  assert.equal((await json('GET', '/nope')).status, 404);
});

test('announces file and comment changes to open pages', async (t) => {
  const { server, root, json } = await setup(t);
  await fetch(`${server.url}/page.html`); // a page has to be loaded before it is watched
  await fetch(`${server.url}/style.css`);

  const res = await fetch(`${server.url}/__inliner/events`);
  assert.equal(res.headers.get('content-type'), 'text/event-stream');
  const reader = res.body.getReader();
  t.after(() => reader.cancel());
  const decoder = new TextDecoder();
  let buffer = '';
  const next = async (event) => {
    const pattern = new RegExp(`event: ${event}\\ndata: (.*)\\n\\n`);
    for (;;) {
      const m = pattern.exec(buffer);
      if (m) {
        buffer = buffer.slice(m.index + m[0].length);
        return JSON.parse(m[1]);
      }
      const { value, done } = await reader.read();
      if (done) throw new Error('event stream ended');
      buffer += decoder.decode(value, { stream: true });
    }
  };

  fs.writeFileSync(path.join(root, 'page.html'), PAGE.replace('Hello', 'Goodbye, cruel'));
  assert.deepEqual(await next('reload'), { path: 'page.html' });
  fs.writeFileSync(path.join(root, 'style.css'), 'body { color: blue; }');
  assert.deepEqual(await next('reload'), { path: 'style.css' });

  await json('POST', '/comments', { ...comment, anchor: {} });
  assert.deepEqual(await next('comments'), {});
  server.store.setStatus([1], 'resolved'); // e.g. `inliner resolve 1` in another process
  assert.deepEqual(await next('comments'), {});
});

test('falls back to the next port when the requested one is taken', async (t) => {
  const { server, root } = await setup(t);
  const second = await startServer({ root, port: server.port });
  t.after(() => second.close());
  assert.notEqual(second.port, server.port);
  await assert.rejects(startServer({ root, port: server.port, strictPort: true }), /already in use/);
});

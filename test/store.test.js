import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { Store, StoreError, normalizeFile } from '../src/store.js';

function tempStore(files = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'inliner-store-'));
  for (const [name, text] of Object.entries(files)) fs.writeFileSync(path.join(root, name), text);
  return new Store(root);
}

const PAGE = '<html>\n<body>\n<h1>Title</h1>\n<p class="lead">Hello world</p>\n</body>\n</html>\n';
const textComment = (over = {}) => ({
  file: 'page.html',
  kind: 'text',
  body: 'Say more here',
  anchor: { quote: 'Hello', prefix: '', suffix: ' world', selector: 'body > p', tag: '<p class="lead">', line: 4, lineTag: 'p' },
  ...over,
});

test('normalizeFile keeps paths inside the root', () => {
  assert.equal(normalizeFile('a/b.html'), 'a/b.html');
  assert.equal(normalizeFile('/a//b.html'), 'a/b.html');
  assert.equal(normalizeFile('a\\b.html'), 'a/b.html');
  assert.equal(normalizeFile('a/../b.html'), 'b.html');
  for (const bad of ['../x.html', 'a/../../x', '..', '', '.', null, 42]) assert.equal(normalizeFile(bad), null);
});

test('add assigns ids, writes comments.json, feedback.md and a .gitignore', () => {
  const store = tempStore({ 'page.html': PAGE });
  const a = store.add(textComment());
  const b = store.add({ file: 'page.html', kind: 'page', body: '  Darker theme  ', anchor: { quote: 'ignored' } });
  assert.equal(a.id, 1);
  assert.equal(b.id, 2);
  assert.equal(b.body, 'Darker theme');
  assert.deepEqual(b.anchor, {});
  assert.equal(fs.readFileSync(path.join(store.dir, '.gitignore'), 'utf8'), '*\n');
  assert.equal(JSON.parse(fs.readFileSync(store.file, 'utf8')).comments.length, 2);

  const md = fs.readFileSync(store.feedbackFile, 'utf8');
  assert.match(md, /Open comments: 2/);
  assert.match(md, /### #1 · `page\.html:4` · text/);
  assert.match(md, /- Selected text: "Hello"/);
  assert.match(md, /- In context: "…\[\[Hello\]\] world…"/);
  assert.match(md, /- Inside: `<p class="lead">` \(selector `body > p`\)/);
  assert.match(md, /> Say more here/);
  assert.match(md, /### #2 · `page\.html` · page/);
  assert.match(md, /resolve <id> -m/);
});

test('rejects invalid comments', () => {
  const store = tempStore();
  assert.throws(() => store.add(textComment({ body: '   ' })), StoreError);
  assert.throws(() => store.add(textComment({ kind: 'nope' })), StoreError);
  assert.throws(() => store.add(textComment({ file: '../etc/passwd' })), StoreError);
  assert.equal(fs.existsSync(store.file), false);
});

test('ids are never reused after deletion', () => {
  const store = tempStore();
  store.add(textComment());
  store.add(textComment());
  assert.deepEqual(store.remove([2, 9]).map((c) => c.id), [2]);
  assert.equal(store.add(textComment()).id, 3);
});

test('resolve, reopen and clear', () => {
  const store = tempStore({ 'page.html': PAGE });
  store.add(textComment());
  store.add(textComment({ body: 'Second' }));

  const { changed, missing } = store.setStatus([1, 7], 'resolved', { note: 'Rewrote it' });
  assert.deepEqual(changed.map((c) => c.id), [1]);
  assert.deepEqual(missing, [7]);
  assert.deepEqual(store.list()[0].resolution, { by: 'cli', note: 'Rewrote it' });
  assert.doesNotMatch(store.feedback(), /### #1 /);
  assert.match(store.feedback({ includeResolved: true }), /### #1 [\s\S]*Status: resolved — Rewrote it/);

  store.setStatus([1], 'open');
  assert.equal(store.list()[0].resolution, undefined);
  assert.match(store.feedback(), /### #1 /);

  store.update(2, { status: 'resolved' });
  assert.equal(store.clear(), 1);
  assert.deepEqual(store.list().map((c) => c.id), [1]);
  assert.equal(store.clear({ all: true }), 1);
  assert.match(fs.readFileSync(store.feedbackFile, 'utf8'), /No open comments\./);
});

test('feedback drops a line number that no longer matches the file', () => {
  const store = tempStore({ 'page.html': PAGE });
  store.add(textComment());
  fs.writeFileSync(path.join(store.root, 'page.html'), `<html>\n<body>\n<nav></nav>\n<h1>Title</h1>\n<p class="lead">Hello world</p>\n</body>\n</html>\n`);
  const md = store.feedback();
  assert.match(md, /### #1 · `page\.html` · text/);
  assert.match(md, /line number unknown/);
});

test('syncAnchors records moves and losses without touching updatedAt', () => {
  const store = tempStore({ 'page.html': PAGE.replace('<h1>', '<nav></nav>\n<h1>') });
  const { updatedAt } = store.add(textComment());

  assert.equal(store.syncAnchors('page.html', [{ id: 1, anchored: true, line: 5, lineTag: 'p' }]), true);
  assert.equal(store.syncAnchors('page.html', [{ id: 1, anchored: true, line: 5, lineTag: 'p' }]), false);
  assert.equal(store.syncAnchors('other.html', [{ id: 1, anchored: false }]), false);
  assert.match(store.feedback(), /### #1 · `page\.html:5` · text/);
  assert.equal(store.list()[0].updatedAt, updatedAt);

  assert.equal(store.syncAnchors('page.html', [{ id: 1, anchored: false }]), true);
  const md = store.feedback();
  assert.match(md, /### #1 · `page\.html` · text/);
  assert.match(md, /was not found in the page as last rendered/);
});

test('feedback can be limited to one file and survives a missing file', () => {
  const store = tempStore({ 'page.html': PAGE });
  store.add(textComment());
  store.add(textComment({ file: 'gone.html', body: 'Other page' }));
  assert.doesNotMatch(store.feedback({ file: 'page.html' }), /gone\.html/);
  assert.match(store.feedback(), /## gone\.html\n\n_This file no longer exists at this path\._/);
});

test('a corrupt comments.json is reported, not overwritten', () => {
  const store = tempStore();
  store.add(textComment());
  fs.writeFileSync(store.file, '{ nope');
  assert.throws(() => store.add(textComment()), /not valid JSON/);
  assert.equal(fs.readFileSync(store.file, 'utf8'), '{ nope');
});

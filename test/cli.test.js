import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { Store } from '../src/store.js';

const BIN = fileURLToPath(new URL('../bin/inliner.js', import.meta.url));

function project() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'inliner-cli-'));
  fs.mkdirSync(path.join(root, 'out'));
  fs.writeFileSync(path.join(root, 'out', 'page.html'), '<body>\n<p>Hello world</p>\n</body>\n');
  fs.writeFileSync(path.join(root, 'other.html'), '<body>\n<h1>Other</h1>\n</body>\n');
  const store = new Store(root);
  store.add({ file: 'out/page.html', kind: 'text', body: 'Shorter', anchor: { quote: 'Hello', line: 2, lineTag: 'p' } });
  store.add({ file: 'other.html', kind: 'page', body: 'Needs a footer' });
  const run = (...args) => {
    const r = spawnSync(process.execPath, [BIN, ...args], { cwd: root, encoding: 'utf8' });
    return { code: r.status, out: r.stdout, err: r.stderr };
  };
  return { root, store, run };
}

test('show prints the feedback, optionally for one file or as JSON', () => {
  const { run, root } = project();
  const all = run('show');
  assert.equal(all.code, 0);
  assert.match(all.out, /### #1 · `out\/page\.html:2` · text/);
  assert.match(all.out, /### #2 · `other\.html` · page/);

  const one = run('show', 'out/page.html');
  assert.match(one.out, /Open comments: 1/);
  assert.doesNotMatch(one.out, /other\.html/);

  assert.deepEqual(JSON.parse(run('show', '--json').out).map((c) => c.id), [1, 2]);
  assert.equal(run('show', '../elsewhere.html').code, 2);

  // From a subdirectory, -C points back at the project root.
  const sub = spawnSync(process.execPath, [BIN, 'show', '-C', '..'], { cwd: path.join(root, 'out'), encoding: 'utf8' });
  assert.match(sub.stdout, /Open comments: 2/);
});

test('resolve, reopen, rm and clear', () => {
  const { run, store } = project();
  assert.deepEqual(run('resolve', '#1', '-m', 'Trimmed the intro'), { code: 0, out: '#1 resolved\n', err: '' });
  assert.deepEqual(store.list()[0].resolution, { by: 'cli', note: 'Trimmed the intro' });
  assert.doesNotMatch(run('show').out, /### #1 /);
  assert.match(run('show', '--all').out, /Status: resolved — Trimmed the intro/);

  const missing = run('resolve', '2', '9');
  assert.equal(missing.code, 1);
  assert.equal(missing.out, '#2 resolved\n');
  assert.match(missing.err, /no comment #9/);

  assert.equal(run('reopen', '2').out, '#2 reopened\n');
  assert.equal(run('clear').out, '1 comment deleted\n');
  assert.equal(run('rm', '2').out, '#2 deleted\n');
  assert.equal(run('rm', '2').code, 1);
  assert.equal(store.list().length, 0);
  assert.equal(run('show').out, '# Inline review comments\n\nNo open comments.\n');
});

test('usage errors', () => {
  const { run } = project();
  assert.equal(run('resolve').code, 2);
  assert.equal(run('resolve', 'abc').code, 2);
  assert.equal(run('--bogus').code, 2);
  assert.equal(run('no-such-file.html', '--no-open').code, 2);
  assert.equal(run('out/page.html', '--no-open', '-p', 'abc').code, 2);
  assert.match(run('--help').out, /Usage/);
  assert.match(run('--version').out, /^\d+\.\d+\.\d+\n$/);
});

test('serve prints the page URL and stops on SIGTERM', async () => {
  const { root } = project();
  const { spawn } = await import('node:child_process');
  const child = spawn(process.execPath, [BIN, 'out/page.html', '--no-open', '-p', '0'], { cwd: root });
  let out = '';
  child.stdout.on('data', (d) => (out += d));
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`no URL printed:\n${out}`)), 5000);
    child.stdout.on('data', () => {
      const m = /http:\/\/localhost:\d+\/out\/page\.html/.exec(out);
      if (m) {
        clearTimeout(timer);
        resolve(m[0]);
      }
    });
  });
  assert.match(await (await fetch(url)).text(), /data-inliner-file="out\/page\.html"/);
  const exited = new Promise((resolve) => child.on('exit', (code) => resolve(code)));
  child.kill('SIGTERM');
  assert.equal(await exited, 0);
});

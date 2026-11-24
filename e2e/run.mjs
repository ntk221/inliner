// End-to-end check in a real browser: comment on a page, see the feedback file,
// let "the AI" rewrite the page, and watch the comments follow.
//
//   node e2e/run.mjs
//
// Builds the binary with `go build` (or uses INLINER_BIN) and drives it with
// Playwright + Chromium (`npm i -g playwright`); skipped when Playwright is missing.
// Set INLINER_E2E_SHOTS=<dir> to keep screenshots of each step.

import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import fs from 'node:fs';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

let chromium;
try {
  ({ chromium } = createRequire(import.meta.url)('playwright'));
} catch {
  console.log('playwright is not installed — skipping e2e');
  process.exit(0);
}

const PAGE_V1 = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Report</title>
  <style>
    body { font: 16px/1.7 system-ui, sans-serif; max-width: 680px; margin: 40px auto; }
    .hero { background: #eef2ff; padding: 24px; border-radius: 12px; }
  </style>
</head>
<body>
  <header class="hero">
    <h1>Quarterly report</h1>
    <p class="lead">Revenue grew strongly this quarter, driven by new customers.</p>
  </header>
  <main>
    <h2 id="summary">Summary</h2>
    <p>Revenue is up <strong>120%</strong> year over year.
       Costs stayed flat, so margins improved.</p>
    <img id="chart" src="chart.svg" alt="Revenue chart" width="240" height="80">
    <p>See the next page for details.</p>
  </main>
</body>
</html>
`;

// The same page after an edit that pushes everything down and re-wraps a paragraph.
const PAGE_V2 = PAGE_V1.replace('<body>', '<body>\n  <nav>\n    <a href="#summary">Summary</a>\n  </nav>').replace(
  'Revenue is up <strong>120%</strong> year over year.\n       Costs stayed flat, so margins improved.',
  'Revenue is up\n      <strong>120%</strong>\n      year over year. Costs stayed flat, so margins improved.',
);

const shots = process.env.INLINER_E2E_SHOTS;
const root = fs.mkdtempSync(path.join(os.tmpdir(), 'inliner-e2e-'));
fs.writeFileSync(path.join(root, 'page.html'), PAGE_V1);
fs.writeFileSync(path.join(root, 'chart.svg'), '<svg xmlns="http://www.w3.org/2000/svg" width="240" height="80"><rect width="240" height="80" fill="#93c5fd"/></svg>');

let bin = process.env.INLINER_BIN;
if (!bin) {
  bin = path.join(root, '..', `inliner-e2e-bin-${process.pid}${process.platform === 'win32' ? '.exe' : ''}`);
  execFileSync('go', ['build', '-o', bin, '.'], { cwd: fileURLToPath(new URL('..', import.meta.url)), stdio: 'inherit' });
}

// Start the real command, as a user would, and read the URL it prints.
const child = spawn(bin, ['page.html', '--no-open', '--port', '0'], { cwd: root, stdio: ['ignore', 'pipe', 'inherit'] });
const server = {
  url: await new Promise((resolve, reject) => {
    let out = '';
    child.stdout.on('data', (chunk) => {
      out += chunk;
      const m = /(http:\/\/localhost:\d+)\/page\.html/.exec(out);
      if (m) resolve(m[1]);
    });
    child.on('exit', (code) => reject(new Error(`inliner exited with code ${code}:\n${out}`)));
    child.on('error', reject);
  }),
  close: () =>
    new Promise((resolve) => {
      child.removeAllListeners('exit');
      child.on('exit', resolve);
      child.kill();
    }),
};
const browser = await chromium.launch();
const failures = [];

try {
  const context = await browser.newContext({ viewport: { width: 1200, height: 800 }, locale: 'en-US', deviceScaleFactor: shots ? 2 : 1 });
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const page = await context.newPage();
  page.on('pageerror', (err) => failures.push(`page error: ${err.message}`));
  page.on('console', (msg) => msg.type() === 'error' && failures.push(`console error: ${msg.text()}`));

  const shot = async (name) => shots && page.screenshot({ path: path.join(shots, `${name}.png`) });
  const feedback = () => fs.readFileSync(path.join(root, '.inliner', 'feedback.md'), 'utf8');
  const stored = () => JSON.parse(fs.readFileSync(path.join(root, '.inliner', 'comments.json'), 'utf8')).comments;
  const highlighted = (name) => page.evaluate((n) => [...(CSS.highlights.get(n) ?? [])].map((r) => r.toString().replace(/\s+/g, ' ')), name);

  /** Drag-select the first occurrence of `text` inside `selector`, like a user would. */
  async function selectText(selector, text) {
    const box = await page.evaluate(
      ([sel, needle]) => {
        const walker = document.createTreeWalker(document.querySelector(sel), NodeFilter.SHOW_TEXT);
        for (let node = walker.nextNode(); node; node = walker.nextNode()) {
          const at = node.data.indexOf(needle);
          if (at === -1) continue;
          const range = document.createRange();
          range.setStart(node, at);
          range.setEnd(node, at + needle.length);
          const r = range.getBoundingClientRect();
          return { x1: r.left, x2: r.right, y: r.top + r.height / 2 };
        }
        return null;
      },
      [selector, text],
    );
    assert.ok(box, `text not found: ${text}`);
    await page.mouse.move(box.x1 + 1, box.y);
    await page.mouse.down();
    await page.mouse.move(box.x2 - 1, box.y, { steps: 8 });
    await page.mouse.up();
  }

  await page.goto(`${server.url}/page.html`);
  await page.locator('.fab').waitFor();

  // 1. Text comment.
  await selectText('.lead', 'driven by new customers');
  await page.locator('.selbtn').click();
  await page.locator('.composer textarea').fill('Too vague — name the two biggest customers.');
  await shot('1-composer');
  await page.keyboard.press('Control+Enter');
  await page.locator('.pin').first().waitFor();
  assert.deepEqual(await highlighted('inliner-active'), ['driven by new customers']);
  assert.match(feedback(), /### #1 · `page\.html:14` · text/);
  assert.match(feedback(), /Selected text: "driven by new customers"/);
  assert.match(feedback(), /> Too vague — name the two biggest customers\./);

  // 2. Text comment spanning inline markup and a line break in the source.
  await page.evaluate(() => {
    const p = document.querySelector('main p');
    const range = document.createRange();
    range.setStart(p.firstChild, 'Revenue is '.length);
    range.setEnd(p.lastChild, p.lastChild.data.indexOf('Costs stayed flat') + 'Costs stayed flat'.length);
    getSelection().removeAllRanges();
    getSelection().addRange(range);
  });
  await page.locator('.selbtn').click();
  await page.locator('.composer textarea').fill('Split this into two bullet points.');
  await page.locator('.composer .primary').click();
  await page.waitForFunction(() => document.querySelector('inliner-root').shadowRoot.querySelectorAll('.card').length === 2);
  assert.equal(stored()[1].anchor.quote, 'up 120% year over year. Costs stayed flat');
  assert.match(feedback(), /### #2 · `page\.html:18` · text/);

  // 3. Element comment on the image.
  await page.locator('.fab').click();
  await page.locator('.btn', { hasText: 'Pick element' }).click();
  await page.mouse.click(...(await page.evaluate(() => {
    const r = document.querySelector('#chart').getBoundingClientRect();
    return [r.left + 20, r.top + 20];
  })));
  await page.locator('.composer textarea').fill('Make the chart full width.');
  await page.keyboard.press('Control+Enter');
  await page.locator('.box:not(.draft)').waitFor();
  assert.match(feedback(), /### #3 · `page\.html:20` · element/);
  assert.match(feedback(), /Element: `<img id="chart" src="chart\.svg"/);
  assert.match(feedback(), /Selector: `#chart`/);

  // 4. Page comment.
  await page.locator('.btn', { hasText: 'Whole page' }).click();
  await page.locator('.composer textarea').fill('Overall: use a darker theme.');
  await page.locator('.composer .primary').click();
  await page.waitForFunction(() => document.querySelector('inliner-root').shadowRoot.querySelectorAll('.card').length === 4);
  assert.match(feedback(), /### #4 · `page\.html` · page/);
  await shot('2-panel');

  // 5. Copy for AI puts the same Markdown on the clipboard.
  await page.locator('.btn', { hasText: 'Copy for AI' }).click();
  await page.locator('.toast', { hasText: 'Copied' }).waitFor();
  const clipboard = await page.evaluate(() => navigator.clipboard.readText());
  assert.match(clipboard, /# Inline review comments/);
  assert.match(clipboard, /Open comments: 4/);

  // 6. The AI rewrites the file: the page reloads and the comments follow their text.
  const reloaded = page.waitForEvent('load');
  fs.writeFileSync(path.join(root, 'page.html'), PAGE_V2);
  await reloaded;
  await page.waitForFunction(() => document.querySelector('inliner-root')?.shadowRoot.querySelectorAll('.pin').length === 3);
  assert.deepEqual((await highlighted('inliner-comment')).sort(), ['driven by new customers', 'up 120% year over year. Costs stayed flat']);
  await page.waitForFunction(() => document.querySelector('inliner-root').shadowRoot.querySelector('.panel:not([hidden])'));
  await assert.doesNotReject(waitFor(() => /### #1 · `page\.html:17` · text/.test(feedback())), 'line of #1 follows the edit');
  assert.match(feedback(), /### #2 · `page\.html:21` · text/);
  assert.match(feedback(), /### #3 · `page\.html:24` · element/);
  await shot('3-after-rewrite');

  // 7. The AI removes the commented text: the comment stays, flagged as not found.
  const reloadedAgain = page.waitForEvent('load');
  fs.writeFileSync(path.join(root, 'page.html'), PAGE_V2.replace(', driven by new customers', ''));
  await reloadedAgain;
  await page.locator('.warn').waitFor();
  await waitFor(() => stored()[0].anchored === false);
  assert.match(feedback(), /#1 · `page\.html` · text[\s\S]*?was not found in the page as last rendered/);

  // 8. The AI resolves the comment from the command line: the open page shows it.
  assert.equal(execFileSync(bin, ['resolve', '1', '-m', 'Named Acme and Globex.'], { cwd: root, encoding: 'utf8' }), '#1 resolved\n');
  await page.locator('.note', { hasText: 'Named Acme and Globex.' }).waitFor();
  assert.equal(await page.locator('.fab .badge').textContent(), '3');
  assert.doesNotMatch(feedback(), /### #1 /);

  // 9. Resolve and delete from the panel.
  await page.locator('.card', { hasText: 'Overall: use a darker theme.' }).locator('.link', { hasText: 'Resolve' }).click();
  await waitFor(() => stored()[3].status === 'resolved');
  page.once('dialog', (dialog) => dialog.accept());
  await page.locator('.card', { hasText: 'Make the chart full width.' }).locator('.link', { hasText: 'Delete' }).click();
  await waitFor(() => stored().length === 3);
  await page.locator('.box:not(.draft)').waitFor({ state: 'detached' });
  await shot('4-resolved');
} catch (err) {
  failures.push(err.stack ?? String(err));
} finally {
  await browser.close();
  await server.close();
  fs.rmSync(root, { recursive: true, force: true });
  if (!process.env.INLINER_BIN) fs.rmSync(bin, { force: true });
}

async function waitFor(check, timeout = 5000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    try {
      if (check()) return;
    } catch {
      // not there yet
    }
    if (Date.now() > deadline) throw new Error(`timed out waiting for: ${check}`);
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
}

if (failures.length) {
  console.error(`e2e FAILED\n\n${failures.join('\n\n')}`);
  process.exit(1);
}
console.log('e2e ok');

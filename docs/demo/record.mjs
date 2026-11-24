// Records docs/demo.gif: a real session against the real binary, driven by
// Playwright, with captions added by ffmpeg.
//
//   node docs/demo/record.mjs
//
// Needs Go, ffmpeg, a Japanese font, and Playwright with Chromium.

import { execFileSync, spawn } from 'node:child_process';
import fs from 'node:fs';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const { chromium } = createRequire(import.meta.url)('playwright');

const WIDTH = 1000;
const HEIGHT = 620;
const CAPTION_HEIGHT = 46;
const FPS = 10;
const repo = fileURLToPath(new URL('../..', import.meta.url));
const output = path.join(repo, 'docs', 'demo.gif');

// --- the page under review, before and after "the AI" acts on the comments ---

const page1 = (lead, buttonStyle) => `<!doctype html>
<html lang="ja">
<head>
  <meta charset="utf-8">
  <title>チームプラン</title>
  <style>
    body { margin: 0; background: #f7f7f5; color: #1f2430; font-family: system-ui, "Hiragino Sans", "Noto Sans JP", sans-serif; }
    main { max-width: 560px; margin: 36px 44px; }
    h1 { font-size: 28px; margin: 0 0 14px; }
    .lead { font-size: 16px; line-height: 1.9; margin: 0 0 24px; }
    .cards { display: flex; gap: 12px; margin-bottom: 28px; }
    .card { flex: 1; background: #fff; border: 1px solid #dedcd6; border-radius: 10px; padding: 14px 16px; }
    .card b { display: block; font-size: 13px; color: #6b7280; font-weight: 600; }
    .card span { font-size: 20px; font-weight: 700; }
    .cta { display: inline-block; border-radius: 8px; text-decoration: none; ${buttonStyle} }
  </style>
</head>
<body>
  <main>
    <h1>新しい「チームプラン」</h1>
    <p class="lead">${lead}</p>
    <div class="cards">
      <div class="card"><b>料金</b><span>月額 980 円</span></div>
      <div class="card"><b>メンバー</b><span>無制限</span></div>
      <div class="card"><b>履歴</b><span>90 日間</span></div>
    </div>
    <a class="cta" href="#">無料で試す</a>
  </main>
</body>
</html>
`;

const VAGUE = 'すごく便利でとても使いやすいプラン';
const BEFORE = page1(`チームプランは、${VAGUE}です。<br>まずは 14 日間、無料でお試しください。`, 'padding: 8px 16px; background: #e5e5e2; color: #555; font-size: 14px;');
const AFTER = page1(
  'チームプランは、メンバー数無制限・履歴 90 日保存で、<br>月額 980 円から使えます。まずは 14 日間、無料でお試しください。',
  'padding: 12px 28px; background: #2563eb; color: #fff; font-size: 16px; font-weight: 700;',
);

const COMMENT_1 = '抽象的すぎる。料金や機能の具体的な数字を入れて';
const COMMENT_2 = 'ボタンが目立たない。色を付けて大きく';

const CAPTIONS = [
  '1. AI が書いた HTML を inliner で開く',
  '2. 気になるテキストを選択してコメント',
  '3. ボタンや画像は「要素を選択」でクリック',
  '4. コメントは .inliner/feedback.md に。AI に「これを反映して」と頼むだけ',
  '5. AI が直すと自動でリロード。対応メモも届く',
];

// --- helpers ---

const escapeHtml = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

/** A terminal-looking page; `lines` are revealed by reveal() below. */
function terminalHtml(command, lines) {
  const body = lines
    .map((line) => {
      const cls = /^#/.test(line) ? 'h' : /^>/.test(line) ? 'q' : /^\s+inliner /.test(line) ? 'c' : '';
      return `<div class="line ${cls}" hidden>${escapeHtml(line) || '&nbsp;'}</div>`;
    })
    .join('\n');
  return `<!doctype html>
<meta charset="utf-8">
<style>
  body { margin: 0; padding: 18px 22px; background: #16181d; color: #d6d9df; font: 12.5px/1.5 "DejaVu Sans Mono", "IPAGothic", monospace; }
  .prompt { color: #8bd49c; }
  .h { color: #7fb4ff; font-weight: 700; }
  .q { color: #f2c46d; }
  .c { color: #8bd49c; }
  .line { white-space: pre-wrap; overflow-wrap: anywhere; }
  #caret { display: inline-block; width: 7px; height: 14px; background: #d6d9df; vertical-align: -2px; }
</style>
<div><span class="prompt">~/site $</span> <span id="cmd" data-text="${escapeHtml(command)}"></span><span id="caret"></span></div>
${body}
`;
}

/** Type the command, then show the output. */
async function reveal(page) {
  await page.waitForTimeout(500);
  await page.evaluate(async () => {
    const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
    const cmd = document.querySelector('#cmd');
    for (const ch of cmd.dataset.text) {
      cmd.textContent += ch;
      await sleep(45);
    }
    await sleep(350);
    document.querySelector('#caret').remove();
    for (const line of document.querySelectorAll('.line')) {
      line.hidden = false;
      await sleep(18);
    }
  });
}

// A visible mouse pointer: headless browsers do not draw one.
const CURSOR_SCRIPT = `
  if (location.protocol.startsWith('http')) addEventListener('DOMContentLoaded', () => {
    const cursor = document.createElement('div');
    cursor.style.cssText = 'position:fixed;top:0;left:0;z-index:2147483647;pointer-events:none;width:22px;height:22px;display:none;transition:scale .12s;transform-origin:3px 2px';
    cursor.innerHTML = '<svg viewBox="0 0 22 22" width="22" height="22"><path d="M3 2l0 15 4.2-3.6 2.6 6 2.6-1.1-2.6-5.9 5.6-.4z" fill="#111" stroke="#fff" stroke-width="1.4" stroke-linejoin="round"/></svg>';
    document.documentElement.append(cursor);
    addEventListener('mousemove', (e) => {
      cursor.style.display = 'block';
      cursor.style.translate = e.clientX - 3 + 'px ' + (e.clientY - 2) + 'px';
    }, true);
    addEventListener('mousedown', () => (cursor.style.scale = '0.8'), true);
    addEventListener('mouseup', () => (cursor.style.scale = '1'), true);
  });
`;

const work = fs.mkdtempSync(path.join(os.tmpdir(), 'inliner-demo-'));
const site = path.join(work, 'site');
const binDir = path.join(work, 'bin');
fs.mkdirSync(site);
fs.mkdirSync(binDir);
fs.writeFileSync(path.join(site, 'plan.html'), BEFORE);
const bin = path.join(binDir, 'inliner');
execFileSync('go', ['build', '-o', bin, '.'], { cwd: repo, stdio: 'inherit' });

// With the binary on PATH, the feedback file says "inliner resolve …" rather than a full path.
const env = { ...process.env, PATH: `${binDir}${path.delimiter}${process.env.PATH}` };
const tidy = (text) => text.replaceAll(fs.realpathSync(site), '~/site');

const child = spawn(bin, ['plan.html', '--no-open'], { cwd: site, env, stdio: ['ignore', 'pipe', 'inherit'] });
let banner = '';
const url = await new Promise((resolve, reject) => {
  child.stdout.on('data', (chunk) => {
    banner += chunk;
    const m = /http:\/\/localhost:\d+\/plan\.html/.exec(banner);
    if (m) setTimeout(() => resolve(m[0]), 100); // let the rest of the banner arrive
  });
  child.on('exit', (code) => reject(new Error(`inliner exited with code ${code}`)));
});

const browser = await chromium.launch();
const marks = []; // seconds into the recording at which each caption starts
let videoPath;

try {
  const context = await browser.newContext({
    viewport: { width: WIDTH, height: HEIGHT },
    locale: 'ja-JP',
    recordVideo: { dir: work, size: { width: WIDTH, height: HEIGHT } },
  });
  await context.addInitScript(CURSOR_SCRIPT);
  const page = await context.newPage();
  const started = Date.now();
  const caption = () => marks.push((Date.now() - started) / 1000);
  const pause = (ms) => page.waitForTimeout(ms);

  let mouse = { x: 500, y: 300 };
  /** Move the pointer at a human pace. */
  async function moveTo(x, y, ms = 450) {
    const steps = Math.max(2, Math.round(ms / 16));
    const from = mouse;
    for (let i = 1; i <= steps; i++) {
      const t = i / steps;
      const ease = t * t * (3 - 2 * t);
      await page.mouse.move(from.x + (x - from.x) * ease, from.y + (y - from.y) * ease);
      await pause(16);
    }
    mouse = { x, y };
  }
  async function click(locator, ms) {
    const box = await locator.boundingBox();
    await moveTo(box.x + box.width / 2, box.y + box.height / 2, ms);
    await pause(150);
    await page.mouse.down();
    await pause(90);
    await page.mouse.up();
  }
  const terminal = async (name, command, lines) => {
    const file = path.join(work, `${name}.html`);
    fs.writeFileSync(file, terminalHtml(command, lines));
    await page.goto(pathToFileURL(file).href);
    await reveal(page);
  };

  // 1. Open the page.
  await page.goto('about:blank');
  caption();
  await terminal('open', 'inliner plan.html', tidy(banner).replace(/\n+$/, '').split('\n'));
  await pause(1300);
  await page.goto(url);
  await page.locator('.fab').waitFor();
  await page.mouse.move(mouse.x, mouse.y);
  await pause(1200);

  // 2. Select text, comment on it.
  caption();
  const span = await page.evaluate((needle) => {
    const node = document.querySelector('.lead').firstChild;
    const range = document.createRange();
    range.setStart(node, node.data.indexOf(needle));
    range.setEnd(node, node.data.indexOf(needle) + needle.length);
    const r = range.getBoundingClientRect();
    return { x1: r.left, x2: r.right, y: r.top + r.height / 2 };
  }, VAGUE);
  await moveTo(span.x1 + 1, span.y, 600);
  await page.mouse.down();
  await moveTo(span.x2 - 1, span.y + 2, 800);
  await page.mouse.up();
  await pause(500);
  await click(page.locator('.selbtn'));
  await pause(400);
  await page.keyboard.type(COMMENT_1, { delay: 55 });
  await pause(500);
  await click(page.locator('.composer .primary'));
  await page.locator('.pin').waitFor();
  await pause(1300);

  // 3. Pick an element, comment on it.
  caption();
  await click(page.locator('.fab'));
  await pause(600);
  await click(page.locator('.btn', { hasText: '要素を選択' }));
  await pause(300);
  const button = await page.locator('.cta').boundingBox();
  await moveTo(button.x + button.width / 2, button.y + button.height / 2, 700);
  await pause(500);
  await page.mouse.down();
  await pause(90);
  await page.mouse.up();
  await pause(400);
  await page.keyboard.type(COMMENT_2, { delay: 55 });
  await pause(500);
  await click(page.locator('.composer .primary'));
  await page.locator('.box:not(.draft)').waitFor();
  await pause(1600);

  // 4. What the AI gets.
  caption();
  const feedback = tidy(fs.readFileSync(path.join(site, '.inliner', 'feedback.md'), 'utf8'));
  await terminal('feedback', 'cat .inliner/feedback.md', feedback.replace(/\n+$/, '').split('\n'));
  await pause(4500);

  // 5. The AI edits the file and resolves the comments; the page follows.
  await page.goto(url);
  await page.locator('.pin').first().waitFor();
  await page.mouse.move(mouse.x, mouse.y);
  caption();
  await pause(1500);
  fs.writeFileSync(path.join(site, 'plan.html'), AFTER);
  execFileSync(bin, ['resolve', '1', '-m', 'メンバー無制限・履歴 90 日・月額 980 円を明記'], { cwd: site, env });
  execFileSync(bin, ['resolve', '2', '-m', '青い塗りにして、余白と文字を大きく'], { cwd: site, env });
  await page.locator('.note').first().waitFor();
  await pause(4000);
  marks.push((Date.now() - started) / 1000);

  await context.close();
  videoPath = await page.video().path();
} finally {
  await browser.close();
  child.kill();
}

// --- captions: rendered by the browser so they use the same fonts ---

const captionBrowser = await chromium.launch();
const captionPage = await captionBrowser.newPage({ viewport: { width: WIDTH, height: CAPTION_HEIGHT } });
const captionFiles = [];
for (const [i, text] of CAPTIONS.entries()) {
  await captionPage.setContent(`<body style="margin:0;height:${CAPTION_HEIGHT}px;display:flex;align-items:center;padding:0 22px;box-sizing:border-box;
    background:#e8930c;color:#fff;font:700 17px system-ui,'Hiragino Sans','Noto Sans JP',sans-serif">${escapeHtml(text)}</body>`);
  captionFiles.push(path.join(work, `caption-${i}.png`));
  await captionPage.screenshot({ path: captionFiles[i] });
}
await captionBrowser.close();

// --- assemble the GIF ---

const first = marks[0];
const duration = marks.at(-1) - first;
const filters = [`[0:v]trim=start=${first}:duration=${duration},setpts=PTS-STARTPTS,pad=iw:ih+${CAPTION_HEIGHT}:0:${CAPTION_HEIGHT}[v0]`];
captionFiles.forEach((_, i) => {
  const from = (marks[i] - first).toFixed(2);
  const to = (marks[i + 1] - first).toFixed(2);
  filters.push(`[v${i}][${i + 1}:v]overlay=0:0:enable='between(t,${from},${to})'[v${i + 1}]`);
});
filters.push(
  `[v${captionFiles.length}]fps=${FPS},split[a][b]`,
  '[a]palettegen=max_colors=96:stats_mode=diff[p]',
  '[b][p]paletteuse=dither=none:diff_mode=rectangle',
);
execFileSync(
  'ffmpeg',
  ['-y', '-loglevel', 'error', '-i', videoPath, ...captionFiles.flatMap((f) => ['-i', f]), '-filter_complex', filters.join(';'), output],
  { stdio: 'inherit' },
);

fs.rmSync(work, { recursive: true, force: true });
console.log(`${path.relative(repo, output)}: ${(fs.statSync(output).size / 1e6).toFixed(1)} MB, ${duration.toFixed(0)} s`);

// inliner overlay. Runs inside the page under review; everything it draws lives
// in a shadow root so that neither side's styles leak into the other, and the
// page's own DOM is left untouched (text highlights use the CSS Highlight API).
(() => {
  'use strict';
  if (window.top !== window || window.__inlinerLoaded) return;
  const FILE = document.currentScript?.dataset.inlinerFile;
  if (!FILE || !document.body) return;
  window.__inlinerLoaded = true;

  const API = '/__inliner/api';
  const LINE_ATTR = 'data-inliner-line';
  const HOST_TAG = 'inliner-root';
  const SKIP = `script,style,noscript,template,${HOST_TAG}`;
  const CONTEXT = 32; // characters of text kept on each side of a quote
  const WS = /\s/;

  const T = (navigator.language || '').toLowerCase().startsWith('ja')
    ? {
        comments: 'コメント',
        comment: 'コメント',
        save: '保存',
        cancel: 'キャンセル',
        edit: '編集',
        resolve: '解決',
        reopen: '再開',
        remove: '削除',
        copy: 'AI用にコピー',
        copied: 'コピーしました。AI に貼り付けてください',
        nothingToCopy: '未解決のコメントはありません',
        pick: '要素を選択',
        pickHint: 'コメントする要素をクリック(Esc でキャンセル)',
        page: 'ページ全体',
        placeholder: 'AI へのフィードバックを書く…',
        saveHint: '⌘/Ctrl + Enter で保存',
        empty: 'まだコメントはありません。\nテキストを選択して「コメント」を押すか、\n「要素を選択」で画像やブロックを指定します。',
        resolved: '解決済み',
        orphan: '現在のページ内に該当箇所が見つかりません',
        kindText: 'テキスト',
        kindElement: '要素',
        kindPage: 'ページ全体',
        confirmRemove: 'このコメントを削除しますか?',
        reloadPending: 'ファイルが更新されました。コメントを閉じると再読み込みします',
        failed: '失敗しました: ',
        close: '閉じる',
        note: '対応メモ',
      }
    : {
        comments: 'Comments',
        comment: 'Comment',
        save: 'Save',
        cancel: 'Cancel',
        edit: 'Edit',
        resolve: 'Resolve',
        reopen: 'Reopen',
        remove: 'Delete',
        copy: 'Copy for AI',
        copied: 'Copied — paste it to your AI',
        nothingToCopy: 'No open comments',
        pick: 'Pick element',
        pickHint: 'Click the element to comment on (Esc to cancel)',
        page: 'Whole page',
        placeholder: 'Write feedback for the AI…',
        saveHint: '⌘/Ctrl + Enter to save',
        empty: 'No comments yet.\nSelect some text and press “Comment”,\nor use “Pick element” for images and blocks.',
        resolved: 'Resolved',
        orphan: 'This location is no longer found in the page',
        kindText: 'Text',
        kindElement: 'Element',
        kindPage: 'Whole page',
        confirmRemove: 'Delete this comment?',
        reloadPending: 'The file changed. The page reloads when you close the comment',
        failed: 'Failed: ',
        close: 'Close',
        note: 'Note',
      };

  // --- small helpers ----------------------------------------------------------

  function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    for (const [key, value] of Object.entries(attrs ?? {})) {
      if (value == null || value === false) continue;
      if (key === 'class') el.className = value;
      else if (key === 'text') el.textContent = value;
      else if (key.startsWith('on')) el.addEventListener(key.slice(2), value);
      else el.setAttribute(key, value === true ? '' : value);
    }
    for (const child of children.flat()) if (child != null && child !== false) el.append(child);
    return el;
  }

  async function api(method, route, body) {
    const res = await fetch(API + route, {
      method,
      headers: body ? { 'Content-Type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    const isJson = (res.headers.get('content-type') || '').includes('json');
    if (!res.ok) throw new Error((isJson && (await res.json()).error) || res.statusText);
    return isJson ? res.json() : res.text();
  }

  const clip = (s, max) => (s.length > max ? `${s.slice(0, max - 1)}…` : s);
  const squash = (s) => s.replace(/\s+/g, ' ').trim();

  /** First index in sorted `arr` whose value is >= `value`. */
  function lowerBound(arr, value) {
    let lo = 0;
    let hi = arr.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (arr[mid] < value) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }

  // --- locating text ----------------------------------------------------------
  // A text comment remembers the selected text plus a little context on each
  // side, compared with whitespace collapsed. That survives the page being
  // re-indented or re-wrapped, and edits anywhere else in the document.

  function buildIndex() {
    const nodes = []; // { node, start } with start = offset into `raw`
    const chunks = [];
    let length = 0;
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const parent = node.parentElement;
      if (!parent || parent.closest(SKIP)) continue;
      nodes.push({ node, start: length });
      chunks.push(node.data);
      length += node.data.length;
    }
    const raw = chunks.join('');

    const out = [];
    const map = []; // norm index -> raw index
    let inSpace = false;
    for (let i = 0; i < raw.length; i++) {
      const ch = raw[i];
      if (WS.test(ch)) {
        if (inSpace) continue;
        inSpace = true;
        out.push(' ');
      } else {
        inSpace = false;
        out.push(ch);
      }
      map.push(i);
    }
    return { nodes, starts: nodes.map((n) => n.start), norm: out.join(''), map };
  }

  /** Raw offsets [start, end) covered by `range`, or null if it holds no text. */
  function rangeToOffsets(index, range) {
    let start = -1;
    let end = -1;
    for (const { node, start: base } of index.nodes) {
      const len = node.data.length;
      let before;
      let after;
      try {
        before = range.comparePoint(node, 0);
        after = range.comparePoint(node, len);
      } catch {
        continue;
      }
      if (after < 0) continue; // node ends before the range starts
      if (before > 0) break; // node starts after the range ends
      const from = before < 0 ? range.startOffset : 0;
      const to = after > 0 ? range.endOffset : len;
      if (to <= from) continue;
      if (start === -1) start = base + from;
      end = base + to;
    }
    return start === -1 ? null : { start, end };
  }

  function offsetsToRange(index, start, end) {
    const { nodes, starts } = index;
    const first = nodes[lowerBound(starts, start + 1) - 1]; // last node starting at or before `start`
    const last = nodes[lowerBound(starts, end) - 1]; // last node starting before `end`
    if (!first || !last) return null;
    const range = document.createRange();
    range.setStart(first.node, start - first.start);
    range.setEnd(last.node, Math.min(end - last.start, last.node.data.length));
    return range;
  }

  function describeTextRange(index, range) {
    const offsets = rangeToOffsets(index, range);
    if (!offsets) return null;
    let from = lowerBound(index.map, offsets.start);
    let to = lowerBound(index.map, offsets.end);
    while (from < to && index.norm[from] === ' ') from++;
    while (to > from && index.norm[to - 1] === ' ') to--;
    if (to <= from) return null;
    return {
      quote: index.norm.slice(from, to),
      prefix: index.norm.slice(Math.max(0, from - CONTEXT), from),
      suffix: index.norm.slice(to, to + CONTEXT),
    };
  }

  function locateText(index, anchor) {
    const quote = anchor.quote;
    if (!quote) return null;
    const prefix = anchor.prefix || '';
    const suffix = anchor.suffix || '';
    let best = -1;
    let bestScore = -1;
    let tries = 0;
    for (let at = index.norm.indexOf(quote); at !== -1 && tries < 1000; at = index.norm.indexOf(quote, at + 1), tries++) {
      // Score an occurrence by how much of the remembered context still surrounds it.
      let score = 0;
      for (let i = 1; i <= prefix.length && index.norm[at - i] === prefix[prefix.length - i]; i++) score++;
      for (let i = 0; i < suffix.length && index.norm[at + quote.length + i] === suffix[i]; i++) score++;
      if (score > bestScore) {
        best = at;
        bestScore = score;
      }
    }
    if (best === -1) return null;
    return offsetsToRange(index, index.map[best], index.map[best + quote.length - 1] + 1);
  }

  // --- locating elements --------------------------------------------------------

  function cssPath(el) {
    const parts = [];
    for (let node = el; node && node.nodeType === 1 && node !== document.documentElement; node = node.parentElement) {
      if (node === document.body) {
        parts.unshift('body');
        break;
      }
      if (node.id) {
        const sel = `#${CSS.escape(node.id)}`;
        if (document.querySelectorAll(sel).length === 1) {
          parts.unshift(sel);
          break;
        }
      }
      const tag = node.localName;
      const twins = [...node.parentElement.children].filter((c) => c.localName === tag);
      parts.unshift(twins.length > 1 ? `${CSS.escape(tag)}:nth-of-type(${twins.indexOf(node) + 1})` : CSS.escape(tag));
    }
    return parts.join(' > ');
  }

  function openTag(el) {
    const clone = el.cloneNode(false);
    clone.removeAttribute(LINE_ATTR);
    return clip(clone.outerHTML.replace(/<\/[^>]+>$/, ''), 240);
  }

  /** Source line of the nearest element that came from the HTML file itself. */
  function lineInfo(node) {
    const el = (node.nodeType === 1 ? node : node.parentElement)?.closest(`[${LINE_ATTR}]`);
    const line = Number(el?.getAttribute(LINE_ATTR));
    return line > 0 ? { line, lineTag: el.localName } : {};
  }

  function locateElement(anchor) {
    if (!anchor.selector) return null;
    let el;
    try {
      el = document.querySelector(anchor.selector);
    } catch {
      return null;
    }
    if (!el || el.closest(HOST_TAG)) return null;
    const tag = /^<([^\s>/]+)/.exec(anchor.tag || '');
    if (tag && tag[1].toLowerCase() !== el.localName.toLowerCase()) return null;
    return el;
  }

  // --- overlay DOM ------------------------------------------------------------------

  const STYLE = `
    :host { all: initial; position: fixed; top: 0; left: 0; z-index: 2147483647; }
    * { box-sizing: border-box; }
    [hidden] { display: none !important; }
    .root {
      --bg: #fff; --fg: #1d1d1f; --muted: #6b7280; --line: #e2e3e6; --card: #f6f6f5;
      --accent: #e8930c; --danger: #c62828; --shadow: 0 8px 30px rgba(0, 0, 0, .18);
      font: 13px/1.5 system-ui, -apple-system, "Segoe UI", "Hiragino Sans", "Noto Sans JP", sans-serif;
      color: var(--fg); text-align: left; letter-spacing: normal;
    }
    @media (prefers-color-scheme: dark) {
      .root { --bg: #1f1f21; --fg: #ececec; --muted: #9aa0a6; --line: #3a3a3d; --card: #2a2a2d; --shadow: 0 8px 30px rgba(0, 0, 0, .6); }
    }
    button { font: inherit; color: inherit; }
    .layer { position: fixed; inset: 0; overflow: hidden; pointer-events: none; }
    .pin {
      position: absolute; top: 0; left: 0; min-width: 18px; height: 18px; padding: 0 4px;
      border: 1.5px solid #fff; border-radius: 9px 9px 9px 2px; background: var(--accent); color: #fff;
      font-size: 10px; font-weight: 700; line-height: 15px; text-align: center; cursor: pointer;
      pointer-events: auto; box-shadow: 0 1px 3px rgba(0, 0, 0, .35);
    }
    .pin.active { background: #d2470b; }
    .box { position: absolute; top: 0; left: 0; border: 2px solid var(--accent); border-radius: 3px; background: rgba(232, 147, 12, .08); }
    .box.active { border-color: #d2470b; background: rgba(232, 147, 12, .2); }
    .box.draft, .hover { border-color: #2f7df6; background: rgba(47, 125, 246, .12); }
    .hover { position: absolute; top: 0; left: 0; border: 2px solid #2f7df6; border-radius: 3px; }
    .capture { position: fixed; inset: 0; cursor: crosshair; }

    .fab {
      position: fixed; right: 16px; bottom: 16px; height: 40px; padding: 0 14px; display: flex; align-items: center; gap: 8px;
      border: 1px solid var(--line); border-radius: 20px; background: var(--bg); box-shadow: var(--shadow);
      font-weight: 600; cursor: pointer;
    }
    .fab svg { width: 16px; height: 16px; }
    .badge { background: var(--accent); color: #fff; border-radius: 999px; padding: 0 7px; font-size: 11px; font-weight: 700; }

    .panel {
      position: fixed; top: 0; right: 0; bottom: 0; width: 340px; max-width: 100vw; display: flex; flex-direction: column;
      background: var(--bg); border-left: 1px solid var(--line); box-shadow: var(--shadow);
    }
    .head { display: flex; align-items: center; gap: 8px; padding: 12px 14px; border-bottom: 1px solid var(--line); }
    .title { flex: 1; min-width: 0; font-weight: 700; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .tools { display: flex; flex-wrap: wrap; gap: 6px; padding: 10px 14px; border-bottom: 1px solid var(--line); }
    .btn { border: 1px solid var(--line); border-radius: 6px; background: var(--bg); padding: 5px 10px; cursor: pointer; white-space: nowrap; }
    .btn:hover { background: var(--card); }
    .btn.primary { border-color: var(--accent); background: var(--accent); color: #fff; font-weight: 600; }
    .btn.primary:hover { filter: brightness(1.06); }
    .btn.on { border-color: #2f7df6; color: #2f7df6; }
    .link { border: 0; background: none; padding: 2px 6px; border-radius: 4px; color: var(--muted); font-size: 12px; cursor: pointer; }
    .link:hover { background: var(--line); color: var(--fg); }
    .list { flex: 1; overflow: auto; overscroll-behavior: contain; display: flex; flex-direction: column; gap: 10px; padding: 12px 14px 24px; }
    .card { border: 1px solid var(--line); border-radius: 8px; background: var(--card); padding: 10px; cursor: pointer; }
    .card.active { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
    .card.done { opacity: .75; }
    .meta { display: flex; align-items: center; gap: 6px; color: var(--muted); font-size: 12px; }
    .quote {
      margin-top: 6px; padding-left: 8px; border-left: 3px solid var(--accent); color: var(--muted); font-size: 12px;
      display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere;
    }
    .body { margin-top: 6px; white-space: pre-wrap; overflow-wrap: anywhere; }
    .warn { margin-top: 6px; color: var(--danger); font-size: 12px; }
    .note { margin-top: 6px; color: var(--muted); font-size: 12px; white-space: pre-wrap; overflow-wrap: anywhere; }
    .actions { display: flex; gap: 2px; margin: 6px 0 -4px -6px; }
    .empty { color: var(--muted); text-align: center; white-space: pre-line; padding: 24px 4px; }
    .section { color: var(--muted); font-size: 12px; font-weight: 600; margin-top: 6px; }

    .selbtn { position: fixed; top: 0; left: 0; box-shadow: var(--shadow); }
    .composer {
      position: fixed; top: 0; left: 0; width: 320px; max-width: calc(100vw - 16px); padding: 10px;
      background: var(--bg); border: 1px solid var(--line); border-radius: 10px; box-shadow: var(--shadow);
    }
    .composer .quote { margin: 4px 0 8px; -webkit-line-clamp: 2; }
    textarea {
      display: block; width: 100%; min-height: 88px; resize: vertical; padding: 8px; font: inherit; color: var(--fg);
      background: var(--bg); border: 1px solid var(--line); border-radius: 6px; outline: none;
    }
    textarea:focus { border-color: var(--accent); }
    .row { display: flex; align-items: center; gap: 6px; margin-top: 8px; }
    .hint { flex: 1; color: var(--muted); font-size: 11px; }
    .toast {
      position: fixed; left: 50%; bottom: 24px; transform: translateX(-50%); max-width: calc(100vw - 32px);
      background: #222; color: #fff; padding: 8px 14px; border-radius: 8px; box-shadow: var(--shadow);
    }
  `;

  const ICON =
    '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" aria-hidden="true"><path d="M2.5 3.5h11v7.5H7l-3 2.5V11H2.5z"/></svg>';

  const host = document.createElement(HOST_TAG);
  const shadow = host.attachShadow({ mode: 'open' });

  const layer = h('div', { class: 'layer' });
  const hoverBox = h('div', { class: 'hover', hidden: true });
  const draftBox = h('div', { class: 'box draft', hidden: true });
  layer.append(hoverBox, draftBox);
  const capture = h('div', { class: 'capture', hidden: true });

  const fabCount = h('span', { class: 'badge' });
  const fab = h('button', { class: 'fab', onclick: () => setPanel(true) });
  fab.innerHTML = ICON;
  fab.append(h('span', { text: T.comments }), fabCount);

  const panelTitle = h('div', { class: 'title' });
  const pickBtn = h('button', { class: 'btn', text: T.pick, onclick: () => setPicking(!picking) });
  const list = h('div', { class: 'list' });
  const panel = h(
    'div',
    { class: 'panel', hidden: true },
    h('div', { class: 'head' }, panelTitle, h('button', { class: 'link', text: T.close, onclick: () => setPanel(false) })),
    h(
      'div',
      { class: 'tools' },
      pickBtn,
      h('button', { class: 'btn', text: T.page, onclick: () => openComposer({ kind: 'page' }) }),
      h('button', { class: 'btn primary', text: T.copy, onclick: copyFeedback }),
    ),
    list,
  );

  const selBtn = h('button', {
    class: 'btn primary selbtn',
    hidden: true,
    text: T.comment,
    onmousedown: (e) => e.preventDefault(), // keep the page selection alive
    onclick: commentOnSelection,
  });

  const composerLabel = h('div', { class: 'meta' });
  const composerQuote = h('div', { class: 'quote' });
  const textarea = h('textarea', { placeholder: T.placeholder });
  const composer = h(
    'div',
    { class: 'composer', hidden: true },
    composerLabel,
    composerQuote,
    textarea,
    h(
      'div',
      { class: 'row' },
      h('span', { class: 'hint', text: T.saveHint }),
      h('button', { class: 'btn', text: T.cancel, onclick: () => closeComposer() }),
      h('button', { class: 'btn primary', text: T.save, onclick: saveDraft }),
    ),
  );
  const toastEl = h('div', { class: 'toast', hidden: true });

  const root = h('div', { class: 'root' }, layer, capture, fab, panel, selBtn, composer, toastEl);
  shadow.append(h('style', { text: STYLE }), root);

  // Typing or clicking in the overlay must not trigger the page's own handlers
  // (slide decks advancing on click or arrow keys, and the like).
  for (const type of ['click', 'dblclick', 'mousedown', 'mouseup', 'pointerdown', 'pointerup', 'keydown', 'keyup', 'keypress', 'wheel', 'touchstart']) {
    root.addEventListener(type, (e) => e.stopPropagation());
  }

  // Highlight colours have to be declared in the document itself.
  const HIGHLIGHTS = typeof Highlight === 'function' && CSS.highlights ? CSS.highlights : null;
  if (HIGHLIGHTS) {
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(`
      ::highlight(inliner-comment) { background-color: rgba(255, 196, 0, .4); }
      ::highlight(inliner-active) { background-color: rgba(255, 138, 0, .62); }
      ::highlight(inliner-draft) { background-color: rgba(47, 125, 246, .3); }
    `);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }

  // --- state ------------------------------------------------------------------

  let comments = [];
  const anchors = new Map(); // comment id -> { range } | { el }
  const marks = new Map(); // comment id -> { pin, box? }
  let activeId = null;
  let draft = null; // { kind, anchor?, range?, el?, comment? } while the composer is open
  let picking = false;
  let reloadPending = false;

  const byId = (id) => comments.find((c) => c.id === id);
  const isShown = (c) => c.status === 'open' || c.id === activeId;

  let toastTimer;
  function toast(message, ms = 2600) {
    toastEl.textContent = message;
    toastEl.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (toastEl.hidden = true), ms);
  }
  const fail = (err) => toast(T.failed + (err?.message ?? err), 5000);

  // --- anchoring and drawing ---------------------------------------------------

  function reanchor() {
    const index = buildIndex();
    const updates = [];
    anchors.clear();
    for (const c of comments) {
      if (c.kind === 'page') continue;
      let anchor = null;
      if (c.kind === 'text') {
        const range = locateText(index, c.anchor);
        if (range) anchor = { range };
      } else {
        const el = locateElement(c.anchor);
        if (el) anchor = { el };
      }
      if (anchor) anchors.set(c.id, anchor);
      if (c.status !== 'open') continue;

      // Tell the server where the comment sits now, so that the feedback file
      // keeps pointing at the right line as the page is rewritten.
      const info = anchor ? lineInfo(anchor.el ?? anchor.range.startContainer) : {};
      const anchored = Boolean(anchor);
      const moved = anchored && info.line && (info.line !== c.anchor.line || info.lineTag !== c.anchor.lineTag);
      if (c.anchored !== anchored || moved) {
        c.anchored = anchored;
        if (info.line) Object.assign(c.anchor, info);
        updates.push({ id: c.id, anchored, ...info });
      }
    }
    if (updates.length) api('POST', '/anchors', { file: FILE, updates }).catch(() => {});
  }

  function paint() {
    if (HIGHLIGHTS) {
      const open = new Highlight();
      const active = new Highlight();
      active.priority = 1;
      for (const c of comments) {
        const range = anchors.get(c.id)?.range;
        if (!range || !isShown(c)) continue;
        (c.id === activeId ? active : open).add(range);
      }
      HIGHLIGHTS.set('inliner-comment', open);
      HIGHLIGHTS.set('inliner-active', active);
      if (draft?.range) {
        const pending = new Highlight(draft.range);
        pending.priority = 2;
        HIGHLIGHTS.set('inliner-draft', pending);
      } else {
        HIGHLIGHTS.delete('inliner-draft');
      }
    }

    // One pin per visible comment, plus an outline for element comments.
    const wanted = new Set();
    for (const c of comments) {
      const anchor = anchors.get(c.id);
      if (!anchor || !isShown(c)) continue;
      wanted.add(c.id);
      let mark = marks.get(c.id);
      if (!mark) {
        mark = { pin: h('button', { class: 'pin', text: String(c.id), onclick: () => activate(c.id) }) };
        if (anchor.el) mark.box = h('div', { class: 'box' });
        layer.append(...(mark.box ? [mark.box, mark.pin] : [mark.pin]));
        marks.set(c.id, mark);
      }
      mark.pin.classList.toggle('active', c.id === activeId);
      mark.box?.classList.toggle('active', c.id === activeId);
    }
    for (const [id, mark] of marks) {
      if (wanted.has(id)) continue;
      mark.pin.remove();
      mark.box?.remove();
      marks.delete(id);
    }
    layout();
  }

  function firstRect(anchor) {
    if (anchor?.range) {
      for (const r of anchor.range.getClientRects()) if (r.width > 0 && r.height > 0) return r;
      return null;
    }
    if (anchor?.el?.isConnected) {
      const r = anchor.el.getBoundingClientRect();
      if (r.width > 0 || r.height > 0) return r;
    }
    return null;
  }

  function place(el, rect, pad = 0) {
    el.style.transform = `translate(${rect.left - pad}px, ${rect.top - pad}px)`;
    el.style.width = `${rect.width + pad * 2}px`;
    el.style.height = `${rect.height + pad * 2}px`;
  }

  let layoutQueued = false;
  function queueLayout() {
    if (layoutQueued) return;
    layoutQueued = true;
    requestAnimationFrame(layout);
  }
  function layout() {
    layoutQueued = false;
    for (const [id, mark] of marks) {
      const rect = firstRect(anchors.get(id));
      const visible = Boolean(rect) && rect.bottom > 0 && rect.top < innerHeight && rect.right > 0 && rect.left < innerWidth;
      mark.pin.hidden = !visible;
      if (mark.box) mark.box.hidden = !visible;
      if (!visible) continue;
      mark.pin.style.transform = `translate(${Math.max(2, rect.left - 10)}px, ${Math.max(2, rect.top - 12)}px)`;
      if (mark.box) place(mark.box, rect, 3);
    }
    const draftRect = draft?.el ? firstRect({ el: draft.el }) : null;
    draftBox.hidden = !draftRect;
    if (draftRect) place(draftBox, draftRect, 3);
  }

  function renderPanel() {
    const open = comments.filter((c) => c.status === 'open');
    const done = comments.filter((c) => c.status !== 'open');
    fabCount.textContent = String(open.length);
    fabCount.hidden = open.length === 0;
    panelTitle.textContent = `${T.comments} (${open.length}) — ${FILE.split('/').pop()}`;

    const card = (c) => {
      const kind = c.kind === 'text' ? T.kindText : c.kind === 'element' ? T.kindElement : T.kindPage;
      const excerpt = c.kind === 'text' ? c.anchor.quote : c.kind === 'element' ? c.anchor.text || c.anchor.tag : '';
      const orphan = c.status === 'open' && c.kind !== 'page' && !anchors.has(c.id);
      const act = (label, fn) =>
        h('button', {
          class: 'link',
          text: label,
          onclick: (e) => {
            e.stopPropagation();
            fn();
          },
        });
      return h(
        'div',
        { class: `card${c.id === activeId ? ' active' : ''}${c.status === 'open' ? '' : ' done'}`, 'data-id': c.id, onclick: () => activate(c.id) },
        h('div', { class: 'meta' }, h('span', { class: 'badge', text: `#${c.id}` }), h('span', { text: kind }), c.status === 'open' && anchors.has(c.id) && c.anchor.line ? h('span', { text: `· L${c.anchor.line}` }) : null),
        excerpt ? h('div', { class: 'quote', text: clip(excerpt, 300) }) : null,
        h('div', { class: 'body', text: c.body }),
        orphan ? h('div', { class: 'warn', text: T.orphan }) : null,
        c.resolution?.note ? h('div', { class: 'note', text: `${T.note}: ${c.resolution.note}` }) : null,
        h(
          'div',
          { class: 'actions' },
          c.status === 'open' ? act(T.edit, () => openComposer({ kind: c.kind, comment: c })) : null,
          act(c.status === 'open' ? T.resolve : T.reopen, () => setStatus(c, c.status === 'open' ? 'resolved' : 'open')),
          act(T.remove, () => removeComment(c)),
        ),
      );
    };

    list.replaceChildren(
      ...(comments.length ? [] : [h('div', { class: 'empty', text: T.empty })]),
      ...open.map(card),
      ...(done.length ? [h('div', { class: 'section', text: `${T.resolved} (${done.length})` }), ...done.map(card)] : []),
    );
  }

  function refresh() {
    reanchor();
    renderPanel();
    paint();
  }

  // --- actions -----------------------------------------------------------------

  function setPanel(open) {
    panel.hidden = !open;
    fab.hidden = open;
    try {
      sessionStorage.setItem('inliner:panel', open ? '1' : '');
    } catch {
      // storage unavailable: the panel just starts closed next time
    }
  }

  function activate(id) {
    activeId = id;
    setPanel(true);
    renderPanel();
    paint();
    list.querySelector(`[data-id="${id}"]`)?.scrollIntoView({ block: 'nearest' });
    const anchor = anchors.get(id);
    const rect = firstRect(anchor);
    if (anchor && (!rect || rect.top < 60 || rect.bottom > innerHeight - 60)) {
      (anchor.el ?? anchor.range.startContainer.parentElement)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }
  }

  async function load() {
    try {
      comments = (await api('GET', `/comments?file=${encodeURIComponent(FILE)}`)).comments;
      if (activeId !== null && !byId(activeId)) activeId = null;
      refresh();
    } catch (err) {
      fail(err);
    }
  }

  async function setStatus(comment, status) {
    try {
      Object.assign(comment, await api('PATCH', `/comments/${comment.id}`, { status }));
      if (status !== 'open' && activeId === comment.id) activeId = null;
      refresh();
    } catch (err) {
      fail(err);
    }
  }

  async function removeComment(comment) {
    if (!confirm(T.confirmRemove)) return;
    try {
      await api('DELETE', `/comments/${comment.id}`);
      comments = comments.filter((c) => c !== comment);
      if (activeId === comment.id) activeId = null;
      refresh();
    } catch (err) {
      fail(err);
    }
  }

  async function copyFeedback() {
    if (!comments.some((c) => c.status === 'open')) return toast(T.nothingToCopy);
    try {
      const text = await api('GET', `/feedback?file=${encodeURIComponent(FILE)}`);
      try {
        await navigator.clipboard.writeText(text);
      } catch {
        // Clipboard API refused (no focus or permission): fall back to a hidden textarea.
        const scratch = h('textarea', { style: 'position:fixed;opacity:0' });
        scratch.value = text;
        root.append(scratch);
        scratch.select();
        const ok = document.execCommand('copy');
        scratch.remove();
        if (!ok) throw new Error('clipboard unavailable');
      }
      toast(T.copied);
    } catch (err) {
      fail(err);
    }
  }

  // --- composer ---------------------------------------------------------------

  function placeNear(el, rect) {
    el.style.transform = 'translate(0, 0)';
    const { width, height } = el.getBoundingClientRect();
    let left = innerWidth / 2 - width / 2;
    let top = 72;
    if (rect) {
      left = rect.left;
      top = rect.bottom + 8;
      if (top + height > innerHeight - 8) top = rect.top - height - 8;
    }
    left = Math.max(8, Math.min(left, innerWidth - width - 8));
    top = Math.max(8, Math.min(top, innerHeight - height - 8));
    el.style.transform = `translate(${Math.round(left)}px, ${Math.round(top)}px)`;
  }

  function openComposer(next) {
    draft = next;
    setPicking(false);
    selBtn.hidden = true;
    const c = next.comment;
    const excerpt = c ? (c.kind === 'text' ? c.anchor.quote : c.anchor.text || c.anchor.tag) : next.anchor?.quote || next.anchor?.text || next.anchor?.tag;
    composerLabel.textContent = c ? `#${c.id} · ${T.edit}` : next.kind === 'text' ? T.kindText : next.kind === 'element' ? T.kindElement : T.kindPage;
    composerQuote.textContent = excerpt ? clip(excerpt, 200) : '';
    composerQuote.hidden = !excerpt;
    textarea.value = c ? c.body : '';
    composer.hidden = false;

    const existing = c ? anchors.get(c.id) : null;
    const rects = next.range ? [...next.range.getClientRects()].filter((r) => r.width > 0) : [];
    placeNear(composer, rects.at(-1) ?? firstRect(next.el ? { el: next.el } : existing));
    paint();
    textarea.focus();
  }

  function closeComposer() {
    draft = null;
    composer.hidden = true;
    paint();
    if (reloadPending) location.reload();
  }

  async function saveDraft() {
    const body = textarea.value.trim();
    if (!body || !draft) return textarea.focus();
    try {
      if (draft.comment) {
        Object.assign(draft.comment, await api('PATCH', `/comments/${draft.comment.id}`, { body }));
      } else {
        const created = await api('POST', '/comments', { file: FILE, kind: draft.kind, body, anchor: draft.anchor });
        comments.push(created);
        activeId = created.id;
        document.getSelection()?.removeAllRanges();
      }
      closeComposer();
      refresh();
    } catch (err) {
      fail(err);
    }
  }

  textarea.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.isComposing) {
      e.preventDefault();
      saveDraft();
    }
  });

  // --- commenting on a text selection -------------------------------------------

  function selectedRange() {
    const sel = document.getSelection();
    if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return null;
    const range = sel.getRangeAt(0);
    const node = range.commonAncestorContainer;
    const el = node.nodeType === 1 ? node : node.parentElement;
    if (!el || !document.body.contains(el) || el.closest(HOST_TAG)) return null;
    return range.toString().trim() ? range : null;
  }

  function updateSelectionButton() {
    const range = draft || picking ? null : selectedRange();
    const rect = range && [...range.getClientRects()].filter((r) => r.width > 0 && r.height > 0).at(-1);
    selBtn.hidden = !rect;
    if (!rect) return;
    const { width, height } = selBtn.getBoundingClientRect();
    const left = Math.max(8, Math.min(rect.right - width / 2, innerWidth - width - 8));
    const top = rect.bottom + 6 + height > innerHeight ? rect.top - height - 6 : rect.bottom + 6;
    selBtn.style.transform = `translate(${Math.round(left)}px, ${Math.round(Math.max(8, top))}px)`;
  }

  function commentOnSelection() {
    const range = selectedRange()?.cloneRange();
    const described = range && describeTextRange(buildIndex(), range);
    if (!described) return (selBtn.hidden = true);
    const node = range.commonAncestorContainer;
    const el = node.nodeType === 1 ? node : node.parentElement;
    const anchor = { ...described, selector: cssPath(el), tag: openTag(el), ...lineInfo(range.startContainer) };
    openComposer({ kind: 'text', range, anchor });
  }

  let mouseDown = false;
  let selectionTimer;
  document.addEventListener('mousedown', () => {
    mouseDown = true;
    selBtn.hidden = true;
  });
  document.addEventListener('mouseup', () => {
    mouseDown = false;
    setTimeout(updateSelectionButton);
  });
  document.addEventListener('selectionchange', () => {
    if (mouseDown) return; // wait for the drag to finish
    clearTimeout(selectionTimer);
    selectionTimer = setTimeout(updateSelectionButton, 150);
  });

  // Clicking highlighted text opens its comment.
  document.addEventListener('click', (e) => {
    if (draft || picking || !document.getSelection()?.isCollapsed) return;
    for (const c of comments) {
      const range = anchors.get(c.id)?.range;
      if (!range || !isShown(c)) continue;
      for (const r of range.getClientRects()) {
        if (e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom) return activate(c.id);
      }
    }
  });

  // --- commenting on an element --------------------------------------------------

  function setPicking(on) {
    picking = on;
    capture.hidden = !on;
    hoverBox.hidden = true;
    pickBtn.classList.toggle('on', on);
    if (on) {
      selBtn.hidden = true;
      toast(T.pickHint, 4000);
    } else if (toastEl.textContent === T.pickHint) {
      toastEl.hidden = true;
    }
  }

  const elementAt = (x, y) => document.elementsFromPoint(x, y).find((el) => el !== host && el !== document.documentElement) ?? null;

  capture.addEventListener('mousemove', (e) => {
    const el = elementAt(e.clientX, e.clientY);
    hoverBox.hidden = !el;
    if (el) place(hoverBox, el.getBoundingClientRect());
  });
  capture.addEventListener('click', (e) => {
    const el = elementAt(e.clientX, e.clientY);
    if (!el) return setPicking(false);
    const anchor = { selector: cssPath(el), tag: openTag(el), text: clip(squash(el.innerText ?? el.textContent ?? ''), 200), ...lineInfo(el) };
    openComposer({ kind: 'element', el, anchor });
  });

  // --- global wiring -------------------------------------------------------------

  document.addEventListener(
    'keydown',
    (e) => {
      if (e.key !== 'Escape' || e.isComposing) return;
      if (draft) closeComposer();
      else if (picking) setPicking(false);
      else if (!selBtn.hidden) selBtn.hidden = true;
      else return;
      e.stopPropagation();
      e.preventDefault();
    },
    true,
  );

  addEventListener('scroll', queueLayout, { capture: true, passive: true });
  addEventListener('resize', queueLayout);
  new ResizeObserver(queueLayout).observe(document.body);

  // Pages that render themselves with scripts change after load: find the comments again.
  let mutationTimer = null;
  new MutationObserver(() => {
    if (mutationTimer) return;
    mutationTimer = setTimeout(() => {
      mutationTimer = null;
      refresh();
    }, 500);
  }).observe(document.body, { childList: true, subtree: true, characterData: true });

  const events = new EventSource('/__inliner/events');
  events.addEventListener('comments', load);
  events.addEventListener('reload', (e) => {
    const changed = JSON.parse(e.data).path;
    // Another page being rewritten is none of our business; a stylesheet or image might be.
    if (changed !== FILE && /\.html?$/i.test(changed)) return;
    if (draft) {
      reloadPending = true;
      toast(T.reloadPending, 6000);
    } else {
      location.reload();
    }
  });

  document.documentElement.append(host);
  try {
    if (sessionStorage.getItem('inliner:panel')) setPanel(true);
  } catch {
    // storage unavailable
  }
  renderPanel();
  load();
})();

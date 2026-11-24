// Prepares an HTML document for review without touching the file on disk:
// every start tag gets a `data-inliner-line` attribute carrying its source line
// (so a comment can be reported as `file:line`), and the overlay script is added.

export const LINE_ATTR = 'data-inliner-line';
export const CLIENT_PATH = '/__inliner/client.js';

// Tags that never render anything a reviewer could point at.
const SKIP_TAGS = new Set(['html', 'head', 'meta', 'link', 'title', 'script', 'style', 'base', 'noscript']);

// Tags whose content is raw text: anything that looks like a tag inside is not one.
const RAW_TEXT_TAGS = new Set(['script', 'style', 'textarea', 'title', 'xmp', 'iframe', 'noembed', 'noframes']);

const isNameStart = (ch) => (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z');
const isNameEnd = (ch) => ch === '>' || ch === '/' || ch === ' ' || ch === '\n' || ch === '\t' || ch === '\r' || ch === '\f';

/**
 * Add `data-inliner-line="N"` to each start tag. Never adds or removes
 * newlines, so line numbers keep matching the file on disk.
 */
export function annotateLines(src) {
  const n = src.length;
  let out = '';
  let copied = 0; // src[0..copied) is already in `out`
  let line = 1;
  let counted = 0; // newlines in src[0..counted) are included in `line`
  const lineAt = (pos) => {
    for (; counted < pos; counted++) if (src.charCodeAt(counted) === 10) line++;
    return line;
  };

  let i = 0;
  while (i < n) {
    const lt = src.indexOf('<', i);
    if (lt === -1) break;
    const next = src[lt + 1];

    if (src.startsWith('<!--', lt)) {
      const end = src.indexOf('-->', lt + 4);
      i = end === -1 ? n : end + 3;
      continue;
    }
    if (next === '!' || next === '?' || next === '/') {
      const end = src.indexOf('>', lt + 2);
      i = end === -1 ? n : end + 1;
      continue;
    }
    if (next === undefined || !isNameStart(next)) {
      i = lt + 1;
      continue;
    }

    let nameEnd = lt + 1;
    while (nameEnd < n && !isNameEnd(src[nameEnd])) nameEnd++;
    const name = src.slice(lt + 1, nameEnd).toLowerCase();

    // Find the end of the tag; `>` inside a quoted attribute value does not count.
    let tagEnd = nameEnd;
    let quote = '';
    while (tagEnd < n) {
      const ch = src[tagEnd];
      if (quote) {
        if (ch === quote) quote = '';
      } else if (ch === '"' || ch === "'") {
        quote = ch;
      } else if (ch === '>') {
        break;
      }
      tagEnd++;
    }
    if (tagEnd >= n) break; // unterminated tag: leave the rest alone

    if (!SKIP_TAGS.has(name)) {
      out += `${src.slice(copied, nameEnd)} ${LINE_ATTR}="${lineAt(lt)}"`;
      copied = nameEnd;
    }
    i = tagEnd + 1;

    if (RAW_TEXT_TAGS.has(name)) {
      const close = new RegExp(`</${name}[\\s/>]`, 'ig');
      close.lastIndex = i;
      const m = close.exec(src);
      i = m ? m.index : n;
    }
  }
  return out + src.slice(copied);
}

const escapeAttr = (s) => s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');

/** Append the overlay script, just before `</body>` when there is one. */
export function injectClient(html, file) {
  const tag = `<script src="${CLIENT_PATH}" data-inliner-file="${escapeAttr(file)}" defer></script>`;
  const idx = html.toLowerCase().lastIndexOf('</body');
  if (idx === -1) return `${html}\n${tag}\n`;
  return `${html.slice(0, idx)}${tag}\n${html.slice(idx)}`;
}

export function prepareHtml(src, file) {
  return injectClient(annotateLines(src), file);
}

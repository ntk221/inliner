// Renders review comments as Markdown addressed to the AI that wrote the HTML.

import fs from 'node:fs';
import path from 'node:path';

const clip = (s, max) => (s.length > max ? `${s.slice(0, max - 1)}…` : s);
const code = (s) => (s.includes('`') ? `\`\` ${s} \`\`` : `\`${s}\``);

/**
 * A stored line number is only as fresh as the last time the page was open in
 * the browser. Report it only if that line of the current file still opens the
 * same tag; a missing line number is better than a wrong one.
 */
function verifiedLine(anchor, lines) {
  const { line, lineTag } = anchor ?? {};
  if (!Number.isInteger(line) || line < 1 || !lineTag || !lines) return null;
  const text = lines[line - 1];
  if (text === undefined) return null;
  return new RegExp(`<${lineTag.replace(/[^\w:-]/g, '')}(?![\\w:-])`, 'i').test(text) ? line : null;
}

function describe(comment, line) {
  const { anchor = {}, kind, file } = comment;
  const out = [];
  const where = line ? `${file}:${line}` : file;
  out.push(`### #${comment.id} · ${code(where)} · ${kind}`);
  out.push('');

  if (kind === 'text') {
    out.push(`- Selected text: "${clip(anchor.quote ?? '', 400)}"`);
    const short = (anchor.quote ?? '').length < 80;
    if (short && (anchor.prefix || anchor.suffix)) {
      out.push(`- In context: "…${anchor.prefix ?? ''}[[${anchor.quote}]]${anchor.suffix ?? ''}…"`);
    }
    if (anchor.tag) out.push(`- Inside: ${code(anchor.tag)}${anchor.selector ? ` (selector ${code(anchor.selector)})` : ''}`);
  } else if (kind === 'element') {
    if (anchor.tag) out.push(`- Element: ${code(anchor.tag)}`);
    if (anchor.selector) out.push(`- Selector: ${code(anchor.selector)}`);
    if (anchor.text) out.push(`- Element text: "${clip(anchor.text, 200)}"`);
  } else {
    out.push('- Applies to the page as a whole.');
  }

  if (kind !== 'page' && comment.anchored === false) {
    out.push('- Note: this location was not found in the page as last rendered — it may already have been changed or removed.');
  } else if (kind !== 'page' && !line) {
    out.push('- Note: line number unknown; locate it by the quoted text or selector.');
  }
  if (comment.status === 'resolved') {
    out.push(`- Status: resolved${comment.resolution?.note ? ` — ${comment.resolution.note}` : ''}`);
  }

  out.push('');
  out.push(...comment.body.split('\n').map((l) => (l ? `> ${l}` : '>')));
  out.push('');
  return out;
}

/**
 * @param {object[]} comments
 * @param {{ root: string, cli?: string, includeResolved?: boolean }} opts
 */
export function renderFeedback(comments, { root, cli = 'inliner', includeResolved = false }) {
  const shown = comments.filter((c) => includeResolved || c.status === 'open');
  const open = shown.filter((c) => c.status === 'open').length;

  const out = ['# Inline review comments', ''];
  if (shown.length === 0) {
    out.push('No open comments.', '');
    return out.join('\n');
  }

  out.push(
    'A human reviewer left these comments on the rendered HTML pages, using inliner.',
    'Each one points at a place in a source file: the heading gives `file:line` (the opening',
    'tag of the nearest element), followed by what was selected and the comment itself (quoted).',
    '',
    `- Root: ${code(root)} (file paths are relative to it)`,
    `- Open comments: ${open}`,
    '',
    'Apply each requested change to the source file. After addressing a comment, mark it resolved:',
    '',
    `    ${cli} resolve <id> -m "what you changed"`,
    '',
  );

  const byFile = new Map();
  for (const c of shown) {
    if (!byFile.has(c.file)) byFile.set(c.file, []);
    byFile.get(c.file).push(c);
  }

  for (const file of [...byFile.keys()].sort()) {
    let lines = null;
    try {
      lines = fs.readFileSync(path.join(root, file), 'utf8').split('\n');
    } catch {
      // The file may have been moved or deleted; comments are still worth showing.
    }
    const items = byFile
      .get(file)
      // No line for a comment whose target is gone: where it used to be would only mislead.
      .map((c) => ({ c, line: c.kind === 'page' || c.anchored === false ? null : verifiedLine(c.anchor, lines) }))
      .sort((a, b) => (a.line ?? Infinity) - (b.line ?? Infinity) || a.c.id - b.c.id);

    out.push(`## ${file}`, '');
    if (!lines) out.push('_This file no longer exists at this path._', '');
    for (const { c, line } of items) out.push(...describe(c, line));
  }
  return out.join('\n');
}

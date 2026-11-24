// Comment storage: `<root>/.inliner/comments.json` is the source of truth and
// `<root>/.inliner/feedback.md` is regenerated from it on every write, so an AI
// can pick up the current feedback by reading one file.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { renderFeedback } from './feedback.js';

export const STORE_DIR = '.inliner';
const KINDS = new Set(['text', 'element', 'page']);
const MAX_BODY = 20_000;

/** Shell command that runs this CLI from any directory on this machine. */
export function cliCommand() {
  const bin = fileURLToPath(new URL('../bin/inliner.js', import.meta.url));
  let real = bin;
  try {
    real = fs.realpathSync(bin);
  } catch {
    // keep the unresolved path
  }
  for (const dir of (process.env.PATH ?? '').split(path.delimiter)) {
    // npx/npm put a temporary node_modules/.bin on PATH; other shells will not have it.
    if (!dir || dir.includes(`node_modules${path.sep}.bin`)) continue;
    try {
      if (fs.realpathSync(path.join(dir, 'inliner')) === real) return 'inliner';
    } catch {
      // not in this directory
    }
  }
  return `node ${/^[\w@%+=:,./-]+$/.test(bin) ? bin : JSON.stringify(bin)}`;
}

/** Normalise a root-relative file reference; returns null if it escapes the root. */
export function normalizeFile(file) {
  if (typeof file !== 'string' || !file || file.includes('\0')) return null;
  const norm = path.posix.normalize(file.replace(/\\/g, '/')).replace(/^\/+/, '');
  if (!norm || norm === '.' || norm === '..' || norm.startsWith('../')) return null;
  return norm;
}

const str = (v, max) => (typeof v === 'string' && v ? v.slice(0, max) : undefined);

function cleanAnchor(kind, a) {
  if (kind === 'page' || !a || typeof a !== 'object') return {};
  const anchor = {
    selector: str(a.selector, 1000),
    tag: str(a.tag, 300),
    line: Number.isInteger(a.line) && a.line > 0 ? a.line : undefined,
    lineTag: str(a.lineTag, 40),
  };
  if (kind === 'text') {
    anchor.quote = str(a.quote, 5000);
    anchor.prefix = str(a.prefix, 64);
    anchor.suffix = str(a.suffix, 64);
  } else {
    anchor.text = str(a.text, 300);
  }
  return JSON.parse(JSON.stringify(anchor)); // drop undefined
}

function cleanBody(body) {
  if (typeof body !== 'string' || !body.trim()) throw new StoreError('Comment body is empty.');
  return body.trim().slice(0, MAX_BODY);
}

export class StoreError extends Error {
  /** @param {number} status HTTP status the server should answer with */
  constructor(message, status = 400) {
    super(message);
    this.status = status;
  }
}

export class Store {
  constructor(root) {
    this.root = path.resolve(root);
    this.dir = path.join(this.root, STORE_DIR);
    this.file = path.join(this.dir, 'comments.json');
    this.feedbackFile = path.join(this.dir, 'feedback.md');
  }

  read() {
    let text;
    try {
      text = fs.readFileSync(this.file, 'utf8');
    } catch (err) {
      if (err.code === 'ENOENT') return { version: 1, nextId: 1, comments: [] };
      throw err;
    }
    let data;
    try {
      data = JSON.parse(text);
    } catch (err) {
      // Refuse to carry on: the next write would silently discard every comment.
      throw new StoreError(`${this.file} is not valid JSON (${err.message}). Fix or delete it.`, 500);
    }
    const comments = Array.isArray(data?.comments) ? data.comments : [];
    const maxId = comments.reduce((m, c) => Math.max(m, c.id || 0), 0);
    return { version: 1, nextId: Math.max(data?.nextId || 1, maxId + 1), comments };
  }

  write(data) {
    fs.mkdirSync(this.dir, { recursive: true });
    const ignore = path.join(this.dir, '.gitignore');
    if (!fs.existsSync(ignore)) fs.writeFileSync(ignore, '*\n');
    const tmp = `${this.file}.${process.pid}.tmp`;
    fs.writeFileSync(tmp, `${JSON.stringify(data, null, 2)}\n`);
    fs.renameSync(tmp, this.file);
    fs.writeFileSync(this.feedbackFile, this.feedback({ comments: data.comments }));
  }

  /** Markdown for the AI. `file` limits it to one page. */
  feedback({ file, includeResolved = false, comments } = {}) {
    let list = comments ?? this.read().comments;
    if (file) list = list.filter((c) => c.file === file);
    return renderFeedback(list, { root: this.root, cli: cliCommand(), includeResolved });
  }

  list({ file } = {}) {
    const { comments } = this.read();
    return file ? comments.filter((c) => c.file === file) : comments;
  }

  add({ file, kind, body, anchor }) {
    const rel = normalizeFile(file);
    if (!rel) throw new StoreError('Invalid file.');
    if (!KINDS.has(kind)) throw new StoreError('Invalid comment kind.');
    const data = this.read();
    const now = new Date().toISOString();
    const comment = {
      id: data.nextId++,
      file: rel,
      kind,
      body: cleanBody(body),
      status: 'open',
      anchor: cleanAnchor(kind, anchor),
      anchored: true,
      createdAt: now,
      updatedAt: now,
    };
    data.comments.push(comment);
    this.write(data);
    return comment;
  }

  update(id, patch) {
    const data = this.read();
    const comment = data.comments.find((c) => c.id === id);
    if (!comment) return null;
    if (patch.body !== undefined) comment.body = cleanBody(patch.body);
    if (patch.status !== undefined) {
      if (patch.status !== 'open' && patch.status !== 'resolved') throw new StoreError('Invalid status.');
      setStatus(comment, patch.status, { by: patch.by ?? 'reviewer', note: patch.note });
    }
    comment.updatedAt = new Date().toISOString();
    this.write(data);
    return comment;
  }

  remove(ids) {
    const data = this.read();
    const drop = new Set(ids);
    const removed = data.comments.filter((c) => drop.has(c.id));
    if (removed.length) {
      data.comments = data.comments.filter((c) => !drop.has(c.id));
      this.write(data);
    }
    return removed;
  }

  /** Mark comments resolved/open. Returns { changed, missing }. */
  setStatus(ids, status, { by = 'cli', note } = {}) {
    const data = this.read();
    const changed = [];
    const missing = [];
    for (const id of ids) {
      const comment = data.comments.find((c) => c.id === id);
      if (!comment) {
        missing.push(id);
        continue;
      }
      setStatus(comment, status, { by, note });
      comment.updatedAt = new Date().toISOString();
      changed.push(comment);
    }
    if (changed.length) this.write(data);
    return { changed, missing };
  }

  /** Delete resolved comments, or everything with `all`. Returns how many went. */
  clear({ all = false } = {}) {
    const data = this.read();
    const keep = all ? [] : data.comments.filter((c) => c.status !== 'resolved');
    const removed = data.comments.length - keep.length;
    if (removed) {
      data.comments = keep;
      this.write(data);
    }
    return removed;
  }

  /**
   * Record where the browser currently finds each comment (line number, or that
   * it is gone). Bookkeeping only: does not count as an edit of the comment.
   * Returns true if anything changed.
   */
  syncAnchors(file, updates) {
    if (!Array.isArray(updates) || !updates.length) return false;
    const data = this.read();
    let dirty = false;
    for (const u of updates) {
      const comment = data.comments.find((c) => c.id === u?.id && c.file === file);
      if (!comment || comment.kind === 'page') continue;
      const anchored = u.anchored !== false;
      if (comment.anchored !== anchored) {
        comment.anchored = anchored;
        dirty = true;
      }
      if (anchored && Number.isInteger(u.line) && u.line > 0) {
        const lineTag = str(u.lineTag, 40);
        if (comment.anchor.line !== u.line || comment.anchor.lineTag !== lineTag) {
          comment.anchor.line = u.line;
          comment.anchor.lineTag = lineTag;
          dirty = true;
        }
      }
    }
    if (dirty) this.write(data);
    return dirty;
  }
}

function setStatus(comment, status, { by, note }) {
  comment.status = status;
  if (status === 'resolved') {
    comment.resolvedAt = new Date().toISOString();
    comment.resolution = { by, ...(typeof note === 'string' && note.trim() ? { note: note.trim().slice(0, 2000) } : {}) };
  } else {
    delete comment.resolvedAt;
    delete comment.resolution;
  }
}

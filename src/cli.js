import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { parseArgs } from 'node:util';
import { startServer } from './server.js';
import { STORE_DIR, Store, normalizeFile } from './store.js';

const USAGE = `inliner — comment inline on AI-generated HTML, hand the feedback back to the AI

Usage
  inliner [path]              Open an HTML file or directory for review (default: .)
  inliner show [file]         Print open comments as Markdown (what the AI should read)
  inliner resolve <id...>     Mark comments resolved        (-m "note" to say what changed)
  inliner reopen <id...>      Reopen resolved comments
  inliner rm <id...>          Delete comments
  inliner clear               Delete resolved comments      (--all: delete every comment)

Options
  -p, --port <n>     Port to listen on (default 4600, next free port if taken)
      --no-open      Do not open the browser
  -C, --root <dir>   Project root: where ${STORE_DIR}/ lives and what file paths are relative to
                     (default: the current directory)
  -m, --message <s>  Note to attach when resolving
      --all          show: include resolved comments / clear: delete everything
      --json         show: print raw JSON
  -h, --help         Show this help
  -v, --version      Show version

Comments are stored in ${STORE_DIR}/comments.json; ${STORE_DIR}/feedback.md always holds the
current open comments, ready to be read by an AI.
`;

const COMMANDS = new Set(['serve', 'show', 'resolve', 'reopen', 'rm', 'clear', 'help']);

class UsageError extends Error {}

function parseIds(args) {
  if (!args.length) throw new UsageError('Give at least one comment id.');
  return args.map((a) => {
    const id = Number(a.replace(/^#/, ''));
    if (!Number.isInteger(id) || id < 1) throw new UsageError(`Not a comment id: ${a}`);
    return id;
  });
}

function openBrowser(url) {
  const [cmd, args] =
    process.platform === 'darwin'
      ? ['open', [url]]
      : process.platform === 'win32'
        ? ['cmd', ['/c', 'start', '""', url]]
        : ['xdg-open', [url]];
  try {
    const child = spawn(cmd, args, { stdio: 'ignore', detached: true });
    child.on('error', () => {}); // no browser opener available: the URL is printed anyway
    child.unref();
  } catch {
    // same
  }
}

async function serve(target, opts) {
  const abs = path.resolve(target ?? '.');
  let stat;
  try {
    stat = fs.statSync(abs);
  } catch {
    throw new UsageError(`No such file or directory: ${target}`);
  }

  // Root is where the AI works from, so that `file:line` in the feedback matches
  // the paths it sees. Fall back to the target's own directory when it lies elsewhere.
  const cwd = fs.realpathSync(process.cwd());
  const real = fs.realpathSync(abs);
  const inside = (dir) => real === dir || real.startsWith(dir + path.sep);
  let root = opts.root ? fs.realpathSync(path.resolve(opts.root)) : cwd;
  if (!inside(root)) {
    if (opts.root) throw new UsageError(`${target} is not inside --root ${opts.root}`);
    root = stat.isDirectory() ? real : path.dirname(real);
  }

  let port = 4600;
  if (opts.port !== undefined) {
    port = Number(opts.port);
    if (!Number.isInteger(port) || port < 0 || port > 65535) throw new UsageError(`Invalid port: ${opts.port}`);
  }

  const server = await startServer({ root, port, strictPort: opts.port !== undefined });
  const rel = path.relative(root, real).split(path.sep).filter(Boolean).map(encodeURIComponent).join('/');
  const url = `${server.url}/${rel}${stat.isDirectory() && rel ? '/' : ''}`;

  console.log(`
  inliner  ${url}

  root      ${root}
  feedback  ${path.join(path.relative(cwd, root) || '.', STORE_DIR, 'feedback.md')}

  Select text in the page to comment. Ctrl+C to stop.
`);
  if (opts.open) openBrowser(url);

  await new Promise((resolve) => {
    const stop = () => resolve();
    process.once('SIGINT', stop);
    process.once('SIGTERM', stop);
  });
  await server.close();
  return 0;
}

function show(store, file, opts) {
  let rel;
  if (file) {
    rel = normalizeFile(path.relative(store.root, path.resolve(file)).split(path.sep).join('/'));
    if (!rel) throw new UsageError(`${file} is outside the root ${store.root}`);
  }
  if (opts.json) {
    const comments = store.list({ file: rel }).filter((c) => opts.all || c.status === 'open');
    console.log(JSON.stringify(comments, null, 2));
  } else {
    process.stdout.write(store.feedback({ file: rel, includeResolved: opts.all }));
  }
  return 0;
}

export async function main(argv) {
  let parsed;
  try {
    parsed = parseArgs({
      args: argv,
      allowPositionals: true,
      options: {
        port: { type: 'string', short: 'p' },
        'no-open': { type: 'boolean' },
        root: { type: 'string', short: 'C' },
        message: { type: 'string', short: 'm' },
        all: { type: 'boolean' },
        json: { type: 'boolean' },
        help: { type: 'boolean', short: 'h' },
        version: { type: 'boolean', short: 'v' },
      },
    });
  } catch (err) {
    console.error(`inliner: ${err.message}\n\n${USAGE}`);
    return 2;
  }
  const { values, positionals } = parsed;

  if (values.version) {
    const pkg = JSON.parse(fs.readFileSync(new URL('../package.json', import.meta.url), 'utf8'));
    console.log(pkg.version);
    return 0;
  }
  const command = COMMANDS.has(positionals[0]) ? positionals.shift() : 'serve';
  if (values.help || command === 'help') {
    console.log(USAGE);
    return 0;
  }

  try {
    if (command === 'serve') {
      if (positionals.length > 1) throw new UsageError('Give a single file or directory.');
      return await serve(positionals[0], { port: values.port, root: values.root, open: !values['no-open'] });
    }

    const store = new Store(values.root ?? '.');
    switch (command) {
      case 'show':
        if (positionals.length > 1) throw new UsageError('Give at most one file.');
        return show(store, positionals[0], { all: values.all, json: values.json });
      case 'resolve':
      case 'reopen': {
        const status = command === 'resolve' ? 'resolved' : 'open';
        const { changed, missing } = store.setStatus(parseIds(positionals), status, { by: 'cli', note: values.message });
        for (const c of changed) console.log(`#${c.id} ${command === 'resolve' ? 'resolved' : 'reopened'}`);
        for (const id of missing) console.error(`inliner: no comment #${id}`);
        return missing.length ? 1 : 0;
      }
      case 'rm': {
        const ids = parseIds(positionals);
        const removed = new Set(store.remove(ids).map((c) => c.id));
        for (const id of ids) {
          if (removed.has(id)) console.log(`#${id} deleted`);
          else console.error(`inliner: no comment #${id}`);
        }
        return removed.size === ids.length ? 0 : 1;
      }
      case 'clear': {
        const n = store.clear({ all: values.all });
        console.log(`${n} comment${n === 1 ? '' : 's'} deleted`);
        return 0;
      }
    }
  } catch (err) {
    if (err instanceof UsageError) {
      console.error(`inliner: ${err.message}`);
      return 2;
    }
    throw err;
  }
  return 0;
}

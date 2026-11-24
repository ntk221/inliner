import assert from 'node:assert/strict';
import test from 'node:test';
import { annotateLines, injectClient, prepareHtml } from '../src/annotate.js';

test('adds the source line to each start tag without changing line count', () => {
  const src = '<!doctype html>\n<html>\n<body>\n  <h1>Title</h1>\n  <p class="a">One\n  two</p>\n</body>\n</html>\n';
  const out = annotateLines(src);
  assert.match(out, /<h1 data-inliner-line="4">Title<\/h1>/);
  assert.match(out, /<p data-inliner-line="5" class="a">One/);
  assert.equal(out.split('\n').length, src.split('\n').length);
});

test('leaves non-rendering tags alone but still marks body', () => {
  const out = annotateLines('<html><head><meta charset="utf-8"><title>T</title><link rel="x"></head><body><br/></body></html>');
  assert.equal(out, '<html><head><meta charset="utf-8"><title>T</title><link rel="x"></head><body data-inliner-line="1"><br data-inliner-line="1"/></body></html>');
});

test('ignores markup inside comments, scripts, styles and textareas', () => {
  const src = '<!-- <p>no</p> -->\n<script>const s = "<div>no</div>";</script>\n<style>a > b { }</style>\n<textarea><b>no</b></textarea>\n<p>yes</p>';
  const out = annotateLines(src);
  assert.equal(out.match(/data-inliner-line/g).length, 2); // textarea and p
  assert.match(out, /<textarea data-inliner-line="4"><b>no<\/b>/);
  assert.match(out, /<p data-inliner-line="5">yes/);
});

test('a ">" inside a quoted attribute does not end the tag', () => {
  const out = annotateLines('<p title="a > b">x</p><i>y</i>');
  assert.equal(out, '<p data-inliner-line="1" title="a > b">x</p><i data-inliner-line="1">y</i>');
});

test('stray "<" and unterminated tags are left as they are', () => {
  assert.equal(annotateLines('a < b and 1<2'), 'a < b and 1<2');
  assert.equal(annotateLines('<p>ok</p><div class="x'), '<p data-inliner-line="1">ok</p><div class="x');
});

test('injects the overlay script before </body>, or at the end', () => {
  const tag = '<script src="/__inliner/client.js" data-inliner-file="a/b.html" defer></script>';
  assert.equal(injectClient('<body><p>x</p></BODY></html>', 'a/b.html'), `<body><p>x</p>${tag}\n</BODY></html>`);
  assert.equal(injectClient('<p>x</p>', 'a/b.html'), `<p>x</p>\n${tag}\n`);
  assert.match(injectClient('', 'we"ird<.html'), /data-inliner-file="we&quot;ird&lt;.html"/);
});

test('prepareHtml does both', () => {
  const out = prepareHtml('<body>\n<p>x</p>\n</body>', 'p.html');
  assert.match(out, /<p data-inliner-line="2">x<\/p>\n<script src="\/__inliner\/client\.js"/);
});

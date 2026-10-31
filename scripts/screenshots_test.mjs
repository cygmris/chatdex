// scripts/screenshots.mjs 纯函数部分的测试。由 test/e2e/screenshots_test.go 包进 go test。
// 用虚构的用户名 / 主机名：这个文件会随整树进公开仓。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  compilePatterns, identityPatterns, forbiddenHits, pngChunkTypes, TEXT_CHUNKS,
  assertDemoHome, DEMO_MARKER, SHOTS,
} from './screenshots.mjs';

test('compilePatterns：剥掉注释与空行，其余逐行编译', () => {
  const re = compilePatterns('# 注释\n\n  foo\\d+  \n(?<![a-z])alice(?![a-z])\n');
  assert.equal(re.length, 2);
  assert.ok(re[0].test('foo12'));
  assert.ok(re[1].test('/home/alice/x'));
});

test('compilePatterns：空列表与非法正则都 throw，不能静默当成「干净」', () => {
  assert.throws(() => compilePatterns('# 只有注释\n'), /没有任何模式/);
  assert.throws(() => compilePatterns('ok\n(unclosed\n'), /第 2 行/);
});

test('identityPatterns + forbiddenHits：用户名带边界，主机名与 HOME 是字面量', () => {
  const pats = identityPatterns({ user: 'alice', host: 'alice-pc', home: '/home/alice' });
  assert.deepEqual(forbiddenHits('path /home/alice/.config', pats).map((h) => h.sample), ['/home/alice', 'alice']);
  assert.equal(forbiddenHits('realice alicex reasoning', pats).length, 0);
  assert.ok(forbiddenHits('host: alice-pc', pats).some((h) => h.sample === 'alice-pc'), '主机名字面量命中');
  assert.equal(forbiddenHits('Alice said hi', pats).length, 1, '大小写不敏感');
});

test('forbiddenHits：返回命中的模式序号，供报错指认', () => {
  const pats = compilePatterns('zzz\nsecret-\\d+');
  const hits = forbiddenHits('a secret-42 b', pats);
  assert.deepEqual(hits, [{ index: 1, sample: 'secret-42' }]);
});

function png(chunks) {
  const sig = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  const parts = [sig];
  for (const [type, data] of chunks) {
    const len = Buffer.alloc(4); len.writeUInt32BE(data.length);
    parts.push(len, Buffer.from(type, 'latin1'), data, Buffer.alloc(4)); // CRC 不校验
  }
  return Buffer.concat(parts);
}

test('pngChunkTypes：列出块类型；tEXt 一类必须被认出来', () => {
  const clean = png([['IHDR', Buffer.alloc(13)], ['IDAT', Buffer.alloc(3)], ['IEND', Buffer.alloc(0)]]);
  assert.deepEqual(pngChunkTypes(clean), ['IHDR', 'IDAT', 'IEND']);
  const tainted = png([['IHDR', Buffer.alloc(13)], ['tEXt', Buffer.from('Author\0alice')], ['IEND', Buffer.alloc(0)]]);
  const types = pngChunkTypes(tainted);
  assert.ok(types.some((t) => TEXT_CHUNKS.includes(t)));
  assert.throws(() => pngChunkTypes(Buffer.from('not a png')), /不是 PNG/);
});

test('assertDemoHome：没有标记文件就拒绝', () => {
  const dir = mkdtempSync(join(tmpdir(), 'shots-'));
  assert.throws(() => assertDemoHome(dir), /标记/);
  writeFileSync(join(dir, DEMO_MARKER), '');
  assert.equal(assertDemoHome(dir), dir);
});

test('SHOTS：14 张，文件名互不重复，每张都有视图与就绪条件', () => {
  assert.equal(SHOTS.length, 14);
  assert.equal(new Set(SHOTS.map((s) => s.file)).size, 14);
  for (const s of SHOTS) {
    assert.ok(s.file.endsWith('.png'), s.file);
    assert.ok(s.view, s.file);
    assert.ok(typeof s.ready === 'string' && s.ready.length > 0, s.file);
  }
});

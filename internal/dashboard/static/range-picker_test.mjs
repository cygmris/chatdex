// CD.rangeMath 的纯函数测试。由 internal/dashboard/rangepicker_test.go 以 TZ=Asia/Bangkok 启动；
// 直接跑：TZ=Asia/Bangkok node --test internal/dashboard/static/range-picker_test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
vm.runInThisContext(readFileSync(join(here, 'range-picker.js'), 'utf8'), { filename: 'range-picker.js' });
const M = globalThis.CD.rangeMath;

// 阳性对照：测试进程必须真的在 UTC+7，否则下面「UTC 陷阱」那条测的是空气。
test('测试环境在 UTC+7', () => {
  assert.equal(new Date('2026-10-07T19:55:00Z').getTimezoneOffset(), -420, '请用 TZ=Asia/Bangkok 跑');
});

test('localDay 用本地日历：UTC 10-07 19:55 在 +7 是 10-08', () => {
  const t = new Date('2026-10-07T19:55:00Z');
  assert.equal(t.toISOString().slice(0, 10), '2026-10-07'); // 旧写法得到的是这个（昨天）
  assert.equal(M.localDay(t), '2026-10-08');
});

test('预设按本地今天算', () => {
  const now = new Date('2026-10-07T19:55:00Z'); // 本地 2026-10-08 02:55
  assert.deepEqual(M.preset('today', now), { from: '2026-10-08', to: '2026-10-08' });
  assert.deepEqual(M.preset('yesterday', now), { from: '2026-10-07', to: '2026-10-07' });
  assert.deepEqual(M.preset('7d', now), { from: '2026-10-02', to: '2026-10-08' });
  assert.deepEqual(M.preset('30d', now), { from: '2026-09-09', to: '2026-10-08' });
  assert.deepEqual(M.preset('month', now), { from: '2026-10-01', to: '2026-10-08' });
  assert.deepEqual(M.preset('lastMonth', now), { from: '2026-09-01', to: '2026-09-30' });
  assert.deepEqual(M.preset('all', now), { from: '', to: '' });
});

test('上月跨年、本月 1 日就是今天', () => {
  assert.deepEqual(M.preset('lastMonth', new Date(2026, 0, 15)), { from: '2025-12-01', to: '2025-12-31' });
  assert.deepEqual(M.preset('month', new Date(2026, 9, 1, 3)), { from: '2026-10-01', to: '2026-10-01' });
});

test('parseDay 严格且查日历', () => {
  assert.deepEqual(M.parseDay('2026-10-08'), { y: 2026, m: 10, d: 8 });
  for (const bad of ['2026-02-30', '2026-13-01', '2026-1-8', '20261008', '', 'abc', '2026-10-08x']) {
    assert.equal(M.parseDay(bad), null, bad);
  }
});

test('shift 跨月跨年', () => {
  assert.equal(M.shift('2026-10-01', -1), '2026-09-30');
  assert.equal(M.shift('2025-12-31', 1), '2026-01-01');
  assert.equal(M.shift('bad', 1), '');
});

test('normalize 先大后小交换，空串保留', () => {
  assert.deepEqual(M.normalize('2026-10-08', '2026-10-02'), { from: '2026-10-02', to: '2026-10-08' });
  assert.deepEqual(M.normalize('2026-10-02', '2026-10-08'), { from: '2026-10-02', to: '2026-10-08' });
  assert.deepEqual(M.normalize('', '2026-10-08'), { from: '', to: '2026-10-08' });
  assert.deepEqual(M.normalize(undefined, undefined), { from: '', to: '' });
});

test('label 四种形态', () => {
  assert.equal(M.label('', ''), '全部');
  assert.equal(M.label('2026-10-08', '2026-10-08'), '2026-10-08');
  assert.equal(M.label('2026-10-07', '2026-10-08'), '2026-10-07 → 2026-10-08');
  assert.equal(M.label('2026-10-07', ''), '2026-10-07 →');
  assert.equal(M.label('', '2026-10-08'), '→ 2026-10-08');
});

test('monthGrid 周一起始、42 格、本月标记', () => {
  const g = M.monthGrid(2026, 10); // 2026-10-01 是周四
  assert.equal(g.length, 42);
  assert.equal(g[0].day, '2026-09-28');
  assert.equal(g[0].inMonth, false);
  assert.equal(g[3].day, '2026-10-01');
  assert.equal(g[3].inMonth, true);
  assert.equal(g.filter((c) => c.inMonth).length, 31);
  // 1 日恰好是周一的月份：首格就是 1 日
  assert.equal(M.monthGrid(2026, 6)[0].day, '2026-06-01');
});

// CD.comboMath 的纯函数测试。由 internal/dashboard/combo_test.go 启动；
// 直接跑：node --test internal/dashboard/static/combo_test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
vm.runInThisContext(readFileSync(join(here, 'combo.js'), 'utf8'), { filename: 'combo.js' });
const M = globalThis.CD.comboMath;
const vals = (list) => list.map((o) => o.value);

const tools = [
  { value: 'Bash', label: 'Bash' }, { value: 'Edit', label: 'Edit' }, { value: 'read_file', label: 'read_file' },
  { value: 'mcp__x__bash_run', label: 'mcp__x__bash_run' }, { value: 'Grep', label: 'Grep' },
];

test('filter：前缀命中排在子串命中前，大小写不敏感', () => {
  assert.deepEqual(vals(M.filter(tools, 'ba')), ['Bash', 'mcp__x__bash_run']);
  assert.deepEqual(vals(M.filter(tools, 'BASH')), ['Bash', 'mcp__x__bash_run']);
});

test('filter：同一档保持原顺序', () => {
  const o = [{ value: '1', label: 'foo bar' }, { value: '2', label: 'baz bar' }, { value: '3', label: 'bar none' }];
  assert.deepEqual(vals(M.filter(o, 'bar')), ['3', '1', '2']);   // 前缀 3 先；1、2 同为子串保持原序
});

test('filter：空串原样返回（新数组）；无匹配为空；中文子串可匹配', () => {
  const all = M.filter(tools, '  ');
  assert.deepEqual(vals(all), vals(tools));
  assert.notEqual(all, tools);
  assert.deepEqual(M.filter(tools, 'zzz'), []);
  const p = [{ value: '/a/代付核心', label: '代付核心' }, { value: '/b/代收', label: '代收' }];
  assert.deepEqual(vals(M.filter(p, '付')), ['/a/代付核心']);
});

test('filter 也比 value：label 是缩短路径时仍能按全路径搜', () => {
  const p = [{ value: '/home/u/workflow/alpha', label: 'alpha' }];
  assert.deepEqual(vals(M.filter(p, 'workflow')), ['/home/u/workflow/alpha']);
});

test('move：回绕两端；无选项 -1；未高亮时 ↓ 到首项、↑ 到末项', () => {
  assert.equal(M.move(2, 1, 3), 0);
  assert.equal(M.move(0, -1, 3), 2);
  assert.equal(M.move(1, 1, 3), 2);
  assert.equal(M.move(-1, 1, 3), 0);
  assert.equal(M.move(-1, -1, 3), 2);
  assert.equal(M.move(0, 1, 0), -1);
});

test('labelOf：命中取 label；未命中显示值本身（经 fallback），不是空也不是「全部」', () => {
  const p = [{ value: '/home/u/workflow/alpha', label: 'alpha' }];
  assert.equal(M.labelOf(p, '/home/u/workflow/alpha'), 'alpha');
  assert.equal(M.labelOf(p, '/home/u/workspace/x', (v) => v.split('/').pop()), 'x');
  assert.equal(M.labelOf(p, '/home/u/workspace/x'), '/home/u/workspace/x');
  assert.equal(M.labelOf(p, ''), '');
});

test('withCustom：不在列表里才加「用「q」」，且放在匹配项之后；精确匹配或空串不加', () => {
  const out = M.withCustom(tools, 'FooBar');
  assert.equal(out.length, tools.length + 1);
  const last = out[out.length - 1];
  assert.equal(last.value, 'FooBar');
  assert.equal(last.custom, true);
  assert.ok(last.label.includes('FooBar'));
  assert.equal(out[0].value, 'Bash', '字面值不能排在匹配项前面——Enter 默认要选最佳匹配');
  assert.equal(M.withCustom(tools, 'Bash'), tools);
  assert.equal(M.withCustom(tools, '   '), tools);
  // 无匹配时它是唯一一项：手敲全名回车直接生效
  assert.deepEqual(M.withCustom([], 'Zig').map((o) => o.value), ['Zig']);
});

#!/usr/bin/env python3
"""变异扫描：逐条给断言注入一个它本该拦住的改动，看它咬不咬。

一条不会失败的断言比没有断言更糟——它让人以为这件事有人看着。本项目已经撞见
过五次「断言其实是摆设」（窗口越界、匹到自己写的注释、锚点对不上、字节窗口太
短、以及反方向的误报），全部是偶然发现的。这个脚本把它变成例行检查。

三条硬约束，每一条都是栽过之后加的：

1. **注入必须先确认命中**。`find` 找不到就报「注入失败」，不能报成「断言不咬」
   ——那会让人去弱化一条本来是好的断言。
2. **必须 `-count=1`**。e2e 在运行时 `go build`，测试缓存不知道 embed 资源变了。
3. **必须还原**。`finally` 保证异常退出也还原，否则会把变异留在工作区。

用法：python3 test/mutation/run.py [用例名过滤]
不进常规 go test：跑一遍要重复构建二十多次，属开发期工具。
"""
import json
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
CASES = ROOT / "test/mutation/cases.json"


def run_case(c):
    """返回 ('咬'|'不咬'|'注入失败', 详情)。"""
    path = ROOT / c["file"]
    original = path.read_text(encoding="utf-8")

    if c["find"] not in original:
        return "注入失败", f"找不到锚点：{c['find'][:60]}"

    try:
        # all=True 时替换全部出现处。有些命题只有全改才成立——例如许可文本里
        # "MIT" 出现两次，只改第一处，断言仍能在第二处找到它而不失败，
        # 于是看起来像"断言不咬"，实际是变异太弱。
        n = -1 if c.get("all") else 1
        path.write_text(original.replace(c["find"], c["replace"], n), encoding="utf-8")
        # -count=1 是必须的：e2e 在运行时 go build，缓存不知道 embed 资源变了
        r = subprocess.run(
            ["go", "test", "-count=1", c["pkg"], "-run", f"^{c['test']}$"],
            cwd=ROOT, capture_output=True, text=True,
        )
    finally:
        path.write_text(original, encoding="utf-8")

    survived = r.returncode == 0
    want_bite = c.get("expect") != "no-bite"

    if want_bite:
        return ("不咬" if survived else "咬"), ""
    # 哨兵用例：这个变异**不该**触发该断言（用来确认断言不是「什么都咬」）
    return ("咬" if survived else "不咬"), "" if survived else "哨兵反被咬，说明断言过宽"


def main():
    filt = sys.argv[1] if len(sys.argv) > 1 else ""
    cases = json.loads(CASES.read_text(encoding="utf-8"))
    cases = [c for c in cases if filt in c["test"]]

    print(f"{'断言':<44} {'结果':<10} 变异")
    print("─" * 100)
    tally = {"咬": 0, "不咬": 0, "注入失败": 0}
    bad = []
    for c in cases:
        verdict, detail = run_case(c)
        tally[verdict] += 1
        mark = {"咬": "✓", "不咬": "✗", "注入失败": "!"}[verdict]
        label = "哨兵" if c.get("expect") == "no-bite" and verdict == "咬" else verdict
        print(f"{c['test']:<44} {mark} {label:<8} {c['why']}")
        if detail:
            print(f"{'':<44}   └ {detail}")
        if verdict != "咬":
            bad.append((c["test"], verdict, detail or c["why"]))

    print("─" * 100)
    print(f"共 {len(cases)} 条：咬 {tally['咬']} · 不咬 {tally['不咬']} · 注入失败 {tally['注入失败']}")
    if bad:
        print("\n待处理：")
        for t, v, d in bad:
            print(f"  {v}  {t} —— {d}")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())

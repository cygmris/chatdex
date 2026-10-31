import json, time, urllib.parse, urllib.request

QUERIES = [
    "rsync", "camoufox", "指纹浏览器", "部署", "的", "会话", "索引",
    "systemd", "sqlite FTS5", "Railway 部署", "端口占用", "OpenClash",
    "代理 节点", "并发 竞态", "spec-workflow", "摘要", "Ollama",
    "docker compose", "证书 续期", "回国 线路",
]
lat = []
for q in QUERIES:
    url = "http://127.0.0.1:5022/api/search?limit=20&q=" + urllib.parse.quote(q)
    t0 = time.perf_counter()
    with urllib.request.urlopen(url) as r:
        d = json.load(r)
    ms = (time.perf_counter() - t0) * 1000
    lat.append((ms, q, len(d.get("sessions") or [])))

lat_sorted = sorted(x[0] for x in lat)
p = lambda q: lat_sorted[min(int(len(lat_sorted) * q), len(lat_sorted) - 1)]
for ms, q, n in lat:
    print(f"  {ms:8.1f} ms  {n:3d} 条  {q}")
print(f"\n中位 {p(0.5):.0f} ms · p95 {p(0.95):.0f} ms · 最慢 {lat_sorted[-1]:.0f} ms （目标 p95 < 300 ms）")
print("PASS" if p(0.95) < 300 else "FAIL")

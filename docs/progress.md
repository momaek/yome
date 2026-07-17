# 开发进度

> 配套：[dev-plan.md](dev-plan.md)（任务分解，勾选状态以本文件为准的摘要）、[plan.md](plan.md)（设计与决策）。
> 记录规则：只写**已发生**的事与**为什么**。真机验收未做就是未做，不预支勾选。

## 摘要

| 里程碑 | 状态 |
|---|---|
| M0 通路验证 | ✅ 完成（2026-07-17，真机实测） |
| M1 写回引擎 + daemon 骨架 | 🟡 **代码完成，待真机验收**（T1.1–T1.9 done，T1.10 阻塞于验收） |
| M2 感知 + 单轮 AI | ⬜ 未开工（**卡在"写回语言"决策门**） |
| M3 完整 Agent + 手势 | ⬜ 未开工 |
| M4 打磨 | ⬜ 未开工 |

---

## M1 — 写回引擎与 daemon 骨架（2026-07-17）

从零起正式项目结构，m0 中被真机验证过的**算法内容**按"参照已验证逻辑重写"移植，
全部带单测。m0/ 保留至真机验收通过（见下方"未完成"）。

### 已完成

| 任务 | 落点 | 说明 |
|---|---|---|
| T1.1 项目骨架 | `cmd/` `internal/{geom,layout,inject,capture,ui,config}` `assets/` `Makefile` | 交叉编译产物：静态 ARM EABI5，2.8MB |
| T1.2 config | `internal/config` | TOML；分区 api / gesture / layout / inject / ui；**M0 全部标定常数做成带默认值的配置项**；缺文件走默认值 |
| T1.3 inject | `internal/inject` | emit/report、坐标换算、压力包络、hover 着陆；设备按 evdev **名字**枚举 |
| T1.4 layout | `internal/layout` | Hershey + SVG path 迁移；**新增排版器**：词边界换行、边距、行距下限、整页预算、溢出切分 |
| T1.5 capture | `internal/capture` | Go 原生截帧：maps 定位 → /proc/pid/mem → u16(0–30) → 灰度 PNG → transpose=3；附黑像素计数 |
| T1.6 ui | `internal/ui` | 图谱加载 + probe / tap / run 三原语；探针轮询（150ms / 2s 超时）；select_pen / undo / erase_page / set_pen 路径 |
| T1.7 CLI | `cmd/rm2-ai-daemon` | `write-text` `draw-svg` `capture` `ui-run` `erase-page` `probe` `devices`，均支持 `-debug` |
| T1.8 单测 | 各包 `*_test.go` | 全部离设备可跑；girl.svg / cat.svg golden 用例 |
| T1.9 工程基建 | `.github/workflows/ci.yml` | CI = fmt + vet + race test + 交叉编译产物；结构化日志（slog，无时间戳交给 journald）；设备 IO 错误全部带上下文包装 |

### 设计决定（偏离或补充计划的地方，都有理由）

- **新增 `internal/geom` 包**。计划的结构里 `Stroke` 没有归属；layout 产出它、inject 消费它，
  放在任一边都会造成反向依赖。独立的坐标基元包同时让裁剪 / 包围盒 / 变换有了可测的家。
- **`assets/` 做成 Go 包**。`go:embed` 够不到包目录之外，而计划的结构写明字体在 `assets/`。
  做成包既保住了这个结构，也让 UI 图谱（按固件版本一文件）用同一套嵌入方式。
- **UI 图谱补 3 个引擎注解**：`skip_if`（配合 features 的 `?` 后缀）、`verify`（后置探针）、
  以及 controls 段的字段说明。**不是新增实测数据**——是把 M0 已测的状态机接线显式化。
  `docs/` 与 `assets/` 两份图谱同步改，文件里注明了哪些是实测、哪些是注解。
- **systemd unit 退回 M3**。计划把它列在 T3.8，而 `serve` 常驻模式要 M3 才有；
  现在装一个指向不存在命令的 unit 等于装一个坏服务。`make install` 目前只做二进制 + 配置。
- **`config.example.toml` 有单测**盯着，防止示例与编译进去的默认值漂移——
  示例文件的唯一价值就是准确。

### 开发中发现并修掉的真问题

- **首行字冒出上边距**。Hershey Simplex 的小写升部（b/d/f/h/k/l）比大写还高，
  而排版以 cap top 定位，于是第一行的 `d`、`l` 会越过页边距约 2px。
  加 `OvershootPx` 参与首行定位与整页预算。单测 `TestTextStaysInsideItsArea` 守着。
- m0 的 `drawStroke` 里压力包络的分段常数是写死的，移植时拆成 `PressureEnvelope` 结构
  并进 config——"字迹风格"本来就是 plan.md 里点名要可配的东西。

### 未完成（阻塞项，不预支）

- **T1.10 拆脚手架**：要等 CLI 调试命令真机验收通过才能删 `m0/`。当前 m0/ 原样保留。
- **M1 验收全部待真机**（本机无设备，无法执行）：
  - [ ] 200 字符英文自动换行写入，无断笔，< 60s
  - [ ] `capture` PNG 竖屏方向正确、手写内容清晰
  - [ ] `erase-page` 全链路（强制切笔 → 擦除 → 恢复工具）带探针验证跑通
  - [ ] 写后验墨：黑像素增量校验通过；人为切到橡皮时**报错而非静默**

  验收命令：
  ```bash
  make deploy
  ssh root@10.11.99.1 '/home/root/rm2-ai-daemon write-text -debug \
    -text "the quick brown fox jumps over the lazy dog. ..."'   # 约 200 字符
  ssh root@10.11.99.1 '/home/root/rm2-ai-daemon capture -out /tmp/page.png'
  ssh root@10.11.99.1 '/home/root/rm2-ai-daemon erase-page -yes -debug'
  ```
  橡皮场景：手动把 xochitl 切到橡皮 → 跑 `write-text` → 期望非零退出并报
  "no ink appeared"，而不是安静地什么都没发生。

- **`capture` 的 transpose=3 映射未经真机核对**。M0 是用 ffmpeg 管线验的方向，
  Go 侧按 `dst(x,y) = src(1871-y, 1403-x)` 重写，单测只能保证它是双射且尺寸正确，
  **方向对不对要看真机截图**。若上下颠倒或镜像，改 `capture.rawIndex` 一处即可。

### 下一步

1. 真机跑 M1 验收 → 通过后删 `m0/`（T1.10），进 M2。
2. **M2 开工前需拍板写回语言**（只英文 / 含中文）——影响 system prompt 与 layout 范围。
   当前 layout 只装了 Hershey 英文；中文走 makemeahanzi + 现成的 SVG path 管线（M0 已验证可行）。

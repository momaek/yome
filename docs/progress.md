# 开发进度

> 配套：[dev-plan.md](dev-plan.md)（任务分解，勾选状态以本文件为准的摘要）、[plan.md](plan.md)（设计与决策）。
> 记录规则：只写**已发生**的事与**为什么**。真机验收未做就是未做，不预支勾选。

## 摘要

| 里程碑 | 状态 |
|---|---|
| M0 通路验证 | ✅ 完成（2026-07-17，真机实测） |
| M1 写回引擎 + daemon 骨架 | ✅ **完成（2026-07-18 真机验收通过，T1.1–T1.10 全部完成）** |
| M2 感知 + 单轮 AI | ✅ **完成（2026-07-18 真模型验收通过，openai 自建端点实测）** |
| M3 完整 Agent + 手势 | 🟡 **验收 A 通过（真模型全链路 + 真手势）；B / C / 故障注入与零散标定待做** |
| M4 打磨 | 🟡 开工：LastPen 笔型恢复代码完成（真机验证待做） |

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

- **`capture` 的 transpose=3 映射未经真机核对**（仅影响 3.11：设备现跑 3.27，其帧原生竖屏
  `rotation="none"`，无此风险）。M0 是用 ffmpeg 管线验的方向，Go 侧按
  `dst(x,y) = src(1871-y, 1403-x)` 重写，单测只能保证它是双射且尺寸正确，
  **方向对不对要看真机截图**。若上下颠倒或镜像，改 `capture.Spec.rawIndex` 一处即可。

### 下一步

1. 真机跑 M1 验收 → 通过后删 `m0/`（T1.10），进 M2。
2. **M2 开工前需拍板写回语言**（只英文 / 含中文）——影响 system prompt 与 layout 范围。
   当前 layout 只装了 Hershey 英文；中文走 makemeahanzi + 现成的 SVG path 管线（M0 已验证可行）。

---

## M1 增补 — 固件 3.27.3.0 支持（2026-07-18）

M1 主体是按 3.11.2.5 写的，而设备已升 3.27.3.0（Qt6）：截帧定位与像素格式全变，
不补上这块 M1 验收就没法在真机跑。本次把 2026-07-17/18 标定的 3.27 数据全部接进引擎。

### 已完成

| 内容 | 落点 | 说明 |
|---|---|---|
| capture 按 Spec 驱动 | `internal/capture` | `Spec{定位方法, 偏移, 像素格式, 旋转}`；3.11 = fb0 后首个匿名区 +8 / u16 灰度 / transpose3;3.27 = fb0 下一映射区 +2629640 / BGRA8888 / 原生竖屏。Spec 随 UI 图谱走,新固件=一份标定文件,不用改代码 |
| 区域暗比读取 | `capture.InkRatioRect` | 只读覆盖行的单次 pread(3.27 工具选中态是 110×110 反色块,单像素探针会误读) |
| 3.27 UI 图谱进 assets | `assets/ui/ui-map-3.27.3.0.toml` | docs 版重构:⋮ 菜单拍平为 `menu_*` 控件、补 verify/requires 引擎注解、capture 段结构化、朝向探针结构化;docs/ 与 assets/ 两份同步 |
| dark_block 区域探针 | `internal/ui` | `region = [x0,y0,x1,y1]` + `expect = "dark_block"` + 可选 `ratio` 阈值 |
| 朝向检测 | `ui.Engine.DetectOrientation` | 竖屏工具列 vs 横屏工具条暗比对比(实测 0.94/0.000 与 0.000/0.138);两者都≈0 → 报错拒绝动作,不盲打。3.11 图谱无横屏探针 → 恒竖屏 |
| 横屏坐标变换 | `geom` + `ui.Engine` | 视图→物理:`phys=(view_y, 1871−view_x)`。笔画注入、探针区域、控件点击统一走此变换;xochitl 横屏重排的控件(page_overview/tag/⋮/菜单项)用 `landscape_overrides` 实测坐标覆盖 |
| 会话接线 | `cmd/rm2-ai-daemon` | 启动顺序改为:固件检测 → 选图谱 → 图谱给出 capture spec → 开帧缓冲。无匹配图谱时 capture/UI 一并禁用(命令降级或明确报错,绝不猜)。write-text/draw-svg 自动检测朝向:横屏笔记本按 1872×1404 视图排版后旋转注入 |

### 设计决定

- **capture spec 放进 UI 图谱而不是 config**。两者本来就同源(都是按固件版本标定的
  逆向数据),分开放会出现"图谱匹配但截帧参数是另一个版本"的错配态。config 仍可用
  `ui.map_path` 整体覆盖。
- **横屏用"公式 + 例外表"而非全量重标**。3.27 实测面板类 UI 完全遵守视图旋转(≤1px),
  只有工具栏尾部四项与 ⋮ 菜单被 xochitl 重排——图谱只存例外,公式写进引擎。
- **朝向检测失败 = 拒绝 UI 动作**,但 write 路径降级为竖屏假设 + 写后验墨兜底
  (探针测不到工具栏 ≠ 一定不能写,可能只是全屏内容页)。

### 待真机验证(并入 M1 验收清单)

- [x] 3.27 截帧:capture PNG 方向/灰度正确(BGRA 路径首次真机跑,一次通过)
- [x] dark_block 探针阈值:pen_selected/eraser_selected 真机工作正常(选中块暗比远超 0.5)
- [x] erase_all 点击(★ 已实测:横屏下全链路盲打+探针验证通过,页面完全清空)
- [x] 横屏 write-text 全链路(排版→旋转→注入→验墨;5 行 200 字符 42s,方向正立无断笔)

---

## M1 真机验收 ✅（2026-07-18,设备固件 3.27.3.0,横屏笔记本实测）

全部验收项通过。验收当天设备恰好开着横屏笔记本,整条链路(含朝向检测/旋转注入)
在横屏形态下走完——比计划多验了一个形态。

| 验收项 | 结果 |
|---|---|
| 200 字符英文自动换行写入 | ✅ 5 行 288 笔画,41.9s(< 60s),无断笔,词边界换行,首字母完整 |
| capture PNG 方向/内容 | ✅ BGRA8888 路径一次通过,竖屏方向正确,手写内容清晰 |
| erase-page 全链路 | ✅ 强制切笔→橡皮面板(600ms 长按)→Erase all→恢复笔工具,全程探针验证 |
| 写后验墨 | ✅ 区域限定计数(见下);荧光笔场景实测确认笔迹异常可被察觉 |
| 朝向检测 | ✅ 实测暗比 0.94/0.000(竖)与 0.000/0.139(横),与标定值吻合 |
| 唤醒 | ✅ 注入 KEY_POWER 到 event0 可远程唤醒休眠设备 |

### 真机踩坑与修复(全部当场修掉,写进 config/图谱/代码)

1. **工具栏带是禁写区**。横屏首测:每行行首字符扫过工具栏(视图左缘 ~110px),
   笔尖点击了图标——先切到荧光笔(第 3 行起字迹变灰块),再点开文本工具(弹出全屏键盘)。
   笔事件和手指一样能按 UI 按钮。修复:`layout.margin.left` 默认 100→**150**,
   且 config 校验拒绝 < 115。
2. **触摸点击工具栏后 ~3s 内注入的笔画被 xochitl 静默丢弃**(实测两次复现:200 字符丢
   "The quick ",短文本 22 笔全丢)。m0 期免疫的原因:手测时无 UI 点击。修复双管齐下:
   `select_pen` 改条件步(`tool_pen?` + skip_if=pen_selected,**笔已选中则零点击**,常态
   零开销);真的切了工具则等 `inject.tool_settle_ms`(默认 3500ms)再动笔。
3. **全帧验墨会被 UI 重绘污染**:键盘弹出贡献 +110 万"墨"像素,第一次写入的验墨
   实为键盘,不是笔迹。修复:验墨改为**只统计笔画包围盒区域**(InkCountRect,单次
   pread),且写后轮询等待 e-ink 合成(单次立读曾测得 -276 的假阴性)。
4. **3.27 工具栏并非纯常驻**:文本工具会话结束后会收起为左上 ⊙(即 toggle,此前
   标定误记为"时钟/专注")。图谱恢复 `toolbar_toggle` 控件(skip_if/verify=toolbar_open),
   `toolbar_open` 探针改为笔工具位图标区暗比(≥0.03),feature 全部以 `toolbar_toggle?` 开局。
5. **空白判定阈值不适用于点阵模板页**:blank_page 探针的 black_max=4000 来自 3.11 空白
   模板;3.27 点阵模板页 + 展开工具栏的基线 ≈ 19339。M3 用到空白判定时需按模板重标或
   改为"相对基线增量"。

### 新增调试命令(m0 完全接替,T1.10 已删 m0/)

`tap` `swipe` `record` `preview`(SVG→本地 PNG)——加上原有 7 个命令,
m0 工具箱全部有了正式替代;`record` 留给 M3 的 new_page 录制实验。

### 遗留

- 3.11 的 transpose=3 帧方向映射依旧未真机核对(设备已升 3.27,无法回验;代码保留,
  真要跑 3.11 时看首张截图即知,改 `capture.Spec.rawIndex` 一处)。
- tool_settle_ms=3500 是"够用"值,精确死区窗口未二分标定(3.5s 实测无丢笔)。

---

## M2 — 感知与单轮 AI（2026-07-18）

决策门当日拍板：**写回语言跟随用户手写的原始语言**（中文→中文、英文→英文，模型看图判断）。
因此中文写回管线从 M4 提前并入本里程碑。

### 已完成

| 任务 | 落点 | 说明 |
|---|---|---|
| T2.1 API 抽象层 | `internal/llm` | 统一 messages/tools/vision 内部类型;anthropic(messages)与 openai(chat completions,兼容 vLLM/Ollama)两个薄适配器,裸 net/http 保持静态编译;429/5xx/网络错误退避重试,4xx 不重试;httptest 双向线格式单测 |
| T2.2 agent 循环 | `internal/agent` | 调 API → tool_use 分发 → 结果回传 → 终止;MaxTurns 为工具执行轮数上限,超限即截断(动作已执行,对话不再续);工具错误转 is_error tool_result 不中断循环 |
| T2.3 工具注册 | `internal/agent` | 工具表按会话装配;未注册工具安全失败(erase_page 门控的机制基础,有测试钉住) |
| T2.4 system prompt v1 | `internal/agent/prompt.go` | 设备/画布/剩余空间/双语字符预算动态注入;语言跟随原文;"一次规划一次写入"约束 |
| T2.5 会话礼仪 | `cmd` + `internal/ui` | CurrentTool 探针记录用户原工具、会话结束恢复;失败注入角落"×"标记(best effort) |
| T2.6 网络自检 | `cmd/ai.go` | 模型调用前 preflight(5s 超时),失败不碰页面 |
| **中文写回** | `assets/cjk` + `internal/layout` | makemeahanzi **medians**(中线笔画,天然单线含笔顺)9574 字,自制二进制格式 gzip 后 1.97MB 嵌入;Catmull-Rom 过点平滑消除中线折角;`Face` = Hershey(拉丁) + CJK(汉字)混排:汉字基线对齐(em 内基线 900/1024)、逐字可断行、英文单词整词换行、全角标点降级 ASCII;数据生成器 `tools/gen-cjk` 可复现 |
| `ai` 命令 | `cmd/ai.go` | capture → 视图旋转 → 剩余空间计算 → 模型 → write_text → 排版注入;`-image` 离线回放(不碰设备)、`-dry-run`、`-max-turns`(默认 1) |

### 真机验证（mock 协议服务替代真模型,其余全真）

Mac 上起 Anthropic 协议 mock(USB 网络 10.11.99.6),设备端 `ai` 完整跑通:
横屏检测 → 截帧转视图方向 → 剩余空间定位到英文墨迹下方(Y:615) → 记录用户工具(pen) →
write_text 中英混排 3 行 224 笔画注入(45.2s) → 区域验墨 delta 27095 ✓。
**中文楷书落墨质量佳**(平滑无折角、基线对齐、字距正常)。

### 开发中发现并处理的问题

- **点阵模板的网格点是纯黑的**(实测 1-2px、值 0),按暗度无法与笔迹区分。
  剩余空间检测改用**连续游程**判定:行内存在 ≥3px 连续墨段才算内容行,孤立点永远不构成。
- 横屏截图给模型看必须先做视图旋转(物理帧是侧躺的),离线回放同理——`-image` 需喂视图方向的图。
- 设备实测同时连着 WiFi(有真实互联网),真模型验收只差 api_key。

### 验收状态

- [x] mock 全链路:截图 → "模型" → write_text → 中文/混排写回真机页面,全程无人工干预
- [ ] **真模型验收待做**:设备 `/home/root/.config/rm2-ai/config.toml` 填真实 api_key
  (当前留的是指向开发机 mock 的配置,需替换),手写中文页 → `ai` → 润色写回
- [ ] openai provider 对自建端点的实测(适配器有单测,线上端点未验)

### 下一步

1. 用户配置真实 API key → 真模型验收(M2 收尾)。
2. M3:trigger 手势正式化、read_page/new_page/draw 工具、erase_page 门控、systemd 常驻。

---

## M3 — 完整 Agent 与手势触发（2026-07-18,代码完成;真机验收待做）

### 已完成

| 任务 | 落点 | 说明 |
|---|---|---|
| T3.1 trigger | `internal/trigger` | 纯状态机检测器(帧输入,全时序规则离机单测)+ evdev 监听器(只读不 grab,MT 协议 B,多指即取消);双击=新页,双击+第二击长按 1.5s=原地——**在阈值时刻触发而非松手**,按住本身就是确认;Pause/Resume 供会话注入期挂起(注入的触摸事件与用户手指走同一设备);新增 `trigger` CLI 标定命令 |
| tool_result 图片 | `internal/llm` `internal/agent` | read_page 的前提:anthropic 侧 tool_result content 变 part 数组;openai chat completions 的 tool 消息不支持图片,降级为紧随的 user 图片消息;`agent.Tool.Run` 返回结构化 `Result{Text, Images}`;双适配器线格式单测 |
| T3.2 read_page | `cmd/…/tools.go` + `pagenav.go` | 视图向横滑翻页 + 截图 + 翻回;全程帧指纹(md5 整帧)校验:滑后帧未变=已到首/末页(如实告知模型),翻回后指纹不符=警告模型禁止再写;截图等两次连读一致(防撕裂帧) |
| T3.3 draw | `cmd/…/tools.go` | SVG path → ParsePaths → Fit(等比归一化+边界裁剪)→ 注入;目标框可选,缺省流式排在已写内容之下;过小区域(<40px)拒绝并提示 new_page |
| T3.4 new_page | 图谱 `add_page` feature | **改走 ⋮ 菜单显式 Add page 入口**(3.27 图谱已标定,两朝向都有),不再用"末页滑动"(M0 实测过触发)也无需 record 回放实验;帧指纹变化验证加页生效,失败即报错不假装成功 |
| T3.5 erase_page + 门控 | `internal/backup` + tools.go | 仅原地会话注册(新页会话的工具表里没有它);擦除前字节级复制笔记本全部文件(`uuid*` glob,不解析 .rm)到 `~/.cache/rm2-ai/backups/`,**备份失败即中止擦除**;当前笔记本=最新 mtime 的 .content(启发式,日志记录所选 uuid) |
| T3.6 style 参数 | tools.go + `ui.SetPen` | write_text/draw 可带 {pen,size,color},走图谱面板路径;词表排除 highlighter/shader(半透明工具把正文渲染成灰块,M1 实测);同款样式去重零开销,真实切换后等 tool_settle |
| T3.7 溢出续写 | tools.go + prompt | write_text 返回未写入行 + "call new_page then write exactly those lines";toolbox 可写区域流动:写入推进,new_page/erase_page 重置为整页 |
| T3.8 serve + systemd | `cmd/…/serve.go` `deploy/rm2-ai.service` | 手势→会话循环:执行期挂起识别、每次手势重探朝向(朝向是笔记本属性)、preflight 失败/会话失败/panic 均角落 × + 继续服务;沙漏状态笔迹(单笔画=单步 undo,capture 之后注入故模型看不见);`make install` 装 unit 并启动,固件升级后重跑即可 |
| T3.9 prompt v2 | `internal/agent/prompt.go` | 感知→判断→一次规划整体执行三段式;任务判断准则(问答/整理/润色/重绘/图文);放置策略(短回复页内、长回复与重绘走新页);溢出协议;样式成本提示;原地模式段(擦除前先构思完整重写,信息只增不减);本页/新页双预算 |

### 设计决定

- **new_page 弃用 record 回放方案**。dev-plan T3.4 原计划录制真人加页操作;3.27 图谱标定时发现 ⋮ 菜单有显式 "Add page" 入口,盲打两 tap + 帧指纹验证即可,不依赖脆弱的事件序列回放。record 命令保留,若 menu_add_page 点击真机验证失败再回退。
- **手势即模式,阈值即触发**。原地手势在长按达到 1.5s 的瞬间触发(用户还按着),而非松手后——用户得到"按住生效"的确定反馈,检测器也无需区分松手时序。
- **沙漏画在 capture 之后**。状态笔迹是给用户看的,模型不该看见;会话礼仪的工具记录也提前到沙漏之前(画沙漏会强制切笔)。
- **笔样式改动不可完全恢复**。会话结束恢复的是工具类别(笔/橡皮/选择);模型若换了笔型/颜色,xochitl 会记住——用户原笔型我们读不到(.content LastPen 解析排 M4),先在日志里如实警告。
- **翻页方向语义待复核**。3.27 图谱注明"左滑/右滑均触发 loadPage,方向语义待 M3 复核";pagenav 按 M0 语义(视图向左滑=下一页)实现,真机验证清单第一项就是它。

### 真机首轮踩坑(2026-07-18,设备开着 PDF 书籍视图时实测)

1. **非笔记视图不会被拒绝,反而盲打**。设计要求"朝向探测失败=拒绝 UI 动作",但
   `s.orientation()` 失败时静默降级竖屏继续执行:`ui-run select_pen` 在 PDF 目录页上
   照样点了 (55,55) 把书的工具栏点开了。修复:serve 会话新增 `ensureNotebookView`
   守门——探不到工具栏先试两个朝向的 toggle 展开(工具栏在笔记本里也会收起,M1 发现),
   仍找不到则**拒绝会话且不注入任何笔迹**(× 标记本身也是墨,错误界面上不能画)。
   `ui-run`/`write-text` 等调试命令保留降级行为(标定本来就需要盲打能力)。
2. **PDF 阅读视图与笔记本无法用探针区分**:工具栏展开后 pen_selected/朝向探针全部为
   真(同结构 UI)。拍板:不区分——PDF 页也是可写画布(钢笔注入=批注),用户在书上
   主动触发手势就按其意图执行。守门只拦"完全没有工具栏"的界面(主屏/菜单/弹层)。
3. capture 在书籍视图正常(1404×1872,墨 127k px),新二进制 3.27 路径无回归。
4. **页码浮层污染帧指纹**(第二轮,点阵笔记本实测):翻页后底部出现瞬态 "Page X of Y"
   指示条(实测差异 936px,bbox (626,1825)-(776,1849),数秒后淡出),整帧 md5 必然失配。
   修复:指纹改为视图空间、**遮罩底部 100px** 再哈希(maskedSum)。一石二鸟:首/末页
   判定靠"滑后帧未变",而浮层的出现本身就会污染这个判定——遮罩后一并成立。
   已知代价:相邻两张同为空白的页遮罩后指纹相同,会误报"已到首/末页"(给模型的
   截图本来就一样,危害低,记录为限制)。失配路径新增取证日志(diffBBox 打差异区域)。

### 真机第二轮:mock 版验收 A 全链路通过(2026-07-18,点阵笔记本竖屏)

Mac 起 Anthropic 协议 mock(USB 10.11.99.6),剧本 read_page(-1) → new_page →
write_text,设备端 `ai` 一次跑完(58s,4 轮 3 工具):
翻到上一页截图 → **截图以 tool_result 图片回传**(线协议实测,17.6k b64)→ 翻回 →
⋮ 菜单加新页 → 新页上写中英混排 4 行(263 笔画,验墨 delta 49423 ✓)。
Script Complex 手写体(当日切换)首次真机落墨。

标定与单点验证:
- [x] 翻页方向语义:**"视图左滑=下一页"在 3.27 成立**(标记页/空白页墨量判别:右滑
      28362=回标记页,左滑 19018=回空白页,与此前捕获逐字节一致——渲染确定性成立)
- [x] `ui-run -feature add_page`:menu_add_page 竖屏点击 ✅(两次实测:空白新页插入
      当前页之后,菜单正常关闭,页码指示 +1;横屏坐标未测)
- [x] read_page 全链路:翻页→截图→翻回,遮罩指纹往返一致(修复浮层污染后)
- [x] `trigger -debug`:真人手指 13 次手势全识别(双击 new-page ×10、长按 in-place ×3,
      坐标全部落区;快速连点会成对连发,serve 中因触发即挂起监听而无害)
- [ ] add_page 横屏坐标(menu_add_page landscape override (1348,1560) 未点过)
- [ ] read_page 首页边界:offset=-1 在第一页应如实报"已是第一页"(未测)
- [ ] style 路径:`select_pen_type` 面板点击在会话流程中实测(面板坐标 9 项已单测点击,组合流程未跑)

### 真机第三轮:真手指手势 → 完整会话(2026-07-18,验收 A 机械链路通过)

真人双击 → 沙漏 → read_page → new_page → 新页写入,50s 全自动完成(模型仍为 mock
剧本;真模型版待 api_key)。过程中抓到并当场修掉三个问题:

1. **笔的防误触会静默吞掉手势**(最重要的发现)。手写笔悬停/搁在屏幕上时,触摸屏在
   驱动层被整体抑制——30s 内核级录制 0 事件,手指怎么点都无反应;笔拿开后立刻恢复
   (25s 录到 76 事件,手势秒识别)。合成注入直写内核事件流,不受影响,因此此前
   "合成通、真手不通"。**这是设备特性不是 bug**;写入用户文档:触发手势时笔要离开
   屏幕。误触发场景(写字时手掌拍到角落)也因此天然免疫——笔在屏幕上时手势必然失效。
2. **沙漏曾被工具栏死区吞掉,反馈迟到 10 秒**。原顺序:探针→(可能)点 toggle→preflight
   →截图→画沙漏——toggle 点击后 ~3s 的注入丢弃窗口正好吃掉沙漏,用户第一个可见反应
   是 10s 后的翻页。改为**手势识别瞬间在点击处裸注入沙漏**(不切笔不验证,物理坐标
   无需朝向探测):点击→反馈 ~0.5s。橡皮选中时沙漏不可见但无害;模型可能在截图里
   看到角落沙漏,prompt 加一句"那是你自己的状态标记,忽略"。
3. **同页重渲染有 1px 级抖动,严格指纹误报翻错页**。取证日志显示失配仅 8px,集中在
   笔画边缘一条 1px 竖线(抗锯齿抖动)。read_page 回程校验改为容差比对
   (diff ≤100px=同页;真翻错页差数万像素)。

沙漏改裸注入后 serve 不再依赖 markBusy 前的工具记录顺序;serve 停止/重启期间的
遗留教训:**killall 时二进制被占用会让 scp 部署失败**(text file busy),先杀进程再部署。

验收(dev-plan 4 节):
- [x] A:纯设备操作——手写一页 → 角落双击 → 真模型读页 → 回答写回(2026-07-18,见下)
- [ ] B:手绘潦草流程图 → 手势 → Agent 在新页画出工整版本(draw 实战)
- [ ] C:原地模式——双击+长按 → 备份 → 擦除本页 → 重写(含工具恢复)
- [ ] 断网/超时/API 错误均角落 × 且 daemon 不崩;systemd 重启后功能正常
- [x] (M2 遗留)真模型验收 + openai provider 对自建端点实测(同日,同一会话打包完成)

### 真机第四轮:真模型验收 A 通过 + 守门新 bug 两枚(2026-07-18)

用户配置了 OpenAI-compatible 自建端点(gpt-5.5,`/v1/chat/completions`),
真模型全链路首次跑通,M2 收尾与 M3 验收 A 同日完成:

- 手写中文提问 → 双击 → 截图 → 真模型 → 页内写回 4 行中文回答
  (410 笔画 1m48s,验墨 delta 45058 ✓);
- **M4 LastPen 恢复首次实战成功**:模型 set_pen 换 fineliner/medium 写字,
  会话结束把用户的 marker/thin/black 如数放回;
- openai 适配器(tool 消息、vision、write_text 工具调用)线上端点全部工作。

首次真人手势却踩出两个守门 bug,当场修掉(m3-gate-settle 分支):

1. **沙漏笔注入后 xochitl 忽略合成触摸数秒**——toggle 展开的两次点击全被吞,
   守门把可写的笔记页误判为"非笔记视图"拒绝会话。这是 M1"触摸点击后丢笔 3s"
   的**镜像现象**,且对合成触摸同样生效(对照实验:关沙漏即通过,开沙漏必失败)。
   修复:markBusy 后记录 `penQuietUntil`(复用 tool_settle_ms),ensureNotebookView
   的 toggle 点击前等完这个窗口;探针是纯读不受影响,不等。真机复验:同场景
   (收起工具栏+沙漏开启)守门通过,会话正常执行。
2. **守门拒绝后沙漏残留屏上**,看起来像永远 loading(用户如此报告)。修复:
   拒绝路径用合成橡皮(M4 的 `inject.Erase`,BTN_TOOL_RUBBER 免切工具)蛇形
   覆擦沙漏包围盒;擦前等 tool_settle(toggle 触摸点击刚开过丢笔窗口)。
   非画布界面上画与擦均为无操作,无条件调用安全。**可见擦除未实测**——
   修复 1 之后"画布上守门拒绝"几乎不可复现,此路径为保险带,机制(rubber
   注入)在 M4 橡皮工作中已真机验证过。

顺带净化:验收过程在测试页右下角留下了两枚残叠沙漏(修复前的两次拒绝),
可手动 undo/橡皮清理。

---

## M4 — 打磨（2026-07-18 开工）

### LastPen 笔型恢复（代码完成，真机验证待做）

M3 的遗留承诺兑现：会话若替模型换过笔样式，结束时把用户原笔放回去，不再只是
日志里道歉。原以为要解析 `.content`，实测发现 xochitl 把笔状态存在
`~/.config/remarkable/xochitl.conf` 的 `LastWritingTool` 键里——QSettings 转义
包着一段 QDataStream 序列化的 QVariantMap（3.27.3.0 实测 12 个键：LastPen /
LastPenColor / LastPenSize 及橡皮、副笔各字段）。

- 新包 `internal/penstate`：INI 反转义 + QVariant 解码（越界即错，不 panic），
  测试用例以真机 conf 原行为基准（testdata，532 字节全量消费验证）。
  **实测格式没有 is-null 字节**（与常见 QVariant 流描述不同），按字节验证为准。
- 映射:笔型 id → 图谱 `pen_*` 控件（经典 v1/v2 双 id 全表);颜色 0/1/2/6/7 →
  五个已标定 `color_*`;尺寸 1/2/3 → `size_*`。**shader 等未验证 id 故意不猜**——
  恢复错笔比不恢复更糟,未知 id 只留诚实日志。副笔的 highlighter(18)+yellow(3)
  组合与已知词表自洽,作为映射表的旁证。
- 会话礼仪接线（ai.go）：loop 前记录（模型自己的 set_pen 会让 xochitl 覆写该键，
  必须先读）；结束时先恢复样式再恢复工具类别（面板路径会把笔选中，非笔工具要
  最后切回）；**错误路径同样恢复**，且角落 × 先画——反馈不排在恢复点击之后
  （沙漏教训沿用）。恢复失败均为警告日志，会话结果不受影响。
- 每种笔各自记忆粗细（M1 标定发现），所以笔型 id 映射不出来时尺寸/颜色也一并
  跳过——在模型选的笔上点尺寸，等于污染那支笔的记忆。

真机待验证（★）：
- [x] xochitl 何时落盘 conf（2026-07-18 真机实测）：**换笔不即时落盘**——真人换到
      marker 后 6 分钟 mtime 纹丝不动，直到关闭笔记本前后才写入（09:59:23）。
      落盘值 pen=16(marker)/size=1.0 与所换的笔完全对账,顺带验证了 v2 id 区块
      （15=ballpoint、16=marker 连号）与"每笔各自记忆粗细"。
      **已知限制**：本次打开笔记本之后才换的笔不在 conf 里,恢复的会是上一次
      落盘的笔——仍严格优于留着模型的笔;recordPenState 日志带 conf_age 供取证。
- [x] 会话内换笔 → 结束后恢复（2026-07-18 两次真机会话：daemon 强制 fineliner/medium
      写字，结束后 marker/thin/black 如数放回。原工具为橡皮的 order 路径未单独测）
- [ ] shader / 新增笔型的真实 id 标定（选中后读 conf 即得,补进映射表）

### 底部进度状态行（2026-07-18,真机验收通过）

用户在首次真手势会话时的原话是"一直 loading 有点懵"——沙漏只说"开始了",
说不了"现在在干嘛"。状态行补上这块:页面底边一行小字(cap 20px),随会话阶段
擦旧写新:思考中... → 写入中.../画图中.../读页中.../擦除中...,会话结束整行擦掉。

- **落点三不占**(有单测钉住几何不变量):在 pagenav 底部 100px 指纹遮罩区内
  (不污染翻页判定);在排版下边距之外(不碰验墨);x∈[150, viewW−150]
  (不进工具栏死区,不挤手势角)。两朝向都成立。
- **裸注入,best-effort**:不强制切笔(省 3.5s/次)、不验墨。用户停在橡皮上时
  状态行不可见——降级的是反馈,不是正确性。
- **合成橡皮擦除**(BTN_TOOL_RUBBER 免切工具),更新一次 = 蛇形覆擦 + 重写。
- **双向输入抑制窗口全面接线**(本功能的真正基建):xochitl 笔注入后吞合成触摸、
  工具栏点击后吞笔注入,两个 ~3.5s 窗口现在由 `penQuiet/touchSettle` 与
  `touchQuietUntil/penSettle` 对称跟踪。所有 UI 点击走 Engine.BeforeTouch 钩子、
  翻页滑动前显式等待、所有裸笔/橡皮注入前等反向窗口。守门与 clearBusyMark 的
  内联/无条件等待全部换成精确等待。
- 模型侧:prompt 告知底部小字是 daemon 自己的状态显示,忽略勿提;会话首屏
  截图在状态行落墨**之前**拍,模型读的原始页永远干净。
- 配置 `[gesture] status_line`(默认开)。

真机验收(竖屏点阵页,gpt-5.5 真模型,5m52s 会话):"写入中..."在模型写正文
期间清晰可读(小字 CJK 无糊笔),结束后底边擦净无残留。踩到并当场修掉:
**new_page 的标签会被留在旧页上**(标签写在翻页前,擦除动作却落在翻页后的新页)
——改为离开旧页前先清行,new_page 不再有独立标签(修复后的组合流程待下次
会话顺带复验)。

已知限制:状态行的每次擦写都是 undo 历史里的真实步骤;快速连续的工具调用会
让底边闪烁(e-ink 特性);"思考中"首写若紧跟守门 toggle 点击,会等 3.5s 丢笔
窗口后才可见。

# rm2-ai-daemon 开发计划

> 配套文档：[plan.md](plan.md)（总体设计与决策记录）、[ui-map-3.11.2.5.toml](ui-map-3.11.2.5.toml)（UI 图谱）。
> 本文档是执行层面的任务分解：做什么、按什么顺序、验收标准是什么。
> 状态标记：`[ ]` 待做 / `[x]` 完成 / `[~]` 进行中

## 0. 现状基线（2026-07-17）

M0 全部完成，无遗留技术未知数。
**M1 代码已完成（T1.1–T1.9，含 2026-07-18 增补的固件 3.27.3.0 截帧/图谱/横屏支持），待真机验收；进度与踩坑记录见 [progress.md](progress.md)。**

**m0/ 的定位：施工脚手架,不是代码基础。** 正式开发按完整项目标准从零起项目结构；m0 中被真机验证过的**算法内容**（解析器/包络/事件编码/换算公式）移植进带单测的正式包——移植是"参照已验证逻辑重写",不是复制文件。m0 工具在开发期保留作调试对照,daemon 自带调试命令完全接替后于 M1 收尾删除（git 历史可溯）。知识与常数（图谱/标定/踩坑）已沉淀于 docs/ 与 config 默认值,与 m0 代码无关,永久保留。

| 已验证内容 | 当前位置 | 去向（重写目标） |
|---|---|---|
| evdev 注入（笔/触摸/压力包络/hover 着陆） | m0/main.go | → `internal/inject` |
| Hershey 英文渲染 + JHF 解析 | m0/hershey.go | → `internal/layout` |
| SVG path 解析 + 贝塞尔拍平（中文/图形共用） | m0/svgpath.go | → `internal/layout` |
| 双击手势检测器 | m0/main.go triggerCmd | → `internal/trigger` |
| 截帧方法（maps 定位 + dd + u16 灰度 + transpose） | shell 管线 | → `internal/capture`（Go 原生重写） |
| 本地 SVG 预览渲染 | m0/preview.go | → `tools/preview`（开发工具） |
| UI 图谱 + 时序模型 | docs/ui-map-3.11.2.5.toml | → config 默认值 + `internal/ui` |
| 全部标定常数（坐标系/格式/时序） | docs/plan.md M0 节 | → `internal/config` 默认值 |

## 1. 总路线

```
M1 写回引擎(daemon 骨架)  →  M2 感知+单轮 AI  →  M3 完整 Agent+手势  →  M4 打磨
   2-3 天                     2-3 天              4-6 天               持续
```

关键依赖关系：
- M2 开工前需拍板 **写回语言**（只英文 / 含中文）——影响 system prompt 与 layout 范围
- M2 的 API 层按多供应商设计（anthropic / openai 兼容 self-hosted），模型必须支持 vision
- M3 的 new_page 需先用 record 模式录制真人加页操作（一次性实验，30 分钟）

## 2. M1 — 写回引擎与 daemon 骨架（2–3 天）

目标：把 m0 验证代码重构为正式模块，daemon 以 CLI 调试模式可用。

### 任务

- [x] **T1.1 项目骨架**：`cmd/rm2-ai-daemon/main.go` + `internal/{inject,layout,capture,ui,config}` + Makefile（build/deploy/install 三目标，交叉编译 GOARCH=arm GOARM=7）
- [x] **T1.2 config**：TOML 加载（`/home/root/.config/rm2-ai/config.toml`）；分区：api、gesture、layout（字号/行距/边距/点间隔/压力包络）、ui 图谱（按固件版本分段）；所有 M0 标定常数做成带默认值的配置项
- [x] **T1.3 inject**：从 m0 迁移 emit/report/坐标换算/drawStroke（压力包络、hover 着陆保留）；接口 `Inject(strokes []Stroke, opts)`；设备按名字枚举（"Wacom I2C Digitizer" / "pt_mt"），不写死 eventX
- [x] **T1.4 layout**：Hershey 渲染 + SVG path 管线迁移；新增**排版器**：自动换行（词边界）、边距、行距（≥1.8 倍大写高度）、整页预算计算与溢出切分；输出统一 `[]Stroke`
- [x] **T1.5 capture**：Go 原生截帧——读 /proc/<pid>/maps 定位 fb0 后匿名区 +8、/proc/<pid>/mem 读帧、u16(0-30) → 灰度 PNG、transpose=3 竖屏；附黑像素计数（探针/验墨用）
- [x] **T1.6 ui**：图谱加载 + `probe(state)` / `tap(control)` / `run(feature)` 三原语；探针轮询等待（150ms 间隔 / 2s 超时降级）；实现 select_pen / undo / erase_page / set_pen(type,size,color) 路径
- [x] **T1.7 CLI 调试命令**：`rm2-ai-daemon write-text|draw-svg|capture|ui-run|erase-page --debug`（前台模式，M0 工具的正式替代品）
- [x] **T1.8 纯逻辑单元测试**：JHF/SVG path 解析、贝塞尔拍平、排版换行、坐标换算、压力包络——全部离设备可测；建 golden 用例（如 girl.svg → 期望折线集）
- [x] **T1.9 工程基建**：CI（lint + test + 交叉编译产物）、结构化日志（journald 友好）、错误处理规范（所有设备 IO 带上下文包装）
- [ ] **T1.10 拆脚手架**：CLI 调试命令验收通过后删除 m0/（git 历史保留）；expect 脚本类辅助并入 Makefile 目标 —— *阻塞于下方真机验收*

### 验收（待真机；开发机无设备）

- [ ] 命令行输入 200 字符英文文本 → 自动换行排版写入设备当前页，无断笔、总耗时 < 60s
- [ ] `capture` 输出的 PNG 竖屏方向正确、手写内容清晰
- [ ] `erase-page` 全链路（强制切笔→擦除→恢复工具）带探针验证跑通
- [ ] 写后验墨：写入后黑像素增量校验通过；人为切到橡皮时能报错而非静默

## 3. M2 — 感知与单轮 AI 调用（2–3 天）

目标：截图 → 模型看图 → 润色文本写回,先以 maxTurns=1、仅 write_text 的退化形态跑通全链。

### 任务

- [ ] **T2.1 API 抽象层**：统一内部接口（messages + tool defs + tool results + vision 图片）；`provider=anthropic` 与 `provider=openai`（兼容 vLLM/Ollama base_url）两个薄适配器；单轮超时 60s、重试 1 次
- [ ] **T2.2 agent 循环骨架**：调 API → tool_use 分发 → 结果回传 → 终止条件（maxTurns=8 / 模型结束）；本阶段先锁 maxTurns=1
- [ ] **T2.3 工具注册机制**：工具表按会话模式装配（为 M3 的 erase_page 门控与 style 参数预留）
- [ ] **T2.4 system prompt v1**：设备/画布说明、行宽字符预算、"一次规划批量执行"约束、输出语言约定（按写回语言决策）
- [ ] **T2.5 会话前置/收尾**：强制 select_pen + 记录并恢复用户原工具；失败注入"×"笔迹提示
- [ ] **T2.6 设备网络自检**：启动时 HTTPS 连通性检查（含系统 CA 验证），失败进日志与状态提示

### 验收

- [ ] 命令行触发：手写一段英文 → 截图上传 → 模型识别并润色 → 结果以笔迹写回新位置，全程无人工干预
- [ ] anthropic 与 openai 两种 provider 配置均跑通（openai 侧可用任一自建端点验证）

### 决策门

- [ ] **写回语言拍板**（影响 T2.4 与 M4 中文优先级）

## 4. M3 — 完整 Agent 与手势触发（4–6 天）

目标：脱离命令行,设备上手势触发完整体验;systemd 常驻。

### 任务

- [ ] **T3.1 trigger 正式化**：迁移双击检测器；手势词汇表（右下角双击=新页模式、双击+长按1.5s=原地模式）；Agent 执行期挂起识别；触发后注入角落状态笔迹（沙漏/×）
- [ ] **T3.2 read_page 工具**：翻页（swipe 注入）+ 截图 + 翻回；页码指纹校验
- [ ] **T3.3 draw 工具**：SVG path 参数 → layout → 注入；边界裁剪与尺寸归一化
- [ ] **T3.4 new_page 工具**：record 真人加页操作确定最小事件序列（一次性实验）；慢速执行 + 逐页校验（页码指纹）
- [ ] **T3.5 erase_page 工具 + 门控**：仅原地手势会话注册；擦除前 .rm 文件字节级备份；写后验墨
- [ ] **T3.6 write_text/draw 的 style 参数**：daemon 内部经 ui 图谱自动切换笔型/粗细/颜色
- [ ] **T3.7 溢出续写协议**：write_text 返回溢出行 → prompt 引导模型 new_page 续写
- [ ] **T3.8 部署**：systemd unit（After=home.mount xochitl.service, Restart=on-failure）、journald 日志、`make install`、固件升级重装说明
- [ ] **T3.9 system prompt v2**：任务判断准则（润色/待办/重绘图形/图文混排）、多工具编排示例、每页预算表

### 验收

- [ ] A：纯设备操作——手写一页 → 角落双击 → 润色文字写入新页
- [ ] B：手绘潦草流程图 → 手势 → Agent 在新页画出工整版本（draw 工具实战）
- [ ] C：原地模式——双击+长按 → 擦除本页 → 重写（含备份与工具恢复）
- [ ] 断网/超时/API 错误均有角落"×"提示且 daemon 不崩溃；systemd 重启后功能正常

## 5. M4 — 打磨（持续,按需排期）

- [ ] **中文写回**：makemeahanzi 数据接入（复用 SVG 管线）；按 `kvg:type` 笔画类型选压力包络（横竖收笔顿、撇捺提锋）
- [ ] **多模板**：手势词汇表扩展 = 新手势 → 新 system prompt + 工具集（机制 M2 已备好）
- [ ] **二段式确认**（config 可选）：armed 标记 + 超时 undo 清除
- [ ] **注入速度调优**：3ms 点间隔实测、插值密度自适应
- [ ] **UI 图谱自举**：新固件标定流程（截帧 → 视觉模型标注 → 生成新版本段）
- [ ] **文档**：README、安装指南、config 全参考、故障排查（含本项目踩坑录）

## 6. 测试与验证策略

- **真机是唯一权威**：每个里程碑的验收在设备上做；capture + 黑像素指纹作为自动化断言（M0 已验证该方法可靠）
- **golden 帧**：关键场景（空白页、工具栏展开、面板打开）存基准帧,探针阈值回归用
- **踩坑清单转测试**：橡皮工具状态、触摸 Y 翻转、200ms 长按、快速连击竞争——每个坑至少一条集成测试
- **模型层面**：system prompt 迭代用固定测试页集（截图存 repo）离线回放,不依赖真机

## 7. 风险与开放项（承接 plan.md 第 7 节）

| 项 | 状态 | 处理点 |
|---|---|---|
| 写回语言 | 待拍板 | M2 决策门 |
| new_page 最小事件序列 | 待一次性实验 | T3.4 |
| 文本/选择工具面板未测绘 | 低优先 | 需要时补图谱 |
| openai 适配器的 vision+tool use 细节差异 | 设计已留位 | T2.1 实测 |

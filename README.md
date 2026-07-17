# yome — rm2-ai-daemon

在 reMarkable 2 上原生运行的 AI 助手：在 xochitl（官方笔记界面）里手写完一页，
用一个手势触发，AI 看图、判断任务类型（润色文字 / 整理待办 / 重画潦草的示意图），
再把结果**以笔迹的形式写回**设备。全程在设备上完成，不需要电脑或手机中转。

```
手写/涂画一页 → 角落手势触发 → Agent（看图 → 决策 → 编排动作）→ 结果以笔迹写入新页
```

设计要点：**Agent 的工具就是它的"手"**——模型的 tool call 直接映射为设备上的注入动作
（写字、画图、翻页），感知—决策—行动在一个循环里闭合。

完整设计见 [docs/plan.md](docs/plan.md)；任务分解见 [docs/dev-plan.md](docs/dev-plan.md)。

## 现状

**M1 代码完成，待真机验收。** 写回引擎（排版 / 注入 / 截帧 / UI 操作）已就绪，
以 CLI 调试命令的形态可用；Agent 与手势触发是 M2 / M3。
详见 [docs/progress.md](docs/progress.md)。

## 工作原理

只通过**内核标准接口**与 xochitl 相处，不打补丁、不劫持函数、不依赖 toltec：

| 模块 | 做法 |
|---|---|
| `inject` | 向 Wacom evdev 设备写合成笔事件——xochitl 分不清合成事件与真笔迹 |
| `capture` | 从 xochitl 进程内存里定位 framebuffer 读帧（全项目唯一的内部耦合点） |
| `layout` | 文本 → Hershey 单线字体笔画；SVG path → 贝塞尔拍平 → 折线。两条路径共用注入管线 |
| `ui` | 按固件版本的 UI 图谱 + 像素探针驱动 xochitl 自己的界面（换笔 / 撤销 / 清空页面） |

两个关键实测结论（M0）：

- **手写感 = 压力包络**。恒压注入是均匀细线，一眼假；起笔加压 → 中段满压 → 末段渐提之后，
  xochitl 的压感笔刷渲染出粗细变化，接近真人笔迹。
- **注入笔迹按 xochitl 当前选中的工具渲染**。用户上次停在橡皮上时，注入即擦除——
  空白页上零效果、零报错。因此每次写入前**强制切笔 + 探针确认**，写后**验墨**兜底。

## 快速开始

设备通过 USB 连接（`10.11.99.1`），SSH 密码在设备的 Settings → Help → Copyrights 页。

```bash
git clone git@github.com:momaek/yome.git
cd yome

make check      # gofmt + go vet + 单元测试（全部离设备可跑）
make build      # 交叉编译 → build/rm2-ai-daemon（静态 ARM 二进制，约 2.8MB）
make deploy     # scp 到设备
make install    # deploy + 初始化 /home/root/.config/rm2-ai/config.toml
```

`make deploy DEVICE=root@192.168.1.50` 可走 wifi。

## CLI 调试命令

M1 的形态：前台命令，M0 验证工具的正式替代品。

```bash
# 排版一段文字写到当前页（自动换行；写后验墨）
rm2-ai-daemon write-text -text "hello from the daemon" -debug

# 不碰设备，只看排版结果与整页字符预算——本机就能跑
rm2-ai-daemon write-text -file notes.txt -dry-run

# 截图（竖屏方向，附黑像素计数）
rm2-ai-daemon capture -out /tmp/page.png

# 画 SVG（path 拍平为折线后注入）
rm2-ai-daemon draw-svg -file cat.svg -width 600

# UI 图谱操作
rm2-ai-daemon ui-run -list
rm2-ai-daemon ui-run -feature select_pen
rm2-ai-daemon ui-run -feature select_pen_type -params pen_type=pen_marker
rm2-ai-daemon probe -name toolbar_open
rm2-ai-daemon erase-page -yes        # 破坏性：清空当前页

# 输入设备枚举（按名字，不写死 eventX）
rm2-ai-daemon devices
```

所有命令支持 `-debug`（详细日志）与 `-config <path>`。

## 配置

`/home/root/.config/rm2-ai/config.toml`，全部字段见
[deploy/config.example.toml](deploy/config.example.toml)。

配置里的默认值就是 M0 的实测标定值（坐标换算、点间隔、压力包络、UI 时序），
调参是改配置而不是重新编译。文件缺失时走默认值，CLI 调试命令零配置即可用。

> **关于密钥**：rM2 上一切皆 root，文件权限不构成保护。真实风险是设备丢失与
> USB web 接口暴露，不是本地其他用户。

## 项目结构

```
cmd/rm2-ai-daemon/     CLI 入口与调试命令
internal/
  geom/                坐标基元（点 / 笔画 / 矩形 / 裁剪）——屏幕空间
  layout/              Hershey 字体、SVG path 解析、排版器
  inject/              evdev 笔/触摸事件合成、坐标换算、压力包络
  capture/             从 xochitl 进程内存截帧
  ui/                  UI 图谱 + 像素探针 + 点击状态机
  config/              TOML 配置（M0 标定常数的默认值）
assets/                go:embed 数据：Hershey 字体、各固件版本 UI 图谱
docs/                  设计文档、开发计划、UI 图谱存档、进度记录
m0/                    M0 验证脚手架（CLI 真机验收通过后删除，见 T1.10）
```

## 开发

```bash
make test           # 单元测试
make test-update    # 刷新 golden 文件
make check          # CI 跑的全部检查
```

单元测试全部离设备可跑：坐标换算、压力包络、JHF / SVG 解析、贝塞尔拍平、排版换行、
UI 状态机（用假屏幕与假点击器）都是纯逻辑。evdev 与 `/proc` 访问在 `linux` build tag
后面，因此本机能编译、能测，只有真正碰设备的入口在非 Linux 上返回错误。

**真机是唯一权威**：每个里程碑的验收在设备上做，`capture` + 黑像素指纹作为自动化断言。

## License

TODO

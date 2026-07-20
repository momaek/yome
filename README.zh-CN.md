# yome — rm2-ai-daemon

[English](README.md) | **简体中文**

在 reMarkable 2 上原生运行的 AI 助手：在 xochitl（官方笔记界面）里手写完一页，
用一个手势触发，AI 看图、判断任务类型（润色文字 / 整理待办 / 重画潦草的示意图 / 回答问题），
再把结果**以笔迹的形式写回**设备。全程在设备上完成，不需要电脑或手机中转。

<p align="center">
  <img src="docs/images/demo-input.png" width="46%" alt="设备上手写的问题" />
  <img src="docs/images/demo-answer.png" width="46%" alt="AI 以笔迹写回新页的答复" />
</p>
<p align="center"><i>左:手写的问题。右:AI 写回新页的答复——均为真机屏幕截帧。</i></p>

```
手写/涂画一页 → 角落手势触发 → Agent(看图 → 决策 → 编排动作)→ 结果以笔迹写入新页
```

设计要点:**Agent 的工具就是它的"手"**——模型的 tool call 直接映射为设备上的注入动作
(写字、画图、翻页),感知—决策—行动在一个循环里闭合。

完整设计见 [docs/plan.md](docs/plan.md);任务分解见 [docs/dev-plan.md](docs/dev-plan.md)。

## 现状

- **M0–M2 完成**——写回引擎、截帧、UI 自动化、单轮感知均已真机 + 真模型验收。
- **M3 核心验收通过**——上面的完整链路(真模型、真手指手势、答复写入新页)端到端可用;
  重画 / 原地改写验收与故障注入测试待做。
- **M4 打磨进行中**——底边状态线、会话取消手势、用户笔型恢复已落地。

详见 [docs/progress.md](docs/progress.md)。

## 怎么用

跑常驻 daemon(`rm2-ai-daemon serve`,随 systemd unit 安装),然后在任意笔记页上:

- **双击屏幕右下角** → Agent 读当前页,把结果写到新页。
- **双击且第二下按住约 1.5 秒** → 原地模式:整页字节级备份后擦除重写。
- **会话进行中再次双击** → 取消。

会话运行期间,角落画一个小沙漏 ⧗,底边状态线随阶段更新——思考中、写入中、
画图中、读页中、擦除中:

<p align="center">
  <img src="docs/images/demo-writing.png" width="55%" alt="答复逐笔写入中,底边状态线显示写入中" />
</p>
<p align="center"><i>会话中途:答复一笔一笔出现,左下角是底边状态线("写入中…")。</i></p>

Agent 也能画图——SVG path 拍平成折线后按笔画注入——中英文混排写回
(拉丁字符走 Hershey 单线字体,汉字走 makemeahanzi 笔画中线):

<p align="center">
  <img src="docs/images/demo-draw.png" width="46%" alt="由 SVG path 以笔迹画出的小猫" />
  <img src="docs/images/demo-chinese.png" width="46%" alt="daemon 写出的中英混排笔迹" />
</p>

## 工作原理

只通过**内核标准接口**与 xochitl 相处,不打补丁、不劫持函数、不依赖 toltec:

| 模块 | 做法 |
|---|---|
| `inject` | 向 Wacom evdev 设备写合成笔事件——xochitl 分不清合成事件与真笔迹 |
| `capture` | 从 xochitl 进程内存里定位 framebuffer 读帧(全项目唯一的内部耦合点) |
| `layout` | 文本 → Hershey 单线字体笔画;SVG path → 贝塞尔拍平 → 折线。两条路径共用注入管线 |
| `ui` | 按固件版本的 UI 图谱 + 像素探针驱动 xochitl 自己的界面(换笔 / 撤销 / 清空页面) |

两个关键实测结论(M0):

- **手写感 = 压力包络**。恒压注入是均匀细线,一眼假;起笔加压 → 中段满压 → 末段渐提之后,
  xochitl 的压感笔刷渲染出粗细变化,接近真人笔迹。
- **注入笔迹按 xochitl 当前选中的工具渲染**。用户上次停在橡皮上时,注入即擦除——
  空白页上零效果、零报错。因此每次写入前**强制切笔 + 探针确认**,写后**验墨**兜底。

## 快速开始

设备通过 USB 连接(`10.11.99.1`),SSH 密码在设备的 Settings → Help → Copyrights 页。

```bash
git clone git@github.com:momaek/yome.git
cd yome

make check      # gofmt + go vet + 单元测试(全部离设备可跑)
make build      # 交叉编译 → build/rm2-ai-daemon(静态 ARM 二进制)
make deploy     # scp 到设备
make install    # deploy + 初始化 /home/root/.config/rm2-ai/config.toml + 安装 systemd unit
```

`make deploy DEVICE=root@192.168.1.50` 可走 wifi。

要用手势触发的 AI 会话,先在配置的 `[api]` 段填好端点与密钥
(`provider`、`model`、`api_key`,OpenAI 兼容端点另填 `base_url`),然后:

```bash
ssh root@10.11.99.1 systemctl start rm2-ai
journalctl -u rm2-ai -f        # 设备上看会话日志
```

下面的 CLI 调试命令不需要配置、不需要 API key。

## CLI 调试命令

每个子系统都能以前台命令单独驱动:

```bash
# 纯命令行跑一次感知-决策-行动会话(不需要手势)
rm2-ai-daemon ai -debug

# 排版一段文字写到当前页(自动换行;写后验墨)
rm2-ai-daemon write-text -text "hello from the daemon" -debug

# 不碰设备,只看排版结果与整页字符预算——本机就能跑
rm2-ai-daemon write-text -file notes.txt -dry-run

# 截图(竖屏方向,附黑像素计数)
rm2-ai-daemon capture -out /tmp/page.png

# 画 SVG(path 拍平为折线后注入)
rm2-ai-daemon draw-svg -file cat.svg -width 600

# UI 图谱操作
rm2-ai-daemon ui-run -list
rm2-ai-daemon ui-run -feature select_pen
rm2-ai-daemon ui-run -feature select_pen_type -params pen_type=pen_marker
rm2-ai-daemon probe -name toolbar_open
rm2-ai-daemon erase-page -yes        # 破坏性:清空当前页

# 手势标定:打印识别到的角落手势
rm2-ai-daemon trigger

# 原始输入:点击 / 滑动 / 事件转储 / 设备枚举
rm2-ai-daemon tap -x 700 -y 900
rm2-ai-daemon swipe -x0 1100 -y0 900 -x1 300 -y1 900
rm2-ai-daemon record -name touch
rm2-ai-daemon devices
```

所有命令支持 `-debug`(详细日志)与 `-config <path>`。

## 配置

`/home/root/.config/rm2-ai/config.toml`,全部字段见
[deploy/config.example.toml](deploy/config.example.toml)。

配置里的默认值就是 M0 的实测标定值(坐标换算、点间隔、压力包络、UI 时序),
调参是改配置而不是重新编译。文件缺失时走默认值,CLI 调试命令零配置即可用。

> **关于密钥**:rM2 上一切皆 root,文件权限不构成保护。真实风险是设备丢失与
> USB web 接口暴露,不是本地其他用户。

## 项目结构

```
cmd/rm2-ai-daemon/     CLI 入口、调试命令、serve 常驻与 agent 会话
internal/
  geom/                坐标基元(点 / 笔画 / 矩形 / 裁剪)——屏幕空间
  layout/              Hershey 字体、汉字笔画中线、SVG path 解析、排版器
  inject/              evdev 笔/触摸事件合成、坐标换算、压力包络
  capture/             从 xochitl 进程内存截帧
  trigger/             角落手势识别(双击 / 长按 / 取消)
  llm/                 模型适配(anthropic / openai 兼容)、视觉请求
  penstate/            用户笔型选择的保存与恢复
  ui/                  UI 图谱 + 像素探针 + 点击状态机
  config/              TOML 配置(M0 标定常数的默认值)
assets/                go:embed 数据:字体、汉字笔画数据、各固件版本 UI 图谱
deploy/                config.example.toml、systemd unit
docs/                  设计文档、开发计划、UI 图谱存档、进度记录
```

## 开发

```bash
make test           # 单元测试
make test-update    # 刷新 golden 文件
make check          # CI 跑的全部检查
```

单元测试全部离设备可跑:坐标换算、压力包络、JHF / SVG 解析、贝塞尔拍平、排版换行、
UI 状态机(用假屏幕与假点击器)都是纯逻辑。evdev 与 `/proc` 访问在 `linux` build tag
后面,因此本机能编译、能测,只有真正碰设备的入口在非 Linux 上返回错误。

**真机是唯一权威**:每个里程碑的验收在设备上做,`capture` + 黑像素指纹作为自动化断言。

## 致谢

- [makemeahanzi](https://github.com/skishore/makemeahanzi) —— 内嵌的 CJK 笔画中线数据
  (`assets/cjk/medians.bin.gz`)由其 `graphics.txt` 生成,数据源自文鼎科技
  (Arphic Technology)以 [Arphic Public License](assets/cjk/ARPHICPL.TXT) 发布的字体。
- [Hershey 字体](https://en.wikipedia.org/wiki/Hershey_fonts) —— 拉丁文单线笔画
  (`assets/hershey/futural.jhf`)来自 Allen V. Hershey 博士(美国国家标准局)的矢量字体,
  JHF 格式由 James Hurt 整理分发。
- reMarkable 社区 —— [remarkable.guide](https://remarkable.guide) 与
  [awesome-reMarkable](https://github.com/reHackable/awesome-reMarkable)
  记录了本项目所依赖的设备内部机制。

## License

[Apache-2.0](LICENSE)。内嵌的 CJK 笔画数据(`assets/cjk/medians.bin.gz`)源自文鼎字体,
仍遵循 [Arphic Public License](assets/cjk/ARPHICPL.TXT)。

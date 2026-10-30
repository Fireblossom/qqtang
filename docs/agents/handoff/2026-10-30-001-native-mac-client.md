# 交接：macOS 原生客户端（2026-10-30）

目标：让 QQ堂 在 Apple Silicon Mac 上原生可玩。做法是用 Python 3 宿主程序（`client/native/qqtclient`，pygame-ce）直接运行原版界面脚本（从原版 Python 2.3 字节码转换而来），连接本仓库的 Go 服务端；对战规则由 Go 辅助进程 `cmd/qqt-battle-sidecar` 演算，它复用服务端的 `internal/game/battleengine`。

用户用中文交流，回复和文档都用中文。

## 分支和提交

- 全部工作在本地分支 `native-mac-client`，**没有推送，也没有合并到 `main`**（要不要推送等用户决定）。最新提交 `1a7d08f`。
- 子代理留下的两个工作区：`/Users/cduan/qqtang-worktrees/social`（分支 `native-social`）和 `/Users/cduan/qqtang-worktrees/multiplayer`（分支 `native-multiplayer`）。两个分支都已完整合并。工作区里有原版脚本的副本，可以用 `git worktree remove` 清理，清理前先问用户。
- `client/native/generated/` 是从原版客户端转换、解密出来的腾讯资源，已在 `.gitignore` 里，**绝不能提交**。`.chat_history` 是用户留下的旧会话记录，也不要提交。

## 这一轮完成的内容

按时间顺序（提交号在括号里）：

- **商城收尾**：README 和测试补齐；道具箱图标偏下的问题，按 `bkImgFlag` 的原版对齐规则修好（`0x10029A37`）。
- **人物服装**：房间和对战里的人物穿着已装备的服装（`7e31f9b`，原版 `room_changePlayer` `0x00458240`）。
- **房间装备道具栏**（`f7be420`）：
  - 仓库列表、`EquipOneProp`、本地 `profile/<号码>.ini` 记格子顺序，按 QQTSection.dll 移植，见 `qqtclient/props.py`；
  - 宿主新增双击、拖放、悬停提示框、可撑高的 `DYLABEL`；
  - `ChangeRoomType` 改为发 0x00D0；
  - 道具场里使用带入的道具（0x1174/0x1175）。
- **界面引擎修正**：
  - 未选中的标签页不画子控件、Static 容器不挡点击（`a0f7528`）；
  - 快捷键、剪贴板、弹窗自动关闭、`Win_GetEventCnt`（`16fb7dd`）；
  - 宽度为 0 的标签不画字（`efcce33`）。
- **好友**（子代理完成，合并于 `4e6c05e`）：名单、上下线、资料、加好友、同意或拒绝、删除、信封消息，见 `qqtclient/friend.py`。
- **探险关卡解密**（`4e015ee`）：`tools/decrypt_pve.py` 把 `config/battlefield_map16xx.dat` 和 `pvedata.dat` 解成 XML，写到 `generated/pve/`。算法是 zlib 加相邻字节异或，有纯 Python 实现；`tools/win32emu.py` 用 Unicorn 执行原版读取函数做逐字节对照。`prepare-ui.sh` 最后一步会运行它。
- **多真人同局**（合并于 `9cd7471`）：
  - 不当房主时，辅助进程打开引擎的 `NativeOutcomeAuthority`，再加新选项 `Rules.NativeRequestPlayerID`，只发本地玩家的请求，共享结果以仲裁方为准；
  - 两名以上真人时，开局拦下地图 11、124（会进 Boss 局）；
  - 双客户端联调两局，服务端警告都是 0。
- **文档**：
  - `client/native/docs/checklist.md`（任务清单，`6d52600`）；
  - 调研报告 `other-rules.md`（其他对战玩法）、`adventure.md`（探险）；
  - 经验教训 `lessons.md`；
  - README 的"已确认的原版约定"记了所有查到的原版地址。
- **夺宝逆向第一轮**（`1a7d08f`）：结论写在 `docs/other-rules.md` 的 2.4 节末尾，要点见下文"下一步"。

## 关键决定

- 用户的决定：
  - 先做多真人同局，再做特殊玩法；
  - 可以给 `battleengine` 加扩展点，但必须**默认关闭**、补测试，每一处单独向用户说明；
  - 允许在 `.venv` 里用 Unicorn；
  - 暂不装 Wine；
  - 要装 Ghidra 时先问用户。
- 原版行为**不要猜，先查**：用项目技能 `.claude/skills/qqt-binary-re`（`pe native/func/dis/strings/xref/name`、`grep_insn`、`vtables`），结论写进 README 的"已确认的原版约定"并注明地址。推断出来的规则要标明"推断"。
- 原版没注册的函数（例如 `ChangePosition`）不实现。
- Boss 局只在客户端拦截，不改服务端。
- 服务端的数据和原版不一致时照实显示（例如商城"使用时间: 0 天"）。
- 子代理：长任务一定要要求它分步提交。多真人同局的子代理两次中途停止，最后由主会话接手完成。之后的特殊玩法建议主会话自己做。

## 测试环境

- 原版客户端：`~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched`（下称 `$C`）。
- 测试服务端：`/tmp/qqt-out/qqt-server-onnx`。在 `~/Downloads/QQTang-Local-v1.3.4/QQTang-Local` 下运行：`nohup /tmp/qqt-out/qqt-server-onnx -config configs/server-mac-test.json &`。
  - 端口 18000（账号和分区）、18001（商城）、18080（目录）、18100（GM 接口 `http://127.0.0.1:18100/gm/api/accounts/<号码>`）。
  - 日志在 `runtime/logs/server-local.jsonl`，是判断两边是否一致的最好依据：每局结束后统计这一局的 warn 和 error。
  - `/tmp` 重启后会清空，服务端程序、`/tmp/qqt-ort`（ONNX 库）、`/tmp/qqt-go`（Go 工具链）都要重新准备。
- Go：`export PATH=/tmp/qqt-go/go/bin:$PATH GOTOOLCHAIN=local`。编译辅助进程：`client/native/tools/build-sidecar.sh`。
- 测试账号（密码都是 123456）：
  - **1000001 糖一**：有 AI 卡 30099；装备了小糖（13013）；有万能药剂 20020 ×4、盖亚之种 20030 ×5，`$C/profile/1000001.ini` 记着道具栏格子。
  - **1000002 糖二**。
  - **1000003 糖三**、**1000004 糖四**：本轮用 GM 接口新建，各有一张 AI 卡；好友关系是糖四的名单里有糖三。
  - 两个客户端同时测试时用不同账号，同一个号同时登录会互相踢下线。
- 运行客户端：`cd client/native && .venv/bin/python -m qqtclient --client-root $C --headless --screen login ...`。
  - 自动化参数有 `--call EXPR@MS`、`--click`、`--rclick`、`--drag`、`--type`、`--key`、`--hold`、`--exit-after`、`--screenshot`。
  - 例子见 README 的"联网对战"一节。双客户端时，把一个放后台，并在它的命令里用 `EnterRoom(1)`、`UserSelTeamInRoom(0,2)`、`UserReady()`。
- 沙箱：
  - 运行客户端要申请完整权限，因为它会写 `~/Library/Application Support/QQTangNative/state.json`；
  - 写 `qqtang-worktrees` 也要完整权限；
  - Unicorn 只能在沙箱外跑。
- 坑：
  - zsh 不会自动拆分变量，重复的命令写成 shell 函数；
  - 看截图的工具按文件名缓存，每次截图换新文件名；
  - 客户端没有 `--screenshot-at` 参数。
- 测试：
  - Python：`.venv/bin/python -m unittest discover -s tests`，全部通过。
  - Go：`go test ./cmd/qqt-battle-sidecar ./internal/game/...` 通过。
  - `go test ./internal/server/probe` 里 `TestLoadConfigSelectsONNXRuntimeLibraryOnlyWhenNeeded` 在 macOS 上**本来就失败**（`onnxruntime_path.go` 没有 darwin 分支），和本轮改动无关。
- 记忆笔记：`/Users/cduan/.claude/projects/-Users-cduan-qqtang/memory/mac-client-goal.md`，进度有变化时要同步更新。

## 下一步：实现夺宝（规则 5）

用户已选定这一项。已查清的原版行为（详见 `docs/other-rules.md` 2.4 节）：

- **复活**：规则对象槽位 4 为真表示能复活，槽位 5 是延迟，夺宝为 5000 毫秒（抢包子 10000，和录像一致）。
  - 仲裁方的组件（`0x00615AAE`）在角色阵亡满 5 秒后发 0x0FBA `{PlayerID, 时间, 行, 列}`；
  - 复活格就是开局时的出生格（角色 `+0x470`/`+0x472`）；
  - 比武和夺宝要等复活格空出来才复活。
- **阵亡掉落**：身上有宝石时只掉宝石（`0x0060B08C`），一颗都没有时才按普通规则掉道具。
- **开局**：宝石 150、151、152（分值 1、2、3）按 `GAME_BEGIN` 给的数量埋进墙里（`0x0060B139`）。
- **胜负**：服务端按"每名队员 22 分"判胜，超时按携带的分值判定（`internal/game/match` 的 `RecordTreasurePickup`）。宝石拾取走普通的 0x0FAC/0x0FAD。
- **还没查**：
  - 复活后有没有保护时间；
  - 分数显示：槽位 10 转到场景 `+0x4B58` 的 `0x5DB07D`，槽位 12 打开编号 0x20 的界面；
  - 宝石被火焰波及时会不会消失；
  - `"Relive"` 动画回调（`0x005FA66C`）也会发 0x0FBA，它的作用还不清楚。

建议的实现步骤：

1. **引擎扩展点**（默认关闭、补测试、单独说明）：
   - 定时复活（`Engine.RespawnActor` 之类）；
   - 宝石作为不产生效果、只计数的拾取物（现在 `supportedPickupEffect` 会拒绝 150～152）；
   - 阵亡时只掉宝石；
   - 胜负由外部（服务端）判定；
   - 放开 sidecar 的"只接受规则 1"门槛（`map_adapter.go` 的几处检查），服务端的校验保持原样。
2. **辅助进程**：仲裁方按上面的规则定时发 0x0FBA；所有客户端收到后执行复活；宝石计数随拾取和掉落变化。
3. **画面**：显示分数。先查清分数显示，再画。
4. **联调**：用 1000003 和 1000004 在夺宝地图上测。普通夺宝图没有 AI 补位，必须两个真人；道具场的夺宝图是 1114；1111 是藏宝室，不能选。

之后的顺序：抢包子（有教程录像 `Tutorial2.qbv`）→ 踢炸弹（`Tutorial3.qbv`）→ 雕像，见 `docs/other-rules.md` 第 4 节。完整的剩余工作见 `client/native/docs/checklist.md`。

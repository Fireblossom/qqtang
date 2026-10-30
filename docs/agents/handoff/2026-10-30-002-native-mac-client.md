# 交接：macOS 原生客户端（2026-10-30 第二版）

接续 `2026-10-30-001-native-mac-client.md`。那一份讲的是项目背景、环境和之前的工作，本文只写之后的进展和现在的状态。两份都要读，再读 `client/native/docs/checklist.md`。

目标不变：让 QQ堂 在 Apple Silicon Mac 上原生可玩。Python 3 宿主程序（`client/native/qqtclient`，pygame-ce）运行原版界面脚本，连本仓库的 Go 服务端；对战规则由 Go 辅助进程 `cmd/qqt-battle-sidecar` 演算，它复用 `internal/game/battleengine`。用户用中文交流，回复和文档都用中文，汇报要用大白话，少用地址和术语（用户说过"没看懂"）。

## 分支和提交

- 本地分支 `native-mac-client`，**没有推送，也没有合并到 `main`**，等用户决定。
- 上一版交接之后的提交：
  - `f0a3ee6` 引擎扩展点（默认关闭）：`Rules.ObjectiveSceneIDs`、`ObjectiveDeathDrops`、`ExternalConclusion`、`Engine.RespawnActor`、`CompetitiveMapConfigOptions.AllowSpecialRules`。
  - `b298b78` 辅助进程能玩夺宝。
  - `ab33cc7` 用户自己提交的 `.gitignore`（加了 `.chat_history`）。
  - `252fed4` 夺宝计分板、复活后的画面、UDP 握手回应一次、文档。
  - `62a9b79` 复活后 3 秒免伤（Unicorn 黑盒结果），新增 `client/native/tools/probe_vm.py`。
  - `290fd2c` 复活等待只看出生格上的地图元素；计分板按座位排。
  - `53b2c4b`、`0229530` 用户另开的会话修好了服务端在 macOS 上自动找 ONNX 运行库。
  - `1e5e6d1` 测试机器人 `--autoplay`；右侧玩家栏按原版重画。
- **未提交、正在进行**：抢包子（规则 3）。改动在 `cmd/qqt-battle-sidecar/bun.go`、`bun_test.go`（新文件）、`online.go`、`main.go`、`autoplay.go`、`treasure_test.go`、`client/native/qqtclient/battle/scene.py`、`client/native/README.md`、`client/native/docs/other-rules.md`。接手时先 `git status`、`git diff` 看清楚，跑一遍测试再决定是继续还是提交。
- `client/native/generated/`（腾讯资源）和 `.chat_history` 绝不能提交。

## 这一轮完成的内容

**夺宝（规则 5）可玩，按原版实现：**

- 宝石 150、151、152（1、2、3 分）只计数、不加属性；带着宝石阵亡只掉宝石；火焰会烧掉露天的宝石，进小鸟回收队列。
- 阵亡 5 秒后由仲裁方发 0x0FBA，在出生格复活。复活后立即能动，有 3 秒免伤（和变身结束时同一套保护），属性恢复成开局值。
- 出生格上有地图元素（被推过去的箱子）时等待；玩家和泡泡不挡。53 张比武、夺宝地图的出生格开局都是空的。
- 右侧计分板按房间座位排，数字和底牌颜色按原版。
- 胜负由服务端判定。双客户端 `--autoplay` 跑过完整一局：捡到宝石、击杀、多次复活，超时按携带分值判出胜方，服务端零警告。
- 没做：复活免伤的闪烁效果（`.eff` 格式没解）。

**右侧玩家栏按原版重画**（所有玩法都用）：每个座位一行，行距 51 像素；头像、名字（右对齐，带黑色阴影）、等级称号（Client.exe 自己的称号表）的位置都按原版。头像只画头部是推断。原版不显示"被困""阵亡"，也不把阵亡玩家变灰。测试账号积分超出称号表，显示"???"，原版也这样。

**测试机器人 `--autoplay`**（`cmd/qqt-battle-sidecar/autoplay.go`）：客户端加这个参数后，本地玩家由辅助进程里的机器人操作。它只看玩家能看到的东西：躲开已知爆炸，走去捡看得见的道具（宝石优先），确认放泡后能逃开才在墙边或对手旁放泡。两个客户端都加上，就能在无人操作时跑完一局，用来联调拾取、击杀、复活、计分。用户说过没时间亲自测，**联调一律用两个机器人扮演两名真人**。

**其他：**

- UDP 握手：收到真人对手的 Type-3 后回一次，避免对方一直重发（`match-protocol.md` §11 第 3 步）。
- README 的"已确认的原版约定"、`docs/other-rules.md` §2.4、`docs/lessons.md`、`docs/checklist.md` 都已同步。§2.4 里更正了一处误读：槽位 8（`0x0060B139`）不是开局埋宝石，而是把玩家身上的宝石放回小鸟回收队列（推断用于中途离开）。

## 抢包子（进行中）

已经查清并写进 README（未提交）的原版规则，代码注释在 `bun.go` 开头：

- 两座包子店（地图元素 8009，3×3，`0x005B720C` 开局时建）放在地图的两个目标格。第一座属于 `GAME_BEGIN` 里第一名玩家的队，第二座属于第一名不同队的玩家。
- 每座店开局有（人数 ÷ 2）个本色包子；抢来的异色包子另记，不能再被拿走。
- 店的四角是实心柱子，中间十字可走。站进对方店、空手、店里有本色包子：请求拿（0x0FB6，BunID 0）；站进自己店、手上有包子：请求放（0x0FB8）；空手站在地上的包子上：请求捡（0x0FB6，BunID 1）。仲裁方核对后发 0x0FB7、0x0FB9，不通过就不回。
- 被击杀时，空手的击杀者直接拿走包子，否则包子掉在死亡位置的格子上。中途离开时包子回到它颜色的店里。
- 复活 10 秒，不等出生格空出来。
- 拿着包子只换帽子（`cap22`、`cap23`），不减速、不禁止放泡。地上的包子画 `object/item/item11`；店里的包子数用 misc161、misc162 一排小包子表示。

接手后要做的：

1. 看清未提交的改动，跑测试（见下文"测试"）。
2. 把 `onlineMatch` 里的复活规则对规则 3 打开（10 秒，不等出生格）。
3. 画面：地上的包子、店里的包子排、携带者的帽子。
4. 机器人：学会去对方店拿包子、回自己店放下。
5. 用两个机器人在抢包子地图上联调。服务端按仲裁方的通知判胜负；看日志里有没有 warn。
6. 对照教程录像 `Tutorial2.qbv`（地图 806）：复活 10 秒、拿放消息的顺序。

之后的顺序（用户定的）：踢炸弹（规则 2，有录像 `Tutorial3.qbv`）→ 雕像 → 其他，见 `docs/other-rules.md` 第 4 节。完整待办见 `checklist.md`。

## 用户的决定（新增的）

- **同意用 Unicorn 黑盒运行 Client.exe 里受虚拟机保护的函数**（`tools/probe_vm.py`），只在本机运行，结果写到 `/tmp` 或 `generated/`，不破解虚拟机本身。已写进 `docs/lessons.md` 和逆向技能。网上的去虚拟化工具（如 tvm-recover）只支持 64 位新版腾讯壳，用不上。
- 用户已经用 Ghidra 把全部模块（包括 Client.exe）反编译成伪 C，放在 `~/qqtang-re/decompiled/<文件名>/`。用法见 `.claude/skills/qqt-binary-re/SKILL.md`。伪 C 会丢掉 `lea ecx, [x+偏移]` 这类 `this` 指针，涉及具体偏移时用 capstone 工具对照原始指令。
- 待办按顺序做：玩家栏（已完成）→ 抢包子 → 踢炸弹。
- 仍然有效的旧决定：引擎扩展点必须默认关闭、补测试、单独说明；暂不装 Wine；Boss 局只在客户端拦截。
- 用户问过能否用 Python 直接加载这些 DLL：不行（32 位 Windows 程序，Mac 是 arm64；规则在 Client.exe 里，和整个游戏绑在一起）。结论是继续"读伪 C + Unicorn 核对单个函数 + 机器人联调"。

## 测试环境

- 原版客户端 `$C` = `~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched`。
- 测试服务端 `/tmp/qqt-out/qqt-server-onnx`，在 `~/Downloads/QQTang-Local-v1.3.4/QQTang-Local` 下运行：`nohup /tmp/qqt-out/qqt-server-onnx -config configs/server-mac-test.json &`。服务端现在能自己找发布包里的 `runtime/onnxruntime/darwin-arm64/libonnxruntime.1.29.0.dylib`。日志 `runtime/logs/server-local.jsonl`。
- Go：`export PATH=/tmp/qqt-go/go/bin:$PATH GOTOOLCHAIN=local`。改了辅助进程要重新编译：`client/native/tools/build-sidecar.sh`。不要在仓库根目录直接 `go build ./cmd/qqt-battle-sidecar`，会在根目录留下二进制文件。
- `/tmp` 重启后会清空，服务端、Go、ONNX 库、下面的联调脚本都要重新准备。
- 测试账号：1000003 糖三、1000004 糖四，密码 123456，用于双客户端。

### 双客户端联调脚本

原来放在 `/tmp/qqt-tools/live/run_auto.sh`，内容如下（zsh，运行时要申请完整权限）。用法：`run_auto.sh <输出目录名> <毫秒>`。糖三建房当房主（仲裁方），糖四进房换到 2 队；结束后把这段时间的服务端日志切到 `server.jsonl`。换地图改 `UserSelMapInRoom(1101)`。

```sh
#!/bin/zsh
C=~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched
LOG=~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/logs/server-local.jsonl
OUT=/tmp/qqt-tools/live/$1
mkdir -p $OUT
wc -l < $LOG > $OUT/server-start-line
cd /Users/cduan/qqtang/client/native
.venv/bin/python -m qqtclient --client-root $C --headless --autoplay --screen login \
  --call "Login(1000003,0,'123456')@500" --call "request_LoginSection(1000003,0,0,0)@2000" \
  --call "CreateRoom('t',0,1,'')@4000" --call "UserSelMapInRoom(1101)@5500" --call "UserReady()@14000" \
  --exit-after ${2:-75000} --screenshot $OUT/owner.png > $OUT/owner.log 2>&1 &
sleep 3
.venv/bin/python -m qqtclient --client-root $C --headless --autoplay --screen login \
  --call "Login(1000004,0,'123456')@500" --call "request_LoginSection(1000004,0,0,0)@2000" \
  --call "EnterRoom(1)@5000" --call "UserSelTeamInRoom(0,2)@6000" --call "UserReady()@7000" \
  --exit-after $(( ${2:-75000} - 3000 )) --screenshot $OUT/guest.png > $OUT/guest.log 2>&1
wait
tail -n +$(( $(cat $OUT/server-start-line) + 1 )) $LOG > $OUT/server.jsonl
echo done
```

检查结果：

- 两边日志里的 `[sidecar]` 行（复活、握手）和 `sidecar failed`（辅助进程退出）；
- `server.jsonl` 里 level 不是 info 的行。客户端退出后 8 秒左右出现一条 `tcp_read_failed` 是正常的，是客户端关闭连接；
- 结果名：`qqt_game_event_ack_get_item_notify_player_<号>_item_<物品>`、`..._player_relive_<号>`、`qqt_competitive_game_over_timeout_winner_team_<队>`。

一整局 180 秒的玩法要给 202000 毫秒，结算表正好在截图里。

### 测试

- Go：`go test ./internal/game/... ./cmd/qqt-battle-sidecar`。有些地图测试要找 `runtime/client-patched`：临时建符号链接 `mkdir -p runtime && ln -s $C runtime/client-patched`（`runtime/` 在 `.gitignore` 里），用完删掉。
- Python：`cd client/native && .venv/bin/python -m unittest discover -s tests`（`1e5e6d1` 时 79 个，全部通过）。

## 坑（新增的）

- zsh 里 `echo ======` 会报 `not found`（`=` 开头会被展开），分隔线用 `echo ---`。
- `grep_insn` 的寄存器写成 `e[a-z]x` 只匹配 eax、ebx、ecx、edx，会漏掉 esi、edi，要写 `e\w+`。
- 后台测试跑到一半、会话被打断时，两个客户端的辅助进程会同时退出，日志只有 `sidecar failed: battle sidecar exited`，没有错误堆栈。这不是代码问题，重跑即可。
- 用户另开了会话（btw）改服务端，大改之前先 `git status`、`git log` 看有没有别人的新提交。
- 记忆笔记 `/Users/cduan/.claude/projects/-Users-cduan-qqtang/memory/mac-client-goal.md` 有进度摘要，进度变化时同步更新。

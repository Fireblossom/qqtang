# 原生客户端（macOS 实验版）

用 Python 3 重新实现原版客户端的宿主程序，直接运行原版界面脚本，可在 Apple Silicon Mac 上原生运行。原版 `Client.exe` 不参与运行；画面、音效和界面逻辑都从用户自备的原版客户端读取。

## 当前进度

| 阶段 | 状态 |
| --- | --- |
| 开场画面、登录界面 | 可显示、输入、点击 |
| 账号密码登录 | 可用：`QQTAUTH1` 挑战应答后请求目录，拿到区服列表 |
| 选区界面 | 可用：显示分区和负载，点分区或“快速加入”进区 |
| 大厅 | 部分可用：房间格子分页、玩家列表、分区名称，每 5 秒刷新房间、每 10 秒刷新玩家；建房、点房间进入、快速加入；大厅聊天、悄悄话（对指定玩家）和小喇叭。好友：“好友”标签页（登录时恢复好友名单，好友上线排到前面、下线排到后面）、点玩家查看资料（在线状态、频道和小区、积分战绩）、加好友（附言）、同意或拒绝、删除好友，申请和结果经聊天栏的信封图标逐条查看。家族、家族聊天、斜杠命令（邀请、屏蔽等）、好友追踪尚未实现 |
| 房间 | 部分可用：座位和人物（按队伍染色）、房主、准备/取消准备、房主开始、换角色、换队伍颜色、选地图、开关空位、踢人、房间聊天和头顶气泡、离开房间；人物穿着各自已装备的服装（对战里也一样）；切换竞技无道具、道具和探险模式；装备道具栏（"我的背包"里双击或拖动道具装上，右键卸下，格子顺序存在客户端目录的 `profile/<号码>.ini`）；开局后进入对战，结束后回到房间。改房间属性已接通但未实测；聊天室背景、婚礼未实现 |
| 对战 | 普通规则地图可玩：地图、人物行走、放泡、爆炸和连锁、炸墙和推箱子、道具出现与拾取、被困和阵亡、计时、玩家列表、胜负提示。离线练习可用；联网时可作为房主与服务端 AI 对战（本客户端担任仲裁方），对局由服务端判定胜负后显示结算画面并回到房间。对战中可使用捡到的道具（1～7 键、Ctrl、Tab）；道具场里可使用从房间带入的道具（服务端只允许道具场带入，一个人在道具场不能补 AI，需要两个真人）。可以和其他真人同局：本客户端是房主时担任仲裁方，替所有人裁定；不是房主时只发自己的请求（拾取、接触、被炸、阵亡），共享结果都以仲裁方的通知为准。两名真人同时进入地图 11 或 124 会触发 Boss 局，房主开局时会被拦下 |
| 其他对战玩法 | 夺宝（规则 5）可玩：宝石只计数、带宝石阵亡只掉宝石、阵亡 5 秒后在出生格复活、右侧计分板；胜负由服务端判定。踢炸弹、抢包子、比武、雕像、机器、箱子、糖客战和 Boss 局尚未开始，都需要客户端自己演算（服务端只记账判胜负），且至少两名真人。调研和顺序见 `docs/other-rules.md` |
| 探险模式 | 未开始：大厅标签页和房间里的模式切换可用；关卡数据（NPC、过关门、各级属性）已能解出（`tools/decrypt_pve.py`），NPC 演算、血量伤害和大地图都还没做。调研见 `docs/adventure.md` |
| 商城 | 部分可用：进出商城、六个大类和子类的商品分页、糖币购买（扣款后物品进背包）、道具箱分页、试穿预览（换角色、换颜色、动作）、保存装备（`ITEM_STATUS_CHANGE`）。服务端只结算糖币，Q 币、Q 点、酷比宝石、赠送、修理、锻造合成、水果机、任务和密保验证都未实现 |

原版脚本共用到 346 个宿主函数，已实现 183 个。运行 `tools/native_coverage.py` 可按界面列出尚未实现的函数。除此之外，服务器消息到达时宿主要回调脚本里的 `Notify*`、`ui_*` 等函数（约 150 种），目前接通了登录的 `LoginOK`、`LoginFailed`，大厅的 `updateRoom`、`ui_NotifyPlayerUpdate`、`ui_sectSetBtnState`、`NotifyBroadcast`（小喇叭），好友的 `updatePlayer`、`startGlint`/`endGlint`、`ReceivedRequestAddFriend`、`ui_msgBox`、`go2playerInfo`、`ui_getPlayerDetail`，以及房间的 `ui_NotifyRoomSeatChange`、`ui_NotifyRoomID`、`ui_NotifyMapChange`、`NotifyRoomPropChange`、`ui_UpdateGameType`、`ui_createRoomOK`、`ui_playerChat`。对战画面和游戏逻辑不在界面脚本里，需要另外实现。

## 工作方式

原版界面逻辑是 `res/uiRes/*.pyc` 等 16 个 Python 2.3 字节码文件，由 `Client.exe` 通过 boost.python 注入原生函数（`Win_*`、`Login` 等）后执行。本目录分两部分：

1. `tools/prepare-ui.sh`：把字节码反编译并转换成 Python 3，输出到 `generated/scripts/`。
   - uncompyle6 还原 Python 2 源码；
   - lib2to3 转换语法；
   - 绕过 uncompyle6 3.9.3 的一个缺陷：连续 5 个以上常量组成的列表会被输出成常量表下标（例如 `RoleNameList`、`allMapTypeList`、等级积分表），转换时对 Python 2 字节码关闭这种合并；
   - AST 处理补齐 Python 2 语义：GBK 字节串解码为文本，`/` 改为 `qqt_py2_div`（整数相除向下取整），`list.sort(cmp)` 改为 `key=qqt_py2_cmp_key(cmp)`，`len()` 改为 `qqt_py2_len`（文本按 GBK 字节数计，脚本用它算文字宽度，如 `len(name) * 6`）；
   - `uiMain` 用 `MyExecFile` 执行的是 `object/itemCFG.py`、`object/commodityCFG.py`、`map/mapDesc.py` 三个源文件，转换时直接读这三个 GBK 源码。同名 `.pyc` 是 2007 年的旧编译结果（记录的源文件时间与 `.py` 不符，Python 2.3 会忽略它），发布包只更新了源码：物品表 2947 项、商品表 2978 项、地图 381 张，均为版本 848，与 `config/Commodity.ini` 一致。
   - 同一个脚本最后运行 `tools/decrypt_pve.py --no-emu`，把探险关卡数据 `config/battlefield_map16xx.dat` 和 `config/pvedata.dat` 解成 XML，写到 `generated/pve/`（不提交）。不带 `--no-emu` 时，它还会用 Unicorn（`tools/win32emu.py`）执行原版 QQTSection.dll 的读取函数逐字节对照；加 `--verify-client` 再跑 Client.exe 自己的战场加载函数。Unicorn 要分配可执行内存，只能在沙箱外运行。
2. `qqtclient/`：宿主程序。
   - `script.py`：脚本共享命名空间；脚本有独立的 `sys`，未实现的原生函数返回 0 并记录到报告；
   - `widgets.py`：由界面类构建控件树；
   - `render.py`：用 pygame 绘制 DIMG 图片和文字；
   - `runtime.py`：界面切换、焦点、定时器、输入、系统消息框，以及网络线程回到界面线程的调用队列；
   - `natives.py`：控件、音频、文件系统等本地原生函数；
   - `session.py`：登录、选区、进区和游戏连接；其他模块通过 `Session.handlers` 注册收包处理，通过 `on_entered`、`on_disconnected` 响应进区和断线；
   - `lobby.py`：大厅的房间分页、玩家列表和定时刷新；
   - `room.py`：建房、进房、快速加入和房间界面；本地状态只在服务端通知到达后改变；
   - `avatar.py`：按 `object/player/player<角色>.ini` 叠加部件画人物，并按 `colorlist.dat` 给队伍颜色染色；
   - `battle/`：对战。`mapfile.py` 读地图和地图元素属性，`sidecar.py` 驱动规则辅助进程，`scene.py` 绘制对战画面和处理键盘，`practice.py` 离线练习，`online.py` 联网对局（TCP 一侧）；
   - `chat.py`：大厅聊天、悄悄话和小喇叭；
   - `props.py`：房间的装备道具栏和道具仓库；
   - `friend.py`：好友名单（大厅“好友”标签页）、加好友和应答、删除好友、聊天栏信封里的好友消息、玩家资料（`FindFriend`）。它包装了 `lobby.py` 的玩家列表函数（模式 1 是好友），所以要在大厅模块之后注册；
   - `shop.py`：商城。商品目录读客户端自己的 `config/Commodity.ini`（服务端只校验版本号，不下发目录）；购买走独立的商城端口，装备变更和背包更新走游戏连接；
   - `net/`：协议实现，均与服务端 Go 代码逐字段对应：`qqtea.py` 加密，`accountauth.py` 账号校验，`game.py` 游戏包信封和连接，`directory.py` 目录区服信息，`player.py` 进区登录，`lobby.py` 房间和玩家列表，`room.py` 建房、进房和房间内的请求与通知，`chat.py` 聊天，`shop.py` 商城列表、购买、物品状态和背包更新，`friend.py` 好友名单、好友申请和应答、查找玩家。

3. `tools/` 里的其他工具：`build-sidecar.sh` 编译规则辅助进程；`native_coverage.py` 按界面列出尚未实现的宿主函数；`decrypt_pve.py` 和 `win32emu.py` 解探险关卡数据（见上）。

反编译和转换的结果来自原版客户端，`generated/` 已加入 `.gitignore`，不得提交。任务清单见 `docs/checklist.md`。

## 使用

需要 [uv](https://docs.astral.sh/uv/)（转换脚本用它固定 Python 3.12 和反编译器版本）以及原版客户端目录，例如发布包里的 `QQTang-Local/runtime/client-patched`。

```sh
cd client/native
tools/prepare-ui.sh <客户端目录>
uv venv --python 3.12 .venv
uv pip install --python .venv/bin/python -r requirements.txt
.venv/bin/python -m qqtclient --client-root <客户端目录> [--server 127.0.0.1]
```

网络会拦截 TLS 时，脚本已对 uv 启用 `--system-certs`；手动执行 `uv` 命令时同样加上该参数。

客户端以客户端目录为工作目录运行，和原版一样，脚本每次启动会重写其中的 `devConfig.txt`。上次登录的 QQ 号保存在 `~/Library/Application Support/QQTangNative/state.json`。

`--screen login` 可跳过开场画面。

### 离线练习

对战规则由仓库中的 Go 程序 `cmd/qqt-battle-sidecar` 演算（复用服务端的 `internal/game/battleengine`，与服务端 AI 用的规则一致），客户端以子进程方式启动它。先用 Go 编译：

```sh
tools/build-sidecar.sh          # 输出 generated/bin/qqt-battle-sidecar
.venv/bin/python -m qqtclient --client-root <客户端目录> --practice 211 --opponents 2
```

方向键移动，空格放泡，1～7 键使用状态栏对应格的道具，Ctrl 使用选中的道具（被困时优先用针自救），Tab 切换选中格；对局结束后回车再来一局，Esc 退出。对手只会随机走动和偶尔放泡。目前只支持普通规则（native rule 1）的地图，例如 211 景阳岗；踢炸弹、抢包子、比武等地图会被规则引擎拒绝。

### 联网对战

在房间里选普通规则地图（如 211 景阳岗）后由房主开始。只有一名真人时，服务端要求房主背包里有 AI 卡（物品 30099）才会补满 AI；本地测试服务端可以用 GM 接口发放：

```sh
curl -X PUT -H 'Content-Type: application/json' -d '{"quantity":1,"available_period":4294967295}' \
  http://127.0.0.1:18100/gm/api/accounts/1000001/inventory/30099
```

开局后规则辅助进程以联网模式运行：它按 GAME_BEGIN 的地图和种子建立与服务端相同的初始局面，自己收发 QQTPPP UDP 场景数据（登记、与每个对手握手、移动/放泡/被困等消息），并把需要走 TCP 的可靠副本（REQUEST_PLAY 0x0083）交给客户端发送。房主就是仲裁方，由本地引擎裁定爆炸、拾取、击杀和救援，并回应 AI 和其他真人的拾取、被困和接触请求；服务端的 AI 依赖这些通知才能正常行动。不是房主时，本客户端只发自己的请求，共享结果都以房主的通知为准（见"已确认的原版约定"里的非仲裁方一条）。多名真人时，标准场各队人数要相同，服务端在两队都有真人时不补 AI；两名以上真人进入地图 11 或 124 会触发 Boss 局，宿主会拦下。道具场要用道具场地图（如 214），一个人在道具场不能开局。服务端发来 GAME_OVER 后先显示 Win/Lose/Draw 卡片，再显示成绩表，10 秒后（或按回车、Esc、点击）回到房间。使用道具时客户端先发 REQUEST_USE_ITEM（0x0FAF），等服务端的 0x0FB0 回来才生效，与原版一致。

对战协议和规则引擎的调研见 `docs/match-protocol.md` 和 `docs/battle-engine.md`，其他对战玩法见 `docs/other-rules.md`，探险模式见 `docs/adventure.md`（两份都是只读调研，引用的 `/tmp/qqt-tools` 临时文件可能已不存在）。开发中踩过的坑和做事方法上的教训见 `docs/lessons.md`。退出时会把运行中实际调用到的未实现函数及首次参数写入 `generated/missing-natives.json`。

`--autoplay` 让辅助进程里的测试机器人代替键盘操作本地玩家（`cmd/qqt-battle-sidecar/autoplay.go`）：它只看玩家能看到的东西，按已放的泡算哪些格子会被炸，躲开爆炸，走去捡看得见的道具（宝石优先），确认放泡后还能逃开时才在墙边或对手旁放泡。两个客户端都加上它，就能在没人操作时跑完一局特殊玩法，用来测拾取、击杀、复活和计分。它不是服务端 AI，也不模仿真人打法。

自动化调试参数：`--headless`、`--exit-after MS`、`--screenshot PATH`、`--click X,Y@MS`、`--rclick X,Y@MS`、`--drag X1,Y1:X2,Y2@MS`、`--type TEXT@MS`、`--key NAME@MS`、`--hold NAME@FROM:TO`、`--call EXPR@MS`（直接调用宿主函数）。例如无界面跑一局联网对战：

```sh
.venv/bin/python -m qqtclient --client-root <客户端目录> --headless --screen login \
  --call "Login(1000001,0,'123456')@500" --call "request_LoginSection(1000001,0,0,0)@2000" \
  --call "CreateRoom('test',0,1,'')@4000" --call "UserSelMapInRoom(211)@5500" --call "UserReady()@6500" \
  --exit-after 60000 --screenshot /tmp/battle.png
```

### 服务端

客户端连接本仓库的服务端，默认 `127.0.0.1`。登录依次使用三条 TCP 连接：

1. 18000：账号密码校验，服务端按来源 IP 和 QQ 号记下授权；
2. 18080：目录请求 0x0133，把授权转为进区许可并返回频道和分区；
3. 分区服务器（默认 18000）：进区登录 0x0064，之后大厅请求都走这条连接。

服务端可在 macOS 上原生构建：

```sh
CGO_ENABLED=1 go build -tags onnxruntime -o qqt-server ./cmd/qqt-server
```

在发布包目录下运行时，ONNX Runtime 需使用官方 osx-arm64 1.29.0 版 `libonnxruntime.1.29.0.dylib`。把它放到发布包的 `runtime/onnxruntime/darwin-arm64/` 下，配置里不写 `competitive_ai.shared_library_path` 时服务端会自动用它；也可以在这一项里填写别的路径（相对配置文件所在目录）。发布脚本不打包 macOS 服务端，这个文件要自己从官方发布包里取。默认测试账号为 `1000001`、`1000002`，初始密码 `123456`；本机测试库里另用 GM 接口建了 `1000003`（糖三）、`1000004`（糖四），用于双客户端联调。

## 测试

```sh
.venv/bin/python -m unittest discover -s tests
```

测试不需要原版客户端、服务端或显示器。

## 已确认的原版约定

- `framescheme` 每行 8 个数，是禁用、悬停、按下、正常四种状态各自的起止帧；第二行用于勾选状态。未写方案的按钮按正常、悬停、按下、禁用的顺序取第 0～3 帧。
- `NUMLABEL`（`gameUI.dll` `0x10018CFC`）用单帧数字条图绘制，字形顺序为 `0123456789/:-+.`：第 i 个字形是条图上 `x = i × textwidth` 处 `textwidth × textheight` 的格子（不是图宽均分），每个字符（包括不画的空格）前进 `textsize`，`textstyle` 含 `dt_center` 时整串水平居中。`PICLABEL` 每帧一个字形，顺序相同，字距为 `textsize`。
- 文字排版按宋体点阵（`0x10021E86`、折行 `0x10021C74`；宽度为 0 的标签一行都画不出，例如资料框里暂存号码的 `avtUin`）：GBK 单字节字符宽为字号的一半，双字节字符为两倍于此；每行最多 `控件宽 ÷ (字号 ÷ 2)` 个字节，逐字符折行；行距为 `textsize + rowspace`（默认 12 和 0）；超出控件高度的行不画，宽度容不下一个汉字的控件什么都不画。宿主用系统字体画字，比宋体宽时把整行水平压缩到宋体宽度。所以价格框（48 像素宽）里的"1000 糖块"只显示"1000 糖"，原版用这份商品表也是如此。`textLineType` 是线型（原价的删除线），与折行无关。
- `TABWIN`（`0x10004C98`）：控件本身 1×1，`hotrect` 是点击区，`hotcover` 总是画在 `hotrect` 处，未选中画第 0 帧、选中画第 1 帧；点击后同组（`groupid`）其他标签取消选中。未选中的标签不画子控件（虚表 `+0x80` 的覆盖 `0x10004D27` 只在状态位 `0x10` 置位时画），但子控件仍然可见、可以被点到。例如大厅切到探险模式后，竞技模式下的三个子标签都不再显示。
- 消息分发每处理一条消息就把计数加 1（`[0x10049928]`，`Win_GetEventCnt` 读取它）。按下左键时，如果有打开的弹窗（`wgtstyle_popup`），而点在弹窗外，就先隐藏它（`0x10030916`）。界面类的 `accel` 表是（回调名、虚拟键码、修饰键码……），宿主从焦点控件沿父链往上找匹配的条目；在 Mac 上把 Cmd 也当作 Ctrl，这是宿主自己加的。
- `Win_SetFocus`（重载 `0x1001C69E`、`0x1001C6AC`）最终调用 `0x1003027D`，直接把控件记为焦点（管理器 `+0x2AC`），不会转给子控件。`ui_setCapture` 把焦点给整个对话框之后，按键怎样送到对话框里的输入框还没有追到；目前加好友对话框要先点一下输入框才能打字。
- Static 控件把点击交给最近的可见、启用、非 Static 祖先（`0x10027269`）。宿主在找到的是界面（SCREEN）或根控件时返回空，让点击落到下面的兄弟控件。这是推断的：TPoser 默认是全屏的 Static 容器，大厅的 `chatArea` 就盖在模式标签上；如果把点击交给界面，原版的这些标签就点不动了。界面的状态位没有追到。
- 底图对齐 `bkImgFlag`（存于控件 `+0xC4`，绘制在 `0x10029A37`）只看低 3 位：等于 1（`dt_center`）时按 DIMG 画布尺寸（不是帧尺寸）水平、竖直都居中，等于 5（`dt_center|dt_vcenter`）时只把底边对齐控件底边，其余不移动；帧偏移照常另加。道具箱的 `picture` 是放在格子中心的 1×1 控件，靠这条规则把图标居中。
- 房间人物服装（`Client.exe` `room_changePlayer` → `0x00458240`）：队伍和角色都没变时什么都不做；否则重建人物，再取座位信息（`+0x54` 物品个数，`+0x56` 起每项 0x12 字节的物品结构），对角色字节（+7）等于新角色的每件物品回调脚本 `useCommItem(座位号, 物品编号, 特效, 颜色)`，脚本再对人物部件调用 `room_playerWear`。不看物品状态，未装备的物品角色字节为 0，不会匹配。角色为 -1 表示清空座位。
- 探险关卡数据是先 zlib 压缩、再做相邻字节异或。解密（`QQTSection.dll` `0x1000BEC8`）：先 `b[n-1] ^= b[0]`，再从 n−2 倒序到 0 做 `b[i] ^= b[i+1]`。读取函数是房间模块虚表 `0x1005419C` 的 `+0x64`（`0x1000E38E` → `0x1002BCDF` → `0x10035AE4`），形式为 `bool ReadCfg(path, size, out)`：解密、解压到 `size` 字节、把 CRLF 和 CR 换成 LF。`size` 来自 `config/GameCFG.ini` 的 `mapCfgSize`、`pvedataCfgSize`。Client.exe 的 `CBattleFieldMgr` 加载函数（`0x00892588`）通过应用对象 `+0x7D0` 调用它，再交给 TinyXML。过关门格子存在 `+0x8C`（LevelRow）、`+0x90`（LevelCol）。目录里没有 1647，1646 的下一关却写着 1647。
- 非仲裁方：辅助进程打开引擎的 `NativeOutcomeAuthority`（服务端 AI 运行时也用它），并把本地玩家设为 `NativeRequestPlayerID`。这样引擎替本地玩家产生拾取、接触、踢泡、推动、用道具等请求事件，由辅助进程发给仲裁方，火焰仍然只判本地玩家被炸；爆炸、拾取、击杀、救人都以仲裁方的通知为准。仲裁方在开局时选定，之后不跟着服务端的仲裁方变更切换引擎模式。
- 夺宝（规则 5）：
  - 宝石 150、151、152 分值 1、2、3，随 `GAME_BEGIN.NewItems` 和普通道具一起按 ItemSeed 埋进墙里，拾取走普通的 0x0FAC/0x0FAD，只计数、不加属性。
  - 火焰会烧掉露在外面的宝石，和普通道具一样进入小鸟的回收队列。火焰遍历（`0x0060FAB6`～`0x0060FAFC`）只放过场景物品 44、46、80、201、203、204。
  - 带着宝石阵亡时只掉宝石（槽位 6 `0x0060B08C`，顺序为 150、151、152），一颗都没有才按普通规则掉道具。
  - 槽位 8（`0x0060B139`）接收一个角色：把它身上的宝石放进小鸟回收队列，再清零计数。推断用于玩家中途离开。开局埋宝石不走这里。
  - 复活：仲裁方的组件（`0x00615AAE`）在玩家阵亡满 5 秒（槽位 5）后发 0x0FBA `{PlayerID, 时间, 行, 列}`，行列是出生格。比武和夺宝还要等出生格上没有地图元素：`0x005D865C` 查的场景格子 `+0xE78` 是地图元素层（推箱子消息 0x0FB3 的处理 `0x00604208` 就在这一层里移动元素），不管玩家和泡泡。原版遇到一个被挡住的出生格就结束这一帧的检查，其他人也等到下一帧。宿主的引擎把两层地图元素合成一格，检查的是"这一格有没有地图元素"；所有比武和夺宝地图的出生格开局时都是空的（53 张图核对过），所以只有被推过来的箱子会挡住。
  - 0x0FBA 的接收方（`0x00603754`）把角色移到格子中心（列×40+20、行×40+20），用 `0x005AD5E1` 把这一格记为出生格，再切换角色的状态机。
  - 复活状态的入口 `0x00881FE5` 在虚拟机保护代码里，用 `tools/probe_vm.py` 在 Unicorn 里黑盒运行，结果是：
    - 角色状态字节 `+0x3B7` 设为 1，即正常状态（救人 `0x005ACC42` 写的也是 1），所以复活后立即可以行动；
    - 清掉变身（`+0x404`）；
    - 经 `0x005B5202` 装上 3000 毫秒的 `flash2.eff` 免伤保护，和变身结束时一样。宿主还没画这个闪烁效果（`.eff` 格式没解）。
    - 它不写任何属性记录。属性恢复成开局时的值，依据是教程录像 `Tutorial2.qbv`：玩家 6 阵亡前放的泡威力是 4，复活后是 2。
  - 计分板（场景 `+0x4B58`，更新 `0x005DB0B5`，绘制 `0x005E1F3A`）：每个玩家一格，x=636、y=110+51×格位处画底牌 misc350，这名玩家所在队的总分为最高时用第 1 帧（橙色），否则用第 0 帧（蓝色）；底牌上画这名玩家自己身上的分值，队友用 misc351（绿），对手用 misc353（粉）。一位数画在 (640, 120+51×格位)，两位数画在 x=633、647，100 以上不画。格位（条目 `+0x3E`，取自角色 `+0x396`）是玩家在房间里的座位号：`0x005ACF42` 把它传给脚本的 `ui_NotifyShowLevelIcon`，脚本再用它调用 `GetSeatInfo`。第一次更新之前底牌都是蓝色。
- 对战右侧的玩家栏按座位排，每个座位 51 像素（`0x005E1E46`，所有玩法都用）：名字右对齐到 x=788、y=115+51×座位（12 号字，颜色 `0xFFC7F2FC`，先在右下偏 1 像素处画一遍黑色当阴影），第二行（等级称号）在 y=135+51×座位；脚本里的等级图标 `playerListDlg.level<座位>` 也在 y=130+51×座位。头像（`0x005DCF44` 建、`0x005DD1C3` 定位）是玩家站立朝下的形象，精灵点在 (685, 162+51×座位)，角色 41 再高 10 像素；场上角色的精灵点是坐标加 (10, 30)（`0x005B514E`，常量在 `0x7A1CA8`）。头像只画另一组部件（`+0x2CC`），推断只是头部，宿主按 `HEAD_CROP` 裁出头部。整个玩家栏和计分板都画在 UI 上面：计分板的数字跨在面板左侧不透明的竖条上，画在下面就看不清。第二行称号按竞技积分查 Client.exe 自己的表（`0x7F5940`，与 levelCFG 的称号有几处不同，如"泡泡糖战士"），最高三档按性别加"王子/公主"等，超出表的显示"???"（测试账号 1000003 的积分就超出了）。原版玩家栏不显示"被困""阵亡"，也不把阵亡玩家变灰。
- `DYLABEL`（`0x1001132C`）是随文字增高的提示框；没有文字时什么都不画，连底图也不画。有文字时底图按 1/3 分成上中下三段，中段拉伸到文字高度。宿主让框的上下边距都等于标题区的 y，这一点是推断的。
- 控件回调序号表在 `gameUI.dll` `0x100471D8`（`OnClick` 7、`OnRBtnUp` 8、`OnDBClick` 17、`OnRClick` 29、`OnDragItemDrop` 36、`OnBeginItemDrag` 37……），之后是控件类型名。双击的第二下由 `WM_LBUTTONDBLCLK` 触发 `OnDBClick`，不算一次按下，所以它的抬起不会再触发 `OnClick`；宿主按 Windows 默认的 500 毫秒和 4 像素判断双击。拖放：在带 `dragtype` 的控件上按住并移动超过 4 像素开始拖动（`OnBeginItemDrag`，图片由 `Win_SetDragImg` 指定），松开时落点控件有 `dragtype` 就收到 `OnDragItemDrop`。哪些拖放类型可以互相接收没有追到。`tipwidget` 在悬停时显示、移开时隐藏，这是根据提示控件默认隐藏推断的。
- 房间装备道具栏在 `QQTSection.dll` 的游戏数据对象里（类 ID 在 `0x10054630` 注册，接口虚表 `0x10054928`；`Client.exe` 的 `EquipOneProp` 通过 GUID `0x0079A908` 取到这个接口再调用 `+0x304`）。7 格放在 `+0xBF0C`，只接受 20001～22999 号道具。`EquipOneProp`（`0x1002B37A`）：格子 -1 表示放进第一个空格，满了就替换第 0 格；道具 0 表示卸下；已经在栏里的道具会被挪到目标格。每次装上或卸下都发 `ITEM_STATUS_CHANGE`（状态 1 或 0，角色 0，`0x1002B508`），并把 7 格写进客户端目录的 `profile/<号码>.ini`（`0x1002B81C`）。登录时（`0x1002B8A3`）状态为 1 的道具回到文件里保存的格子，其余放进第一个既空着、又没被保存给其他已装备道具的格子；没有文件就按物品顺序放。仓库列表（`0x1002B683`）包含 20001～22999、30001～31999 和状态不为 4 的 24001～24499 号物品，最多 500 项。脚本调用的 `ChangePosition` 在原版里没有注册。
- 改房间模式由 `ChangeRoomType` 发送 `0x00D0`（号码、时间、模式、关卡编号），其他人收到 `0x00D1`。道具场必须用道具场地图（如 16、113～117、214），服务端只在道具场把可带入的道具作为 `GAME_BEGIN` 里玩家的 `NewItems` 下发。对战中使用发 `0x1174`，服务端确认后向所有人广播 `0x1175`，并用 `0x00D9` 更新剩余数量。宿主把带入的道具固定放在状态栏前几格，捡到的道具接在后面；原版的排法没有追到，因为设置格子的代码（`0x005DAC82`）有一个调用方在加了虚拟机保护的代码里。万能药剂（20020）按说明清除减速和强制滑行，礼花只是效果，宿主不画。
- 登录成功后宿主调用脚本的 `NotifyMyUin(号码)`；脚本自己的 `uin` 平时由登录按钮设置，`go2shop` 等会把它传回宿主。
- 商城：`fetchCommItem` 把商品记录里的物品结构原样交给脚本；脚本在期限 ≥ 0 时显示"使用时间"，否则显示"道具数量"。服务端生成的 `Commodity.ini` 全部写 `AVAILPERIOD = 0`，所以确认框都显示"使用时间: 0 天"。道具箱的页码就是商城当前大类，物品所属大类按 `itemCFG.py` 的 `itemType`。
- `Win_SetValue` 的 41 号通道是每帧透明度变化量，901 号通道是淡入（1）或淡出（2）；其余通道编号见 `uiConst` 的 `value_channel_*`。
- 事件处理函数的参数按需传入：`OnClick(this)` 收到控件路径，`OnInit()` 不收参数。
- 脚本按 Windows 路径处理文件，宿主把 macOS 文件系统映射为 `Z:` 盘（与 Wine 相同）。
- 进区成功后由宿主切到大厅（`Client.exe` 中的 `GotoUIScreen('selRoom')`），脚本没有对应回调。
- 大厅每页 8 个房间格子，房间号从 `MinRoomID + 页码 × 8` 开始；服务端只返回已有房间，其余格子显示为空位。玩家列表第 0 项总是自己（`Sect.meGetPlayerInfo`）。
- 测试向量：房间列表请求与 `internal/protocol/game/room_test.go` 中抓包的明文逐字节一致；进房、快速加入回包和进房通知的测试数据 `tests/data/room_vectors.txt` 由服务端的 Go 编码函数生成。
- 建房成功只返回房间号，客户端随后用该房间号发 `EnterRoom` 取得完整房间快照（服务端按"已在房间中"处理）。脚本给的房间名为空时，宿主使用"一起来玩吧"。
- `GetSeatInfo().seatStatus` 在脚本中是 0 空位、1 关闭（显示 STOP）、2 有人，与两种协议编码都不同。
- 玩家列表刷新：`Client.exe` 的 `NotifyPlayerUpdate`（`0x0048ED30`）只在当前界面是 selRoom 时调用 `ui_NotifyPlayerUpdate()`，它同时重画引导栏列表（`updatePlayer`）和大厅右侧列表（`updatePlayer2`）。之前宿主只调用 `updatePlayer`，大厅列表进入后不再刷新。
- 好友的逻辑在 `QQTSection.dll` 的 `CFriendHandler`，宿主函数都转到 `XSection` 接口（`Client.exe` 中 `DoFriendTask` `0x0054DCC0` 调 `+0x274`，`GetAttribute` 调 `+0x27C`，`GetValidFriendsCount` 调 `+0x280`）：
  - `DoFriendTask(文字, 长度, QQ号, 操作)`：0 取名单，1 加好友，2 删除，3 追踪，5/7 屏蔽/取消屏蔽（发 `/m 号码`、`/u 号码`），6 应答（文字为 y/Y 时结果 0，n/N 时 0x33）。
  - 加好友（`0x1000F51E`）：自己或已是好友时不发；附言前面放 21 字节的自己的昵称，长度加 0x15。收到申请时（`0x10017FCA`）前 20 字节是昵称，第 21 字节起是附言。
  - 主分发（`0x100189CE`）只把 0xA2～0xA5 交给好友处理（`0x1000FAAA`）；0xA6 不处理。未关联请求的 0xA3 是别人的申请，关联请求的 0xA3 是自己申请的结果：0 成功，0x2C 消息失败，0x33 被拒绝，0x34 对方下线，0x35 名单已满，0x36 已是好友。本服务端改用 0xA6 回结果，宿主按这些含义显示。
  - 0xA5（`0x10010EBD`）只更新名单里已有的号码；上线的好友移到最前，下线的移到最后。原版只在上线时更新昵称、性别、身份，其余从 `./data/<号码>-Friends.dat` 读；宿主没有这个缓存，每次都更新。名单变化后执行 `updatePlayer()`（`0x100110A9`），大厅列表要等下一次 `ui_NotifyPlayerUpdate` 才重画。
  - `fetch_PlayerInfoInPlayerList(i, 1)`（`0x10010558`）：`m_nPlayerStatus` 是在线标志，家族字段取自大厅玩家列表。脚本不用这个状态，所以好友标签页里在线和离线看起来一样，只有顺序不同；在线状态在玩家资料里显示。
  - `GetValidFriendsCount`（`0x10011154`）是 `min(名单上限, 当前人数)`，灰色遮罩盖住的是超过上限的好友，不是离线好友。`GetAttribute(号码, 0)` 是否好友，1 是否屏蔽，2 自己身份是否含 0x2000。
  - `GetPlayerNickNamebyUin`（`0x1002D7DF`）查大厅玩家列表，查不到返回号码本身。
- 聊天栏信封：好友申请和结果放进“留言”队列，信封闪烁（`startGlint`/`endGlint`，`Client.exe` `0x0052D1D0`），每点一次 `GetLeaveWord`（`QQTSection` `0x1002A206`）取一条：申请填好 `UI.friendDlg.name`、`.text` 后执行 `ReceivedRequestAddFriend(号码)`；其他消息用系统对话框显示，后面还有消息时句柄 -3、类型 11（“下一条”，脚本的 -3 分支再调 `GetLeaveWord`），最后一条句柄 0、类型 3（只有关闭）。文字：“%s[%u]接受了您的添加好友的请求”“%s[%u]拒绝了…”“%u玩家已经下线”“你的好友列表已满”“%u玩家已经在你的好友列表中”“消息传送失败”。
- 系统对话框：`Client.exe` 显示 `UI.SysMsgbox` 后执行 `ui_msgBox(类型)`（`0x0048B320`），由脚本换按钮图片：1 同意/拒绝，0 确定/取消，11 下一条/关闭，其他只有关闭。
- 查看玩家资料：`FindFriend` 只发号码 ≥ 10000 的查询（`0x0048EAB9`）。回包由 `QQTSection` `0x1001D126` 填进资料结构：状态字节原样作为 `m_bStatus`（脚本 1 在线、2 在房间、3 游戏中，本服务端只发 0 和 1）；在线时才填频道、小区、地区名（按分区号查目录）、昵称和房间名，离线时昵称和性别取自好友名单，不是好友就为空。随后 `FindFriendInfoRecv`（`0x0048EB20`）执行 `go2playerInfo(号码, 0)` 和 `ui_getPlayerDetail()`。
- 好友关系在本服务端是单向的：A 的申请被 B 同意后只有 A 的名单里有 B。原版同意对话框的“同时添加对方为好友”默认勾选，同意时先向对方发一条反向申请。
- 房主的"开始"和其他人的"准备"都调用 `UserReady(0)`，由宿主按身份发开始或准备/取消准备。
- 位置类原生函数（`Win_Move2Pos`、`Win_GetX/Y`、`Win_SetRect`/`Win_GetRect` 的 winrect）使用屏幕坐标。
- 按下任何可用控件都会取得焦点：队伍颜色、座位等按钮在 `OnClick` 里用 `Win_GetFocusPath` 取自己的序号。
- 命中检测按 `gameUI.dll` 的实现（虚函数 `+0xB4`，基类 `0x1002912C`）：隐藏或禁用的控件连同子控件一起跳过，点击落到下面的控件；子控件从最上层往下检查；任何可见、启用的控件都会接住点击，不要求有 `OnClick`；Static 控件把点击交给最近的可见、启用、非 Static 祖先。
- 切换界面时宿主先调用所有子控件的 `OnInit`，再调用界面本身的；房间座位在自己的 `OnInit` 中上报人物位置。
- 对战画面：场地 600×520 画在 (10, 27)，外框 `object/ui/game/gametop.img`、`gameleft.img` 由宿主绘制；地图元素图片按 `mapElem.py` 的 `offset` 向左上偏移（墙比格子高，向上伸出）；先画地板，再逐行画墙和该行的人物，使下一行的墙能挡住上一行的人。火焰图 `flame1` 为中心，`flame2..5` 为臂、`flame6..9` 为臂端，方向依次为右、上、左、下。开局时宿主调用 `ui_SetGameMode(0)`、`ui_InitPVEBoss()`、`game_setMapID(地图)`，每秒写 `UI.game.playerListDlg.time`；右侧玩家栏里的头像、名字和状态由宿主自己画。被困时画角色的整身被困造型 `cloth<站立衣服编号>_trigger.img`（如角色 1 的 `cloth10101_trigger.img`，23 帧），阵亡时画 `cloth<编号>_die.img`；ini 中 `[trigger]`、`[die]` 段写的衣服编号没有对应图片。被困的糖浆团是 `object/misc/misc111`：先播 `_stand`（5 帧，糖浆成形）再循环 `_trigger`（13 帧），这是根据素材判断的，未在 Client.exe 中找到直接引用。
- 默认按键（`0x0048AE40` 填写，Windows VK 码，存为 26 字节数组）：方向键移动，空格放泡，Tab 切换道具，Ctrl 使用道具，回车聊天，1～7 使用道具 0～6，8、9、0、小键盘减号、I、O、P 为商店 0～6，F12 系统菜单，F1 帮助，Q 快速回复。Ctrl 和 Tab 具体如何选格未追到，目前按"使用选中格、被困时用针"实现。
- 状态栏右侧 7 格（`baseItemIcon0..6`、`itemNum0..6`）是对战中持有道具的 7 个槽位，由 `0x005DAC82` 设置图标：动作 41 用 `res/uiRes/icon/item/item21.img`，42 用 `item23.img`，其余用 `item<动作编号>.img`；编号在 20000～23000 的从房间带入的道具也放在这 7 格里。
- 场景对象编号为 `类型 << 24 | 编号 << 8 | 动作`（`0x00427050` 取类型，`0x00447750` 把类型映射为 player、bomb、item、mapElem、flame、misc、cap……目录；动作 1 为 stand，2 为 trigger，与 `object/player/player.py` 的 actStand、actTrigger 一致）。
- 结算画面素材（`object/misc/misc<N>_stand.img`）：295/296/297 为 Draw/Lose/Win 卡片，300 为成绩表底图（胜负、昵称、杀敌数、救人数、等级、特殊荣誉，8 行），311/312/313 为大号"胜利/失败/平局"，314/315/316 为小号，302 救人、303 杀敌、306 奖励、307 得分为数据框标签，301/308/309/310 为数字字体。卡片和表格的摆放位置是自定的，未对照原版。
- 人物部件绘制顺序（由后到前）：npack、foot、leg、body、cloth、head、face、hair；方向帧 0 右、1 上、2 左、3 下。普通图即红队配色；其他队伍用 `_m` 灰度图按 `colorlist.dat` 的色相染色，保持 HLS 亮度。

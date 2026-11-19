# 分区定时器、小喇叭队列、聊天房间点人与登出

原版宿主在这几处会主动调用脚本，原生客户端以前缺了：小喇叭的排队、显示时长和气泡（`NotifyRemoveMsg`、`NotifyAddPopo`、`NotifyRemovePopo`），每秒重画大厅两个玩家列表（`updatePlayer`、`updatePlayer2`），聊天房间里右键、双击人物（`ShowKickoutDlg`、`chatWithPlayerInChatRoom`），以及关窗口时的 `leaveSection()` 和登出消息。实现见 `qqtclient/section_timer.py`、`roomwalk.py`、`session.py`，测试见 `tests/test_section_timer.py`。

地址都是静态读 Ghidra 伪 C（`~/qqtang-re/decompiled/`）并用 capstone 核对过的；标“推断”的是没有完全追到的。

## 1. 分区定时器（QQTSection）

- 进区成功：`responselogin`（`0x1001B2EA`，结果 0）调用 `0x10021094`。这个函数先销毁旧定时器（`0x10021108`），再新建一个 500 ms 的窗口定时器：`0x1003DAF7(timer, 500, 0)` → `SetTimer(hwnd, 7, 500, NULL)`。
- 定时器的回调对象在 XSection `+0x160`，虚表 `0x10054D8C` 只有一项（`0x100010E1` → `0x100211FD`：`mov ecx, [ecx+4]; call 0x1002120A`）。
- `0x1002120A`（XSection::OnTimer）每次：
  1. `0x10008FD8`：小喇叭队列（第 2 节），对象在 XSection `+0x3C`。
  2. `+0xC548` 计数模 2，奇数次直接返回；偶数次（即每 1000 ms）往下执行。
  3. `+0x30` 处的对象不为空时调用 `0x10016FC2`：它每次都执行脚本 `updatePlayer()` 和 `updatePlayer2()`（字符串 `0x1005FDB8`、`0x10060418`），另外自己的计数每 10 次（10 s）调用一次 `0x10017041`，把成员表按 `+0x24` 位 0 分两组重排（在线的在前，推断是家族成员表：同一个对象的函数里有 `ui_updateMemberList`、`updateKinManagerDlg`）。
  4. 其余是家族、留言等计时，和本文无关。
- 定时器在登出时（`0x1001C23E`）又被重建，之后一直运行，没有找到停掉它的地方。

**结论**：大厅的两个玩家列表在原版里每秒重画一次，不是“好友变化后”才重画。好友模块自己（`0x100110A9`）只调用 `updatePlayer()`，大厅右侧列表靠这个定时器每秒刷新。宿主：`SectionTimer` 在 `on_login` 成功时启动，每 500 ms 一次；一帧太长时只补一次（Windows 的 `WM_TIMER` 最多只排一条）。登出后没有分区数据，宿主不再调用这两个函数（原版仍在调用，列表在别的界面上，看不出差别）。

## 2. 小喇叭（CBugleHandler，QQTSection）

### 2.1 收到

`0x1001F3D6`（NOTIFY_SECTION_MSG 0x0092 的处理）：身份字段 `+0x40A` 含 `0x4000` 时是小喇叭，调用 `0x10008A0F(uin = +0x406, effect = +0x40E, 文字)`。服务端的布局是 `u32 UIN, u32 identity, u32 XEffectID, u32 Point, 8 字节家族旗`（`internal/protocol/game/chat.go`），`+0x40E` 就是 XEffectID。

`0x10008A0F`：

1. `0x100086BA(buf, 文字, 1)`：去掉末尾的 `\r`、`\n`，对 `\`、`"`、`'` 前插反斜杠（`0x10008D10`，给后面拼脚本字符串用），中间的 `\n` 换成空格。
2. 本机 `+0x1C8 & 0x70006` 为 0（普通玩家）时用 `0x1002B1FA` 拆分：在字节里找第一个 `说:`（`0x1005EA88`，GBK `CB B5 3A`），位置 `i` 满足 `0 < i < 0x14` 时名字是前 `i` 个字节，正文从 `说:` 开始（`说:` 留在正文里）；否则名字就是 `说:`，正文是整行。`+0x1C8` 有官方身份位时，名字是 `[%u]`（号码），正文是整行。`+0x1C8` 由 `0x100206EF` 从登录信息 `+0x24` 复制，推断是本机身份。
3. 队列是 CList（`+0x38`，头 `+0x3C`，尾 `+0x40`，数量 `+0x44`），每条 0x11C 字节：`u32 uin, char name[20], char text[256], u32 effect`。数量大于 49 时先丢最旧的（`0x100092E3`），再加到队尾（`0x10009345`），即最多 50 条。

### 2.2 每 500 ms（`0x10008FD8`）

| 字段 | 含义 |
| --- | --- |
| `+0x28` | 少于 10 条时的隔次计数（模 2） |
| `+0x2C` | 队列空时的气泡计数（模 6） |
| `+0x30` | 模 5 计数，归零时置 `+4 = 1`（发送节流，本文不涉及） |
| `+0x54/+0x56/+0x58` | 三个显示位的倒计时（short，-1 为停） |

顺序：

1. 队列不空：三个倒计时都置 -1。
2. 多于 9 条：显示一条（第 2.3 节）；显示后少于 10 条时 `+0x28 = 0`。
3. 1～9 条：`+0x28 = (+0x28 + 1) % 2`，为 0 时显示一条；显示后队列空了，就 `+0x2C = 0`，倒计时设为 `0x38, 0x3A, 0x3C`。
4. 队列空：`+0x2C = (+0x2C + 1) % 6`，为 0 时调用 `NotifyRemovePopo()`；三个倒计时各自不小于 0 就减 1，减到 0 时调用 `NotifyRemoveMsg()`。

所以：积压 10 条以上时每 500 ms 显示一条，否则每 1000 ms 一条（队列从空变成 1 条时要等 500～1000 ms）。最后一条显示后 2.5 s 去掉气泡（之后每 3 s 再调一次），27.5 s、28.5 s、29.5 s 各调用一次 `NotifyRemoveMsg()`，脚本依次清掉三行（`msgcnt = 3`）。仍有排队时三行都不清。

### 2.3 显示一条（`0x10008D7E`）

依次执行脚本 `NotifyRemovePopo()`、`NotifyBroadcast("名字", 号码, "正文")`（格式由 `0x1005EC64/60/5C/58` 的 `"`、`",`、`,"`、`")` 拼成，号码用 `%u`）、`NotifyAddPopo(effect)`，然后把这条从队头删掉。effect 为 0 时脚本用 `img_popo.img`，否则按 itemList 的 `xeffect` 子编号换图。`0x10009267`（`NotifyRemovePopo0()`）没有调用者。

宿主按此实现（`BugleQueue`），只是参数直接传 Python 值，不再拼字符串，所以不用转义；名字长度判断仍按转义后的字节数算。正文缓冲区原版是 256 字节、`memcpy` 不检查长度（超长会写到 effect 和下一个节点，原版 bug），宿主不截断。以前宿主收到就直接 `NotifyBroadcast(昵称 + "说:", …)`：没有排队和清除，房间里名字按钮里多了“说:”，也没有气泡。

本地服务端不填 XEffectID（恒为 0），所以目前只能看到默认气泡；服务端没有“喇叭特效”的装备概念，这一项没有改。

## 3. 聊天房间点人物（Client.exe）

- 房间场景每帧（`0x00457740`）在 `+0x315 == 4`（聊天房间）时调用 `0x00457AE0`；别的房间不调用。
- `0x00457AE0`：偷看输入队列的第一个事件（`0x004365A0`，不取出）；鼠标位置取输入对象 `+0x504/+0x508`（`0x00432E50`）。按座位 0..7 找第一个有人物、并且 `x-25 < mx < x+25`、`y-65 < my < y` 的座位（`x`、`y` 是人物脚下的点，即 `room_setPlayerPos` 的点）。找到后：事件是 0xC（`WM_RBUTTONDOWN`）就取出事件、执行 `ShowKickoutDlg(座位)`；是 0xB（`WM_LBUTTONDBLCLK`）就取出事件、执行 `chatWithPlayerInChatRoom(座位)`；别的事件不动。不排除自己。
- 事件来自窗口过程 `0x0042F140` → `0x00435C40`：`0x201`→8，`0x202`→9，`0x203`→0xB，`0x204`→0xC，`0x205`→0xD；`WM_MOUSEMOVE` 不入队。
- 帧顺序（`0x00431ED0`）：先更新场景（`+0x98` → `0x00441590` → 场景虚表 → `0x00457740`），再由 `0x00431CB0` 把队列逐个交给界面（`0x00432760`）。所以人物上的右键、双击在控件之前被拿走，不管上面有没有控件；座位按钮在聊天房间里本来就被 `forbidPlayer` 禁用。
- 脚本：`ShowKickoutDlg` 弹出“查看/踢人/取消”，非房主时踢人按钮禁用；`chatWithPlayerInChatRoom` 用 `NotifyWisper` 把悄悄话对象设成这个人。

宿主：`RoomWalk.click`。事件钩子返回 True 表示事件已用掉，`__main__` 不再交给控件。左键双击按 Windows 的规则自己判断（时间和距离，与控件无关），用掉后清掉控件那边的上次按下记录。

## 4. 关窗口和登出

### 4.1 leaveSection（Client.exe 窗口过程 `0x0042F140`）

- `WM_SYSKEYDOWN`（`0x104`）且按键不是老板键（`DAT_007D9D3C`）时：`VK_F4`（`0x73`，即 Alt+F4）执行脚本 `leaveSection()`，再 `PostQuitMessage(0)`。
- 老板键：隐藏窗口、静音、`leaveSection()`、加托盘图标。
- 标题栏关闭（`WM_SYSCOMMAND SC_CLOSE` `0xF060`）：隐藏窗口、静音（`0x005937B0`）、`leaveSection()`、加托盘图标（`0x00441250` → `Shell_NotifyIconA(NIM_ADD)`），程序不退出。`WM_CLOSE` 只隐藏并加托盘图标。托盘菜单的“退出”（`0x9C42`）直接 `PostQuitMessage`。
- 脚本 `leaveSection()` = `doUI('UI.selRoom', 'OnEscape')`：没有密码框时点大厅的“离开”按钮，其中调用 `LogoutSection(uin)` 并回到选区界面。

宿主没有托盘：关窗口（`pygame.QUIT`，含 Cmd+Q）和 Alt+F4 都先执行 `leaveSection()` 再退出。

### 4.2 LogoutSection

`LogoutSection`（`0x0048B800`）取服务 `0x0079A958` 调 `+0x24`。这个服务在 QQTModules.dll（接口表 `0x1006C950`，类虚表 `0x1006CB44` 的 `+0x24` → `0x10008637` → 主虚表 `+0x6C` → `0x100094AD`）：取分区接口（GUID `0x1006C690`，即 QQTSection 的 XSection `+0xBF30`，虚表 `0x10054928`），先调 `+0x8C(0)`（`0x1001EB3F`：房间模块本地退出，参数 0 不发离开房间消息），再调 `+0x24`。

QQTSection `+0x24` 是 `0x10019E0D`：

1. 统计服务标志 `+0xC610` 打开时取一组 5 字节统计项。
2. `+0x1A8`（进区成功时写入的号码）不为 0 时，组 0x1FD 字节的 REQUEST_LOGOUT：`u32 号码, u32 时间（0x1004AEB9）, u8 项数, 项[5×n]`，交给 `+0x34`（`0x1001C23E`）：执行脚本 `NotifyRemoveMsg()`，重置好友等模块，重建分区定时器，再经网络接口 `+0x40` 发出。`+0x2588` 不为 0 时对它再发一次（用途未知）。
3. 关闭连接（网络接口 `+0x12C`），不等回包。

服务端 `internal/server/probe/login_service.go` 处理 0x0065（`DecodeLocalLogoutRequest`，项数最多 100）：离开房间、通知好友下线、离开大厅，保留一次免密码选区的授权，回 `qqt_logout_success`。宿主以前只断开连接，服务端日志里没有登出记录；现在按上面的顺序执行，统计项为 0 个。

## 5. 验证

- 单元测试 `tests/test_section_timer.py`：拆名字、队列时序（显示间隔、积压、50 条上限、气泡 2.5 s、三次清除 27.5/28.5/29.5 s）、定时器每秒重画、0x0092 的 effect、REQUEST_LOGOUT 字节、登出顺序、关窗口和 Alt+F4、人物点击框和双击。
- 双客户端实测（本地测试服，1000014 建聊天房间，1000015 进入）：
  - 1000015 右键房主人物弹出“查看/踢人/取消”且踢人按钮禁用；双击房主人物，输入栏的对象变成“测4”。
  - 房主右键 1000015 的人物，踢人按钮可用，点踢人后服务端收到请求（对方有防踢保护，回“踢人失败”，`room_kick_rejected/protected`）。
  - 小喇叭：房间里依次显示两条，带白色气泡，2.5 s 后气泡消失，约 28 s 后淡出；大厅里两条按新旧排成两行。
  - `leaveSection()` 后服务端记录 `player_logged_out`、`qqt_logout_success`。
- 没测：真窗口里的关闭按钮（只有单元测试）。

## 6. 从房间座位查看资料（2026-11-19 已做）

“查看”按钮（以及普通房间座位的右键）调用 `UserClickSeatInRoom(座位, 1)`。原版路径：`UserClickSeatInRoom`（Client `0x0053C340`）→ 分区接口（GUID `0x0079A908`，即 XSection `+0xBF30`）虚表 `+0x144` → `0x10022801` → 房间模块 `0x1000CF2B(座位, 1)`：座位上有人、并且身份 `+0x20 & 0x700` 为 0（或 `0x100`、`0x200` 都有）时，不发请求，直接用座位上的数据（号码、名字、PlayerID、性别、身份、0x2F 字节战绩、物品）拼一个与找人回包相同的 0x2463 字节结构，状态按房间模块 `+4` 取 2/3/1，分区和房间取本机的；先交给 `+0x60`（`0x1001D126`，就是找人回包的处理，宿主在 `friend.py` 的 `on_find`），再调 `+0xF0(号码)`（`0x10020917`，按是否好友、是否自己补充资料，没读完）。

`+0xF0` 就是原生函数 `FindFriend(号码)` 走的同一个函数（`slash-commands-re.md` 5.7）：先发 0x107 婚姻信息，再发 0x67 找人（目标是自己时在本地拼回包），所以资料框先显示座位数据，找人回包到达后再刷新一次。

宿主（`room.py` 的 `view_seat`、`seat_found_player`，`friend.py` 的 `show_found`）：

- [静态] 按上面的条件和字段拼 `FoundPlayer`：号码、名字、PlayerID、性别、头像号、身份、战绩、物品取座位记录；服务器号、小区号取本机分区，房间号、房间名取本房间；家族、荣誉保持 0（原版结构先 memset 为 0）。
- 交给找人回包的处理（与收到 0x67 回包同一函数：存详情、`go2playerInfo(号码, 0)`、穿上对方的装扮、`ui_getPlayerDetail()`），然后调用脚本命名空间里的 `FindFriend(号码)`（已被婚姻模块包了一层，先发 0x107）。
- [推断] 状态：房间模块 `+4`（`0x1000274D`）为 1 时取 2，为 2 时取 3，否则取 1。“查看”只在房间界面里能点，宿主固定取 2（在房间内），没有确认房间模块 `+4` 在房间里就是 1。
- 练习房间没有服务端：只显示座位数据，不调 `FindFriend`。
- 身份位 `0x100/0x200/0x400` 的含义没查。

验证：`tests/test_offline.py`（`SeatViewTest`：座位数据字段、随后 `FindFriend`、空座位和隐藏身份不显示、练习房间不发请求）、`tests/test_client.py`（找人回包路径）。本地测试服双客户端（1000011 建房，1000012 进房后右键房主座位）：资料框显示“测1”、在房间内、房间 1 seatview、战绩和人物，“加为好友”可点并弹出加好友框（为不改测试服数据库，没有点确定发送）。截图：`/private/tmp/claude-501/-Users-cduan-qqtang/2d415321-6cf4-4bec-92d7-63a0de7d5ee6/scratchpad/live/b_view2.png`、`/private/tmp/claude-501/-Users-cduan-qqtang/2d415321-6cf4-4bec-92d7-63a0de7d5ee6/scratchpad/live/b_addfriend.png`。

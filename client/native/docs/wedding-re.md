# 婚姻与婚礼（求婚、婚礼、离婚、爱情宣言）逆向：宿主原生函数与回调

目标：让 `client/native/qqtclient` 用 Python 提供原版婚姻系统需要的全部原生函数、服务器回包处理和房间场景绘制。原版界面脚本（`generated/scripts/res/uiRes/uiMain.py`、`uiRoom.py`、`uiSelRoom.py`）不改。

标注：
- **[证实]**：读过伪 C 或机器码，或者核对过 `QQTMsgData.bin` 的 schema 表、`data/object.pkg` 的索引。
- **[推断]**：有间接证据，没有逐条走通。
- **[未知]**：没有查清。

地址都是虚拟地址。`QQTSection.dll` 基址 `0x10000000`，`Client.exe` 基址 `0x400000`。

---

## 0. 结论摘要

1. 婚姻原生函数全部注册在 Client.exe 里（`natives.tsv`，注册处 `0x0048D873`～`0x0048D939`），另有房间模块的 `SetWeddingEffect`（`0x0053D789`）。发包类函数用 GUID `0x79A908` 取 section 接口（QQTSection 的 `XSection`，`S+0xBF30`，虚表 `0x10054928`），再调虚表 `+0x384`～`+0x3A0` [证实，见 §3]。
2. 协议是 0x104～0x10E 共 11 个命令，schema 0xBEA～0xBFC。Go 服务端 `internal/protocol/game/marriage.go` 的线上布局与 `QQTMsgData.bin` 的 schema **逐字段一致** [证实，§2]。
3. 原版**只在本地缓存两份婚姻资料**：自己的（Client.exe `0x7FF5E0`，QQTSection 另存一份在 `S+0x40`）和最近查询的别人的（`0x7FE078`）。`GetMyMarriageDetail` / `GetMarriageDetail` 只是返回缓存的副本 [证实]。只有 0x107 应答会更新缓存；自己的资料更新后执行脚本 `ui_UpdateMyMarriageInfo()`，别人的更新**不调任何脚本** [证实]。
4. 0x107 由客户端在**登录成功**、**FindFriend**（查看任何人的资料，先于 0x67 发出）、**离婚应答**、**婚礼成功**时自动发送 [证实]。**接受求婚之后不会刷新** [证实]。
5. 回包处理里，0x104/0x106/0x10A 都**不看 ResultID** [证实，机器码]：
   - 0x104/0x106 总是弹出 ResultWord，并且总是播 `sparkFail.wav`（不在对局中时）。
   - 0x10A（开始婚礼）总是执行 `SetWeddingStart(...)`，失败也一样，结果文字不显示。
6. 婚礼仪式：0x10C 成功时，新人（Uin 或 TargetUin 是自己）执行 `MyWeddingOver()` 弹祝贺框，并刷新 0x107；其余人执行 `WeddingOver(uin,target)`，播放约 14 s 的牧师问答，最后 `SetWeddingEffect('effect\\lihua2.eff')` 放烟花 [证实]。
7. 房间场景有两类特效 [证实]：
   - 婚礼特效（房间 `+0x318`）：`lihua2.eff` 固定画在 (250, 330)，循环播放，直到下一次 `SetWeddingEffect` 或离开房间。
   - 情侣特效（`+0x31C` 列表）：座位资料里的 SpouseUin 等于另一个座位的 UIN 时配成一对。两人 x、y 方向的距离**都小于 50** 时，在两人中点上方 70 像素处播放 `love.eff`，每 2000 ms 重新判断一次。
8. 进房应答（0x68，RESPONSE_ENTER_ROOM_OLD）中 **GameType==4 且 RoomFlag==2** 时，执行 `UpdateWeddingMode(WeddingModeID)` [证实]；跨小区邀请里 `roomflag==2` 时弹"邀请您观看婚礼" [证实]。
9. 和 Go 服务端对不上的主要地方（§8）：
   - 0x10A 只发给发起人，对方的 `sparkUin` 没被设置，`AnswerWedding` 可能发出 TargetUin=0，会被服务端静默丢弃；
   - 开始婚礼失败时仍回 0x10A，客户端照样锁住婚礼面板，也不提示；
   - 重新登录后脚本的 `beSparkedUin` 是 0，`StartWedding(0, mode)` 会被拒；
   - 接受求婚后双方的缓存不刷新。
10. 宿主现在缺：上述 8 个发包原生函数、`GetSeatIDByUin`、`SetWeddingEffect`、两份缓存、11 个回包处理、FindFriend 和登录时的 0x107、进房时的 `UpdateWeddingMode`、房间场景里的两种特效。清单见 §9。

---

## 1. 记号与相关对象

- `S`：QQTSection 的 `CMySection`。`XSection = S+0xBF30`（虚表 `0x10054928`）。XSection 方法里的 `param_1[-0x2f66]` 就是 `S+0x198`（本人 UIN，婚姻请求都用它），`param_1[-0x2f5c]` 是 `S+0x1C0`（序号），`+0x192` 是 ServerID [证实]。
- `S+0x40..+0x14D`：自己的 RESPONSE_MARRIAGE_INFO（0x10E 字节，含 ResultID）。其中：
  - `S+0x46` = MarriageUin，即配偶 UIN；SaveLoveWord 和 Divorce 用它当对方。
  - `S+0x136` = SpouseNickname，被动离婚提示用它 [证实]。
- `S+0xC544`：本人发起离婚的标志（Divorce 发包成功后置 1，收到 0x109 后清 0）[证实]。
- `S+0xC614`：界面状态，等于 3 时结果文字进留言队列，不弹框 [证实比较；"3 = 对局中"是推断]。
- `S+0x158`：留言（信封）队列。`FUN_10032238` 往里压一条类型 0 的文字留言（0x210 字节记录 `{u8 type=0, …, 0xFF, u32 len, char text[512]}`）。这和家族邀请用的 `FUN_10016084` 是同一个队列 [证实]，宿主对应 `friend.py` 的 `push(LeaveWord(text))`。
- 接口 `0x10054380` 的 `+0x10(expr)`：执行一段脚本字符串，例如 `ReceiveSpark(123)`、`PlaySound('…',1)` [证实用法]。
- 接口 `0x10054430`：
  - `+0x10(0, text, 3)` 是只有"关闭"按钮的系统消息框，宿主写成 `message_box(text, 3, 0)`（同 `kin-re.md`）[推断接口身份]；
  - `+0x6C` 取得界面对象，它的 `+0x3C(path, text)` 就是 `Win_SetText` [证实用法]。
  - 原版用三个隐藏的 TLabel 给脚本传字符串：`UI.TextBuffer`、`UI.TextBuffer2`、`UI.TextBuffer3`（`uiMain.py:1557`）。宿主应先 `Win_SetText` 写入，再调用脚本。

---

## 2. 线上协议 [证实：QQTMsgData schema 与 Go 编码器逐项核对]

整数大端。计数字符串只发实际长度（schema 的 dyn 字段）。定长字符串（昵称 20 字节）补 0。"→S" 表示客户端发出，"←" 表示服务器发来。

| 命令 | schema | 方向 | 字段 |
|---|---|---|---|
| 0x104 | 0xBEA REQUEST_SPARK | →S | `Uin u32, TargetUin u32, SparkWordLength u8, SparkWord[n]`（≤200） |
| 0x104 | 0xBEB RESPONSE_SPARK | ← | `ResultID u16, Uin u32, TargetUin u32, ResultWordLength u8, ResultWord[n]` |
| 0x105 | 0xBEC NOTIFY_SPARK | ← | `Uin u32`（求婚者）`, TargetUin u32, SparkerNickname[20], SparkWordLength u8, SparkWord[n]` |
| 0x106 | 0xBED REQUEST_ANSWER_SPARK | →S | `TargetUin u32`（**本人**）`, Uin u32`（求婚者）`, ResultID u16`（0 接受，1 拒绝） |
| 0x106 | 0xBEE RESPONSE_ANSWER_SPARK | ← | 与 0xBEB 相同 |
| 0x107 | 0xBEF REQUEST_MARRIAGE_INFO | →S | `Uin u32, TargetUin u32` |
| 0x107 | 0xBF0 RESPONSE_MARRIAGE_INFO | ← | `ResultID u16, TargetUin u32, MarriageUin u32, MarriageLoyalty u32, MarriageLevel u16, LevelValue u32, MarriageAge u32, LoveWordLength u16, LoveWord[n], Nickname[20], SpouseNickname[20], RingID u32` |
| 0x108 | 0xBF1 REQUEST_MODIFY_LOVEWORD | →S | `Uin u32, SpouseUin u32, LoveWordLength u16, LoveWord[n]` |
| 0x108 | 0xBF2 RESPONSE_MODIFY_LOVEWORD | ← | `Uin u32, TargetUin u32, ResultID u16` |
| 0x109 | 0xBF3 REQUEST_DIVORCE | →S | `Uin u32, TargetUin u32` |
| 0x109 | 0xBF4 RESPONSE_DIVORCE | ← | `Uin u32, TargetUin u32, ResultID u16` |
| 0x10A | 0xBF5 REQUEST_START_WEDDING | →S | `Uin u32, TargetUin u32, WeddingModeID u16` |
| 0x10A | 0xBF6 RESPONSE_START_WEDDING | ← | `Uin u32, TargetUin u32, NickName[20], TargetNickName[20], ResultID u16, ResultWordLength u8, ResultWord[n], WeddingModeID u16` |
| 0x10B | 0xBF7 NOTIFY_WEDDING_CONFIRM | ← | `Uin u32, TargetUin u32, NickName[20], TargetNickName[20], NotifyWordLength u8, NotifyWord[n]` |
| 0x10C | 0xBF8 REQUEST_ANSWER_WEDDING | →S | `Uin u32, TargetUin u32, ResultID u16` |
| 0x10C | 0xBF9 RESPONSE_ANSWER_WEDDING | ← | `Uin u32, TargetUin u32, NickName[20], TargetNickName[20], ResultID u16, ResultWordLength u8, ResultWord[n]` |
| 0x10D | 0xBFA REQUEST_CHANGE_WEDMODE | →S | `Uin u32, WeddingModeID u16` |
| 0x10D | 0xBFB RESPONSE_CHANGE_WEDMODE | ← | `Uin u32, ResultID u16, ResultWordLength u8, ResultWord[n]` |
| 0x10E | 0xBFC NOTIFY_CHANGE_WEDMODE | ← | `Uin u32, WeddingModeID u16` |

- 收包分派：`FUN_100189CE`（`0x105`→`0x1003292D`，`0x104`→`0x100327EF`，`0x106`→`0x10032CC7`，`0x107`→`0x10032439`，`0x108`→`0x10033000`，`0x109`→`0x10033305`，`0x10A`→`0x10033AAD`，`0x10B`→`0x10033C6A`，`0x10C`→`0x10033F77`，`0x10D`→`0x100336DD`，`0x10E`→`0x100337CD`）[证实]。
- 0x106、0x109、0x10C 没有单独的 NOTIFY schema，服务器用同一个命令号把应答发给对方或房间里的其他人。客户端的处理函数不区分是自己的应答还是转发 [证实：处理函数里没有"等待中"的判断]。
- 回包处理都**不检查是否有对应的请求**，所以服务器可以主动推送 0x107/0x10D/0x10E [证实]。
- 发包 header 是 `{S+0x198, 1, 4, schema, cmd, 0, seq}`，走 `net+0x18` [证实]。Go 回包和通知用 route 2（`localPlayerProfileRoute`），宿主用 `session.send` 的默认 route 即可。

进房相关字段 [证实]：
- RESPONSE_ENTER_ROOM_OLD（0x7DF）：在 `Players` 和 `PlayersAttach` 之后，依次是 `GameType u8, BackgroundID u32, PlayersSpouseUin[PlayerCount] u32, WeddingModeID u16, PatternPoints…`。
- NOTIFY_ENTER_ROOM_OLD（0x3F8）的末尾有 `SpouseUin u32`。
- RESPONSE_JOIN_ROOM_OLD（0x805）有 `PlayersSpouseUin`，但**没有** `WeddingModeID`。
- 宿主的 `net/room.py` 已经解析出 `spouse_uin`、`wedding_mode_id`。

---

## 3. 原生函数

Client.exe 的包装函数都是 `svc = QueryInterface(0x79A908); svc->vtbl[off](args)`。下表的 QQTSection 实现已经用虚表 `0x10054928` 核对过 [证实]。

| 原生函数 | Client.exe | 虚表 | QQTSection | 脚本调用 | 原版发送 / 返回 |
|---|---|---|---|---|---|
| `Spark(len, text, uin)` | `0x48EDE0` | +0x388 | `0x100325F9` | sparkDlg.send：`Spark(len(txt), txt, beSparkedUin)` | 0x104 `{S+0x198, uin, (u8)len, text[len]}`（结构 0xD1 字节，`strncpy`）|
| `AnswerSpark(result, uin)` | `0x48EE50` | +0x38C | `0x10032B04` | receiveSparkDlg：`AnswerSpark(0/1, sparkUin)` | 0x106 `{S+0x198, uin, (u16)result}` |
| `SaveLoveWord(len, text)` | `0x48EF20` | +0x390 | `0x10032E05` | modifyLoveWordDlg.save | 0x108 `{S+0x198, S+0x46（缓存的配偶）, (u16)len, text}` |
| `Divorce()` | `0x48EEC0` | +0x394 | `0x10033140` | divorceDlg.confirm | 0x109 `{S+0x198, S+0x46}`，发送成功后 `S+0xC544=1` |
| `RequestChangeWedMode(mode)` | `0x48EF90` | +0x398 | `0x10033522` | weddingDlg 左右按钮在点击后 5000 ms 的 OnTimer 里调用 | 0x10D `{S+0x198, (u16)mode}` |
| `StartWedding(uin, mode)` | `0x48EFF0` | +0x39C | `0x100338EA` | weddingDlg.startWedding：`StartWedding(beSparkedUin, weddingModeID)` | 0x10A `{S+0x198, uin, (u16)mode}`，**uin 原样使用脚本给的值** |
| `AnswerWedding(result, uin)` | `0x48F060` | +0x3A0 | `0x10033DB4` | marriageConfirmDlg：`uin==sparkUin` 时传 `beSparkedUin`，否则传 `sparkUin` | 0x10C `{S+0x198, uin, (u16)result}` |
| `GetSeatIDByUin(uin)` | `0x48F150` | +0x384 | `0x100321D4`→`0x1000A9D3` | `PlayerWord()` | 在房间 8 条记录里找 UIN，返回 `(TermAndSeat & 0xF) - 1`；找不到返回 0xFFFF（短整数 −1）。脚本用 `< 0` 判断 |
| `GetMyMarriageDetail()` | `0x48D110` | — | — | `ShowMarriageDlg`、`ui_getPlayerDetail` | 返回 `0x7FF5E0` 处 0x10E 字节的副本 |
| `GetMarriageDetail()` | `0x48D140` | — | — | 同上（`queryingUin != uin` 时） | 返回 `0x7FE078` 处的副本 |
| `UpdateMyMarriageInfo(ptr)` | `0x48F0D0` | — | — | 只由 QQTSection 以脚本串 `UpdateMyMarriageInfo(%d)`（参数是 `S+0x40` 的地址）调用 | 复制 0x10E 字节到 `0x7FF5E0`，再执行 `ui_UpdateMyMarriageInfo()` |
| `UpdateMarriageInfo(ptr)` | `0x48F120` | — | — | 只由 QQTSection 以 `UpdateMarriageInfo(%d)`（参数是栈上应答的地址）调用 | 复制到 `0x7FE078`，**不调脚本** |
| `SetWeddingEffect(file)` | `0x459690` | — | — | weddingOverDlg.confirm、`UI.room.tempTimer` | 见 §6.1 |

脚本里的长度参数是 `qqt_py2_len(txt)`。原版是 Python 2.3，`str` 就是 GBK 字节，所以这个长度是**字节数**。宿主收到 Python 3 字符串后，要先编码成 GBK、截到 200 字节，再用字节数，不要用脚本传来的 len [推断：原版把 len 截成 u8/u16 后直接 `strncpy`]。

### 3.1 RESPONSE_MARRIAGE_INFO 对象（boost.python 注册在 `0x0050B010`）[证实]

结构就是 schema 的紧排布局。脚本能读的属性和偏移：

| 属性 | 偏移 | 脚本用途 |
|---|---|---|
| `m_dwTargetUin` | 2 | — |
| `m_dwMarriageUin` | 6 | 配偶 UIN，0 表示未婚 |
| `m_dwMarriageLoyalty` | 10 | 亲密度 `closeNum` |
| `m_wMarriageLevel` | 14 | 等级图标（signed short） |
| `m_dwLevelValue` | 16 | 本级上限；进度条宽度 `int(135*Loyalty/LevelValue)`，**为 0 会除零** |
| `m_dwMarriageAge` | 20 | `"<n> 天"` |
| `m_nLoveWordLength` | 24 | — |
| `m_szLoveWord` | 26 | 爱情宣言 |
| `m_szSpouseNickname` | 246 | 配偶昵称 |
| `m_dwRingID` | 266 | >0 时画 `object/ui/marriage/ring/ring%d.img` |

- 结构里没有 ResultID 和 Nickname 属性。
- 缓存的初值全为 0 [证实：全局变量]。宿主的默认对象应给出上面全部属性，值为 0 或空串。`friend.py` 现在的桩只有两个属性。

---

## 4. 服务器回包：原版做什么，宿主调哪个脚本

"弹框"指 `message_box(text, 3, 0)`。"留言"指 `push(LeaveWord(text))`，宿主的 `push` 会调 `startGlint()`。"对局中"指 `S+0xC614 == 3`，宿主可以用 `current_screen == 'game'` 判断 [推断]。

### 0x104 求婚应答、0x106 回复求婚的应答（`0x100327EF`、`0x10032CC7`，两者逻辑相同）[证实]
- **不看 ResultID**，ResultWord 截到 ResultWordLength。
- 不在对局中：弹框 ResultWord，再执行 `PlaySound('sparkFail.wav',1)`。**成功时也播这个声音**。
- 对局中：把 ResultWord 压进留言队列，不播声音。
- 不改缓存，不发 0x107。

### 0x105 收到求婚（`0x1003292D`）[证实]
1. `UI.TextBuffer` = SparkWord，`UI.TextBuffer2` = SparkerNickname。
2. 执行 `ReceiveSpark(<Uin>)`。脚本会设置 `sparkUin`，显示 `receiveSparkDlg`，正文是 `name[uin]:\nword`。但脚本在商店的部分状态下、或网页打开时会直接 return，**这次求婚就丢了**（脚本行为）。
3. 执行 `PlaySound('sparkCome.wav',1)`。

### 0x107 婚姻资料（`0x10032439`）[证实]
- 不看 ResultID，把 LoveWord 在长度处截断。
- `TargetUin == 本人`：复制到 `S+0x40`，再执行 `UpdateMyMarriageInfo(&S+0x40)`。效果是自己的缓存更新，并执行 `ui_UpdateMyMarriageInfo()`；脚本只在 `queryingUin == uin` 且资料框可见时重画。
- 否则：执行 `UpdateMarriageInfo(&应答)`，只更新"别人"的缓存，不调脚本。

### 0x108 修改爱情宣言的应答（`0x10033000`）[证实]
- ResultID 0：弹框 `修改爱情宣言成功！`，再执行 `UpdateLoveword()`。这个脚本只是把编辑框的文字抄到 marriageInfoDlg。**缓存里的 LoveWord 不更新**，要等下一次 0x107。
- ResultID 0x2C（44）：弹框 `请不要在爱情宣言中使用非法或不文明词句，谢谢！`。
- 其他：弹框 `修改爱情宣言失败！`。

### 0x109 离婚（`0x10033305`）[证实]
- 本人发起（`S+0xC544 != 0`）：
  - 不在对局中：成功时弹框 `离婚成功`，播 `divorceOK.wav`，再发 0x107（查自己）；失败时弹框 `离婚失败`。
  - 对局中：同样的文字进留言队列，不播声音。成功时同样发 0x107。
- 被动方（`S+0xC544 == 0`）：只在 ResultID 0 时，把 `<缓存的 SpouseNickname>与你离婚了！` 压进留言队列（不论界面状态），再发 0x107（查自己）。失败时什么都不做。
- 最后 `S+0xC544 = 0`。

### 0x10A 开始婚礼的应答（`0x10033AAD`）[证实，机器码 `0x10033B79`～`0x10033BED`]
- `UI.TextBuffer` = NickName，`UI.TextBuffer2` = TargetNickName。
- 执行 `SetWeddingStart(<WeddingModeID>, <Uin>, <TargetUin>)`（格式 `%d,%u,%d`）。
- **ResultID 和 ResultWord 都不读**。脚本据此设置 `sparkUin=Uin`、`beSparkedUin=TargetUin` 和两人的昵称，`beWedding=1`（禁用"开始婚礼"和两个模式按钮），再执行 `SetWeddingRoom()`（换背景，设好牧师图片和 marriageConfirmDlg 的底图，播 `weddingWest.ogg`）。

### 0x10B 婚礼确认（`0x10033C6A`）[证实]
- `UI.TextBuffer` = NotifyWord，然后执行 `ReceiveMarriageConfirm()`。脚本关掉 SysMsgbox，把正文填进 `marriageConfirmDlg.marriageWord`，显示确认框。
- 确认框的底图是 `SetWeddingRoom()` 设的 `dlg_marriageConfirmWest/China.img`。脚本默认的 `object/ui/marriage/dlg_marriageConfirm.img` **在 object.pkg 里不存在** [证实]，所以收到 0x10B 的人之前必须执行过 SetWeddingRoom。

### 0x10C 婚礼结果（`0x10033F77`）[证实]
先把 `UI.TextBuffer` 设为 NickName，`UI.TextBuffer2` 设为 TargetNickName，然后：
- **ResultID == 0**：
  - 本人是 Uin 或 TargetUin（新人）：`UI.TextBuffer3` = ResultWord，执行 `MyWeddingOver()`（祝贺框，`weddingOver.wav`），再发 0x107（查自己）。
  - 否则（宾客）：执行 `WeddingOver(<Uin>, <TargetUin>)`，即牧师问答，见 §5.6。
- **ResultID != 0**：
  - 新人：弹框 ResultWord，播 `sparkFail.wav`，执行 `RestartWedding()`（`beWedding=0`，关确认框，`SetWeddingRoom()`）。
  - 宾客：什么都不做。

### 0x10D 改婚礼模式的应答（`0x100336DD`）[证实]
- 只在 ResultID != 0 时弹框 ResultWord。成功时不做任何事，本地在点击时就已经换了模式。

### 0x10E 婚礼模式通知（`0x100337CD`）[证实]
- 执行 `UpdateWeddingMode(<WeddingModeID>)`。脚本设 `beWedding=0`，把模式限制在 0..1，再执行 `SetWeddingRoom()`。

### 进房应答 0x68（RESPONSE_ENTER_ROOM_OLD，`FUN_1001E0DC`，`0x1001E4BF`～`0x1001E543`）[证实]
- `GameType == 4`（聊天房间）并且 `RoomFlag == 2` 时，执行 `UpdateWeddingMode(<WeddingModeID>)`。
- 这一步排在房间界面初始化之后。房间 `OnInit` 末尾会调 `SetRoomChat`，而它会 `weddingModeID=0` 并隐藏 weddingDlg [推断：0x84 调用会同步切换到房间界面]。宿主要在房间界面建好之后再执行 UpdateWeddingMode。
- 座位资料（GetSeatInfo 的 0x23E8 字节）`+0x23E4` 是 SpouseUin：别人的来自 `PlayersSpouseUin[i]`，自己的先填 `S+0x46` [证实]。脚本不读它，只有情侣特效用（§6.2）。
- `NOTIFY_ENTER_ROOM_OLD.SpouseUin` 同样写进新座位的记录 [推断]。

### 何时发 0x107 [证实]
| 时机 | 位置 | Target |
|---|---|---|
| 登录成功 | 登录应答处理 `FUN_1001B2EA`，`0x1001B49D`（紧接 `S+0xC614 = 1`） | 自己 |
| `FindFriend(uin)`，包括 `/WHO`、`/whoami` | `FUN_10020917` | 目标。**在 0x67 之前发**；查自己时在本地拼 0x67 应答之前发 |
| 离婚成功（两方） | `0x10033489` | 自己 |
| 婚礼成功（新人） | `0x10033F77` | 自己 |

查别人：0x107 先发，所以应答通常比 0x67 先到；随后 `ui_getPlayerDetail` 读到的就是新数据。查自己：本地应答先触发 `ui_getPlayerDetail`（读到旧缓存），之后 0x107 应答经 `ui_UpdateMyMarriageInfo()` 再刷新一次。这就是那个回调存在的原因 [推断]。

---

## 5. 完整流程

### 5.1 查看资料与求婚入口（`uiSelRoom.py:1288-1335`）[证实，脚本]
`ui_getPlayerDetail()` 用 `myMarriageInfo = GetMyMarriageDetail()`，并且按 `queryingUin == uin` 选 `GetMyMarriageDetail()` 或 `GetMarriageDetail()`：
- 对方已婚（`m_dwMarriageUin != 0`）：显示配偶名，显示"查看婚姻"（`lookupMarriage`），隐藏"求婚"（`spark`）。
- 查的是自己，或者同性，或者我已婚：显示"求婚"按钮但禁用。
- 其他情况：启用"求婚"按钮。

点"求婚"：`beSparkedUin = 对方UIN`，打开 `sparkDlg`（MULTIEDIT，maxchar 198），点"发送"调 `Spark(...)`。

### 5.2 回复求婚
对方收到 0x105 后弹 receiveSparkDlg，点"接受"或"拒绝"调 `AnswerSpark(0/1, sparkUin)`。两边都收到 0x106 并弹出 ResultWord。原版之后**不刷新缓存** [证实]。

### 5.3 婚姻资料框 `ShowMarriageDlg()`（`uiMain.py:1150-1313`）[证实，脚本]
- 自己的资料可以"离婚""修改"，别人的这两个按钮禁用。
- 等级图标：等级 ≤1 一颗心，2..5 为 2..5 颗心，6..9 为 1..4 颗钻石，10..13 为 1..4 顶皇冠，>13 为 5 顶皇冠。等级 2 那行写成了 `'.levell'`，是原版笔误。
- 亲密度条 `Win_SetDrawTexRect(closeLine, 0,0, int(135*Loyalty/LevelValue), 17)`。
- 戒指：RingID>0 画戒指。否则显示 noRing 按钮（点了去商店）。只有查看自己或配偶、并且当前界面不是 game/room 时，这个按钮才可用。

### 5.4 爱情宣言与离婚
- 修改：`SaveLoveWord(len, txt)` → 0x108 → 成功时执行 `UpdateLoveword()`。
- 离婚：divorceDlg.confirm → `Divorce()` → 0x109。两方都会得到提示（§4），并重查 0x107。

### 5.5 婚礼房间：模式与开始
- 面板 `UI.room.weddingDlg`（initlayer 12000，位置 (512,1,293,175)），和聊天背景面板 `selChatBg` 在同一个位置。`SetWeddingRoom()` 会隐藏 selChatBg。
- 两种模式（`uiMain.py:1463`）：
  - 0：西式 `dlg_westBG/Icon/Priest/PriestS`、`dlg_marriageConfirmWest`；
  - 1：中式 `dlg_chinaBG/…`、`dlg_marriageConfirmChina`。
  - 音乐都是 `weddingWest.ogg`。
  - `SetWeddingRoom` 把 `UI.room.chatRoomBg` **直接换成** `dlg_*BG.img`，不使用 chat0..9。
- 只有房主（`isMaster`）并且 `beWedding==0` 时，三个按钮可用。左右键先在本地换模式，再等 5 s 无操作后发 `RequestChangeWedMode`。其他人收到 0x10E 后执行 `UpdateWeddingMode`。
- 面板出现的途径：进房时 `RoomFlag==2`；收到 0x10E；收到 0x10A；`RestartWedding`。
- 原版房主新建房间时怎么进入婚礼模式：**[未知]**。建房走的是另一条路径，不经过 0x68 的这段检查。
- 开始：`StartWedding(beSparkedUin, weddingModeID)`。`beSparkedUin` 只在三处被设置：点"求婚"按钮、`SetWeddingStart`、`WeddingOver`。所以**重新登录后是 0**。原版服务器是否忽略 TargetUin **[未知]**。

### 5.6 确认与仪式
1. 服务器回 0x10A：`SetWeddingStart` 锁定面板。
2. 服务器发 0x10B，弹 marriageConfirmDlg。点"我愿意"或"拒绝"调 `AnswerWedding(0/1, …)`：自己是 `sparkUin`（发起人）时传 `beSparkedUin`，否则传 `sparkUin`。这说明脚本预期发起人**也可能**收到 0x10B，并且**收到 0x10B 的人事先已经执行过 SetWeddingStart** [推断]。原版服务器把 0x10A/0x10B 发给哪些人 **[未知]**。
3. 0x10C 成功：
   - **新人**：`MyWeddingOver()`，祝贺框的文字是 ResultWord。点"确定"后执行 `SetWeddingEffect(weddingEffectFile)` 放烟花。新人**看不到**牧师问答。
   - **宾客**：执行 `WeddingOver(uin, target)`，时间线如下（每步间隔 2000 ms，所有 `Win_SetValue(…,41/901)` 都是淡入淡出）：
     1. 显示 `priestSmall`；`priestword` 显示 `<sparkNick>:\n你愿意与<beSparkedNick>结为夫妻吗？`；
     2. priestword 淡出，执行 `PlayerWord()`：`GetSeatIDByUin(sparkUin)` 这个座位的气泡显示 `我愿意^^`（`ui_playerChat`），下一句换成对方；
     3. tempTimer：显示第二个问题；
     4. 第二人的气泡显示"我愿意^^"，下一句是 `恭喜你们已经成为QQ堂合法夫妻!`；
     5. 显示恭喜；
     6. `priestwordNum` 变为 4；
     7. tempTimer：priestSmall 淡出，执行 `SetWeddingEffect('effect\\lihua2.eff')`。

     全程大约 14 s。宿主的 `Win_Timer` 对隐藏控件（tempTimer 的 visible=0）也会触发，这一点已经满足。
4. 0x10C 失败：新人弹框，执行 `RestartWedding()`。

### 5.7 观礼邀请 [证实]
房间里的邀请会通过 0x6C 发出 `in_..._roomflag(%d)_...`。收到方解析出 `roomflag == 2` 时，弹框文字是 `玩家%s[%u]邀请您观看婚礼，您是否同意？`（`0x100620D8`，比较在 `0x10027685`），否则是普通的"邀请您一起游戏"。其余流程见 `slash-commands-re.md` §8。

---

## 6. 房间场景绘制（`UI.room.demo` 的 `SC_room_drawPlayer`）

### 6.1 婚礼特效（房间 `+0x318`）[证实]
- `SetWeddingEffect(file)`（`0x00459690`）：
  - 当前场景能动态转换成房间场景时，执行 `0x00459110`：
    1. 释放旧特效；
    2. `+0x318 = 0x00583FF0(file, 0, 0)`，即载入 .eff；
    3. 载入成功就 `0x00582BD0(250, 330)` 设定位置。
  - **不管在哪个场景**都执行 `PlaySound('loveFire.wav',1)`。
- 每帧更新（`0x00457740`）：在移动包之后执行 `0x00582D20(dt)`。绘制（`0x004575D0`）放在**最后**，在情侣特效之后，也就是在全部头像和名牌之上。
- `lihua2.eff` 的内容：播放模式 1（循环）；轨道 magic187（80 ms/帧，长 2240 ms）和 magic186（100 ms/帧，长 2800 ms）；偏移 (0,0)。所以烟花会一直循环，直到下一次 SetWeddingEffect 替换，或者房间场景销毁 [推断：场景析构时释放]。
- 宿主可以复用 `battle/effect.py` 的 `EffectPlayer`，在 (250, 330) 处按累计时间绘制。

### 6.2 情侣特效（房间 `+0x31C`，`std::vector<0x18 字节对象*>`）[证实]
配对（`room_changePlayer` → `0x00458240`，机器码 `0x0045841E`～`0x00458563`）：
- 某座位 s **第一次创建头像**时（之前没有头像，并且 role ≠ −1），逐个检查已有头像的座位 i。如果 `GetSeatInfo(s).SpouseUin == GetSeatInfo(i).player.uin`，就新建一对 `{+0 头像数组(房间+0x230), +4 = s, +8 = i, +0xC = 载入 effect\love.eff, +0x10 active=0, +0x14 计时=0}`（`0x00440CD0`），加入列表。
- 只检查新座位的 SpouseUin，所以后进房的人决定是否成对。双方的 SpouseUin 互相指向时，结果是一样的。
- 头像只是因为换角色或换队伍色而重建时，配对不变。
- `role == -1`（删除头像）时，删掉列表里**第一个**包含该座位的对象（`0x00440F90` 判断）。

每帧更新（`0x00440D70(dt)`，对列表逐个执行）：
```
if timer == 0 or timer >= 2000:
    if effect and abs(A.x - B.x) < 50 and abs(A.y - B.y) < 50:     # 两个方向分别比较，不是欧氏距离
        active = 1; timer = 0
        y = trunc((A.y + B.y) / 2) - 70                           # cdq/sar：向零取整
        x = trunc((A.x + B.x) / 2)
        effect.set_pos(x, y); effect.time = 0                     # 0x00582BD0、0x00582CF0
    else:
        active = 0
if effect and active:
    timer += dt; effect.update(dt)                                # 0x00582D20
```
- 绘制（`0x00440F50`）：`active` 时画特效。画在全部头像之后、婚礼特效之前。
- A、B 是两个头像的位置 `+0x18/+0x1C`，就是 `roomwalk` 里头像的 x、y。
- `love.eff` 的内容：magic188，120 ms/帧，循环；关键帧时长 1080 ms；偏移 (−50, −90)。所以每 2 s 在新的中点重新开始一次。2 s 内位置固定，不跟随走动。

### 6.3 其他
房间 `+4` 处的 8 个对象，以及构造函数里角色 9、10、11、22 的 4 个额外头像，是否与婚礼有关 **[未知]**（`chatroom-move-re.md` §8）。

---

## 7. 资源与道具

- object.pkg 里有 [证实]：
  - `object/ui/marriage/` 下所有对话框和按钮图；
  - 牧师 `priest/dlg_{west,china}Priest{,S}.img`；
  - 背景 `bg/dlg_{west,china}{BG,Icon}.img`；
  - 等级图标 `marriageLevel/dlg_{heart,diamond,coronet}.img`；
  - 戒指 `ring/ring9021.img`～`ring9026.img`。
  - **没有** `dlg_marriageConfirm.img`。
- 声音 `sound/` [证实]：`sparkCome.wav`、`sparkFail.wav`、`divorceOK.wav`、`weddingOver.wav`、`loveFire.wav`；音乐 `music/weddingWest.ogg`。
- 特效 `effect/lihua2.eff`、`effect/love.eff` [证实]。
- 道具：Go 规定求婚必须持有 9020"水晶之恋"（不消耗），戒指 RingID 取双方背包里 9021～9026 中的最高者（`persistence/marriage.go`）。客户端只把 RingID 原样拼进图片名 [证实]。原版服务器的道具规则 **[未知]**。

---

## 8. 与 Go 服务端对照

Go 的命令表（`commands.go:130-140`）、编解码（`marriage.go`）和分派（`probe/marriage_dispatch.go`）覆盖了全部 11 个命令，线上布局全部正确。下面是行为差异：

| # | 问题 | 客户端事实 | 建议 |
|---|---|---|---|
| 1 | 0x10A 只回给发起人；0x10B 只发给对方 | 对方从没执行 `SetWeddingStart`，所以 `sparkUin` 是旧值：可能是本次登录里向他求婚的人，也可能是 0。确认框的底图也没设置（图片不存在）。`AnswerWedding(x, sparkUin)` 可能带 TargetUin=0，`DecodeLocalAnswerWeddingRequest` 拒绝后**不回任何包**，发起人的面板一直锁住 [证实客户端；服务端行为见代码] | 成功时把 0x10A 发给**房间所有人**（至少包括对方）[推断原版做法]，再发 0x10B |
| 2 | 开始婚礼失败时仍回 0x10A（ResultID=2） | 客户端不看 ResultID，照样 `SetWeddingStart`（`beWedding=1`，三个按钮禁用，放音乐），失败原因不显示 [证实] | 失败时不发 0x10A，改回一个 ResultID≠0 的 0x10D 应答，客户端只会弹 ResultWord。或者发给发起人一个失败的 0x10C，客户端会弹框并执行 `RestartWedding()` |
| 3 | `StartWedding` 的 TargetUin 来自脚本全局 `beSparkedUin`，重新登录后是 0 | 解码器拒绝 TargetUin==0，不回包，按钮看起来没有反应 | 服务端遇到 TargetUin 为 0 或不是配偶时，改用当前配偶 [推断原版也不依赖它]。宿主不要改脚本 |
| 4 | 婚姻关系在接受求婚时就建立（Go） | 原版接受求婚后不发 0x107，双方的 `GetMyMarriageDetail` 一直是旧值，直到重新登录、查看自己的资料或婚礼成功。期间"求婚"按钮的状态、`SaveLoveWord`/`Divorce` 用的 `S+0x46` 都不对 | 接受成功后，服务端给双方主动推一个 0x107 应答（TargetUin=各自）。客户端处理函数不检查是否有对应请求 [证实]，所以可以直接收 |
| 5 | 婚礼确认 | 脚本支持发起人也收到 0x10B 并回答；Go 的 `AnswerWedding` 只接受被邀请方 | 维持现状即可。原版是否让双方都确认 **[未知]** |
| 6 | 修改爱情宣言的结果码 | 客户端识别 44（不文明词句）；Go 只发 0 或 2 | 可选：脏词时回 44 |
| 7 | 进房的 RoomFlag | 客户端在 `GameType==4 && RoomFlag==2` 时执行 `UpdateWeddingMode`。Go 的 RoomFlag 是房间属性位（1 密码、2 自由规则），所以**无密码的"自由规则"聊天房**每次进入都会弹出婚礼面板。邀请文字同理 | 要明确 RoomFlag==2 在聊天房里的含义；如果不打算让它表示婚礼房，就别在聊天房里发 2 |
| 8 | 背景 5/7 → 0x10E | 这是 Go 自己的约定（选背景时推送婚礼模式），客户端能正常处理（`UpdateWeddingMode`）。原版并不需要 chat5/chat7 | 无需改 |
| 9 | JOIN_ROOM（0x805）没有 WeddingModeID | 客户端只在 0x68 上检查婚礼模式 [证实：`UpdateWeddingMode` 只有两处引用] | 走 0x805 进房的人要另外推一个 0x10E |
| 10 | 求婚成功的 ResultWord | 客户端总是弹框并播 `sparkFail.wav` [证实] | Go 已经填了"求婚请求已发送"，不必改 |
| 11 | 解码失败时 `rejectedMarriage` 不回包 | 客户端没有超时，界面停在原状 | 对客户端可能发出的错误参数（TargetUin=0）给出可见的失败回包 |

---

## 9. 宿主实现清单

新建 `qqtclient/marriage.py`（模式同 `kin.py`），在 friend.py 之后注册，并替换 friend.py 里两个桩：

1. **状态**：
   - `my_info` 和 `other_info`：各 270 字节 RESPONSE_MARRIAGE_INFO 解析出的字段；
   - `divorcing`（对应 `S+0xC544`）。
   - 在 `session.on_entered`、`on_disconnected` 时清零。
2. **原生函数**（§3）：
   - `Spark`、`AnswerSpark`、`SaveLoveWord`、`Divorce`、`RequestChangeWedMode`、`StartWedding`、`AnswerWedding` 按 §2 打包后 `session.send`。字符串先编码 GBK，长度取字节数，最多 200。
   - `GetMyMarriageDetail` / `GetMarriageDetail` 返回带 §3.1 全部属性的 SimpleNamespace 副本。
   - `GetSeatIDByUin(uin)` 在房间成员里找 UIN，返回座位号 0..7，找不到返回 −1。
   - `UpdateMyMarriageInfo` / `UpdateMarriageInfo` 可以注册，也可以只在内部使用，脚本不调用它们。
   - `SetWeddingEffect(file)`：交给 roomwalk 的场景，并 `PlaySound('loveFire.wav',1)`。
3. **FindFriend**（friend.py）：在发 0x67（或查自己时在本地拼应答）之前，先发 0x107 `{my, target}`。登录完成后发 0x107 `{my, my}`。
4. **回包处理**：按 §4 处理 0x104～0x10E。TextBuffer 用 `Win_SetText` 写，脚本串改成直接调用脚本函数；"对局中"的消息改压留言队列。
5. **进房**：解析 0x68 后，在房间界面 OnInit（SetRoomChat）之后，如果 `game_type == 4 and room_flag == 2`，调用 `UpdateWeddingMode(wedding_mode_id)`。座位资料保存 `spouse_uin`；自己的可以用应答里的值，或者 `my_info.marriage_uin`。
6. **roomwalk**：
   - 增加 `wedding_effect`（文件名和累计时间），在 (250,330) 最后绘制；
   - 增加 `couples` 列表，在 `room_changePlayer` 新建头像或删除头像时维护，按 §6.2 更新和绘制 `love.eff`。
   - 离开房间时清空两者。

---

## 10. 未知与待验证

- 原版服务器把 0x10A、0x10B 发给哪些人；原版是否要求双方都确认婚礼。
- 原版"接受求婚"是订婚还是直接结婚。客户端只在婚礼成功后才刷新 0x107，这一点倾向于"婚礼成功才算结婚"，属于推断。
- 原版房主新建聊天房时怎样进入婚礼模式（建房路径没有读）；RoomFlag==2 在原版聊天房里的准确含义。
- `S+0xC614 == 3` 是否就是"对局中"。
- 婚礼特效在离开房间时是否释放（没有读场景析构函数）。
- 房间 `+4` 处 8 个对象和 4 个额外头像的用途。

---

## 11. 实现状态（2026-11-03）

- 客户端：`qqtclient/net/marriage.py`（11 条消息）、`qqtclient/marriage.py`（两份缓存、§3 的原生函数、§4 的回包处理、登录和 FindFriend 前的 0x107）；`room.py` 的 `SetWeddingEffect`、`GetSeatIDByUin` 用的座位表、进婚礼房执行 `UpdateWeddingMode`、新头像配对情侣；`roomwalk.py` 的婚礼特效和 `Couple`（§6）。friend.py 的两个婚姻桩已删除。
- 服务端（§8 的处理）：
  - #1：开始婚礼成功时，0x10A 先发给房间里其他人，再给配偶发 0x10B。
  - #2：开始婚礼失败时回 0x10D 失败（`BuildLocalStartWeddingFailure`），客户端只弹原因。
  - #3：0x10A、0x10C 的 TargetUin 为 0 时用配偶。
  - #4：接受求婚后给双方推 0x107。
  - #7：聊天房的 RoomFlag 第 2 位只在婚礼房（背景 5/7、或婚礼进行中/已举行）时置位。
  - #9：进入婚礼房（0x68 和 0x805）后推一条 0x10E。
  - 选背景 5/7 时也写进房间状态的婚礼模式。
  - #5、#6、#10、#11 没改。
- 实测：`/Users/cduan/qqtang-test/run-wedding.sh`。
- 没有和原版画面对照：情侣爱心和烟花的位置只来自静态分析；牧师问答先问谁取决于服务端 0x10C 的 Uin。

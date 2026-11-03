# 家族（kin）逆向：宿主原生函数与回调

目标：让 `client/native/qqtclient` 用 Python 实现原版家族功能需要的全部宿主原生函数和回调。原版界面脚本（`generated/scripts/res/uiRes/uiMain.py`、`uiSelRoom.py`、`uiRoom.py`）不改。

标注：
- **[证实]**：读过伪 C 或机器码，或者核对过 `QQTMsgData.bin` 的 schema 表。
- **[推断]**：有间接证据，没有逐条走通。
- **[未知]**：没有查。

地址都是虚拟地址。`QQTSection.dll` 的基址是 `0x10000000`，`Client.exe` 的基址是 `0x400000`。

---

## 0. 结论摘要

1. 脚本里的家族原生函数都注册在 Client.exe 里（`natives.tsv`）。它们取服务对象 `0x79A908`，再用 GUID `0x79A918`（QQTSection 里是 `0x100542E0`）QueryInterface 出家族接口，最后调用虚表方法 [证实]。
   - 家族接口是 `CMySection` 的 `XSection2`，在 `S+0xBF34`，虚表 `0x100548E8`。接口表在 `0x10054610`：`{0x100542D0,+0xBF30}`、`{0x100542E0,+0xBF34}`、`{0x100542F0,+0xBF38}`。
   - 这些方法几乎都转发给家族处理对象 `CKinHandler`（下文记作 **K** = `*(S+0x30)`）[证实]。
2. 原版没有为家族界面单独请求数据。各原生函数**只读 K 里的本地缓存**；网络应答更新缓存后，再执行一段脚本字符串刷新界面（`ui_updateMemberList()`、`updatePlayer()`、`updateKinManagerDlg()`、`ShowkinInfoDlg()`、`ShowkininviteDlg()`、`ui_updateTopkinList()`）[证实]。
3. `DoKinTask(p1, p2, len, n, mode)` 是一个 switch（`0x1002CAC9`，跳转表 `0x1002CF04`）。mode 0～15 中：
   - 8 是空操作，10 不存在，12 是家族聊天（脚本不直接用）。
   - **mode 11 的末尾缺 `break`，会落到 mode 12**：回复邀请之后，还会发一条正文为 `Y`/`N` 的家族聊天 [证实，机器码 `0x1002CDC5`→`0x1002CDCA`]。
4. 和 Go 服务端对不上的地方，详见 §8：
   - 排行榜 40 条记录的分组顺序不同；
   - `GroupMemberNum=0` 会让原版除以零；
   - 少了两条 `SameGroupKinInfo` 记录；
   - 回复邀请时，第一个字段原版填的是**族长** UIN，不是邀请人；
   - 退出家族后没有事件通知；
   - "取消解散"会被当成解散。

---

## 1. 原生函数到实现的对照 [证实]

Client.exe 的包装函数一律是：先取 `[0x7FAF04]+0x14(GUID 0x79A908)`，再 QI 出 `0x79A918`，然后调用虚表槽位。

| 原生函数 | Client.exe 实现 | 家族接口槽位 | QQTSection 实现 |
| --- | --- | --- | --- |
| `DoKinTask(p1,p2,len,n,mode)` | `0x54E190` | `+0x0C` | `0x1002CAC9` |
| （网络消息入口，不是原生函数） | — | `+0x10` | `0x10013069`：按命令号分发应答，见 §5 |
| `GetKinParam(i)` | `0x54E240` | `+0x14` | `0x1001379A` |
| `GetInviteKinInfo(i)` | `0x54EE50` | `+0x18` | `0x1001574A` |
| `GetKinPositionByUin(uin)` | `0x48CE40` | `+0x1C` | `0x100164A3` |
| `GetKinPosition(i)` | `0x48CCF0` | `+0x20` | `0x10016650` |
| `GetKinMemberHonor(uin,mode)` | `0x48CF90` | `+0x24` | `0x1002D262`，按 mode 分到 `0x10016680`/`0x100165A3`/`0x10016E8E`/`0x10017C4D` |
| `GetPlayerKinInfoByUin(uin)` | `0x53C210` | `+0x28` | `0x1003BB49`（查大厅玩家表，**脚本不用**） |
| `GetPlayerInfoByUin(uin)` | `0x54EF60` | `+0x2C` | `0x1003BBFB`（查大厅玩家表，宿主已有） |
| `fetch_topkininfolist(tel,rep,i)` | `0x54FF10` | `+0x30` | `0x100179C2` |

和家族有关、但走主接口 `XSection`（虚表 `0x10054928`）的原生函数：

| 原生函数 | 槽位 | 家族相关分支 |
| --- | --- | --- |
| `GetPlayerCnt(mode)` | `+0xC0` → `0x100200E6` | mode 2 → `0x1001412C`；mode 3 → `0x1001566A` |
| `fetch_PlayerInfoInPlayerList(i,mode)` | `+0xCC` → `0x1001EFA4` | mode 2 → `0x10013D68`；mode 3 → `0x10016108` |
| `getExtItem(i,j,mode)` | `+0x260` → `0x1002AE41` | mode 2 → `0x100171A5` |
| `GetWisperName(1)` | `+0x268` → `0x1002B0F0` | 返回 `S+0xC5E0`，即家族聊天的名字 |
| `SectionChat(0,'/kinchat on|off')` | 斜杠命令 0x1B，见 `docs/slash-commands-re.md` §5.11 | |

---

## 2. K（CKinHandler）的缓存布局 [证实，除非另注]

建议 Python 端照这个模型存状态。

| 偏移 | 含义 | 何时写入 |
| --- | --- | --- |
| `+0x04` | "解散待生效"标志 | 0xB6 应答里 `DismissTime≠0` 时置 1；0xC2 应答成功：Value=1 置 1，Value=2 置 0；事件 5 清 0 |
| `+0x1C` | 本人 UIN | |
| `+0x24` | 成员缓存条数 | |
| `+0x28` | 人数上限 = `KinBase.Status >> 20` | 0xB6 应答 |
| `+0x2C` | **本人家族号**，即 `GetKinParam(0)` | 登录：`RESPONSE_LOGIN.GameInfo.OrgID`（`0x1001B2EA` 调 `0x1001378E(+0x3F)`）；0xB5 成功；0xBD 成功；事件 5、事件 8（自己被移出）清 0 |
| `+0x30 + i*0x40` | 成员缓存。每条：`+0` 在大厅标志；`+4..+0x3F` 是 `KinMemInfoOld`（0x32 字节）加 `Honor`、`ActivePoint` | 0xB7 应答，以及增删事件 |
| `+0x324C` | **本家族 KINBASE**（0x2F6 字节），即 `GetInviteKinInfo(1)` | |
| `+0x3542` | **"别的家族" KINBASE**，即 `GetInviteKinInfo(0)` | 查看别家族时的 0xB6 应答；收到邀请 0xBC |
| `+0x383C` | 在线成员数，即 `GetPlayerCnt(3)` | |
| `+0x3840` | 职位表：`+4` 条数；第 k 条在 `+0x3848+k*0x10`，`+0` 是 grade（int），`+4` 是名字（≤10 字节） | 0xB6、0xC6 应答。从 Title 对象解析，解析函数 `0x100120C9` |
| `+0x3990` / `+0x3994` | 缓存的 BaseUpdate / ListUpdate | 0xB6 |
| `+0x3998` | 上次成员表应答的 ListUpdate | 0xB7 |
| `+0x399C` / `+0x39B0` / `+0x39B4` / `+0x39B8` / `+0x39BA` | 排行榜：已收到标志、42 条记录、OrderTime、GroupNum、GroupMemberNum | 0xCB |
| `+0x39A0` / `+0x39A4` | 置位时，下一次 FetchKinBase 的 Time 字段填 0 | 0xB5、0xBA、0xBD 置位；0xB7 清零 |
| `+0x39AC` | "已处于离开状态"标志 | 0xC1 应答成功时置 1 |

KINBASE 的偏移（schema `KinBase`；Client.exe `0x4D0380` 给 boost.python 注册的字段名）[证实]：

| 偏移 | 字段 |
| --- | --- |
| `+0x00` | `m_dwKinIndex` |
| `+0x04` | `m_dwUin`（族长） |
| `+0x08` | `m_dwTime` |
| `+0x0C` | `m_dwDismissTime` |
| `+0x10` | `m_dwStatus`（高 12 位是人数上限） |
| `+0x14` | `m_iGrade` |
| `+0x18` | `m_stKinFlagID`（`.m_dwIndex`，`.m_iFlagID` 在 `+4`） |
| `+0x20` | `m_wMemberNum` |
| `+0x22` | `m_wContentLen` |
| `+0x24` | `m_wTitleLen` |
| `+0x26` | `m_szName[17]`（schema 里没有长度字段，定长） |
| `+0x37` | `m_szContent[257]`（宣言） |
| `+0x138` | `m_szTitle[200]`（职位对象） |
| `+0x200` | `m_iKinSection`（1 电信，2 网通） |
| `+0x204` / `+0x208` | BaseUpdate / ListUpdate（**没有导出给脚本**） |
| `+0x20C` | `m_wNotificationLen` |
| `+0x20E` | `m_szNotification[200]`（公告） |
| `+0x2D6` / `+0x2DA` | Honor / ActivePoint（未导出） |
| `+0x2DE` | `m_iLastHonor` |
| `+0x2E2` | LastActivePoint |
| `+0x2E6` / `+0x2EA` | LastHonorOrder / LastActivePointOrder |
| `+0x2EE` / `+0x2F2` | BeforeHonorOrder / BeforeActivePointOrder |

- 脚本只读到上表中带 `m_` 名字的字段。
- `m_szName` 是 17 字节、不保证有结尾 0。名字正好 17 字节时，原版转成 Python 字符串很可能会连上后面的 `m_szContent` [推断]。

其他约定：

- **grade**：成员状态字 `Status` 的 bit24～27，`(Status>>24)&0xF`。取值 12 族长、10、8、6、4 [证实，`0x100165A3`；默认名字取自 Go 的 `defaultKinAuthorityTitles`]。
- **在线**：`Status & 1`。在线成员数和在线优先的排序都看这一位 [证实，`0x10017041`]。

---

## 3. DoKinTask(p1, p2, len, n, mode)

公共规则 [证实]：

- **Time 字段**：调用 `0x1004AEB9`，由 NetCenter 返回 [推断：服务器时间]。
- **发包**：`net+0x18(payload, size, header)`。header 里有 `{UIN, 1, 10, schema, cmd, 0, seq, key[12]}`。
- **线上编码**：按 QQTMsgData schema 序列化，大端；计数字符串只发实际长度（与 `kin.go` 一致）。
- **"两处显示"**：先往"当前频道"打一行，再往频道 0x1F（家族聊天面板）打同一行，颜色都是 `0x18FF00`。频道含义见 §6。
- **长度**：脚本传的 `len` 是 `qqt_py2_len`，即 GBK 字节数。

### 3.1 各任务号

| mode | 脚本调用处 | 参数 | 原版的处理 | 发出的包 |
| --- | --- | --- | --- | --- |
| **0** 创建 | `uiMain.py:3914` createkinDlg.confirm：`DoKinTask(name, text, len(text), kinSection, 0)`。name 经 `filterChatMsg`，并去掉换行和空格；kinSection 是 1 电信或 2 网通 | p1=名字，p2=宣言，n=分区 | `0x10012E90`：名字为空时只在当前频道显示`不能创建无名家族`；宣言长度为 0 时只在当前频道显示`家族宣言不能为空`。这两种情况都不发包 | **0xB5** / 0x1771，294 字节：`Uin, Time, Status=0x10, Reserve=0, ContentLen, Name[17]`（截到 17 字节，不补 0）`, Content[≤257], KinSection=n` [证实] |
| **1** 取家族信息 | `uiSelRoom.py:2693`、`uiMain.py:2829`（allKinMemberTab）、`uiMain.py:2854`（kinTab）：n=GetKinParam(0)。`uiMain.py:1918`（查看）：n=lookupKinID | n=家族号 | `0x100137C2`：用 `time()` 限流，**同一秒内只发第一次** | **0xB6** / 0x1773：`Uin, Time, KinIndex=n, IsMember`。Time 实际填缓存的 BaseUpdate（`+0x39A0`/`+0x39A4` 置位时填 0）；IsMember 在 n 是自己的家族时为 1，否则为 0 [证实] |
| **2** 职位升降 | `uiMain.py:3571`/`3590` kinOprMenu：`DoKinTask('Y'或'N', GetKinPositionByUin(uin), len, uin, 2)` | p1='Y'/'y' 表示升，其余表示降；p2=目标当前的职位名；n=目标 UIN | `0x10015021`：见 §3.2 | **0xC5** / 0x178B：`KinIndex, Uin, Time, DstUin=n, AuthorityID=新 grade`。权限不足时两处显示`您的权限不够,无法调整职位`，不发包 |
| **3** 设家族徽章 | `uiMain.py:3177`：`DoKinTask('', str(customtotemID), len, 0, 3)` | p2=徽章号的十进制串 | `0x100148A3`：`BadgeID = atoi(p2)`。为 0 或等于当前 `FlagID`（K+0x3268）时不发包，不提示 | **0xC7** / 0x178F：`KinIndex, Uin, Time, KinBedgeID=atoi(p2), KinDefineBedgeID=-1` |
| **4** 邀请入族 | `uiMain.py:5209` 玩家菜单 kininviteBtn：`DoKinTask('', '', 0, friendUin, 4)`；`/ki <uin>` 斜杠命令也走这里 | n=目标 UIN | `0x10014141`：目标是自己，或本人 grade ≤ 9（GetKinMemberHonor mode 1），就静默结束 | **0xBA** / 0x177B：`Uin, Time, KinIndex, DstUin=n, Para=0, Reserve=0`。发出后两处显示`【家族信息】您向玩家[%u]的邀请已经发出。` |
| **5** 开除 | kinTipDlg.confirm（`uiMain.py:4885`）。TaskMode=5 由 kinOprMenu.fire 设置，提示语`您确定要开除%s[%d]吗?` | n=目标 UIN | `0x10014A2C`：本人不是族长时两处显示`你不是族长,无权开除成员.`；目标是族长时两处显示`不能开除族长.` | **0xC0** / 0x1784：`Uin, Time, DstUin=n, KinIndex` |
| **6** 退出 | kinTipDlg.confirm。TaskMode=6 由 quitBtn 设置，提示语`您确定要退出家族吗?` | n=本人（不用） | `0x10014C4F`：已处于离开状态（K+0x39AC）时显示`你已经处于离开状态`；本人是族长时显示`族长不能退出家族.`。都是两处显示 | **0xC1** / 0x1786：`Uin, Time, KinIndex` |
| **7** 解散或取消解散 | kinTipDlg.confirm。TaskMode=7 由 dismissBtn 设置，提示语`您确定要解散您的家族吗?`。另有 canceldismissBtn 直接 `DoKinTask('', '', 0, uin, 7)`（`uiMain.py:3208`） | — | `0x10014E54`：本人不是族长时两处显示`你不是族长,无权解散家族.` | **0xC2** / 0x1788：`Uin, Time, KinIndex, KinOperationID`。K+4==0 时填 1（解散），否则填 2（取消解散） |
| 8 | 无 | — | `0x1001532B` 只有 `return 1`，是空操作 | 无 |
| **9** 改职位名 | `uiMain.py:3176` kinmanager.confirm：5 个职位名都不为空时，`DoKinTask('', "n0\x07n1\x07n2\x07n3\x07n4\x07", len, 0, 9)` | p2=用 `\x07` 分隔的 5 个名字（≤200 字节） | `0x10014495`：本人必须是族长，否则静默。依次把名字配上 grade 12、10、8、6、4，序列化成职位对象（`0x10011F37`，格式 `"%d "` 数字和 `"%s "` 串，与 `kin.go` 一致）。和缓存的 Title（K+0x3384）相同时不发包 | **0xC6** / 0x178D：`KinIndex, Uin, Time, TitleLen, Title[≤200]` |
| 10 | 无 | — | 跳转表指向函数尾，什么都不做 | 无 |
| **11** 回复邀请 | `uiMain.py:4815`/`4827` kinInviteDlg：`DoKinTask('', 'Y'或'N', 1, 0, 11)` | p2 是 'Y'/'y' 表示同意，其余表示拒绝 | `0x10015516`。**没有 break，接着执行 mode 12** | **0xBC** / 0x1780，0x510 字节：`Uin=K+0x3546`（即**别家族缓存的 KinBase.m_dwUin，族长**）`, Time, KinIndex=K+0x3542, DstUin=本人, Para=0 或 -1, Reserve=0, KinBase`（全 0）`, ReasonDesStr`。Reason 填 `[%u]玩家同意加入您的家族.` 或 `[%u]玩家拒绝加入您的家族.`，%u 是本人 UIN。随后发 **0xBE**，正文是 1 字节的 `Y`/`N`，KinIndex 用 K+0x2C（此时通常为 0，服务端会拒收） |
| **12** 家族聊天 | 脚本不直接调用。`/kinchat on` 之后的普通 SectionChat 走这里（slash 文档 §5.11）；0x140 应答成功后客户端也会自动发一条 | p2=正文，len≤0x200 | `0x10016B84` | **0xBE** / 0x1782：`Uin, Time, KinIndex=K+0x2C, KinMsgLen`（u32）`, KinMsg` |
| **13** 改宣言 | `uiMain.py:3180` kinmanager.confirm：manager 的 `.text` 不为空时调用 | p2=宣言，≤257 | `0x100171FF`：本人必须是族长，且和缓存的宣言（K+0x3283，长度 K+0x326E）`strncmp` 不同才发 | **0x140** / 0x1793：`Uin, Time, KinIndex, ContentLen, Content` |
| **14** 发公告 | `uiMain.py:3796` kinmemberDlg.issue | p2=公告，≤257 | `0x10017445`：本人必须是族长，且和缓存的公告（K+0x345A，长度 K+0x3458）不同才发 | **0x141** / 0x1795：`KinIndex, Uin, Time, NotificationLen, Notification` |
| **15** 取排行榜 | `uiMain.py:2738` menuDlg.topkinBtn：`DoKinTask('', '', 0, lookupKinID, 15)`，n 不用；随后脚本立刻调 `ui_updateTopkinList()` | — | `0x10017691`，不限流 | **0xCB** / 0x1797：`Uin, Time, ClientOrderTime=K+0x39B4, KinIndex, HonorOrder=0, ActivePointOrder=0` |

### 3.2 mode 2 的职位计算（`0x10015021`）[证实]

1. 在职位表里找名字等于 p2 的条目，下标记作 `k`。
   - 从 `count-1` 往下找到 **1**，第 0 条（族长）永远不参与比较；找不到时 `k=0`。
2. 新 grade：
   - 升：`k==0` 时静默返回；否则取第 `k-1` 条的 grade。
   - 降：`k ≥ count-1` 时静默返回；否则取第 `k+1` 条的 grade。
3. 权限：本人 grade 必须大于新 grade，**并且**目标当前 grade 必须小于本人 grade。否则两处显示`您的权限不够,无法调整职位`。

注意：目标的职位名为空（例如 grade 不在表里）时 `k=0`，于是"降"会给出第 1 条的 grade（10）。这是原版就有的怪行为，照抄。

---

## 4. 查询原生函数

所有查询都只读 §2 的缓存，不发网络请求 [证实]。

### `GetKinParam(i)`

| i | 返回 | 脚本里的用途 |
| --- | --- | --- |
| 0 | 本人家族号 K+0x2C | 判断是否入族：0 表示没有家族，弹 `UI.createkinhintDlg` |
| 1 | 本家族的 `m_iLastHonor`（K+0x352A） | `kinmemberDlg.kinHonor` |
| 其他 | 0 | |

### `GetInviteKinInfo(i)` → KINBASE 对象

| i | 返回 | 来源 |
| --- | --- | --- |
| 0 | 别家族缓存（K+0x3542） | 查看别家族时的 0xB6 应答，或邀请 0xBC 里的 KinBase |
| 1 | 本家族（K+0x324C） | 0xB6（自己家族）、0xBD 加入成功、0xB5（只填几个字段，见 §5） |
| 其他 | 全 0 | |

脚本读这些字段：`m_dwKinIndex`、`m_dwUin`、`m_dwStatus`（`(s & -1048576) >> 20` 是规模）、`m_iGrade`、`m_iLastHonor`、`m_wMemberNum`、`m_wContentLen`、`m_szContent`、`m_szName`、`m_szNotification`、`m_iKinSection`、`m_stKinFlagID.m_iFlagID`。

宿主应返回一个带 §2 全部 `m_` 字段的对象。没有数据时返回全 0 或空串。

### `GetKinPosition(i)` → str

返回职位表第 i 条的名字，不检查下标 [证实]。kinmanagerTab 用 `i=0..4` 填 customPosition0～4。

### `GetKinPositionByUin(uin)` → str

1. 每次都重新解析缓存里的 Title 对象。
2. 用成员缓存找到 uin 的 grade。
3. 返回 grade 相同的那条职位名；uin 不在缓存或找不到对应职位时返回 `''` [证实]。

### `GetKinMemberHonor(uin, mode)` → int

在成员缓存里找 uin，找不到返回 0 [证实]。

| mode | 返回 | 脚本里的用途 |
| --- | --- | --- |
| 0 | 该成员的 Honor（MemAttachList） | 成员列表的荣誉列 |
| 1 | grade | 名字颜色：12 是 (255,123,44)，10 是 (247,65,255)，8 是 (69,139,255)，6 是 (179,203,15)，其余是 (24,201,99)；也用于邀请资格（>9） |
| 2 | uin 是族长时返回 K+4（解散待生效），否则返回 0 | kinmanagerTab 决定显示 dismissBtn 还是 canceldismissBtn |
| 3 | 与 mode 0 相同 | |

### `GetPlayerCnt(2)` / `GetPlayerCnt(3)`

- mode 2：返回 **`KinBase.m_wMemberNum`**（K+0x326C）。注意不是成员缓存的条数。
- mode 3：返回在线数（K+0x383C）[证实]。

### `fetch_PlayerInfoInPlayerList(i, 2|3)` → `{m_PlayerInfo, m_nPlayerStatus}`

- mode 2 取成员缓存第 i 条。缓存为空时返回全 0；不检查 i 是否越过缓存条数。
- mode 3 只取前 `在线数` 条（在线成员排在前面）[证实，`0x10013D68`/`0x10016108`]。
- 填法：
  1. 先填 `m_dwPlayerUin` 和昵称（来自成员缓存），以及本家族的 `m_dwKinIndex`、`m_nKinNameLen=strlen(name)`、`m_szKinName`、`m_stKinFlagID`。
  2. 如果该 uin **在当前小区的大厅玩家表里**：整条 PLAYER_INFO 用大厅记录覆盖（kin 字段也用大厅里的），`m_nPlayerStatus=1`，并把这条记录存进"见过的玩家"缓存（K+0x3230）。
  3. 否则 `m_nPlayerStatus=0`。如果"见过的玩家"缓存里有，补上 Point、Identity、Gender、ExtItem。
- 原版把"见过的玩家"缓存持久化在 `./data/<uin>-kininfo.dat`，登录时由 `0x10015C25` 读入 [证实；文件格式未查]。

### `getExtItem(i, j, 2)`

从"见过的玩家"缓存取第 j 个扩展道具，缓存里没有时返回值未初始化 [证实]。宿主返回 0 即可。

### `fetch_topkininfolist(tel, rep, i)` → KININFOTOP

返回对象的字段（Client.exe `0x4F0C20` 注册）[证实]：

| 偏移 | 字段 |
| --- | --- |
| `+0` | `m_dwKinIndex` |
| `+5` | `m_szName` |
| `+0x18` | `m_iValue` |
| `+0x1C` | `m_nOrder`，**是字符串**，例如 `"A12"` |
| `+0x22` | `m_nAscend`（字节） |

规则（`0x100179C2`、`0x1001787F`）[证实]：

- `i ≥ 12`：Client 包装函数返回全 0。还没收到过 0xCB（K+0x399C==0）时也返回全 0。
- `i < 10`：取第 `idx` 条记录。
  - `tel==1, rep==1`（电信荣誉）：`idx = i`；
  - `tel==1, rep==0`（电信活跃）：`idx = i + 10`；
  - `tel==0, rep==1`（网通荣誉）：`idx = i + 20`；
  - `tel==0, rep==0`（网通活跃）：`idx = i + 30`。
- `i == 10 / 11`：本家族那一行。
  - 没有家族，或者 `tel != m_iKinSection`，返回全 0。这里拿 1/0 和 1/2 比较，所以网通家族永远对不上，是原版的怪行为。
  - `i==10`：先取第 40 条（rep==1）或第 41 条（rep==0）记录，即 SameGroupKinInfo；它的 KinIndex 不为 0 时直接返回。
  - 其余情况（含 `i==11`）用本家族 KINBASE 构造：名字；rep==1 时用 `LastHonor/LastHonorOrder/BeforeHonorOrder`，否则用 `LastActivePoint/LastActivePointOrder/BeforeActivePointOrder`。
- `m_nAscend`：Order==Before 时为 0；Order<Before 时为 1；否则为 2；Before==0 时强制为 1。脚本据此用 `arrow_%d.img`。
- `m_nOrder`：设 `g = GroupMemberNum`（K+0x39BA），用 `order/g` 定字母、`order%g` 定数字，**除法不做零检查**，机器码 `idiv esi` 在 `0x10017926`。
  - 余数为 0 时，字母减 1、数字写 100。
  - 字母在 A～Z 之间时写 `"%c%d"`，否则写 `"Z+%3d"`。
  - 本家族行的 order 为 0 时写 `"Z++"`。

### 其他

- `GetPlayerInfoByUin(uin)`：查大厅玩家表。脚本用它显示族长昵称 `m_szPlayerNickname`，族长不在大厅时昵称为空 [证实]。
- `GetWisperName(1)`：返回 `/kinchat on` 时记下的家族名（`S+0xC5E0`）；家族名为空或超过 0x27 字节时，改存家族号的十进制 [证实，`0x1002DE18`]。

---

## 5. 服务端消息：缓存变化、聊天提示、脚本回调

分发函数是 `0x10013069`。它只处理下表这些命令 [证实]：0xB5 0xB6 0xB7 0xBA 0xBC 0xBD 0xBF 0xC0 0xC1 0xC2 0xC4 0xC5 0xC6 0xC7 0xCB 0x140 0x141。

**不处理**：0xB8、0xB9、0xBB（NOTIFY_UPDATE_KIN_FLAG）、0xBE、0xC9、0xCA。

下表的线上布局按 QQTMsgData 的 schema，并且与 Go 的编码器逐项核对过，除 §8 列出的不一致外都相符。"两处"的含义同 §3。

| 命令（schema） | 线上字段 | 原版处理 | 回调 |
| --- | --- | --- | --- |
| **0xB5** 创建应答（0x1772） | `Uin, KinIndex, ErrorNo`（u32）`, ReasonLen`（u16）`, Reason` | ErrorNo==0：K+0x2C=KinIndex，族长=Uin，base.KinIndex=KinIndex，FlagID={0,1}，MemberNum=1；置 +0x39A0，然后立刻发 mode 1 取家族信息。不论成败：ReasonLen>0 时弹系统消息框（接口 `0x10054430`+0x10，参数 `(0, reason, 3)`，即只有"关闭"按钮的消息框，宿主可用 `message_box(reason, 3, 0)`）[推断接口身份]，并两处显示 reason。`0x1005FE..` 里的本地文案（`【系统提示】:恭喜恭喜…`等，按 ErrorNo 1～8 选取）只写进栈缓冲，**从不显示** [证实] | 无 |
| **0xB6** 家族信息应答（0x1774） | `Uin, KinBase` | 若 KinBase.KinIndex == K+0x2C，按"本家族"处理：<br>1. 人数上限 = Status>>20；DismissTime≠0 时置解散标志；记下 BaseUpdate、ListUpdate。<br>2. 整块存入 +0x324C，并按 Title 重建职位表。<br>3. **+0x3998 < ListUpdate 时才发 0xB7**：`Uin, Time, KinIndex, ListUpdate=+0x3998`（`0x10013A9F`）。<br>4. 更新本人 PLAYER_INFO 的家族字段（`0x1002FC8B`；名字长度固定写 17）。<br>否则存入 +0x3542 | 本家族：**无回调**。别的家族：`ShowkinInfoDlg()` |
| **0xB7** 成员表应答（0x1776） | `Uin, ListUpdate, Result`（u16）`, Num`（u16）`, MemList[Num]`（每条：`Uin, NickLen, Nick, Time, Status, StatusTime, Grade, OnlineTime, LastLoginTime`）`, MemAttachList[Num]`（每条：`Honor, ActivePoint`） | Result==0：+0x3998=ListUpdate，清 +0x39A0/+0x39A4，整表替换（最多 200 条）。随后重排（`0x10017041`）：在线（`Status&1`）的排前面；两组内部各按 grade 从高到低选择排序，不稳定。Result≠0 时缓存不变 | `ui_updateMemberList()`、`updatePlayer()`（**不论成败都调**） |
| **0xBA** 邀请应答（0x177C） | `Uin, Time, KinIndex, dstUin, Para, Reserve, ReasonLen, Reason` | Para==0：置 +0x39A0；dstUin 不在成员缓存时**立刻追加**一条，昵称写成 `"%u"`。ReasonLen≠0 时**只在当前频道**显示 reason | Para==0 时：`ui_updateMemberList()`、`updatePlayer()` |
| **0xBC** 收到邀请（0x177F） | `Uin, Time, KinIndex, DstUin, Para, Reserve, KinBase` | KinBase 存入 +0x3542。本人本家族 KinIndex（+0x324C）为 0 时：往留言队列压一条"脚本"类留言 `ShowkininviteDlg()`（`0x10016084`，类型 2）。否则自动拒绝：执行 mode 11 的发包部分，Para=-1，**这条路径不会落到 mode 12** | 玩家点信封时（`GetLeaveWord`，`0x1002A206`）执行 `ShowkininviteDlg()`。宿主做法：在 `friend.py` 的 leave_words 里加一种"脚本"条目，并 `startGlint()` [推断：信封闪烁由宿主负责，与好友留言相同] |
| **0xBD** 邀请结果（0x1781） | 同 0xBC | Para==0：置 +0x39A0；K+0x2C=KinIndex；DstUin 不在成员缓存时追加；KinBase 存入 +0x324C（**职位表不重建**）；两处显示`【家族公告】:热烈欢迎[%u]加入%s家族`（DstUin，KinBase.Name）。否则只在当前频道显示`未能加入家族。` | 无 |
| **0xBF** 家族聊天（0x1783） | `Uin, KinIndex, KinMsgLen`（u32）`, KinMsg` | 不检查 KinIndex。昵称取成员缓存，没有就用 `"%u"`。拼成 `"/<name=%d:uin=%u>%s[%u]说:%s"`，参数依次是昵称字节数、uin、昵称、uin、正文；两处显示，颜色 0x18FF00 | 无 |
| **0xC0** 开除应答（0x1785） | `Result, Uin, DstUin, KinIndex, ReasonLen, Reason` | **缓存不变**，成员由随后的事件 8 删除。ReasonLen>0 时两处显示 | `ui_updateMemberList()`（不论成败） |
| **0xC1** 退出应答（0x1787） | `Result, Uin, KinIndex, ReasonLen, Reason` | Result==0：只置 +0x39AC=1，**家族号和缓存都不清**。然后两处显示 reason；ReasonLen 为 0 时，原版显示的是未初始化的栈缓冲 | 无 |
| **0xC2** 解散应答（0x1789） | `Result, Uin, KinIndex, KinOperationID, ReasonLen, Reason` | Result==0：OperationID=1 时 K+4=1，=2 时 K+4=0。ReasonLen>0 时两处显示 | 无 |
| **0xC4** 家族事件（0x178A） | `EventType, KinIndex, Uin, NickLen, Nick, AttachUin, AttachNickLen, AttachNick, DescLen, Desc` | KinIndex≠K+0x2C 时忽略。否则先两处显示`【家族公告】:%s`（Desc，为空也显示），再按 EventType 处理：<br>**1**：Uin 不在缓存时追加。<br>**5**：K+0x2C=0，清成员缓存、本家族 base 和 K+4。<br>**6**：FlagID={AttachUin, Uin}，并改大厅里本人的家族徽章。<br>**8**：AttachUin 是本人时清 base，K+0x2C=0（成员缓存**不清**）。否则从缓存删 AttachUin（把末条移过来；即使没找到，计数也减 1），并**清大厅里 `Uin` 字段那个玩家的家族信息**——注意是 Uin，不是 AttachUin。<br>**9**：公告缓存 = Desc。<br>其他值只显示 | 只要 KinIndex 匹配，**一律**调 `ui_updateMemberList()`、`updatePlayer()` |
| **0xC5** 任命应答（0x178C） | `ResultID, KinIndex, Uin, DstUin, AuthorityID, ReasonLen, Reason` | 成功：把缓存里 DstUin 的 Status 高字节改成 AuthorityID。ReasonLen>0 时两处显示 | 成功时：`ui_updateMemberList()`、`updatePlayer()` |
| **0xC6** 职位名应答（0x178E） | `ResultID, KinIndex, TitleLen, Title, ReasonLen, Reason` | 成功：按 Title 重建职位表，缓存 Title 和 TitleLen。ReasonLen≠0 时两处显示 | 成功时：`updateKinManagerDlg()` |
| **0xC7** 徽章应答（0x1790） | `ResultID, KinIndex, Uin, KinBedgeID, KinDefineBedgeID` | 成功：FlagID={DefineBedgeID, BedgeID}。**大厅里本人的徽章不改** | 成功时：`updateKinManagerDlg()` |
| **0xCB** 排行榜（0x1798） | `ResultID, OrderTime, Uin, TopKinInfo{OrderTime, KinOrderInfo[40]（定长）, GroupNum, GroupMemberNum, SameGroupKinInfo[2]（定长）}`。KinOrderInfo：`KinIndex, NameLen`（u8）`, Name, Value, Order`（u16）`, BeforeOrder`（u16） | 成功：+0x39B4 = 内层 OrderTime；+0x39B8/+0x39BA = GroupNum/GroupMemberNum；42 条记录拷入新分配的缓冲；+0x399C=1 | 成功时：`ui_updateTopkinList()` |
| **0x140** 宣言应答（0x1794） | `ResultID, Uin, KinIndex, ContentLen, Content, ReasonLen, Reason` | 成功且 Uin==本人：缓存宣言，然后**自动发一条 0xBE 家族聊天**，正文`家族宣言已更新。`。**不显示 reason** | 成功时：`updateKinManagerDlg()` |
| **0x141** 公告应答（0x1796） | `ResultID, KinIndex, Uin, NotificationLen, Notification, ReasonLen, Reason` | **不改缓存**（公告由事件 9 更新）。两处显示`成功设置家族公告`，或`设置家族公告失败 <reason>` | 无 |

登录 [证实]：

- 原版从 `RESPONSE_LOGIN.GameInfo.OrgID`（结构偏移 0x3F）取家族号写进 K+0x2C，然后读 `./data/<uin>-kininfo.dat`。
- 登录时**不**主动取家族信息，要等脚本第一次 mode 1（点开家族页签）。

---

## 6. 聊天输出约定 [证实]

接口 `0x10054380+0x18(place, sub, text, colour)` 往聊天框加一行，分发函数在 Client.exe `0x48A890`。界面原生函数 `+0x34(path, text, 207, r, g, b)`。

| place | 目标 |
| --- | --- |
| 1 | 当前界面是 selRoom 时：`UI.selRoom.chatArea.chatPanel.chatList` |
| 2 | 当前界面是 room 时：`UI.room.chatArea.chatPanel.chatList`；sub>0 时显示成头顶气泡 |
| 3 | 当前界面是 game 且 sub==0 时：`UI.game.chatList` |
| 0x15 | 密语面板 |
| 0x1F | `UI.selRoom.chatArea.kinchatPanel.chatList` 和 `UI.room.chatArea.kinchatPanel.chatList`，按当前界面二选一 |

- **"当前频道"**由 `0x1000A6BD(房间状态 R+4)` 决定：0 → 1，1 → 2，2/3/4 → 3。
- **颜色**：`0x18FF00` 按字节 2、1、0 传入，即 RGB (0x18,0xFF,0x00)。`/kinchat` 失败提示用 `0xB8FF79`，与 `slash.py` 现有的 SYSTEM_COLOR 一致。
- **名字标签**：家族聊天的 `/<name=..:uin=..>` 是 gameUI 聊天列表的可点名字标签。宿主目前处理大厅聊天时丢掉了这类标签，家族聊天可以同样显示成 `昵称[uin]说:正文` [推断：标签由 gameUI 解析]。

---

## 7. 定时刷新 [证实，周期未知]

`S` 的定时函数 `0x1002120A` 每调用两次，执行一次 `0x10016FC2`：

- 每 10 次重新排一遍成员缓存（在线优先，再按 grade 排）。
- **每次**都执行 `updatePlayer()` 和 `updatePlayer2()`。

定时器的实际周期 [未知]。宿主可以用每秒一次近似。

---

## 8. 与 Go 服务端的不一致

| # | 问题 | 依据 | 影响和建议 |
| --- | --- | --- | --- |
| 1 | **排行榜 40 条的分组**：原版是 `[电信荣誉10][电信活跃10][网通荣誉10][网通活跃10]`；Go 的 `BuildLocalFetchKinTopResponse` 写的是 `[荣誉20][活跃20]` | `0x100179C2` [证实] | 页签内容会错位。服务端应按分区和类型各取前 10；或者宿主自己按分区重排（需要知道各家族的分区） |
| 2 | **GroupMemberNum=0**：Go 写 `GroupNum=0, GroupMemberNum=0` | `0x10017926` 的 `idiv` 不做零检查 [证实] | 原版客户端会因整数除零崩溃。宿主遇到 0 要跳过除法，例如 `m_nOrder` 直接用 `str(order)`。服务端宜写 100：原版余数为 0 时硬编码写 100，说明组大小就是 100 [推断] |
| 3 | **SameGroupKinInfo 是定长 2 条**（schema 没有计数字段），Go 一条都不写，只写了两个 u16 | QQTMsgData：`SameGroupKinInfo cap=2 cnt=none` [证实] | 原版解码短包的行为 [未知]。宿主解析时要容忍缺失，当作全 0；服务端应补 60 字节 |
| 4 | **回复邀请的第一个字段**：原版填别家族缓存的 `m_dwUin`（族长），Go 当作邀请人，拿去查 `kin_invitations.inviter_uin` | `0x10015516` [证实]；`AnswerKinInvitation` | 由长老（grade 10）发出的邀请无法接受（ErrKinInvitationMissing）。服务端应改为按 (target, kin) 查邀请，不比对 inviter。另外 +0x3542 会被"查看别家族"覆盖，原版收到邀请后再查看别家族，就会把回复发给错的家族；宿主照抄或修正都可以 |
| 5 | **mode 11 落到 mode 12**，多发一条 `Y`/`N` 家族聊天 | [证实] | Go 因 KinIndex=0 而拒收，无害。宿主可以不发 |
| 6 | **退出没有事件**：原版 0xC1 只置"离开状态"，家族号不清。Go 退出成功后不发任何 0xC4 | `0x10015948` [证实]；`kin_dispatch.go` | 退出者直到重新登录都显示自己还在家族里，其他成员的列表也不更新。建议服务端给退出者本人和其他成员发事件 8（AttachUin=退出者）；原版的"本人被移出"分支正是为此准备的 [推断] |
| 7 | **取消解散**：原版用 KinOperationID=2 表示取消，Go 的 `DismissKin` 不看这个值 | `0x10014E54` [证实] | 现在 Go 立即解散并发事件 5，K+4 随即被清，取消按钮实际不会出现。如果以后做延迟解散，要区分 1 和 2 |
| 8 | **事件 8 的 Uin 字段**：原版第三方客户端会清掉大厅里 `Uin` 那个玩家的徽章，Go 在 Uin 里填的是操作的族长 | `0x100167E2` [证实] | 族长的徽章会在其他成员的大厅列表里消失。原服务端在 Uin 里填什么 [未知]；建议 Uin=AttachUin=被开除者。宿主如果自己维护大厅缓存，可以忽略这一步 |
| 9 | **徽章变更没有广播事件 6**：Go 只刷新大厅资料 | `0x100149C7` 只改发起人的缓存 [证实] | 其他成员缓存里的家族徽章（FlagID）要等下次 0xB6 才更新 |
| 10 | **登录家族号**：原版从 GameInfo.OrgID 取，Go 的 OrgID 恒为 0，改为额外发一个伪 0xB5（ErrorNo=0、空 reason） | `0x1001B2EA` [证实]；`BuildLocalKinSessionRestore` | 宿主按 §5 实现 0xB5，伪包就能生效：它会触发一次 mode 1。也可以直接用登录资料里的 kin_index。两条路不要重复发 0xB6，原版同一秒内只发一次 |
| 11 | **成员在线状态不刷新**：原版只在 ListUpdate 变大时才重取成员表，Go 只在成员增减时加 list_update，上下线不变 | `0x10013945` [证实] | 在线标志会过时。服务端可以在上下线时加 list_update；或宿主每次 mode 1 都发 0xB7（偏离原版） |
| 12 | **公告长度**：客户端最多 257 字节，Go 的上限是 200（KinBase 和事件里的槽位都是 200） | [证实] | 超长时服务端回失败，客户端显示`设置家族公告失败 …`，可以接受 |

逐项核对一致的有：0xB5 0xB6 0xB7 0xBA 0xBC 0xBD 0xBF 0xC0 0xC1 0xC2 0xC4 0xC5 0xC6 0xC7 0x140 0x141 的字段顺序和长度前缀宽度，以及各个请求解码器。另外：

- KinBase.Name 定长 17 字节；
- 职位对象采用 `"%d "` 格式；
- grade 在 Status 的 bit24～27；
- 在线标志在 `Status & 1`；
- 人数上限在 `Status >> 20`。

---

## 9. 宿主实现清单

1. **状态对象**：本人家族号、本家族 base、别家族 base、成员表（含 honor）、职位表、离开标志、解散标志、两个 Update 戳、排行榜 42 条和组参数、mode 1 的秒级限流时间戳。
2. **原生函数**：
   - `DoKinTask`：§3，`p2`、`len` 按 GBK 处理。
   - `GetKinParam`、`GetInviteKinInfo`、`GetKinPosition`、`GetKinPositionByUin`、`GetKinMemberHonor`、`fetch_topkininfolist`：§4。
   - 扩展已有函数：`GetPlayerCnt` 的 mode 2/3，`fetch_PlayerInfoInPlayerList` 的 mode 2/3，`getExtItem` 的 mode 2。
   - `GetWisperName(1)`，以及 slash 的 `/kinchat on`：通过 `UpdateUIKinChat()` 进入家族聊天模式，此后普通聊天走 mode 12。
   - `/ki` → mode 4。
3. **应答处理**：§5 的 17 个命令，回调用宿主现有的 `script("名字")`。
4. **留言队列**：增加"脚本"类留言，用于 `ShowkininviteDlg()`。
5. **定时器**：§7。
6. **脚本自身的小错误**：
   - `ShowkinInfoDlg` 写 `uiKinInfoDlg + 'Proclaim'`，少了点，`Win_SetText` 写到不存在的控件上必须是空操作；
   - `canceldismissBtn` 用的是全局 `uin`。

## 10. 未查

- 定时器周期。
- `kininfo.dat` 的格式（`kininfo %u;%d;%d;%d;%d;%s;`，`0x10015A33`）。
- 原版解码 0xCB 短包的行为。
- 原服务端事件 8 的 Uin 字段填什么。
- 0xC1 的"离开状态"何时生效（推测是延迟退出）。
- 接口 `0x10054430` 就是系统消息框：只有在留言队列和本次的用法里互相印证过。

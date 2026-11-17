# 新手任务（新手教程）

2026-11-17，静态逆向 `Client.exe`、`QQTSection.dll`。代码在 `qqtclient/battle/tutorial.py`，入口接在 `__main__.py`。

## 状态

都在游戏管理对象 `0x00806A78`（`0x005A1DE1` 返回）上：

| 偏移 | 含义 | 读写 |
| --- | --- | --- |
| `+0x56EEF` | 新手任务对局 | `0x00490B70` 置位；`0x005A4181`、`0x005A26C1` 清除 |
| `+0x56EED` | 首次新手任务 | `0x005A4174`；`sc_firstBeginTutorial` 置 1，结束时执行 `NewPlayerPractice()` 后清 0 |
| `+0x56EFF` | 正在播放教学录像 | `0x00490BA0(1)` 置位并把计数清 0；录像链结束时清 0 |
| `+0x56F00` | 已开始播放的录像数 | `0x00490BE0` 加 1，`0x00490C20` 读 |
| `+0x56F04` | 自由练习 | `0x00490C80` |
| `+0x56EF4` | 任务控制器（见下） | GAME_BEGIN 处理 `0x005A2089` 创建 |
| `+0x56EE0` | 模式：1 普通对局，3 录像回放 | |

## 开始

- 大厅“新手练习” → `uiTaskSelDlg` → “新手任务”：脚本先 `DisableEnterRoom()`，再调用 `sc_beginTutorial()`。
- `sc_beginTutorial`（`0x0048E690`）：置 `+0x56EEF`；若已有任务控制器且计数非 0，计数清 0；然后取 Section 服务（GUID `0x0079A908`）虚表 `+0x1B0`，即 QQTSection `0x1001C5A7`。
- QQTSection `0x1001C5A7` 与自由练习的 `0x1001C6F2` 几乎相同：清零 0x5AE 字节的 GAME_BEGIN 结构，人数 1、地图 `0x5DD`（1501），只放本机一名玩家，交给对局监听者 `+8`，也就是本地开局。两者唯一的差别是地图号和一个本来就为 0 的字节（`+0x33D`）。
- `sc_firstBeginTutorial`（`0x0048E650`）：`+0x56EED = 1`，再执行脚本 `sc_beginTutorial()`。它不是脚本调用的，而是 QQTSection 进分区的回包处理 `0x1001B2EA` 在执行 `DealInitialize()` 之后调用的（`0x1001BCD1`），条件是：
  - Section `+0xC768`（回包里的 SectionMode 字节，`0x1001B2EA` 用 `0x10029E4F` 写入）不等于 1（`0x10029E3F`）；
  - 回包 GameInfo 的胜、负、平三项之和小于 1（`0x1001BCBC`）。
  服务端 `internal/protocol/game/profile.go` 的 `TutorialCompleted` 就是按这个条件给一次平局来跳过首次教程。
- `beginTutorial1`（`0x0048E7F0`）只调用 `+0x1B0` 开局、不置 `+0x56EEF`。三个程序和脚本里都没有调用它。
- 宿主：`sc_beginTutorial`、`sc_firstBeginTutorial`、`beginTutorial1` 都已接上；进分区后在 `session.on_entered` 里按同样条件调用首次教程。

## 结束（`0x005A4181`）

新手任务和自由练习的对局结束（`0x005A3E98` 结果显示倒计时到 0）、或玩家点“离开”（`0x005A26C1` 先回 `selRoom`，再 `0x005A2C6D` → `0x005A4181`），都走这个函数：

1. 按地图把关卡号记成 0～3（1501～1504，其他为 4），连同场景时钟交给 `+0x7D0` 对象的虚表 `+0x38`：教程填（关卡号, 1），剩余任务少于 5 条时另填 1000；自由练习填（0, 2）。推断是统计上报，宿主不做。
2. 清 `+0x56EEF`，销毁场景和任务控制器，清掉等级图标。
3. 如果不在播放教学录像（`+0x56EFF` 为 0）：
   - 自由练习：只回 `selRoom`（`0x005A3F12(1)`）。`0x005A439E` 判断 `+0x56F04` 非 0 后直接跳到 `0x005A4438`。
   - 新手任务：回 `selRoom`；首次（`+0x56EED`）则执行 `NewPlayerPractice()`（重新打开练习选择框，并显示 `UI.newPlayerDirectionDlg` 气泡“您现在可以重复新手任务，也可以选择进行自由练习……”），然后清 `+0x56EED`；
     否则通过服务 `0x0079DE58` 虚表 `+0x10`（`0x0052CB80` → `0x0048B130`）弹出“您是否需要观看高手玩家录像？”（`0x007F2378`），句柄 `0x2000008`，之后执行 `ui_msgBox(0)`，按钮是“确定/取消”。
4. 如果正在播放教学录像且 0 < 计数 < 3：执行 `SC_PlayTutorialRecord()`。
5. 清 `+0x56F04`。

所以中途离开新手任务，和打完一样会弹出录像询问（或首次时的练习框）。

宿主以前把自由练习做成“打完后重新弹练习框”，与原版不符，已改为只回大厅；`keylayout-re.md` 的记录一并改正。

## 高手录像

- 询问框按“确定”：脚本 `OnMsgBoxOk` 对非负句柄调用原生 `OnMsgBoxResult(h, 1)`（`0x0048B410`）。它按 `h >> 24` 分发，2 是 Section 服务虚表 `+0x164`，即 QQTSection `0x10023458`；`case 8` 在确定时执行脚本 `SC_PlayTutorialRecord()`（`0x100617C4`）。取消什么也不做。
- `SC_PlayTutorialRecord`（`0x0048C960`）：
  - 计数为 0：`+0x56EFF = 1`、计数清 0，计数加 1，`PlayRecord("tutorial1.qbv")`（`0x005A36CE`）；
  - 计数为 1、2：计数加 1，播放 `tutorial2.qbv`、`tutorial3.qbv`；
  - 其他什么也不做。
- 每段录像结束或被“离开”（回放模式 3 的 `0x005A2C6D`）：`+0x56EFF` 置位且 0 < 计数 < 3 时，先 `ui_NotifyShowLevelIcon(0, i, 0)`（i = 0～7），再执行 `SC_PlayTutorialRecord()` 播下一段；否则清 `+0x56EFF` 和计数，回 `selRoom`。
- 三段录像的内容（`replay-re.md`）：`Tutorial1.qbv` 地图 311 普通对战，`Tutorial2.qbv` 抢包子 806，`Tutorial3.qbv` 踢炸弹 704，都由 QQ 7354982 录制。它们是“高手玩家录像”示范，不是关卡本身。
- 宿主：询问框用 `show_msgbox` + `ui_msgBox(0)` 弹出；原生 `OnMsgBoxResult` 包了一层，只截下 `0x2000008`，其他句柄照旧；录像用已有的 `ReplayBattle` 播放，结束回调按上面的规则接下一段。文件名原版写小写 `tutorial1.qbv`，发布包里是 `Tutorial1.qbv`，宿主不区分大小写查找。

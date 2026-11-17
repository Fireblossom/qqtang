# 探险界面回调：队友离开、泡泡回复圈、提示气泡

2026-11-17。依据为 Client.exe 普通代码的 Ghidra 伪 C 与 capstone 反汇编（`~/qqtang-re/decompiled/Client.exe`），没有运行原版。三项回调分别是 `ui_DisposePVEPlayerLeave`、`ui_SetBaseItemTimeCur/Speed` 和 `NotifyAddHint/NotifyRemoveHint`。前两项已接入，第三项查明不属于探险。

## 1. 队友离开：`ui_DisposePVEPlayerLeave` [证实]

调用链：

1. 对局中有人离开房间，服务端发 0x008E，客户端执行规则虚表槽位 8（与夺宝、抢包子的“中途离开”同一入口，见 README）。规则 12 的虚表是 `0x007A429C`（槽 1 为 `0x00608F83`），槽 8（`0x007A42BC`）没有覆盖，仍是基础规则的 `0x005F4D1F`。
2. `0x005F4D1F(player)`：用玩家 `+0x36C` 的状态机执行 `0x005C0B71`，切换到单例状态 12（虚表 `0x007A2278`，`+0x34` 返回 12）；新手练习标志 `scene+0x56EEF` 置位时，再调用 `NewPlayerPractice`（`0x005A4181`）。
3. 状态 12 的进入函数（虚表 `+0x2C`）是 `0x005C0FF0`：取状态机 `+0xC` 中的玩家，调用玩家虚表 `+0x54`。
4. PVE 玩家类虚表 `0x007A1E60` 的 `+0x54`（`0x007A1EB4`）是 `0x005BA327`：
   - 先执行基础实现 `0x005ACF42`：`+0x3B7 = 8`、`+0x3B8 = 1`，调用 `ui_NotifyShowLevelIcon(积分, 座位, 0)`（隐藏的是竞技玩家栏的等级图标，规则 12 下该栏不显示）；
   - `sprintf("ui_DisposePVEPlayerLeave(%d)", (short)player+0x328)`，参数是 PlayerID；脚本据此隐藏 `pvePlayerList.playerInfo<i>`；
   - 遍历场景 `+0x3FC` 的 `+0x1348` 中的全部 NPC，调用 NPC 虚表 `+0xD4`（AddEnmity，`0x005BC67A`，见 `adventure-npc-ai-re.md` §2）参数为（离开者座位 `+0x396`，`0xFF676981` 即 −9999999），把对这名玩家的仇恨清零（没有记录时建一条 0）。

本机玩家自己离开时直接退出场景，不走这条路径；侧车 `depart` 本来就忽略本机编号。

实现：侧车 `onlineMatch.depart` 在引擎接受离开后调用 `adventurePlayerLeft`，按离开顺序记入 `adventure.left`，并对每个 NPC 执行 `addEnmity(id, -9999999)`（我们的仇恨表按 PlayerID 记，对应原版的座位号）。宿主 `BattleScene._update_adventure` 对 `left` 中每个编号只调用一次 `ui_DisposePVEPlayerLeave`。重复的 0x008E 由 `departed` 去重；换关后场景重建，名单随新的 GAME_BEGIN 重置。

## 2. 泡泡回复圈：`ui_SetBaseItemTimeCur/Speed` [证实]

`0x005BA3CB` 是 PVE 玩家虚表 `+0x1C`（`0x007A1E7C`），也就是放泡时的计数函数（`adventure-combat-re.md` §2.2）：

```text
A+0x760 = 0                                   // 回复计时清零
P.vtbl+0x1c(n)                                // 已用泡数 +1
if 0x00534990(scene, PlayerID):               // 本机玩家（scene+0x3820/+0x3822，录像回放时按录像玩家表）
    ui_SetBaseItemTimeCur(0.0)                // fldz
    res = P+0x304（BombResumeSpeed，秒，经 0x005BDFD1 解密）
    if res < 0.5: v = 0.5                     // 0x007A10E8
    else:         v = 1.0 / res               // 0x007970FC
    ui_SetBaseItemTimeSpeed(v * 100.0)        // 0x007A1F30
```

脚本把 `UI.game.statusBar.baseItemCycle`（DYMASK，`img_maskCycle.img`）当前角设为 0、步进设为该速度；目标角由 `InitBaseItemTime` 设为 360。DYMASK 按度/秒推进（`ui-dymask-re.md`），所以每放一个泡，状态栏第一格的遮罩从整圈开始，以 `100/res` 度每秒擦除。速度和回复时间并不对应（res = 2 s 时 7.2 s 才擦完），这是原版如此，照实现。

**原版 bug，已修正**：`res < 0.5` 时原版直接用下限 0.5 去乘 100，得到 50 度/秒，反而比 `res = 0.5` 时的 200 慢四倍；本实现取 `100 / max(res, 0.5)`。pvedata 最快为 0.7 s，道具效果 `BombResumeSpeed` 在本实现里也还没有改这个值，所以实际触发不到。

实现：侧车 `adventureBubbleRing` 每个引擎 tick 扫描本机玩家自己的、非规则所有的泡（与引擎 `adventureBombResume` 计数的是同一批泡），新出现一个就 `placed += 1`；`adventure` 状态里给出 `placed` 和本机的 `resume_ms`（`BombResumeMS`）。宿主在 `placed` 变化时依次调用 `ui_SetBaseItemTimeCur(0.0)`、`ui_SetBaseItemTimeSpeed(base_item_ring_speed(resume_ms))`。开局前不调用，保持 `InitBaseItemTime` 的 360（不画）。

## 3. 提示气泡：`NotifyAddHint/NotifyRemoveHint` [证实：不属于探险]

- 两个格式串只被 `0x005AFA3E`、`0x005AFAE5` 引用。`0x005AFA3E` 只被 `0x005C6221` 调用（`0x005C6703`、`0x005C6FB5`、`0x005C7008` 三处），`0x005AFAE5` 只被 `0x005C6221` 和 `NewPlayerPractice`（`0x005A4181`）调用。
- `0x005C6221` 是虚表 `0x007A2348` 的槽位，这个类由 `0x005C5CC1` 构造，唯一的创建处是 `0x005A2089`，而且只在 `scene+0x56EEF`（新手练习标志：`0x0048E690` 经 `0x00490B70` 置 1，`0x005A40B3` 在练习结束流程里置 1；`0x0048E690` 是新手任务入口为推断）为真、`+0x56F04` 为假时创建。它是新手任务（地图 1501，海王子）的分步控制器：按步骤号 `+0x70` 推进，文字取自表 `0x007F49C4[+0x84]`（共 12 句，如“有谁能救救我么？我和我的主人遭遇了海难……”）。
- 显示规则（`0x005AFA3E`，参数为非负标志和文字）：GBK 长度 >200 用 `NotifyAddHint(1,270,120,text)`，>100 用 `(2,270,155,text)`，否则 `(3,270,190,text)`；参数为负时什么也不做。
- 探险里 NPC 说话（0x116A）的接收函数 `0x005FDA38` 只交给空函数 `0x005C81D7`，原版不显示任何东西（`boss-lifecycle-re.md` §3.3）。关卡 XML 也没有任何对话定义（49 个关卡文件只有 BattleFields、NPCs、NPCInfo、NPCBounds、NPCSkillInfo、Skills 几种节点）。

结论：探险模式下原版从不调用 `NotifyAddHint`/`NotifyRemoveHint`，本项不做；它属于新手任务流程，实现新手任务时按上表接入。`adventure.md` 第 1.3 节“NPC 说话时回调 NotifyAddHint”的说法有误，已更正。

## 验证

- Go：`TestAdventureLeaverIsDisposedAndLosesTheNPCsEnmity`（离开名单、重复通知去重、本机不记、仇恨只清离开者）、`TestAdventureBubbleRingCountsOnlyTheLocalPlayersOwnBubbles`（远端的泡不算、同一个泡只算一次、第二个泡再加一、`resume_ms`）。
- Python：`tests/test_pve_panel.py`（回调只发一次、开局不发、速度公式及 0.5 s 下限修正）。
- 实测（本地 58000，1000016 房主仲裁 + 1000017，双端 `--autoplay`，路线 1601）：1601 清完进 1602 后 1000017 调用 `LeaveGame`，房主端侧车记 `player 13 left the match`，之后的截图里左侧 PVE 栏只剩 测6 一行（离开前两行）；房主自己的泡放下后，状态栏第一格出现约 90° 的回复圈遮罩，几秒后消失。两端无脚本异常、退出码 0，服务端这段时间零 warn/error。产物 `/private/tmp/claude-501/-Users-cduan-qqtang/2d415321-6cf4-4bec-92d7-63a0de7d5ee6/scratchpad/agentD/live1/`（`a_48.png` 离开前、`a_62.png` 离开后、`a_44_ring.png` 回复圈）。

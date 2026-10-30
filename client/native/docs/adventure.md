# 探险模式（PVE）调研：原生客户端实现所需的流程、差异和步骤

> 更正（2026-10-30）：两种 `.dat` 已经解开，方法见 `tools/decrypt_pve.py` 和 README 的"已确认的原版约定"。解密代码在 `QQTSection.dll` 里（相邻字节异或加 zlib），不在 `Client.exe` 的 TP 变形段；下文第 0 节第 4 条、第 3 节末尾和第 4 节 A 步关于 `0x890521`、TP 段的说法已经过时。

只读调研，没有修改 `/Users/cduan/qqtang`，没有运行客户端，没有访问本地服务端端口。
临时探针放在 `/tmp/qqt-tools/advprobe/`，用 `go test -overlay` 注入到包里运行，不改动仓库文件。

依据标注：**[代码]** 读了源码或反汇编；**[验证]** 跑过测试、探针或读过真实数据；**[推断]** 只是推测。

下文路径：`$R` 指仓库根目录，`$C` 指 `~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched`。

---

## 0. 结论摘要

1. **服务端已经实现了探险的房间、开局、分关、结算和奖励，但不演算对局。** 探险对局和竞技对局一样，由客户端模拟。NPC、Boss、伤害、过关门都在**仲裁方客户端（房主）**里演算。服务端只做三件事：清点 NPC 死亡数、发下一关、结算勇气值和拾取物品。[代码]
2. **探险是一个完全不同的规则对象（地图原生规则 12），不只是换几张地图。** 50 张探险地图全部是规则 12，尺寸从 20×18 到 40×40，都大于竞技的 15×13，需要卷动镜头。玩家有血量（HP），泡泡造成伤害（`PVEBombPower`）。NPC 有血量、攻击、防御、速度、移动策略、战斗策略、仇恨和技能。全队同属一队。[验证]
3. **battleengine 目前演算不了探险。** 缺规则 12 的地图加载、单队合作、血量和伤害模型、NPC 实体和 AI、过关门、复活、NPC 掉落，也缺技能系统。移动、泡泡、火焰、炸墙这些核心机制在 40×40 网格上能正常运行。[验证]
4. **最大的风险：NPC 和关卡数据只存在于加密文件里，解密代码又被 TP 保护变形过。** 每关 NPC 的位置、属性、AI 参数和过关门坐标都在 `config/battlefield_map16xx.dat`；玩家各级的血量、泡泡数和威力在 `config/pvedata.dat`。两种文件都加密并压缩过，原版读取它们的代码位于 `Client.exe` 中被 TP 变形的 `.tp3` 段（`0x874000`–`0x894000`）。[代码]
5. **单人探险可以只靠 TCP 完成。** 探险没有服务端 AI，服务端的清点和结算全部走 `REQUEST_PLAY 0x0083`。房主一人时不需要 UDP 场景流，可以先做单人版。单人开局要求背包里有激活的单人探险卡（物品 99）。[代码]

---

## 1. 从大厅到结算的完整流程

### 1.1 大厅：探险标签

| 步骤 | 脚本行为 | 宿主函数 | 服务端消息 | 依据 |
|---|---|---|---|---|
| 点"探险" | `modeExplore.OnClick` → `ui_SetGameType(2)` → `SetGameType(2)`，然后 `updateRoom()`。注意这里**不调用** `NotifyRoom` | `SetGameType` 已实现（`lobby.py:131`），只改 `session.game_type` | 下一次 5 秒刷新时发房间列表 0x0066，`GameType=2`；服务端按类型过滤（`lobby_lists.go:229`） | [代码] `uiSelRoom.py:2296-2313,2061-2066` |
| 房间格子 | `GetRoomInfo(i, GameType)` | 已实现 | 同上 | [代码] `uiSelRoom.py:1146` |
| 地图过滤按钮 | `request_UserSelGameModeList(mode)` | **未实现**（只影响过滤，探险可以先不做） | — | [代码] `uiSelRoom.py:3725` |
| 玩家信息面板 | `GetPVEData(pvePoint)` 返回 `tagPVEDATA`，含 `m_unLevel`、`m_unHp`、`m_unBombNum`、`m_fBombReSpeed`、`m_unBombPow`、`m_unSpeed` | **未实现**，数据要从 `config/pvedata.dat` 解出（见第 3.3 节） | 无（本地数据，使用 GAME_INFO 的 `m_dwExtPoint`） | [代码] `uiSelRoom.py:1265-1269,1367-1374`；`Client.exe 0x603023`（读 `config/pvedata.dat`，解开后是 `<PVEDATA>` XML，用 `point` 属性分级） |
| 建房 / 快速加入 | `CreateRoom(...)`，`JoinRoom` | 已实现，带上 `session.game_type`（`room.py:324,332`） | 0x006B 建房 / 0x008A 快速加入（`commands.go:51,81`），`GameType=2` 建出探险房；探险房最多 4 人 | [代码] `internal/game/room/state.go:89-125,453-457` |

### 1.2 房间：选关和开始

| 步骤 | 脚本行为 | 宿主函数 | 服务端消息 | 依据 |
|---|---|---|---|---|
| 在房间里改模式 | `selModeDlg` 的 exploreChk → `SetGameType(2)` + `ChangeRoomType(2)` | `ChangeRoomType` **只改本地状态，不发请求**（`room.py:408-412`），这是一个缺口 | 应该发 CHANGE_ROOM_TYPE **0x00D0**，13 字节：`u32 UIN, u32 Time, u8 GameType, u32 ContinueID`；同房间的人会收到通知 0x0435 | [代码] `uiRoom.py:1277-1292`；`change_room_type.go:9-44`；`room_dispatch.go:154-173` |
| 选关 | `setupMapList(2)` 只列出 `'pve'`；`filterMap` 在 GameType==2 时只保留第 13 列为 3 的地图，也就是每条路线的第一关（1601、1607、1612、…、1649，共 12 条）；`continueId = 序号 + 1` | `UserSelMapInRoom(mapID, continueId)` 已实现 | MODIFY_ROOM_INFO 0x0078 `(MapID, GameType, ContinueID)`。服务端只认"可选的第一关"，ContinueID 只在下一关请求里校验 | [代码] `uiRoom.py:216-236,1560-1631,1854-1870`；`room_dispatch.go:175-211`；`room_state_adapter.go:507-529` |
| 不选地图（随机） | 默认 | — | 开局时服务端从 12 条路线的第一关里随机选 | [代码] `catalog.go:1040-1053` |
| 开始 | 房主 `UserReady(0)` | 已实现 | StartGame 0x0082。单人时，背包里没有激活的物品 99 会返回 `0xF004`，提示"单人探险需要激活单人探险卡" | [代码] `match_settlement.go:90-116`；`match_start_dispatch.go:79-81` |

### 1.3 开局（每一关）

服务端 `prepareAdventureRoomMatchStart`（`match_start_dispatch.go:255-307`）的发送顺序 [代码]：

1. 0x0082 应答（2 字节 ResultID）。
2. **0x0089 GAME_BEGIN**，与竞技同一格式（`game_begin_data.go:204-`；`server.go:1479-1535`）。探险的不同之处：
   - `TeamID` 全部为 1（`adventureCooperativeTeamID`，`server.go:1281`）；
   - `ArbitratorPlayerID` = 开局的房主（`SelectAdventureArbitrator`，`match/adventure.go:515-532`）；
   - `ExtPoint` = 各玩家的探险勇气值（决定探险等级和战斗属性）；
   - `Items` 为空：探险没有竞技那只送道具的鸭子；
   - `NewItems` = 按路线规则抽出的**材料类墙内道具**（例如 30067），由客户端用 ItemSeed 分到可破坏的墙里；
   - 每个玩家的 `NewItems` = 从房间带入的准备道具（`preparedAdventureItems`）；
   - `ContinueID = -1`（使用本地 Continue.ini），`GameTimeMS = 600000`（10 分钟，`game_begin.go:20`）。
3. 约 50 毫秒后，**0x0084 携带 schema 0x138A CREATE_PVENPC_BOSS**（`pve_boss.go:12,67-89`；`server.go:1429-1444`）。格式：`u32 组数`，每组 `u16 BossID, u8 BossCount, u16 物品数, n×{u32 ItemID, u16 ItemCount, u16 DropTime}`。这是每种 NPC（按 BossID，即 NPC 基础类型）的数量和**掉落清单**；数量和物品来自 `configs/adventure-rules.json`。原版在生成战场 NPC 之前先读这张表（服务端注释："Client+0x1FCEA5"）。
4. **服务端不设对局超时**：探险分支不会安排 `competitiveTimeout`。[代码] `match_start_dispatch.go:255-307`

客户端一侧 [代码，Client.exe]：

- `CGameScene::Initialize`：地图 ID 在 [1600, 1700) 范围时，创建 `CBattleFieldMgr` 并读入 `config/battlefield_map%d.dat`（`0x5b62b7-0x5b6325`）；`CBattleFieldMgr::Initialize` 把过关门的格子（战场数据 +0x8c、+0x90）写到场景的 +0x3b84、+0x3b86（`0x5e7320-0x5e736c`）。
- 场景规则号为 12 时，宿主依次调用 `ui_SetGameMode(1)`、`ui_InitPVEPlayer()`、`ui_InitPVEBoss()`（`0x5a3d45-0x5a3daa`）。现在的原生客户端固定调用 `ui_SetGameMode(0)`（`battle/scene.py:175`）。
- `ui_SetGameMode(1)` 隐藏右侧的竞技玩家栏，改为显示左侧的 `pvePlayerList`（4 人，头像、名字、等级、血量数字和血条）、右下的 `pveFuncDlg`（仓库按钮 + 离开按钮）和 `pveTime` 计时（`uiGame.py:129-141,566-683`）。
- `ui_InitPVEPlayer()` 调用 `GetPVEPlayerInfo()`，期望返回一个列表，每项有 `m_nPlayerID, m_nRoleID, m_szNickName, m_nPoints, m_nHP`（`uiGame.py:195-219`）。
- `ui_InitPVEBoss()` 调用 `GetPVEBossInfo()`，期望返回场上**Boss**的列表（最多 3 个），每项有 `m_iNPCID, m_iImageID, m_iHP`；头像为 `object/ui/room/char/icon_<ImageID>.img`（`uiGame.py:150-185`）。原版实现在 `0x534240`：规则号不是 12 时返回空列表；是 12 时遍历场景中的 NPC，只取 IsBoss 为真的（虚函数 +0x4c）。
- 原版还会回调 `ui_UpdatePVEPlayerLife(id, hp)`、`ui_UpdatePVEBossLife(id, hp)`、`ui_DisposePVEPlayerLeave(id)`，NPC 说话时回调 `NotifyAddHint(flag, x, y, text)` / `NotifyRemoveHint()`（格式串在 `0x7f30ec`、`0x7f3264`、`0x7f3134`、`0x7f2e8c`）。血条控件类型是 `DYLIFESTRIP`，宿主还缺 `Win_SetStripNum`、`Win_SetStripTotalNum`、`Win_SetStripDestNum`。[代码] native_coverage 报告

### 1.4 对局中：仲裁方要产生的消息

探险与竞技共用 QQTPPP Type-2 UDP 场景流和 TCP 0x0083 可靠副本（见 `match-protocol.md`）。下表只列探险特有的消息，以及服务端结算依赖的消息。字段格式都在 `internal/protocol/game/` 中。

| schema | 名称 | 谁发 | 服务端处理（`game_event_dispatch.go`） | 字段格式 |
|---|---|---|---|---|
| 0x138C | NOTIFY_CREATE_PVENPC | 仲裁方读完战场文件后，用 Type-2 发给同伴 | 只回 ACK（允许范围 0x1389–0x138C） | **Go 里没有编解码**。`QQTMsgData.bin` 元数据：`BossNum` + `PVENPC[]`，每个 PVENPC 依次为 `u32 NPCID, u32 NPCBaseID, u32 IsBoss, char[20] NickName, u32 ImageID, MoveStrategy, Defense, HP, Speed, Harm, BattleStrategy, BornRow, BornCol, View`（共 72 字节）。消息号 [验证] `QQTMsgData.bin` 0x4a8c；字段顺序 [代码] 元数据 0x52424；网络上的字节序和是否紧排是 [推断] |
| 0x0FA2 / 0x0FA3 | NPC 的移动、放泡 | 仲裁方代发，包里的 PlayerID 是 NPC 编号（≥30000） | 0x0065 快速上传接受"仲裁方 + 参与者或 ≥30000 的编号"（`room_fast_dispatch.go:795-804`）；TCP 0x0083 的 0x0FA3 要求参与者身份，所以 NPC 放泡不要走 TCP | 同竞技 |
| 0x10F4 | PLAYER_BE_HARMED（玩家或 NPC 掉血） | 受伤一方或仲裁方 | 探险里走 `requireCompetitiveHarmTarget`，要求竞技对局，**会被拒绝**（只记日志，不应答），所以只走 Type-2 | 13 字节：`u16 ObjectID, u32 Time, u16 PosX, u16 PosY, u8 IsAvatar, u16 LossHP`（`native_rule_event.go:107-114,303`） |
| 0x1169 / 0x116A | NPC 用技能 / NPC 说话 | 仲裁方 | 只验证仲裁方身份后回 ACK（`:366-389`） | `npc_skill.go:25-125` |
| 0x116B | NOTIFY_NPC_DROPITEM（NPC 掉落） | 仲裁方 | 只验证仲裁方身份后回 ACK（`:706-734`） | `u16 ObjectID, u16 PosX, u16 PosY, u8 n, n×GAME_ITEM`（`combat_event.go:74,211-232`） |
| 0x0FAC / 0x0FAD | 拾取请求 / 拾取通知 | 玩家 / 仲裁方 | **0x0FAD 会计入奖励**：`RecordPlayerItemPickup` 按"玩家 + 时间 + 物品 + 坐标"去重，物品在结算时进背包（`:929-1054`；`match/adventure.go:391-418`）。**必须用 TCP 镜像** | 14 字节（`combat_event.go`） |
| 0x0FA7 | NOTIFY_PLAYER_DIE | 玩家自己；NPC 死亡由仲裁方发 | **服务端用 PlayerID 区分**：是参与者就记为玩家死亡；否则当作 NPC 死亡，并且这个 ID **必须是 adventure-rules 里的 BossID**（NPC 基础类型），不然报错"no NPC %d combat rule"，这一关永远清不完（`adventure_death_dispatch.go:45-94`）。每记一次 NPC 死亡，全队每人加一份勇气值（`match/adventure.go:241-277`）。**必须用 TCP 镜像** | `u16 PlayerID, u32 Time, u16 X, u16 Y, u8 n, n×GAME_ITEM`。原版抓包里的 NPC 死亡用的就是 30033（`game_event_test.go:44`） |
| 0x0FAB | NOTIFY_PLAYER_SAVED | 仲裁方 | 增加救人统计；目标若已阵亡，则恢复为存活（医疗类准备道具）（`:852-858`） | 12 字节 |
| 0x0FBA | NOTIFY_PLAYER_RELIVE | 仲裁方 | `RecordPlayerRelive`（`:1194-1227`） | 8 字节（`player_relive.go`） |
| 0x1174 → 0x1175 | 使用准备道具 | 玩家 | 服务端扣物品，推送 0x1175 和背包刷新（`:1055-1112`） | — |
| **0x1178** | **REQUEST_GAME_NEXTMAP（进过关门）** | 站到门上的玩家（TCP 0x0083） | 见第 1.5 节 | 14 字节：`u16 PlayerID, u32 Time, u32 ContinueID, u32 NextMapID`（`game_event.go:183-200`）。抓包：`0001 00009d2a 00000001 00000642`，即第 1 条路线，下一关 1602 |
| 0x0FBB | 客户端 GAME_OVER | 仲裁方（可选） | 改写为探险模式、全队同胜同负后广播（`match_game_over_dispatch.go:67-94`） | §9.1 |

原版发送下一关请求的条件 [代码 `0x615d34-0x615dfa`]：本地玩家所在格子（像素坐标 ÷ 40）等于过关门格子，并且距离某个时刻（推测是本关开始或清完 NPC）已满 10000 毫秒；每关只发一次。请求体中的 ContinueID 和 NextMapIndex 分别取自战场数据的 +0x84 和 +0x88，也就是战场 XML 的 `ContinueID`、`NextMapIndex`。

### 1.5 过关、分关和结算

服务端 `handleAdventureNextMap`（`adventure_next_map_dispatch.go:28-139`）[代码]：

- 本关 NPC 没清完时拒绝（`PrepareNextStage`，`match/adventure.go:628-669`）。下一关 ID 的校验允许 0、目录中真正的下一关、以及"当前关 + 1"（兼容 1646→1647 实际跳到 1648 的情况）。
- **还有下一关时**：
  1. 应答（0x0083 内带 RESPONSE_GAME_NEXTMAP 0x1179，3 字节全为 0）；
  2. 向存活者发 **0x0084 + schema 0x1177 NOTIFY_GAME_NEXTMAP**，正文就是一份新的 GAME_BEGIN_DATA（不带长度前缀），而且**换一个新的 GameID**（原版靠 GameID 变化区分新旧两关的同伴数据包）；
  3. 约 50 毫秒后再发一次 0x138A；
  4. 本关已阵亡的玩家单独收到一份"失败"GAME_OVER 和背包刷新，被移出房间，5 秒后服务端发 KickOffRoom（ReasonID 4）关掉他的场景（`adventure_stage_settlement.go:17,61-233`）。
- 原版客户端收到 NEXTMAP 时，先记下旧 GameID，再用新的 GAME_BEGIN_DATA 重新初始化场景（`0x5a2452-0x5a24a3`）。[代码，后半句含推断]
- **最后一关**：
  - 清完 NPC 后不需要进门。服务端启动 10 秒"捡东西宽限期"，期满后向全队推送胜利 GAME_OVER（`adventure_death_dispatch.go:82-92`；`adventure_victory.go:12,47-115`）；
  - 若有人在最后一关清怪前进门，服务端直接判全队胜利（`adventure_next_map_dispatch.go:50-89`）。
- **全队阵亡**：最后一人的 0x0FA7 到达后 750 毫秒推送失败 GAME_OVER（`adventure_death_dispatch.go:96-136`）。中途离开或断线：最后一个活着的人离开，也按失败结算（`server.go:1575-1669`）。
- **GAME_OVER**（0x0FBB，`GameMode=2`）：每人三组字段，分别是杀怪、救人、拾取的次数和分数（`game_result.go:36-53`）。胜利时勇气值 ×1.5，失败时 ×0.7（`settlement_projection.go:171-220`）。
- **结算之后**：
  - 勇气值累加到 `ExtPoint`（探险等级 0～30，`progression.go:76-100`）；
  - 拾取的物品写入背包，100 毫秒后用 0x00D9 推送物品的最终数量（`adventure_settlement_inventory.go`）；
  - 房间回到准备状态，没有"回到房间"消息（`match_settlement.go:177-254`）。

[验证] `go test ./internal/game/match -run Adventure` 和 `./internal/server/probe -run 'Adventure|PVE'` 全部通过（39 个通过，1 个因为仓库里没有 `runtime/client-patched` 而跳过）。

---

## 2. 探险和竞技在客户端一侧的差别

| 方面 | 竞技（已实现） | 探险 | 依据 |
|---|---|---|---|
| 地图规则 | 原生规则 1（普通） | 全部为**原生规则 12** | [验证] 探针读了 50 张 pve 地图 |
| 地图尺寸 | 15×13，600×520 画在 (10,27) | 20×18 到 40×40（例如 1601 是 40×17，1602 是 40×40，1650 是 21×18），需要**卷动镜头**。PVE 界面元素铺在整个 800×600 上（顶部 Boss 血条、左侧队伍栏、右上计时、右下功能栏），所以探险的场地视口可能是全屏 | [验证] 尺寸；[推断] 视口 |
| `.map` 结构 | v3/v4 | 都是 v4。表结构与竞技一致：墙内道具规则 0 条，隐藏道具格 65～140 个，出生组 A 4 个，出生组 B 0 个。Go 的 `loadCompetitiveMapMetadata` 能读到末尾，但随后因"超出 15×13 的投弹边界"报错（`catalog.go:589`）。Python 的 `mapfile.parse_map` 已支持 v4 尺寸 | [验证] |
| 地图元素素材 | — | 50 张地图用到 340 种元素，图片全部能在 `object.pkg` 里找到（`mapElem/pve`、`exploration`、`common` 等）。其中 13 种（16202、16203、16207、16208、16210、16212、16216、16217、16220、16222、16225、16226、16227）在 `mapElem.py` 里没有属性 | [验证] |
| 队伍 | 至少两队 | 全队同为 1 队，合作 | [代码] |
| 伤害模型 | 被泡泡炸中即被困，再被碰到就阵亡 | 玩家和 NPC 都有**血量**。玩家属性（HP、泡泡数、泡泡恢复速度、威力、速度）由探险等级查 `pvedata.dat` 得到；泡泡对 NPC 的伤害 `PVEBombPower` 可以被道具改变；NPC 有 Harm（攻击）、Defense、HP 和 TouchHarmValue（碰撞伤害）。掉血的消息是 0x10F4。血量归零后怎样进入被困、救人或阵亡，没有追到 | [代码] 字段名见 `Client.exe 0x7f5e3c-0x7f6050,0x7f6780-0x7f68ec`，`uiSelRoom.py:1367-1374`；状态转换 [推断] |
| NPC 由谁演算 | 竞技 AI 由服务端演算 | **由仲裁方客户端演算**：读战场文件生成 NPC，跑 NPC 状态机（"NPC STATE CHANGE TO NPC_BATTLE / NPC_BACK"，`0x5bade5`）、移动策略、战斗策略、视野、仇恨、技能触发（HP、时间、仇恨阈值），用 0x138C 把 NPC 列表广播给同伴，再代发 NPC 的移动和放泡，并发 0x1169/0x116A/0x116B/0x0FA7。同伴只接收 | [代码] `room_fast_dispatch.go:795-804`；`Client.exe 0x5bade5` 起的 NPC 代码不在 TP 段，可以读 |
| NPC 编号 | — | 运行时编号 ≥30000。服务端按 BossID（NPC 基础类型）计死亡数；`NOTIFY_CREATE_PVENPC` 同时带 NPCID 和 NPCBaseID | [代码]；NPCID 和 NPCBaseID 的关系 [推断] |
| 地图和关卡切换 | 一局一张图 | 一条路线 2～7 关（`Continue.ini` 的 12 条路线）。清怪后进门 → 0x1178 → 0x1177（新 GameID、新地图、新种子）→ 0x138A。阵亡的队友被淘汰出局 | [验证] 探针列出 12 条路线，其中 11 号路线为 1646→1648 |
| 死亡上报 | 0x0FA7 / 0x0FA8 / 0x0FA9 由服务端判胜负 | 玩家死亡用 0x0FA7，全队阵亡判负。NPC 死亡同样用 0x0FA7，但 ID 是 BossID。没有击杀请求和击杀判定，NPC 死亡不记在某个人名下 | [代码] |
| 胜负 | 服务端按剩余队伍判定 | 最后一关清怪 → 10 秒宽限 → 全队胜利；全队阵亡 → 失败；没有超时判定 | [代码] |
| 结算奖励 | 积分、糖币 | 勇气值（累入 ExtPoint，胜 ×1.5，负 ×0.7），外加拾取的材料、宠物粮等进背包 | [代码] |
| 鸭子送道具 | 有（`Items`） | 没有（`Items` 为空） | [代码] |
| AI 卡 / UDP 握手 | 对 AI 需要 AI 卡和握手 | 单人需要物品 99（单人探险卡）；没有服务端 AI，单人时不需要 UDP | [代码]；"单人不需要 UDP" 为 [推断]，依据是服务端结算全走 0x0083 |

### battleengine 能不能演算探险

**不能直接演算。** 逐项如下：

- 入口只接受规则 1：`GridFromCompetitiveMap` 要求 `NativeRule == 1`（`map_adapter.go:14`）。`mapdata.CompetitiveMap` 本身也跳过 `pve` 地图（`catalog.go:382`）。[代码]
- 至少要两支队伍：`engine.go:132-133` 报"standard battle requires at least two teams"。[验证] 单队探针直接失败。
- 大地图没有问题：把探针改成两队、网格 40×40，角色在 (1220,1420) 附近能移动，放泡、爆炸、被困都正常（事件统计 `{moved 20, exploded 1, trapped 1}`）。[验证] `/tmp/qqt-tools/advprobe/engine_probe_test.go`
- 还缺这些：
  - 规则 12 地图加载（绕开 15×13 的投弹边界）；
  - 单队合作；
  - 血量和伤害（泡泡威力、防御、碰撞伤害），替换"一炸就困"；
  - NPC 实体：出生、移动和战斗策略、仇恨、视野、技能、掉落、死亡；
  - 过关门、复活、准备道具；
  - 由等级决定的玩家属性；
  - 仲裁方要发的 0x138C、0x10F4、0x116B 等消息。
- 能直接复用的：移动和碰撞、泡泡和火焰的传播、炸墙、墙内道具的种子洗牌（`HiddenPickupsFromCompetitiveWallItems`；探险墙里藏的是材料，是否使用同一套洗牌算法为 [推断]）。
- 改 battleengine 会影响服务端 AI 共用的代码。如果要改，必须做成默认关闭的选项，并请用户单独过目（lessons.md 第七节）。

---

## 3. 画面元素和素材

| 元素 | 位置 | 状态 |
|---|---|---|
| 地图元素 | `data/object.pkg` 中 `object/mapelem/{pve,exploration,common,desert,snow,...}/elem<n>_{stand,die,trigger}.img`，规则沿用 `battle-engine.md` §1.4 | [验证] 340 种全部能找到 |
| NPC / Boss 外观 | 推测按 `ImageID` 对应 `object/player/player<ImageID>.ini`。目录里有 player57、58、67～85、100～144 这些非角色编号，内容基本只有一个 cloth 部件，例如 player67 → `cloth16701_{stand,walk,die}.img`。68 个部件里 65 个有图，缺 player100（body21500）、player73（cloth173701）、player80（cloth180701） | 文件 [验证]；ImageID 与 ini 的对应 [推断] |
| Boss 头像 | `object/ui/room/char/icon_<ImageID>.img`（有 67、69、72、73、75、77、78、79、81、85、117、118、119、124、125） | [验证] |
| 血条 | `object/ui/game/img_lifenull.img`、`img_lifered.img`（Boss），`img_playerlife*.img`（玩家） | [验证]；`DYLIFESTRIP` 控件宿主未实现 |
| PVE 功能栏、仓库按钮 | `object/ui/game/dlg_pvefunc.img`、`btn_storage.img` | [验证] |
| 过关门 | `effect/transdoor.eff`、`transdoor_enter.eff`、`transdoor_trans.eff`（散放在磁盘上），类名 `CMETransDoor`（`0x613c2c`） | [验证]；**.eff 格式宿主未实现** |
| 关卡提示、NPC 对话 | `NotifyAddHint` 使用 `object/ui/game/chatpopo{1,2,3}.img` | [代码] `uiGame.py:8-29` |
| 计时 | `object/ui/common/number3.img`（NUMLABEL，已实现） | [代码] |
| 大厅、房间图标 | `map/bigicon/pve.img`、`map/pve1..12.img`（路线缩略图）、`res/uires/icon/npcicon/npcicon1..7.img` | [验证] |
| 关卡数据 | `config/battlefield_map1601..1650.dat`（加密，437～1033 字节）。解开后是 XML，字段名可从 `Client.exe` 字符串看出：`BattleFields/BattleMap{map, mapCfgSize, ContinueID, NextMapIndex, LevelRow, LevelCol}`、`BattleNum`、`NPCs/NPCInfo{NPCID, NickName, ImageID, IsBoss, MoveStrategy, Defense, Speed, Harm, BattleStrategy, BornRow, BornCol, View, NPCNum, NPCBounds{BeginRow, BeginCol, EndRow, EndCol}, NPCSkillInfo{SkillNums, SkiLLID, SkillTrigger, HP/Time/Enmity/GEnmity 的 Upper/Lower/Trigger}}` | [代码] 字符串表 `0x7f5e3c-0x7f6050`；解密函数在 TP 段 |
| 等级属性 | `config/pvedata.dat`（515 字节，解开后是 2851 字节的 `<PVEDATA>` XML，`lev_0..lev_30`）。仓库只有各级勇气值门槛（`progression.go:76-82`），没有 HP、泡泡数等属性 | [代码] |

两个 `.dat` 文件都不是 zlib 格式，偏移 0～15 都试过；也不是 QQ-TEA，长度不是 8 的倍数；熵约 7.5。[验证]

原版读取它们的路径 [代码]：

- 战场数据经过 `0x892588` 读入，然后由 TinyXML 解析（`0x59a5f0` 即 `FirstChild`）；
- `uncompress` 的调用点 `0x890521` 也在 TP 变形段；
- `pvedata.dat` 通过 room 模块的虚函数 +0x64 读入（`0x603023`）。

这几处代码都夹着 `pushfd/popfd`、`lea esp` 一类的花指令，靠逐条静态阅读还原很费力。

---

## 4. 建议的实现步骤

估算按一个人全职计，**单人探险优先**。

| # | 步骤 | 内容 | 工作量 | 主要风险 | 复用 |
|---|---|---|---|---|---|
| A | 解开两种 `.dat` | 首选在 Python 里用 Unicorn 模拟执行 `Client.exe` 中的解密函数：把 PE 映像映射进模拟器，传入缓冲区，取出明文 XML。不需要 Wine，也不运行原程序。在 `prepare-ui.sh` 阶段把结果生成到 `generated/`，不提交。备选：静态还原变形代码；或请用户在 Windows 上用 `internal/tooling/winlaunch` 一类工具导出明文 | 3～10 天 | **最高**：入口和调用约定要从变形代码里找；函数中途调用别的 import 时要打桩 | 技能 `qqt-binary-re` |
| B | 大厅、房间、开局界面 | 实现 `GetPVEData`（依赖 A，可先填常量）；`ChangeRoomType` 改为发 0x00D0；实现 DYLIFESTRIP 控件和 `Win_SetStrip*`；实现 `GetPVEPlayerInfo`、`GetPVEBossInfo`；规则 12 时调用 `ui_SetGameMode(1)`、`ui_InitPVEPlayer()`、`ui_InitPVEBoss()`；`pveTime`；用 GM 发物品 99 测单人开局 | 2～3 天 | 低 | `lobby.py`、`room.py`、`net/room.py` |
| C | 探险画面 | 加载大地图（复用 `mapfile.py`）、跟随本地玩家的卷动镜头、PVE 布局（去掉 gametop/gameleft，视口待查）、NPC 外观（复用 `avatar.py` 的部件叠加）、Boss 和队伍血条、`.eff` 门特效（需要新写解析器）、NPC 头顶对话 | 5～8 天 | 中：视口位置、`.eff` 格式、绘制顺序都要逆向确认 | `scene.py`、`render.py`、`avatar.py` |
| D | 规则演算 | 先读懂 `Client.exe` 中规则 12 的代码：NPC 状态机（`0x5bade5` 一带）、移动和战斗策略、技能触发、伤害公式、玩家属性、过关门（`0x615d34`）。然后在 sidecar 里实现 PVE 演算 | 读代码 1～2 周，实现 1～2 周 | **高**：AI 和伤害公式很难做到与原版一致；改 battleengine 需要用户批准 | 建议给 battleengine 加默认关闭的选项（单队合作、血量伤害、地图规则 12），NPC AI 放在 `cmd/qqt-battle-sidecar` 自己的包里 |
| E | 单人联网 | 处理 0x138A；用 TCP 0x0083 镜像 0x0FA7（NPC 用 BossID）、0x0FAD、0x0FAB、0x0FBA；进门发 0x1178；收到 0x1177 时用新的 GAME_BEGIN_DATA 和新 GameID 重建 sidecar；最后一关的 10 秒宽限；GAME_OVER 结算画面改用探险字段（杀怪、救人、拾取、勇气值） | 3～5 天 | 中：每个裁定都要报给服务端，并核对服务端日志 | `online.py`、`net/match.py`、sidecar 的 online 模式 |
| F | 多人（与原版客户端同局） | Type-2 广播 0x138C，NPC 包代发；非仲裁方要接收 0x138C 并按收到的消息驱动 NPC；处理分关淘汰（单独的 GAME_OVER 和 5 秒后关场景）以及仲裁方变更 | 1～2 周 | 高：0x138C 的字节布局、NPC 动作的同步都要用真机抓包核对 | `cmd/qqt-battle-sidecar/online.go` 的 UDP 部分 |

**最小可玩路线**：B → C →（D 的近似版）→ E。用 `adventure-rules.json` 里每关的 NPC 数量和属性，配上简单 AI，在地图的空地或出生组附近生成 NPC，过关门放在可推断的位置。这样**不依赖解密就能走通服务端的完整流程**（开局、清怪计数、下一关、结算、奖励进背包）。缺点是关卡布局和 NPC 行为与原版不一致，过关门的位置也没有依据。

**忠实还原路线**：A → B → C → D → E → F，合计约 6～10 周，A 和 D 的不确定性最大。

---

## 5. 需要用户决定的问题

1. **目标是忠实还原还是先能玩？** 近似版可以较早验证服务端的流程，但 NPC 的位置、属性和过关门都是自定的。
2. **是否允许用 Unicorn（Python 包，装在 `.venv` 或 `/tmp` 下）模拟原版的解密函数？** 或者用户能否在 Windows 上导出 `battlefield_map*.dat` 和 `pvedata.dat` 的明文？
3. **是否允许修改 `internal/game/battleengine`？** 会加单队合作、血量伤害、规则 12 等默认关闭的选项，与服务端 AI 共用。另一个选择是在 sidecar 里另写一套 PVE 引擎，代价是要复制移动和泡泡的代码。
4. **先只做单人（需要物品 99），还是一开始就要支持与原版客户端多人同局？**
5. 解出的关卡数据属于腾讯资源，建议像 `generated/` 一样只在本地生成、不提交。是否同意？

---

## 附：本次产生的文件

- `/tmp/qqt-tools/advprobe/probe_test.go`、`overlay.json`、`out.txt`：读取真实客户端的探险目录（12 条路线）、50 张地图的尺寸和解析错误，以及 adventure-rules 对 1601、1602、1606、1650 抽出的掉落。
- `/tmp/qqt-tools/advprobe/engine_probe_test.go`、`overlay-engine.json`：battleengine 在 40×40 网格上的演算，以及单队时的报错。
- `/tmp/qqt-tools/advprobe/msgdata_strings.txt`：`QQTMsgData.bin` 的字符串和偏移。
- `/tmp/qqt-tools/advprobe/vt64.txt`：虚函数槽 0x64 的候选实现（没有找到解密函数）。

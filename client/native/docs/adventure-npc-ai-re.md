# 探险（规则 12）PVE NPC 的 AI、仇恨与技能：Client.exe 逆向

范围：探险模式 NPC（0x138C 生成，类虚表 `0x007A1F54`）的决策频率、战斗/归位状态机、仇恨、MoveStrategy 1～5、寻路、技能表（XML）与技能本体（道具系统），以及受伤保护。
战斗规则（伤害、分区、门、死亡掉落）见 `adventure-combat-re.md`；竞技 Boss 的控制器框架见 `boss-ai-re.md`。本文不重复这两篇已有的结论。

方法：静态读 Ghidra 伪 C + capstone 核对；VM/变形代码（NPC 初始化 `vtbl+0xdc`、进入视野 `vtbl+0xcc`、受伤 `vtbl+0x58`）用 Unicorn 黑盒运行（脚本思路同 `tools/probe_vm.py`，另加了 CBattleFieldMgr 真实加载 1601/1602/1608/1650）。

**标注**：**[证实]** 伪 C + 汇编核对；**[伪C]** 只读伪 C；**[运行]** Unicorn 实际跑出来的字段值；**[推断]** 有依据但未完全追到；**[未知]**。

**记号**：N = NPC 对象；P = `N+0x400` 属性对象；M = 角色管理器（`N+0x3fc`，`M+0x3838` 游戏时间 ms，`M+0x3824 == M+0x3830` 即本机是仲裁方，等价于 `S+0x4b34 == S+0x4b40`）；格子 `col = int(x)/40, row = int(y)/40`；方向码 0 右 1 上 2 左 3 下 9 停。

---

## 0. 结论摘要

1. **PVE NPC 不用 `FUN_005df20b` 控制器**：初始化后 `N+0x888 == 0`（[运行]），所以放泡决策 `vtbl+0x6c`（`0x005B050C`）什么都不做；移动决策是自己的 `vtbl+0x70 = 0x005BAE70`。**PVE NPC 永远不放泡**（另外属性对象里可用泡数也是 0，见 §5）。
2. **频率**与竞技 Boss 相同（`boss-ai-re.md` §1.2）：只在仲裁方、开局 3000 ms 后，移动命令 `0x006112AF` 每 **7 帧**调用一次 `vtbl+0x70`；返回 9 时计数器不清零，下一帧立即再决策。技能检查就在 `vtbl+0x70` 里，所以也是同一频率。
3. **战斗/归位**（`0x005BADE5`，每次决策先做）：统计"站在 NPCBounds 内、活着的玩家"的仇恨之和 `GEnmity`；之和 ≥1 进入战斗（MoveStrategy 暂存后改成 4，战斗计时清零），之和 =0 退出战斗（恢复原 MoveStrategy）。切换时清空路径。
4. **仇恨**：按玩家座位号 `+0x396` 存在 NPC 的 map `N+0x88c` 里。来源只有三个：①进入视野 +20（仲裁方组件 `0x00616143`，玩家在 NPCBounds 内且 `|dx|/40 ≤ View、|dy|/40 ≤ View`，而且该玩家的仇恨条目**已存在且为 0**）；②在当前分区内放泡：给所有**已在战斗中**的 NPC 加 `5 × P.vtbl+8`（`P+0x2b4`，推断为 pvedata 的 level）；③道具效果 `Enmity`。**没有随时间衰减**；只有目标死亡后，战斗寻敌时给它加 −10,000,000（下限截到 0）。
5. **MoveStrategy**（XML 里 467 个 NPC 全是 3，战斗中是 4）：1/2 = 沿 NPCBounds 四个角巡逻（1 随机正反向、2 顺序），失败永久改成 3；3 = 每次随机走到相邻一格；4 = 追 BattleStrategy 选出的玩家；5 = 回出生格。寻路器限制在 NPCBounds 矩形内，A*（步长 1，切比雪夫启发，最多 500 次出队），泡和不可通行的地图元素是障碍。
6. **BattleStrategy = 选目标**：0 = 范围内仇恨最低的玩家，1 = 最高（466/467 个 NPC），2 = 不选（站着不动，只放技能）。
7. **技能就是道具**：SkiLLID 是 PropSet（`config/errorPPP.ini` 解密出的 XML）里的道具 ID。条件满足时仲裁方发 **0x0FB0**（NOTIFY_PLAYER_USE_ITEM，TCP+组播+本机），各端照道具状态机执行：准备（NPC 停住、不能再放技能）→ 陷阱 / 飞行 / 生效；生效时仲裁方算出受影响的角色发 **0x10F7**；伤害由**受害者本机**按效果 `HP=-v` 自发 0x10F4。冷却按道具的 **Tier** 查 `PropFireIntervalTable`，同一 NPC 同一 Tier 共用冷却。
8. **PVE NPC 不发 0x1169**（只有竞技 AIType 5 的 `0x005B4947` 发）。**0x116A 台词**只给 ImageID 为 24/28/34 的 NPC（探险里只有 1601 的"新手训练员" 30033，ImageID 28）。
9. **受伤保护**：NPC 的 `vtbl+0x58`（`0x0087C8FD`）HP>0 时同样调用 `0x005B5202(3000)`（[运行]，`N+0x330=1, N+0x334=3000`），所以 NPC 被打后也有 **3000 ms 免伤**，期间爆炸、火焰都不判定它（`0x005AD175` 为假）。
10. **运行时 NPCID = 30300 + 序号**（序号是 CBattleFieldMgr 读 XML 时的全局计数，构造函数 `0x005E71AF` 清零，赋值在 `0x005E7A33`）[证实+运行]。sidecar 已改用 30300 起。

---

## 1. NPC 对象字段（初始化 `vtbl+0xdc` = `0x005BC907` → VM，Unicorn 实跑）

构造函数 `0x005BA4F2(M, entry.NPCID, entry.ImageID, team=9)`：`+0x328 = +0x32a = NPCID`，`+0x394 = ImageID`（即 RoleID 字节），`+0x395 = 9`（队伍），`+0x93c = +0x940 = 1`，`+0x8c0`（View）`= 3`。

`vtbl+0xdc(entry)` 写入的字段（1608 第 2 个 NPC：NPCID 30301、Base 30048、MoveStrategy 3、Defense 500、HP 15000、Speed 2、Harm 450、BattleStrategy 1、Born (29,17)、View 3、Bounds (27,10)-(39,21)）[运行]：

| 偏移 | 值/来源 | 含义 |
|---|---|---|
| `+0x32a` | entry+4 NPCBaseID | 发给服务端的基础 ID（`vtbl+0x3c`） |
| `+0x396` | 8 | 自己的座位号（仇恨 map 的键空间与玩家共用） |
| `+0x7cc/+0x7ce` | BornRow/BornCol | 出生格；`+0x7d0/+0x7d4` 出生点像素 float |
| `+0x8a0` | MoveStrategy | 当前移动策略（战斗时被改成 4） |
| `+0x8a4` | — | 进战斗前保存的 MoveStrategy |
| `+0x8a8` | 0 | 是否战斗中（`vtbl+0xb4`） |
| `+0x8ac` | BattleStrategy | 选目标方式（§3.3） |
| `+0x8b0/+0x8b4/+0x8b8/+0x8bc` | BeginCol, BeginRow, EndCol−BeginCol, EndRow−BeginRow | NPCBounds（`vtbl+0xb8` 返回 `&N+0x8b0`）。判定 `(col−BeginCol) ≤u W && (row−BeginRow) ≤u H`，**两端都含** |
| `+0x8c0` | View（u16） | 视野格数 |
| `+0x8e4` | 0 | 战斗目标（角色指针） |
| `+0x8e8` | Harm | 接触伤害（`vtbl+0xd8` 读、`+0xbc` 加） |
| `+0x8ec/+0x8ed` | BornCol/BornRow | 当前寻路目标格 {col,row}（初值=出生格，MoveStrategy 5 用） |
| `+0x8f4` | — | 战斗持续时间 ms（`0x005BACC0` 每帧在战斗中累加，否则清零） |
| `+0x8f8` | 寻路器对象 | 虚表 `0x007A374C`（§4.6） |
| `+0x8fc` | vector | 当前路径，元素 3 字节 `{col, row, dir}`，`+0x900` begin、`+0x904` end |
| `+0x91c` | vector | NPCBounds 四角 `(BC,BR) (EC,BR) (EC,ER) (BC,ER)`（`+0x920` begin），既是巡逻点也是寻路多边形 |
| `+0x92c` | 0 | 巡逻点下标 |
| `+0x88c` | map<u8 seat, int> | 仇恨表（`+0x890` 是 end 结点，`+0x898` 元素数） |
| `+0x89c` | — | 本次决策算出的 GEnmity（总仇恨） |
| `+0x8c8` | vector<0x38> | 技能表（0x138C 接收时由 `0x005BBF7C` 填入，§6.1） |
| `+0x8d8` | vector<0x38> | 本次满足条件的技能 |
| `+0x938` | Defense | 防御（`vtbl+0x48` 读、`+0x80` 加） |
| `+0x93c` | 1 | 可移动计数（`vtbl+0x88` 减、`+0x8c` 加，≤0 时不移动） |
| `+0x940` | 1 | 可放技能计数（`vtbl+0x90` 减 "stop use Skill"、`+0x94` 加 "resume use Skill"） |
| `+0x944` | IsBoss | |
| `+0x930/+0x934` | 0 | 一个 bool+倒计时（`0x005BACC0` 每帧减 dt），谁设置 [未知] |

属性对象 P（虚表 `0x007A1B7C`，与 PVE 玩家相同）由 `0x005AA69E(0, 10, 4, 10, Speed, 10, HP)` 初始化 [运行]：可用泡数 0（上限 10）、**泡威力（臂长）4**（上限 10）、速度档 = XML Speed（上限 10）、HP。`P+0x2f0`（PVEBombPower）保持 0。
速度档查 NPC 速度表 `0x007F4988`（`boss-ai-re.md` §1.3）。

**运行时 ID**：`0x005E7A33` 读 `NPCID` 属性时写 `entry+4 = XML NPCID`、`entry+0 = 30300 + g_counter++`（`DAT_0085dc18`，在 CBattleFieldMgr 构造 `0x005E71AF` 清零）[证实]。Unicorn 加载 1601/1602/1608/1650 都是第一个 NPC 30300，按 XML 顺序递增。

---

## 2. 调用链与频率

```
每帧 0x005C87B0（仅仲裁方，开局 >3000 ms，NPC 列表非空）
  for npc in NPC 列表: 0x005F43B1(npc)       // 规则 12 组件表（adventure-combat-re §1.2）
     ├─ 移动命令 0x006112AF：N+0x3b7 ∈ {1,0xB}、状态非 7/8 时
     │      cnt(cmd+8) > 5 ? (d = N.vtbl+0x70(); d==9 ? 停下(不清计数) : 设方向 d, cnt=0) : cnt++
     ├─ 放泡命令 0x00610BA0：N.vtbl+0x6c() → N+0x888==0 → 直接返回
     ├─ 视野组件 0x00616143（§3.2）
     └─ ……
  0x005F3D0A：按方向推进所有 NPC，合包发 0x0FA2
另外每帧（推断各客户端都跑）调用 N.vtbl+0x30 = 0x005BACC0：基类更新 0x005ABE96、+0x934 倒计时、战斗计时 +0x8f4
```

`vtbl+0x70 = 0x005BAE70` [证实]：

```
battleSwitch()                          // 0x005BADE5，§3.1
if N+0x940 > 0: N.vtbl+0xe0()           // 0x005BBAFA 技能（仅战斗中），§6.2
if N+0x93c > 0 && MoveStrategy != 0:
    switch MoveStrategy: 1→0x005BAEF1, 2→0x005BB069, 3→0x005BB1E1, 4→0x005BB33A, 5→0x005BB510
return 9
```

注意顺序：技能先于移动。放技能时本机分发的 0x0FB0 会立刻让道具进入准备状态，`+0x93c` 减到 0，于是同一次决策的移动直接返回 9（NPC 停下）。

---

## 3. 战斗状态机与仇恨

### 3.1 `0x005BADE5` 战斗/归位切换 [证实]

```
total = GEnmity()                       // 0x005BBBFF，同时写 N+0x89c
if N+0x8a8 == 1:                        // 战斗中
    if total != 0: return
    log "NPC STATE CHANGE FROM NPC_BATTLE"
    N+0x8a8 = 0; N+0x8f4 = 0; MoveStrategy = N+0x8a4
else:
    if total < 1: return
    log "NPC STATE CHANGE TO NPC_BATTLE"
    N+0x8f4 = 0; N+0x8a8 = 1; N+0x8a4 = MoveStrategy; MoveStrategy = 4
clearPath()
```

`GEnmity()` `0x005BBBFF`：

```
sum = 0
for a in M+0x1338（全部角色）:
    if isNPC(a.id) (0x00534AD0): continue
    if a 状态 ∈ {7 死亡, 0xC}: continue
    if !inBounds(a) (0x005BAD90，a 的格子在 NPCBounds 内): continue
    sum += enmity[a+0x396]                // map operator[]（0x005BD976）：不存在就插入 0
return N+0x89c = sum
```

所以"进入战斗"需要至少一名在 NPCBounds 内的活玩家仇恨 >0；"离开战斗"需要范围内所有活玩家仇恨都为 0（离开范围、死亡都算不计入）。

### 3.2 仇恨的来源

| 来源 | 地址 | 谁执行 | 规则 |
|---|---|---|---|
| 进入视野 | 组件 `0x00616143` → `vtbl+0xcc` = `0x0087D579`（VM）→ `AddEnmity(seat, 20)` | 仅仲裁方，每帧，NPC×玩家 | 条件：玩家非 NPC；`vtbl+0xc4`（`0x005BAD0C`）为真：玩家在 NPCBounds 内且 `abs(int(p.x−n.x))/40 ≤ View`、`abs(int(p.y−n.y))/40 ≤ View`；并且 `vtbl+0xc8`（`0x005BBCCF`）为假：**仇恨条目存在且值为 0**。日志 "Player Enter view %d"。加 20 是 [运行] 结果 |
| 放泡 | `0x00610BA0`（本机）/ `0x0060857C`（0xFA3 接收） | 每个客户端 | 规则 12，放泡者所在格在**当前分区**矩形内（`ox ≤ col ≤ ox+w−1`，`oy ≤ row ≤ oy+h−1`）时，对 NPC 列表中 `vtbl+0xb4()==1`（**已在战斗**）的每个 NPC：`AddEnmity(placer+0x396, 5 × placer.vtbl+0xb0())`；`+0xb0` → `P.vtbl+8` → `P+0x2b4`（= `P+0x2ec`，pvedata 第一个字段，推断是 level） |
| 道具效果 `Enmity` | `0x005FA37A` | 每个客户端 | 对 NPC 列表里每个 NPC `AddEnmity(target+0x396, v)` |
| 目标已死 | `0x005BB33A` | 仲裁方 | 战斗寻敌时，若上次目标 `+0x8e4` 的状态是 7 或 0xC：`AddEnmity(seat, −10000000)` |

`AddEnmity(seat, v)` = `vtbl+0xd4` = `0x005BC67A` [证实]：

```
log "Player %d Add Enmity %d to Player ..."
it = map.find(seat)
if it == end: map[seat] = max(v, 0)
elif v < 0 && |v| > it.value: it.value = 0
else: it.value += v
```

时序要点：玩家第一次走进 NPCBounds 时还没有条目；下一次决策 `GEnmity()` 用 `operator[]` 建出值为 0 的条目；之后视野组件发现"在视野内且值为 0"才加 20；再下一次决策总和 ≥1 → 进入战斗。目标死亡后条目被清为 0，复活回来走进视野会再次 +20。

**没有找到任何按时间衰减的代码。**

### 3.3 选目标（BattleStrategy / SkillTrigger 共用）[证实]

| 函数 | 规则 |
|---|---|
| `0x005BC25F` 最高仇恨 | 遍历 `M+0x1338`，跳过 NPC、状态 0xC/7、不在 NPCBounds 的；第一个合格者为初值，之后**严格大于**才替换（并列取先出现的）。仇恨用 `operator[]` 读，所以 0 仇恨的玩家也可能被选中 |
| `0x005BC398` 最低仇恨 | 同上，严格小于才替换 |
| `0x005BC4D3` | 建了一个列表但**恒返回 0** |

`0x005BC134`：`N+0x8ac` 0 → 最低、1 → 最高、2 → `0x005BC4D3`（无目标）、其他 → 0。

---

## 4. 移动策略

公共工具 [证实]：

- `pathValid()` `0x005BB7F6`：战斗中要求 `+0x8e4` 目标非空且**目标当前格 == 目标格 `+0x8ec/+0x8ed`**，否则无效；然后检查路径上每格：有泡或地图元素 `elem.vtbl+0x2c(row,col,dir)` 返回非零→无效。实际基础元素非零表示可通，这个条件与新建寻路相反；终点方向9返回零。最后 `trimToCurrent()`。真实谓词及正式适配补证见 `pve-route-elements-re.md`。
- `trimToCurrent()` `0x005BB9F1`：在路径里找当前格，删除它之前的元素，找不到返回 0。
- `centered()` `0x005BB932`：路径非空，且 `x ∈ [cx+10, cx+30]`、`y ∈ [cy+10, cy+30]`（cx,cy 为当前格左上角）。
- `findPath(from, goal)`：`clearPath(); pf.setPolygon(N+0x91c); ok = pf.find(from, goal, &path)`；失败则清路径、目标格设为 (0xFF,0xFF)。
- 返回值统一是 `path 为空 ? 9 : path[0].dir`。

### 4.1 MoveStrategy 1 / 2：巡逻 `0x005BAEF1` / `0x005BB069`

```
if len(path) > 1 && pathValid():
    if centered() && path[0] 不是当前格: 删 path[0]
    return path[0].dir
goal = nextPatrol(randomDir = (strategy==1))      // 0x005BB756
findPath(here, goal)
if path 为空: MoveStrategy = 3                    // 永久退化为随机走
return path 为空 ? 9 : path[0].dir

nextPatrol: n = len(N+0x91c 四角)；n<1 → 当前格
    strategy 1: idx = (GetTickCount()&1) ? idx-1 : idx+1；strategy 2: idx+1
    idx = idx % n（无符号，0-1 会变成 0xFFFFFFFF % 4 = 3）
```

### 4.2 MoveStrategy 3：随机一步 `0x005BB1E1`（所有 XML NPC 的默认值）

```
if len(path) > 1 && pathValid(): (同上) return path[0].dir
r = LCG() % 1000 & 3                     // 0x004522F0，场景随机数
goal = here; 0: col-1（≥0 时）1: col+1（≤cols-1 时）2: row-1 3: row+1
findPath(here, goal)                       // 出 NPCBounds 的目标格会失败 → 9
return path 为空 ? 9 : path[0].dir
```

走完一格（路径只剩当前格）后，下一次决策再随机一格。目标格在界外或不可走时返回 9，计数器不清零，下一帧再随机。

### 4.3 MoveStrategy 4：战斗追击 `0x005BB33A`

```
if len(path) > 1 && pathValid(): (同上) return path[0].dir
if N+0x8e4 && N+0x8e4 状态 ∈ {7, 0xC}: AddEnmity(N+0x8e4.seat, -10000000)
target = selectTarget(N+0x8ac)            // §3.3
N+0x8e4 = target
if !target: clearPath(); return 9
goal = target 当前格
findPath(here, goal)
return path 为空 ? 9 : path[0].dir
```

走到目标格后路径只剩一格 → 返回 9，NPC 停在玩家脚下（配合接触伤害）。目标一离开那格，`pathValid()` 失效，下一次决策重算。

### 4.4 MoveStrategy 5：回出生格 `0x005BB510`

```
if 当前格 == (N+0x8ec, N+0x8ed): return 9
if centered():
    if trimToCurrent(): 删 path[0]，return path 为空 ? 9 : path[0].dir   // 注意：返回的是"下一格"的方向
    findPath(here, goal)
elif path 非空: return path[0].dir
else: findPath(here, goal)
return path 为空 ? 9 : path[0].dir
```

没有代码会把 MoveStrategy 设成 5，XML 也不用，移植可以不做。

### 4.5 寻路器（虚表 `0x007A374C`，`find` = `0x005F10D3`）[伪C，关键处证实]

- 结点是格 `{col,row}`；邻居顺序：上(1)、下(3)、左(2)、右(0)（`0x005F1410`）。
- 每步代价1；启发 `h = max(|dc|, |dr|)`（`0x005F13C4`）；`f = g + h`，从open取f最小者（`0x005F1383`），同f取较大的列/行键；最多500次展开。`0x005F11F2`实际判断open，目标进入open即在下一轮返回，不需要等它出队；第500次展开才发现目标仍失败。完整执行补证见 `pve-astar-re.md`。
- 可走判定 `canStep(from,to,d)` = `0x005F0ADB`：`passable(from, 出方向 d)` 且 `passable(to, 入方向 (d+2)%4, 检查泡)` 且 to 不在 closed。
  `passable` `0x005F0FC1`：格子在多边形内（`0x005F0EAD`，多边形 = NPCBounds 四角，边上算在内）；该格地图元素为空或 `elem.vtbl+0x2c(row,col,dir)` 为真；检查泡时格内有泡即不可走（`0x005F109F`～`0x005F10AF`）。
- 起点要求：目标格至少有一个可走的邻居（`vtbl+0x18` = `0x005F0B70`），否则失败。
- 结果（`0x005F14F0`/`0x005F15D2`）：从起点到终点的格列表，`path[i].dir` = 第 i 格走向第 i+1 格的方向，最后一格 dir = 9。
- 不把玩家、其他 NPC 当障碍。

---

## 5. 放泡

- `vtbl+0x6c`（`0x005B050C`）在 `N+0x888 == 0` 时直接返回；PVE 初始化不建控制器（[运行]，1608/1601 都是 0）。改 AIType 的 `0x005C710A` 只被 `0x005C6221`、`0x005C8CEF` 调用（竞技/调试路径），探险流程里没有。
- 属性对象可用泡数为 0，`PVEBombPower`（`P+0x2f0`）为 0，臂长 4。即使被强行放泡，伤害也是 0 [推断]。
- **结论：PVE NPC 不放泡**，BattleStrategy 与放泡无关，只决定追谁。NPC 对玩家的伤害只有：接触（Harm，`adventure-combat-re` §3.3）和技能。

---

## 6. 技能

### 6.1 XML 技能表与运行时布局 [证实]

关卡 XML `<NPCSkillInfo SkillNums="n"><SkillsK .../>`，读取 `0x005E7CCF`/`0x005E7D8A`：14 个属性都必须**存在**（值可以为空，`atoi("") = 0`），任一缺失整张表失败。0x138C 接收时 `0x005BBF7C` 重排成 0x38 字节的运行时项：

| 运行时偏移 | XML 属性 | 用途 |
|---|---|---|
| +0x00 | TimeTrigger | 0 恒真；1 `Lower ≤ 游戏时间(M+0x3838) < Upper`；2 `Lower ≤ 战斗时长(N+0x8f4) < Upper`；其他恒假（无符号比较） |
| +0x04/+0x08 | TimeLower/TimeUpper | |
| +0x0c | HPTrigger | 0 恒真；1 `HP > Lower`；2 `HP == Lower`；3 `HP < Lower`（**只看 Lower**，HP 是当前血量原值） |
| +0x10/+0x14 | HPLower/HPUpper | Upper 未使用 |
| +0x18 | GEnmityTrigger | 0 恒真；1 `GEnmity > Lower`；2 **把 N+0x89c 设为 Lower**，结果 = `Lower != 0`；3 `GEnmity < Lower` |
| +0x1c/+0x20 | GEnmityLower/Upper | |
| +0x24 | EnmityTrigger | 0 恒真；1 `maxEnmity > Lower`；2 `maxEnmity < Lower`；3 `minEnmity > Lower`；4 `minEnmity < Lower`（max/min 遍历整个仇恨 map，`0x005BC0AF`/`0x005BC0ED`） |
| +0x28/+0x2c | EnmityLower/Upper | |
| +0x30 | SkiLLID | 道具 ID，必须在 20001～22999 |
| +0x34 | SkillTrigger | 选目标：1 最高仇恨、2 最低仇恨、0/3/4/5 无目标 |

49 个关卡 XML 实际用到的触发：TimeTrigger 2（绝大多数）、1、3（21038，恒假，永不释放）；HPTrigger 只出现 `3, Lower=0`（21017、21018 各一次，`HP<0` 永不成立）；GEnmity/Enmity 全为空。

### 6.2 每次决策的技能检查 `0x005BBAFA` [证实]

```
if N+0x8a8 != 1: return                         // 只在战斗中
triggered = []
for e in N+0x8c8（XML 顺序）:
    ok = time(e) & hp(e) & genmity(e) & enmity(e) & RequestUseSkill(N, e.SkillID)   // 位与，全部求值
    if ok: triggered.append(e)
if triggered: N.vtbl+0xe4(triggered[0].SkillID)   // 0x005BC02E 的"打分"恒为 1，等于取第一个
```

`RequestUseSkill` = `0x005F20CF`：ID 不在 20001～22999 → 假；NPC 身上没有这个道具就用 PropFactory 建一个（`0x005EA0D2`，PropSet 里找不到 → 日志 "RequestUseSkill CreateProp error" → 假），挂到 NPC 名下并切到 PickedUp(1)；然后问当前状态 `vtbl+0x20`。只有 PickedUp 状态（`0x005F7CC5`）会答真：

```
GameMode 合格（0x005FC892：0 任意，1 非规则 12，2 仅规则 12；技能都是 2）
&& InitialInterval ≤ M+0x3838
&& (lastFire[Tier][npcId] 不存在 || now − lastFire ≥ FireInterval[Tier])   // 0x005F7E49 / 0x005F7E20
&& !owner.vtbl+0xa8(propId)                                              // 禁用道具列表
```

### 6.3 释放：`vtbl+0xe4` = `0x005BBB23` → 0x0FB0 [证实]

```
log "NPC %u, use Skill %d"
e = 技能表里 SkillID 相同的项
switch e.SkillTrigger:
  1: t = maxEnmityTarget(); if t: send0FB0(N, id, hasTarget=1, row=t.y/40, col=t.x/40)
  2: t = minEnmityTarget(); 同上
  0,3,4,5: send0FB0(N, id, hasTarget=0, 0, 0)
  其他: 不做
```

0x0FB0（30 字节，`0x005F21E7`/`0x005F22C0`）经 TCP（`0x005A1DE1`）、组播（`0x005A2F4C`）、本机分发（`0x005C8794`）三路发出：

| 偏移 | 类型 | 内容 |
|---|---|---|
| 0 | u16 | NPC 运行时 ID |
| 2 | u32 | M+0x3838 |
| 6 | u32 | SkillID |
| 10/12 | u16 | NPC 像素 x / y |
| 14 | u32 | hasTarget |
| 18 | u32 | NPC 朝向 `+0x3b4` |
| 22/26 | i32 | 目标 row / col（hasTarget=0 时为 0） |

接收方 `0x006048BF`（CQQTNotifyPlayerUseItemDisposer::NpcUseSkill，所有客户端）：找到 NPC 与道具（没有就建）；把 NPC 吸到 (x,y)、朝向设为 dir；道具 `+0x51 = dir`、`+0x58 = 时间`；hasTarget 时 `0x005F05C0(row,col)` 记目标格（`+0x50=1, +0x4c=row, +0x4e=col`）；然后 PickedUp.`vtbl+0x24` → 进入 Prepare(2)。

### 6.4 道具状态机（每个客户端各自运行）[伪C]

状态号：0 UnPickUp、1 PickedUp、2 Prepare（虚表 `0x007A3D40`）、3 Trap（`0x007A3D84`）、4 Move（`0x007A3DC8`）、5 Trigger（`0x007A3E0C`）。

**Prepare** `enter 0x005F827B / update 0x005F8139 / exit 0x005F83D0`：
- 进入：记录施法者当前位置为道具位置（`+0x40/+0x44` 像素，`+0x48 row/+0x4a col`），把施法者吸到格心，播 PrepareAnim；`0x005F8624`：施法者 `vtbl+0x88`（可移动 −1）、`vtbl+0x90`（可放技能 −1）、禁放泡、停下。
- 更新：累计时间 > PrepareTime 时：施法者是 NPC → `lastFire[Tier][npc] = now`（`0x005F7E9F`），然后依次尝试切到 Trap(3) → Move(4) → Trigger(5)（XML 有哪个就进哪个）。（玩家走请求路径；超过 PrepareTime+2000 未离开则退回 PickedUp。）
- 离开：`vtbl+0x8c`、`vtbl+0x94` 恢复移动和技能计数。**NPC 只在准备期间停住**，进入生效后就能继续走。
- 进入 Trap/Move/Trigger 时道具从 NPC 名下摘下（`0x005DBB96`），NPC 名下立即换上一个新的同 ID 道具（PickedUp），所以能否再放只看 Tier 冷却。

**Trap**（`enter 0x005F895A / update 0x005F87F4`）：在道具位置（施法者所在格）放一个陷阱场景对象，存在 TrapState.AffectTime 毫秒后移除（`0x005F8AC0`）；有角色踩到时 `vtbl+0x1c = 0x005F8A57` 按 TrapState.PropAffectObject 过滤阵营（1/3 同队、2/4 敌队、其他不限）后进入 Trigger。踩踏检测的调用方 [未知]。

**Move**（飞行道具，`enter 0x005F8CEC / update 0x005F8C14`）：
- 终点：有目标格 → 目标格中心；否则沿 `+0x51` 方向 `MaxMoveDistance × 40` 像素。按 MoveSpeed（推断 px/s）移动，道具格随之更新。
- 每进入新格：①命中（`0x005F8FAD`）：该格里除施法者外、不在困泡/死亡状态的角色加入目标列表，命中数 ≥ HitPlayerPower 时进入 Trigger；②破坏地图元素（DestroyMapItemPower 次）；③IsIgniteBomb：格内有泡 → 泡的计时 −10,000,000（立即爆）并进入 Trigger。到达终点也进入 Trigger。

**Trigger**（`enter 0x005F96CD / update 0x005F95BB`）：
- 字段：`+8 AffectTime, +0xc AffectCount(0 当 1), +0x10 Delay, +0x14 PropAffectShap(0..4), +0x18 AffectFactor, +0x1c PropAffectObject(0..6), +0x20 PropAffectState(默认 1), +0x24 InjureFactor, +0x70 DestroyMapItem, +0x71 RestorePlayerAttr`（`0x005EB147`）。
- 进入：PropAffectObject == 0 → 每个客户端自己把施法者作为目标；否则**只有仲裁方**计算目标并发 0x10F7（§6.6），其他客户端等 0x10F7 到达后才开始计时。
- 更新：`t += dt`；第 k 次（k = 0..Count−1）在 `t ≥ Delay + k × (AffectTime / Count)` 时对目标列表施加全部效果；RestorePlayerAttr 时同时记下"反向值"。次数用完且 `t > AffectTime` 时：RestorePlayerAttr → 对目标施加反向值（**HP 不恢复**）；然后销毁道具。

### 6.5 作用范围与对象 [证实]

中心格：有目标格用目标格，否则用道具格（施法者格，飞行道具为最终所在格）。`0x005FC5C7(F=AffectFactor, d=方向, shape)` 给出行列闭区间：

| Shape | 区域（d 为朝向） |
|---|---|
| 0 | 以中心为心的 (2F+1)×(2F+1) 方块 |
| 1 | 正前方一条线：从中心往 d 方向第 1～F 格（不含中心） |
| 2 | 正前方一格那一行/列上的横条：前方 1 格，宽 2F+1 |
| 3 | 过中心、垂直于朝向的横条，宽 2F+1（F=0 即中心一格） |
| 4 | 不设区域（区域为 (0,0) 一格） |

PropAffectObject（施法者队伍 = NPC 的 9；玩家是另一队）：0 自己；1 区域内**离中心最近**的一名友方（曼哈顿距离，`≤` 时后者替换，排除施法者）；2 最近的一名敌方；3 区域内全部友方；4 全部敌方；5 区域内所有角色。PropAffectState：1 只作用于非困泡(5)、非死亡(7)；2 只作用困泡的；3 只作用死亡的。已在列表里的（飞行命中、陷阱踩踏）先按同样条件过滤（`0x005F9CA7`）。

对 NPC 施法者，"友方"就是 NPC，"敌方"就是玩家。

### 6.6 0x10F7（CQQTNotifyPropTrigger）[证实]

发送：Trigger 进入时的仲裁方，TCP + 组播（本机直接用结果）。0x86 字节：

| 偏移 | 内容 |
|---|---|
| 0 | u32 0x0FB0 里的时间（道具 `+0x58`，用来找道具） |
| 4 | u32 M+0x3838 |
| 8 | u16 施法者 ID；10/14 u32 施法者 x/y；18 u8 朝向 |
| 19 | u8 目标数 n（最多 10） |
| 20 + 11·i | {u16 id, u32 x, u32 y, u8 dir} |
| 130 | u32 道具 ID |

接收 `0x00601C7B`：按时间（或施法者+道具 ID）找到道具，清空并重建目标列表，把每个目标吸到给出的坐标和朝向，置 `+0x8a=1` 开始计时。

### 6.7 效果（`0x005FA37A`）与伤害

技能用到的效果：

| 效果 | 作用 |
|---|---|
| `HP` v<0 | `0x005B57DB`：**只有目标是本机玩家**、且不在免伤中（`+0x318`）时，自发 0x10F4，新 HP = max(0, HP−\|v\|)（三路发送）。所以技能伤害也受 3 s 免伤限制，也会触发 3 s 免伤 |
| `HP` v>0 | `vtbl+0x40(v)` 直接加血（各端本地执行），治疗 NPC 用 |
| `SetTotalPercentHP` | HP = MaxHP × v（21028 死契 v=1 回满，21043 v=0.7） |
| `SetCurPercentHP` | HP = max(1, HP × v) |
| `SpeedDel` / `BombPowerDel` / `BombCountDel` | 速度档 / 臂长 / 泡数增减 |
| `CanUseMoveKey=0` | 停下并 `vtbl+0x88`（可移动 −1）；对 NPC 也生效（21098"停止"RestorePlayerAttr=0 → 永久不动） |
| `CanUseBombKey` / `CanUseItemKey` | 禁放泡 / 禁用道具 |
| `IsReversalControl` | 方向反转计数 `+0x6c8`（`param_3[0x1b2]`）±1 |
| `IsSlip` | 换成滑行移动状态 |
| `SpecialAvatarType` | 变身（57 猪、76/77/79/81 等） |
| `Defence` / `Defense` | `vtbl+0x80` 加防御 |
| `TouchHarmValue` | `vtbl+0xbc` 加接触伤害 Harm |
| `Enmity` | 所有 NPC 对目标加仇恨 |

InjureFactor 全为 1，未见它参与上面的计算 [推断]。

### 6.8 冷却（PropFireIntervalTable，按 Tier）

`Level1 5000, 2 10000, 3 15000, 4 30000, 5 60000, 6 150000, 100 2000, 101 4000, 102 1000, 103 20000, 104 8000, 105～113 47000, 114 2000, 115 2000, 116 9000, 117 3000, 118 3000, 900 3600000`（ms）。记录时刻是准备结束时，键是 (Tier, 施法者 ID)，同一 NPC 的同 Tier 技能互相占用冷却。

### 6.9 技能本体数据

道具表在 **`config/errorPPP.ini`**：QQTSection 同款 XOR 链 + zlib（与 `tools/decrypt_pve.py` 的 `read_cfg` 相同，GameCFG.ini `[prop] propCfgSize=162143`），解出来是 GB2312 的 `<PropSet>`，每个道具 `<ID2xxxx Name Tier Description>` 下有 UnPickUp/PickedUp/Prepare/Trap/Move/Trigger 状态。查找用 `sscanf("%*2s %ld")` 取**第一个**匹配（`0x005EA2F6`），所以文件里重复的 `ID21090` 以"狂暴"（Tier 5）为准。附录 A 是 49 关实际用到的 103 个技能 ID。

**缺失**：21092、21099～21107 在这个客户端的 PropSet 里不存在，`RequestUseSkill` 永远失败，这些技能永不释放（主要影响 1623～1650 的新关卡）。

### 6.10 0x1169 / 0x116A

- **0x1169**：只由 `0x005B4947`（竞技 AIType 5 踢炸弹，`boss-kickbomb-re.md`）和 `0x005B0D86`/`0x005B2AC1`（竞技通用技能、海盗）发出。PVE NPC 不发。
- **0x116A 台词**：`{u16 id, u16 len, char text[512]}`，三路发送。两个来源都要求 ImageID（`+0x394`）∈ {24, 28, 34}：①组件 `0x0060D9E1`（仅仲裁方，基础 ID 在 30000～30200）：`+0x3b7==1` 时距上次 ≥13000 ms 随机说 1/3 句；`==3`（被困）随机说；`==4` 只说一次；`==6`、`==7` 各有固定台词；②每帧 `0x005ABE96` → `0x005AE672`（`+0x3cc ≥ 0` 时）。探险里只有 1601 的 30033"新手训练员"（ImageID 28）满足，用的是竞技 Boss 28 号的台词（"今时非同往日,你没发现吗？!" 等）。

---

## 7. 受伤保护与死亡

- NPC `vtbl+0x58 = 0x0087C8FD`（[运行]，参数为新 HP）：`+0x478=1; +0x3b7=1; P.setHP(newHP)`（`P+0xe0`），HP>0 时 `0x005B514E(x,y)` + `0x005B5202(3000)`：`+0x318` 免伤子对象 `+0x18=1, +0x1c=3000`，播 `effect/flash2.eff`。之后 3000 ms 内 `0x005AD175` 为假，爆炸（`0x00607DDC`）和站火（`0x0060C63A`）都不会再判这个 NPC。
- `0x005AD175` 还要求 `+0x3b7 ∈ {1, 0xA, 0xB}`，且 `P+0xf4`（道具 `Invincible` 计数）≤ 0。
- HP≤0：走 `0x005ACD09` 垂死（`adventure-combat-re` §2.3：NPC `+0x3c0 = 400`，actDie 播一次，朝 270°）；仲裁方的 `0x0060DE79` 下一帧发 0xFA7；0xFA7 接收方 `0x00606606` **立即**播死亡特效 `0x005AE19A(1)` 并移除 NPC（`0x005B8ECC`）。所以 400 ms 的 die 动作在仲裁方本机通常只显示一帧左右，其他客户端取决于 0xFA7 到达的时间 [推断]。
- NPC 死亡时它正在执行的道具（陷阱、飞行、生效）已从 NPC 名下摘下，按自己的计时继续 [推断]。

---

## 8. 移植伪代码（Go，sidecar 作为仲裁方）

```go
// 每帧（仲裁方，开局 3000 ms 后）
for _, n := range npcs {
    viewAggro(n)                         // 组件 0x00616143，每帧
    if n.mode != 1 && n.mode != 0xB || n.dead { continue }
    if n.cmdCnt <= 5 { n.cmdCnt++; continue }
    d := n.decide(now)
    if d == 9 { n.stop() } else { n.setDir(d); n.cmdCnt = 0 }
}
// 每帧所有 NPC：if n.inBattle { n.battleMS += dt } else { n.battleMS = 0 }

func (n *NPC) decide(now) Dir {
    n.battleSwitch()
    if n.canSkill > 0 && n.inBattle { n.trySkill(now) }      // 0x005BBAFA
    if n.canMove <= 0 || n.moveStrategy == 0 { return 9 }
    switch n.moveStrategy { case 1, 2: patrol; case 3: randomStep; case 4: chase; case 5: goHome }
}

func (n *NPC) battleSwitch() {
    total := 0
    for _, p := range livePlayers { if n.bounds.Has(p.Cell()) { total += n.enmity.getOrInsert(p.seat) } }
    if n.inBattle && total == 0 { n.inBattle, n.battleMS, n.moveStrategy = false, 0, n.savedStrategy; n.path = nil }
    if !n.inBattle && total >= 1 { n.inBattle, n.battleMS, n.savedStrategy, n.moveStrategy = true, 0, n.moveStrategy, 4; n.path = nil }
}

func viewAggro(n) { for p in livePlayers: if n.bounds.Has(p.Cell()) && abs(int(p.x-n.x))/40 <= n.view && abs(int(p.y-n.y))/40 <= n.view {
    if v, ok := n.enmity[p.seat]; ok && v == 0 { n.addEnmity(p.seat, 20) } } }

func (n *NPC) trySkill(now) {
    for _, s := range n.skills {                          // XML 顺序，取第一个
        if s.time(now, n.battleMS) && s.hp(n.hp) && s.genmity(n) && s.enmity(n) && n.canUse(s.id, now) {
            target := pickTarget(s.trigger)               // 1 最高仇恨, 2 最低, 其他 nil
            if (s.trigger == 1 || s.trigger == 2) && target == nil { return }
            send0FB0(n, s.id, target)                     // 并在本地立即执行同一道具
            return
        }
    }
}
// canUse: PropSet 有该 ID && InitialInterval <= gameTime && now-lastFire[tier][n.id] >= fire[tier] && 该技能不在准备中
// 准备: canMove--, canSkill--, 停在格心; PrepareTime 后 lastFire[tier][n.id]=now; canMove++, canSkill++; → Trap/Move/Trigger
// Trigger: 仲裁方按 §6.5 算目标、发 0x10F7；第 k 次效果在 Delay+k*AffectTime/Count；HP<0 由玩家客户端自发 0x10F4
```

寻路照 §4.5 实现（NPCBounds 内的 A*，泡和不可走元素为障碍）；随机数用场景 LCG（`0x004522F0`）才能和原版逐帧一致，但其他客户端不跑 AI，只需仲裁方自洽。

**与 sidecar 现状（`cmd/qqt-battle-sidecar/adventure.go`）的主要差异**：
1. 运行时 ID：原版 30300+序号，sidecar 用 30001+序号（`adventureFirstNPCID`）。
2. 决策间隔：原版 7 帧（返回 9 时每帧），sidecar 固定 500 ms。
3. 追击：原版只在战斗（仇恨）中追 BattleStrategy 选出的玩家，并且限制在 NPCBounds 内用 A*；不是"View 内最近的玩家"。View 只用于第一次加仇恨。
4. 平时（MoveStrategy 3）是"随机一格"，不偏向玩家。
5. 技能（0x0FB0/0x10F7）、NPC 的 3 s 免伤都还没有。

---

## 9. 未解决 / 待查

1. 仲裁方主循环的帧间隔（7 帧换算成 ms）没有查；竞技文档也只记了"帧"。
2. 陷阱被踩的检测在哪里调用 Trap 状态的 `vtbl+0x1c`（`0x005F8A57`）[未知]；飞行道具 MoveSpeed 的单位推断为 px/s。
3. `N+0x930/+0x934`（bool + 倒计时）由 VM 代码设置，含义 [未知]。
4. 放泡加仇恨用的 `P+0x2b4 = P+0x2ec`：推断是 pvedata 第一个字段（level），没有用 PVE 玩家初始化（`0x0088338C`）实跑核对。
5. 服务端收到 NPC 的 0x0FB0（TCP）后是否转发、是否校验，原版服务端行为 [未知]；客户端之间靠组播已足够。
6. HPTrigger 只比较 Lower、XML 里 `HPTrigger=3 Lower=0 Upper=100/500` 永不成立——按代码就是这样，可能是原版策划的 bug；TimeTrigger=3（21038）同理永不成立。
7. PropSet 缺 21092、21099～21107：可能原版服务端下发了更新的 errorPPP.ini，本地版本较旧 [推断]。若要让新关卡 NPC 放技能，需要别的数据来源。
8. 0x0FB0 的 hasTarget 目标格只在释放瞬间取一次；目标走开后飞行/范围技能仍打在原格（按代码推断，未实测）。
9. `0x005C6221`/`0x005C8CEF`（会改 AIType 的两处）在探险中是否可能被触发，未逐一排除。

## 10. sidecar 移植状态（技能）

`cmd/qqt-battle-sidecar/adventure_skill.go` 按 §6 实现，仲裁方是 sidecar：

- 决策（§6.2）：只在战斗中，按 XML 顺序取第一个 Time/HP/GEnmity/Enmity 都成立、ID 在 20001～22999、道具存在、`InitialInterval ≤ 游戏时间`、Tier 冷却已过的技能；GEnmity/Enmity 在 49 关里全为空，只实现了"空 = 成立"。
- 释放（§6.3）：0x0FB0 走 NPC 名义组播 + TCP 镜像；SkillTrigger 1/2 取 NPCBounds 内仇恨最高/最低的玩家格做目标。各端收到后把 NPC 吸到格心、停 PrepareTime；TCP 镜像与组播重复时按 (NPC, 时间) 去重。
- 状态机（§6.4）：准备 → 陷阱（TrapTime 内有玩家踩到施法格即生效）/ 飞行（MoveSpeed 按 px/s，进入新格时命中玩家，命中数 ≥ HitPlayerPower 或撞到非空地格或到终点即生效）/ 直接生效。准备中 NPC 死亡则道具随之消失 [推断，原版此时道具仍挂在 NPC 名下]。
- 生效（§6.5～§6.7）：Shape 0～4、Object 0～5 按表实现（State 只排除了死亡玩家，没有困泡状态）；Object 0 各端自己以施法者为目标、不发 0x10F7，其余由仲裁方发 0x10F7，其他端收到后才计时。效果按 `Delay + k·AffectTime/Count` 分次落地。
- 已实现的效果：`HP`（v<0 只由受害者本机发 0x10F4，受 3 s 免伤限制；v>0 直接加血）、`SetTotalPercentHP`、`SetCurPercentHP`、`Defence/Defense`、`TouchHarmValue`、`Enmity`、`SpeedDel/BombPowerDel/BombCountDel`（引擎 `AdjustAdventureAttributes`，只在 `Rules.NativeAdventure` 下生效；速度和泡数下限 0，速度 0 档原地不动；泡威力下限取 1 [推断，原版下限 0，0 威力泡的爆炸形状没查]）、玩家和 NPC 的 `CanUseMoveKey=0`、玩家的 `CanUseBombKey=0`、`IsReversalControl`；`RestorePlayerAttr` 时到期反向恢复（HP 不恢复）。
- `SpecialAvatarType`（全体变猪 57、燃影 79、奥冰 77、电法 81、NPC 狂暴 76）：`0x005FA37A` 走通用变身路径 `0x005F767D(…,0x38)`→`0x005ADF5A`，再把 Trigger 状态 +8（AffectTime）传给新外形的 `vtbl+0x44` 作寿命、`vtbl+0x60(v)` 设外形 [证实]；反向值 < 1 直接返回，所以 RestorePlayerAttr 不撤销，外形按自己的寿命结束。sidecar 只换外观（玩家视图 `avatar`、NPC 视图 role），外形自身的移动/放泡槽位没查、没做 [未知]。
- `CanUseItemKey=0`：本机玩家不能用捡到的和带入的道具；`IsIgniteBomb`（49 关里 20 处）：飞行道具进入新格时格内静止的泡立即爆（引擎 `IgniteBombAt`，只在规则 12 生效），道具在该格生效。`DestroyMapItemPower` 只有玩家道具 20023 用，NPC 技能不涉及。
- `IsSlip`（只有 1610 的 21050）：引擎 `ApplyAdventureSlip` 换成强制滑行（原版 `0x005DB31D(角色, 2)`），恢复时不处理。
- 未实现：困泡状态筛选（PropAffectState 2/3）、道具 `.eff` 动画（画面上只用半透明圆点标出陷阱和飞行道具，不是原版外观）。

---

## 附录 A：49 个关卡 XML 用到的技能

列说明：Tier→CD = 道具 Tier 与冷却 ms；开局锁 = PickedUpState.InitialInterval（游戏时间未到不能放）；状态 = 有 TrapState（陷阱存在时长）或 MoveState（飞行格数@速度、命中上限、是否引爆泡）；生效 = Trigger 的 Delay/AffectCount/AffectTime；形状F/对象 = PropAffectShap+AffectFactor / PropAffectObject（方=方块、线=前方直线、横=前方横条、横心=过中心横条；自/友1/敌1/友群/敌群/全）；"结束恢复" = RestorePlayerAttr=1；XML 触发 = `T<SkillTrigger>/time<TimeTrigger>:<TimeLower>`（加 `HP3<0` 表示 HPTrigger=3、Lower=0），×次数。所有 TimeUpper 都是 900000。

| ID | 名称 | Tier→CD | 开局锁ms | 准备ms | 状态 | 生效(Delay/Count/Time) | 形状F/对象 | 效果 | XML 触发 (次数) | NPC |
|---|---|---|---|---|---|---|---|---|---|---|
| 21000 | 冰弹 | 2→10000 | 0 | 1000 | — | 600/1/10000 | 方0/敌1 | SpeedDel=-2 (结束恢复) | T1/time2:2000×10; T1/time2:3000×1 | 冰霜傀儡、剧毒傀儡 |
| 21002 | 火球术 | 2→10000 | 0 | 1000 | — | 600/1/3000 | 横心0/敌1 | HP=-100 | T1/time2:2000×5 | 火焰傀儡 |
| 21003 | 神经毒气 | 101→4000 | 0 | 1500 | — | 0/1/10000 | 横1/敌群 | SpeedDel=-2 (结束恢复) | T1/time2:2000×7 | 剧毒傀儡、火焰傀儡 |
| 21004 | 施毒术 | 3→15000 | 0 | 1000 | — | 500/1/5000 | 方5/敌群 | BombPowerDel=-50 (结束恢复) | T1/time2:10000×6 | 剧毒傀儡、火焰傀儡 |
| 21005 | 冰环 | 2→10000 | 0 | 1500 | — | 0/1/10000 | 方2/敌群 | SpeedDel=-2 (结束恢复) | T0/time2:2500×1; T1/time2:2000×10 | 年长的雪巨人、暴躁的永冬熊、游荡的冰妖、特别的冰妖、霜冻傀儡 |
| 21006 | 燃烧 | 102→1000 | 0 | 0 | 陷阱60000ms | 0/1/10000 | 方0/敌群 | HP=-200 | T0/time2:2000×9 | 燃烧傀儡 |
| 21007 | 毒雾 | 102→1000 | 0 | 0 | 陷阱60000ms | 0/5/10000 | 方0/敌群 | HP=-50 | T0/time2:2000×6 | 淬毒傀儡 |
| 21008 | 群体冰球1 | 105→47000 | 0 | 1000 | — | 600/1/15000 | 方1/敌群 | SpeedDel=-4 (结束恢复) | T0/time2:2500×3; T3/time2:2000×2 | 暗之傀儡、疯狂的雪球怪、雪巨人 |
| 21009 | 群体冰球1 | 106→47000 | 0 | 1000 | — | 600/1/15000 | 方1/敌群 | SpeedDel=-4 (结束恢复) | T3/time2:3500×1 | 暗之傀儡 |
| 21010 | 群体冰球1 | 107→47000 | 0 | 1000 | — | 600/1/15000 | 方1/敌群 | SpeedDel=-4 (结束恢复) | T3/time2:5000×1 | 暗之傀儡 |
| 21011 | 群体火球术 | 108→47000 | 0 | 1000 | — | 600/1/3000 | 方1/敌群 | HP=-300 | T3/time2:16500×1 | 暗之傀儡 |
| 21012 | 群体火球术 | 109→47000 | 0 | 1000 | — | 600/1/3000 | 方1/敌群 | HP=-300 | T3/time2:18000×1 | 暗之傀儡 |
| 21013 | 群体火球术 | 110→47000 | 0 | 1000 | — | 600/1/3000 | 方1/敌群 | HP=-300 | T3/time2:19500×1 | 暗之傀儡 |
| 21014 | 大毒环 | 111→47000 | 0 | 1000 | — | 600/5/10000 | 方2/敌群 | HP=-100 | T5/time2:21000×10 | 厉害的小绿花、小绿花、暗之傀儡 |
| 21015 | 大毒环 | 112→47000 | 0 | 1000 | — | 600/5/10000 | 方2/敌群 | HP=-100 | T5/time2:33500×1 | 暗之傀儡 |
| 21016 | 大毒环 | 113→47000 | 0 | 1000 | — | 600/5/10000 | 方2/敌群 | HP=-100 | T5/time2:41000×1 | 暗之傀儡 |
| 21017 | 狂怒 | 2→10000 | 0 | 0 | — | 600/2/5000 | 方0/自 | SpeedDel=1 Defence=300 (结束恢复) | T1/time0:0/HP3<0×1; T5/time1:2000×7 | 寒冰元素 |
| 21018 | 木偶连线 | 2→10000 | 0 | 0 | — | 200/1/1500 | 方1/敌群 | HP=-200 | T5/time0:0/HP3<0×1; T5/time2:2000×8 | 粗线假人 |
| 21019 | 大地颤抖（纵） | 114→2000 | 0 | 1000 | — | 200/1/1000 | 线7/敌群 | HP=-1000 | T5/time2:5000×5 | 冰霜之王 |
| 21020 | 大地颤抖（横） | 115→2000 | 0 | 1000 | — | 200/1/1000 | 横3/敌群 | HP=-1000 | T5/time2:16000×5 | 冰霜之王 |
| 21021 | 大地颤抖（中） | 116→9000 | 0 | 1000 | — | 200/1/5000 | 方4/敌群 | CanUseMoveKey=0 CanUseBombKey=0 (结束恢复) | T5/time2:28000×5 | 冰霜之王 |
| 21022 | 气拳 | 100→2000 | 0 | 500 | 飞行10格@350 命中1 引爆泡 | 200/1/1500 | 方0/敌群 | HP=-500 | T0/time2:2000×7 | 深寒元素 |
| 21023 | 自由祝福 | 1→5000 | 0 | 500 | — | 200/2/5000 | 方1/友群 | SpeedDel=1 TouchHarmValue=200 (结束恢复) | T0/time2:2000×1; T1/time2:2000×9 | 丝线假人 |
| 21024 | 自由祝福(增强） | 3→15000 | 0 | 500 | — | 200/2/5000 | 方1/友群 | SpeedDel=1 TouchHarmValue=500 (结束恢复) | T1/time2:2000×1 | 腐烂木偶 |
| 21025 | 死亡镰刀 | 101→4000 | 0 | 500 | 飞行10格@300 命中1 引爆泡 | 0/1/1500 | 方0/敌群 | HP=-1500 | T1/time2:2000×5 | 魔法僧侣 |
| 21026 | 自焚 | 4→30000 | 0 | 1000 | — | 600/1/1000 | 方2/敌群 | HP=-2000 | T1/time2:60000×5 | 魔法僧侣 |
| 21027 | 全体变猪 | 103→20000 | 0 | 500 | — | 0/1/6000 | 方20/敌群 | SpecialAvatarType=57 CanUseBombKey=0 CanUseItemKey=0 SpeedDel=-1 (结束恢复) | T1/time2:2000×1 | 黑暗僧侣 |
| 21028 | 死契 | 5→60000 | 0 | 2000 | — | 0/1/2000 | 方0/自 | SetTotalPercentHP=1 | T1/time2:60000×1 | 黑暗僧侣 |
| 21029 | 死亡镰刀 | 102→1000 | 0 | 500 | 飞行5格@300 命中10 引爆泡 | 0/1/1500 | 方0/敌群 | HP=-100 | T1/time2:5000×1 | 黑暗僧侣 |
| 21031 | 小能量鸡蛋 | 2→10000 | 0 | 0 | 陷阱60000ms | 0/1/5000 | 方0/敌群 | HP=-200 CanUseMoveKey=0 (结束恢复) | T5/time2:5000×8 | 发条小鸡 |
| 21032 | 能量盾 | 2→10000 | 0 | 0 | — | 600/2/5000 | 方0/自 | Defence=100000 (结束恢复) | T5/time2:5000×1 | 机械母鸡 |
| 21033 | 大能量鸡蛋 | 1→5000 | 0 | 0 | 陷阱60000ms | 0/1/5000 | 方0/敌群 | HP=-800 CanUseMoveKey=0 (结束恢复) | T5/time2:7000×1 | 机械母鸡 |
| 21034 | 时间静止 | 117→3000 | 0 | 1000 | — | 600/3/15000 | 方1/敌1 | HP=-100 CanUseMoveKey=0 (结束恢复) | T5/time2:5000×6 | 闹钟机器 |
| 21035 | 时间静止(boss) | 2→10000 | 0 | 1000 | — | 200/1/5000 | 方3/敌群 | CanUseMoveKey=0 CanUseBombKey=0 HP=-800 (结束恢复) | T5/time2:5000×1 | 闹钟机器BOSS |
| 21036 | 时间爆炸 | 117→3000 | 0 | 0 | — | 0/1/10000 | 方0/敌1 | HP=-200 IsReversalControl=1 (结束恢复) | T5/time2:7000×1 | 闹钟机器BOSS |
| 21037 | 毒雾 | 2→10000 | 0 | 1000 | — | 0/5/30000 | 方0/敌1 | HP=-1000 | T2/time2:5000×10 | 小机械螃蟹 |
| 21038 | 毒雾(boss) | 2→10000 | 0 | 1000 | — | 0/10/60000 | 方0/敌1 | HP=-1000 | T5/time3:5000×1 | 机械螃蟹BOSS |
| 21039 | 反向毒 | 4→30000 | 0 | 1000 | — | 0/1/30000 | 方0/敌1 | HP=-200 IsReversalControl=1 (结束恢复) | T3/time2:7000×1 | 机械螃蟹BOSS |
| 21040 | 慢速陷阱 | 102→1000 | 0 | 0 | 陷阱60000ms | 0/1/10000 | 方0/敌群 | SpeedDel=-10 (结束恢复) | T5/time2:5000×8 | 战斗机器 |
| 21041 | 自爆 | 5→60000 | 0 | 10000 | — | 0/1/1000 | 方12/敌群 | HP=-2000 | T5/time2:20000×8 | 战斗机器 |
| 21042 | 瞄准射击 | 4→30000 | 0 | 5000 | — | 0/1/1000 | 方0/敌1 | HP=-10000 | T2/time2:5000×1 | 战斗机器BOSS |
| 21043 | 自动修复 | 3→15000 | 0 | 2000 | — | 0/5/20000 | 方0/自 | SetTotalPercentHP=0.7 | T5/time2:30000×1 | 战斗机器BOSS |
| 21044 | 半月弯刀 | 117→3000 | 0 | 0 | — | 0/0/1000 | 方2/敌群 | HP=-300 | T0/time2:2500×6; T5/time2:3000×12 | 厉害的大红花、大红花、扑克牌战士、扑克牌法师、扑克牌王、花蝴蝶 |
| 21045 | 狂暴姿态 | 103→20000 | 0 | 0 | — | 0/1/8000 | 方0/自 | TouchHarmValue=500 (结束恢复) | T5/time2:8000×1 | 扑克牌王 |
| 21046 | 防御姿态 | 2→10000 | 0 | 0 | — | 600/1/8000 | 方0/自 | Defence=1000 (结束恢复) | T5/time2:12000×1 | 扑克牌王 |
| 21047 | 快速闪电 | 117→3000 | 0 | 300 | — | 600/1/1000 | 方0/敌1 | CanUseMoveKey=0 HP=-200 (结束恢复) | T1/time2:3000×17 | 小恶魔 |
| 21048 | 群体闪电 | 117→3000 | 0 | 300 | — | 600/1/1000 | 方1/敌群 | CanUseMoveKey=0 HP=-200 (结束恢复) | T0/time2:2500×3; T1/time2:3000×13; T5/time2:3000×1 | 大恶魔、精锐魔王兵 |
| 21049 | 全体反向术 | 1→5000 | 0 | 1000 | — | 0/1/500 | 方100/敌群 | IsReversalControl=1 (结束恢复) | T1/time2:5000×21 | 厉害的大红花、厉害的彩虹虫、大恶魔、彩虹虫、花蝴蝶、鼹鼠工人 |
| 21050 | 加速运动 | 117→3000 | 0 | 0 | 陷阱60000ms | 0/1/1000 | 方0/敌群 | IsSlip=1 (结束恢复) | T5/time2:3000×1 | 女巫莱斯 |
| 21051 | 死亡炸弹 | 118→3000 | 0 | 0 | 陷阱60000ms | 0/1/10000 | 方0/敌群 | HP=-1000 CanUseMoveKey=0 (结束恢复) | T5/time2:5000×1 | 女巫莱斯 |
| 21052 | 狂暴 | 6→150000 | 10000 | 3000 | — | 0/1/1000000 | 方0/自 | SpecialAvatarType=76 TouchHarmValue=500 Defence=500 (结束恢复) | T5/time2:60000×1 | 女巫莱斯 |
| 21053 | 单体治疗术 | 2→10000 | 0 | 0 | — | 0/1/2000 | 方0/自 | HP=1000 | T1/time2:10000×1; T5/time2:10000×1 | 水法奥冰 |
| 21054 | 群体治疗术 | 103→20000 | 0 | 0 | — | 0/5/10000 | 方100/友群 | HP=1000 | T5/time2:12000×14 | 厉害的蓬头树、水法奥冰、蓬头树 |
| 21055 | 火焰提示 | 100→2000 | 0 | 100 | — | 600/1/3000 | 方0/敌1 | HP=-500 | T1/time2:3000×2 | 火法燃影 |
| 21056 | 飞行火球 | 1→5000 | 0 | 1000 | 飞行10格@300 命中10 引爆泡 | 0/1/1500 | 方0/敌1 | HP=-1000 | T1/time2:5000×2 | 火法燃影 |
| 21057 | 快速闪电 | 117→3000 | 0 | 300 | — | 600/1/1000 | 方0/敌1 | CanUseMoveKey=1000 HP=-800 (结束恢复) | T1/time2:3000×1 | 电法光刃 |
| 21058 | 群体闪电 | 1→5000 | 0 | 300 | — | 600/1/1000 | 方1/敌群 | CanUseMoveKey=0 HP=-500 (结束恢复) | T1/time2:5000×1 | 电法光刃 |
| 21059 | 变奥冰 | 4→30000 | 10000 | 3000 | — | 0/1/9000 | 方100/敌1 | SpecialAvatarType=77 SpeedDel=-2 (结束恢复) | T5/time2:5000×1 | 米能达 |
| 21060 | 全体变燃影 | 103→20000 | 10000 | 3000 | — | 0/1/9000 | 方100/敌1 | SpecialAvatarType=79 BombPowerDel=-500 (结束恢复) | T5/time2:10000×1 | 米能达 |
| 21061 | 全体变电法 | 2→10000 | 10000 | 3000 | — | 0/1/9000 | 方100/敌1 | SpecialAvatarType=81 IsReversalControl=9000 (结束恢复) | T5/time2:15000×1 | 米能达 |
| 21062 | 冰霜陷阱 | 104→8000 | 0 | 1000 | 陷阱10000ms | 0/1/1000 | 方0/敌群 | SpeedDel=-3 HP=-10 (结束恢复) | T0/time2:2000×2; T0/time2:3000×16 | 冰冰泡、冰法师、冰泡冰、冰泡泡、泡冰泡 |
| 21063 | 火焰陷阱 | 104→8000 | 0 | 1000 | 陷阱10000ms | 0/1/1000 | 方0/敌群 | HP=-20 (结束恢复) | T0/time2:3000×11 | 小火龙、火法师、火泡泡 |
| 21064 | 单体火球 | 103→20000 | 0 | 1000 | — | 600/1/3000 | 方2/敌1 | HP=-50 | T1/time2:10000×2; T1/time2:3000×12; T1/time2:4000×1 | 小火龙、泡火泡、火法师、火泡泡、火泡火、火火泡 |
| 21065 | 半月弯刀 | 1→5000 | 0 | 0 | — | 0/0/800 | 方2/敌群 | HP=-100 | T0/time2:2500×10; T0/time2:3000×2; T0/time2:3500×1 | 卡牌战士、厉害的菜青虫、小绿花、彩虹虫、暴躁的永冬熊、花蝴蝶、鼹鼠工人 |
| 21066 | 毒陷阱 | 104→8000 | 0 | 1000 | 陷阱10000ms | 0/1/1000 | 方0/敌群 | HP=-20 (结束恢复) | T0/time2:3000×4 | 毒泡泡 |
| 21067 | 毒陷阱 | 103→20000 | 0 | 1000 | 陷阱20000ms | 0/3/9000 | 方0/敌群 | HP=-80 (结束恢复) | T0/time2:10000×2 | 毒泡泡 |
| 21068 | 全体反向术 | 103→20000 | 0 | 1000 | — | 0/1/3000 | 方100/敌群 | IsReversalControl=1 (结束恢复) | T1/time2:10000×1; T1/time2:3000×2; T1/time2:3500×1 | 毒泡泡 |
| 21069 | 半月弯刀 | 1→5000 | 0 | 0 | — | 0/0/800 | 方2/敌群 | HP=-500 | T0/time2:2500×16; T0/time2:3000×10 | 卡牌队长、厉害的小绿花、厉害的彩虹虫、厉害的菜青虫、大红花、小绿花、彩虹虫、石像 |
| 21070 | 群体火球术 | 1→5000 | 0 | 300 | — | 600/1/1000 | 方1/敌1 | HP=-500 (结束恢复) | T1/time2:3000×7 | 小火龙、火法师 |
| 21071 | 远程区减速 | 1→5000 | 0 | 1500 | — | 0/1/3000 | 方2/敌1 | SpeedDel=-10 (结束恢复) | T1/time2:3000×6 | 冰冰泡、冰法师 |
| 21072 | 群体闪电 | 2→10000 | 0 | 300 | — | 600/1/1000 | 方1/敌群 | HP=-500 (结束恢复) | T1/time2:3000×4 | 电法师 |
| 21073 | 快速闪电 | 1→5000 | 0 | 300 | — | 600/1/1000 | 方0/敌1 | CanUseMoveKey=0 HP=-800 (结束恢复) | T1/time2:3000×6 | 电法师、菜青虫 |
| 21074 | 闪电陷阱 | 100→2000 | 0 | 1000 | 陷阱10000ms | 0/1/1500 | 方0/敌群 | HP=-299 CanUseMoveKey=0 (结束恢复) | T0/time2:3000×4 | 电法师 |
| 21075 | 冰霜陷阱 | 102→1000 | 0 | 1000 | 陷阱10000ms | 0/1/3 | 方0/敌群 | SpeedDel=-20 HP=-500 (结束恢复) | T0/time2:3000×1 | 黑巫师 |
| 21076 | 火焰陷阱 | 1→5000 | 0 | 1000 | 陷阱20000ms | 0/1/1000 | 方0/敌群 | HP=-800 (结束恢复) | T0/time2:3000×1 | 黑巫师 |
| 21077 | 闪电陷阱 | 102→1000 | 0 | 1000 | 陷阱10000ms | 0/1/3000 | 方0/敌群 | HP=-300 CanUseMoveKey=0 (结束恢复) | T0/time2:3000×1 | 黑巫师 |
| 21078 | 群体闪电 | 1→5000 | 0 | 300 | — | 600/1/1000 | 方1/敌群 | HP=-800 CanUseMoveKey=0 (结束恢复) | T1/time2:3000×3 | 菜青虫、骷髅骑士 |
| 21079 | 闪电陷阱 | 102→1000 | 0 | 1000 | 陷阱10000ms | 0/1/3000 | 方0/敌群 | HP=-500 CanUseMoveKey=0 (结束恢复) | T0/time2:3000×1 | 骷髅骑士 |
| 21080 | 防御姿态 | 4→30000 | 0 | 0 | — | 600/1/15000 | 方0/自 | Defence=1000 (结束恢复) | T5/time2:3000×1 | 骷髅骑士 |
| 21081 | 单体治疗术 | 2→10000 | 0 | 0 | — | 0/1/2000 | 方0/自 | HP=3000 | T5/time2:3000×1 | 红巫师 |
| 21082 | 群体火球 | 101→4000 | 0 | 1000 | 飞行10格@300 命中10 引爆泡 | 0/1/1500 | 方0/敌群 | HP=-1000 | T1/time2:3000×5 | 厉害的大红花、大红花、红巫师 |
| 21083 | 火焰陷阱 | 100→2000 | 0 | 1000 | 陷阱10000ms | 0/1/1000 | 方0/敌群 | HP=-1000 (结束恢复) | T0/time2:3000×1 | 红巫师 |
| 21084 | 全体反向术 | 1→5000 | 0 | 1000 | — | 0/1/3000 | 方100/敌群 | IsReversalControl=1 (结束恢复) | T1/time2:3000×1 | 红巫师 |
| 21085 | 灼热 | 2→10000 | 0 | 1000 | — | 600/3/3000 | 方0/敌1 | HP=-250 (结束恢复) | T1/time2:3000×1 | 深渊火龙 |
| 21086 | 狂暴 | 6→150000 | 10000 | 3000 | — | 0/1/10000 | 方0/自 | TouchHarmValue=300 Defence=300 (结束恢复) | T5/time2:3000×2 | 沙漠骑士、深渊火龙 |
| 21087 | 半月弯刀 | 117→3000 | 0 | 0 | — | 0/0/1000 | 方2/敌群 | HP=-800 | T0/time2:2500×13; T0/time2:3000×1 | 厉害的大红花、厉害的小绿花、厉害的彩虹虫、厉害的菜青虫、厉害的蓬头树、大红花、年 |
| 21088 | 自由祝福 | 3→15000 | 0 | 500 | — | 200/1/5000 | 方1/友群 | HP=1000 (结束恢复) | T5/time2:3000×2 | 厉害的红巫师、深渊火龙 |
| 21089 | 灼热 | 2→10000 | 0 | 1000 | — | 600/3/3000 | 方0/敌1 | HP=-350 (结束恢复) | T1/time2:3000×1 | 地狱火龙 |
| 21090 | 狂暴 | 5→60000 | 10000 | 3000 | — | 0/1/10000 | 方0/自 | TouchHarmValue=500 Defence=500 (结束恢复) | T5/time2:3000×1 | 地狱火龙 |
| 21091 | 即死 | 4→30000 | 0 | 1000 | — | 0/1/5000 | 方0/敌1 | HP=-30000 (结束恢复) | T1/time2:3000×2 | 地狱火龙、沙漠骑士 |
| 21092 | (PropSet 中无) |
| 21093 | 大地颤抖（纵） | 2→10000 | 0 | 1000 | — | 200/1/5000 | 线4/敌群 | CanUseMoveKey=0 CanUseBombKey=0 (结束恢复) | T5/time2:3000×10 | 厉害的大树人、厉害的石头人、岩浆兽、岩浆怪、石头人 |
| 21094 | 大地颤抖（中） | 2→10000 | 0 | 1000 | — | 200/1/5000 | 方2/敌群 | CanUseMoveKey=0 CanUseBombKey=0 (结束恢复) | T5/time2:3000×9 | 厉害的石头人、岩浆兽、岩浆怪、石头人 |
| 21098 | 停止 | 6→150000 | 10000 | 0 | — | 0/1/1000000 | 方0/自 | CanUseMoveKey=0 | T4/time2:1000×3; T4/time2:3000×2 | 泡火泡、火泡泡、火泡火、火火泡 |
| 21099 | (PropSet 中无) |
| 21100 | (PropSet 中无) |
| 21101 | (PropSet 中无) |
| 21102 | (PropSet 中无) |
| 21103 | (PropSet 中无) |
| 21104 | (PropSet 中无) |
| 21105 | (PropSet 中无) |
| 21106 | (PropSet 中无) |
| 21107 | (PropSet 中无) |


## 远端保持速度后续（2026-11-04）

探险NPC构造及VM初始化已成功执行，真实47C控制器与默认NPC相同；接收端按玩家表换算，仲裁方按NPC表。初始化见证和260个探险角色保持帧逐float32位对照已接入，见 `pve-remote-re.md`。这不代表完整网络队列或全部技能状态已对照。


2026-11-04补充：探险默认仲裁物理、三字节路线消费、目标格停止、场景共享移动计数和准备/禁走状态的顺序已按实际执行修正；1036条基准及边界见 `pve-motion-re.md`。随后新建路线已由BFS替换成原版A*，另有106条完整搜索基准核对平局、open目标提前终止、方向通行及500次截止，见 `pve-astar-re.md`。

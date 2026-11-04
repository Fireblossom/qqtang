# 踢炸弹 Boss（AIType 5，鲁尼 / 小小罗）的 AI 与技能 10～12（Client.exe 静态逆向）

范围：规则 2（踢炸弹）地图 701/702 上由 `CREATE_NPC_BOSS` 生成的 RoleID 36（鲁尼，技能 `{10,11,12}`）和 RoleID 35（小小罗，技能 `{0,11,12}`）。它们的控制器是 AIType 5。
本文接着 `boss-ai-re.md`（控制器框架、WFPathFinder、AIType 3、通用技能、0x1169 接收方）和 `boss-lifecycle-re.md`（创建、受伤、死亡、掉落）往下写，那两篇里已有的内容这里不再重复。

标注：**[汇编]** 表示已用 capstone 核对原始指令（常数、比较方向、参数顺序）；**[伪C]** 表示只读了 Ghidra 伪 C；**[推断]** 表示有依据但没有完全追到。地址都是 Client.exe 的虚拟地址。
记号沿用前两篇：N 是 Boss 对象，G = `N+0x3fc`（角色管理器，等于 `S+0x1310`），S 是场景，`G+0x3838` = `S+0x4b48` 是游戏时间（ms），`S+0x10` 是带种子的随机数发生器（下称 RNG）。格子是 `col = int(x)/40`、`row = int(y)/40`，方向码是 0 右、1 上、2 左、3 下、9 停。

---

## 0. 结论摘要

1. **调用频率与 AIType 3 相同**：规则 2 的命令表（`0x0060BD76`）也是先注册移动命令 `0x0061126E`，阈值 T = 5（只有规则 8 是 10），所以每 7 帧决策一次；返回 9 时计数器不清零，下一帧马上再决策。规则 2 的命令表**没有放泡命令** `0x00610B63`，AIType 5 的放泡槽 `0x005DF399` 本身也是空的，所以 **Boss 永远不放普通泡**。
2. **Boss 会踢泡，走的是和玩家完全相同的通用踢泡命令** `0x0060CF49`（虚表 `0x007A4588`，规则 2 的命令表里有它）。条件是：模式 `+0x3b7 == 1`、正在移动（`+0x3b8 == 2`）、规则 2 槽 3 `0x0060C0E0` 返回 0（即人人都能踢）。Boss 朝一个泡走过去时，仲裁方就以 Boss 的 ID 发 **0x0FB4**；仲裁方自己的 `0x006040CF` 再把它回显成 **0x139C**。AI 本身**从不发 0x1159（PlayerThrowBomb）**，也不直接发 0x139C。NPC 类里另有一个"踢脚下的泡"函数 `0x005B429F → 0x005B438D`，但它**没有任何静态引用**，属于死代码。
3. 决策 `0x005B0C35` 用一个**独立的"足球寻路器"**（`N+0x698`，A*，`0x005E0351`）：只走 4 邻格，代价恒为 1；**单个普通泡算可通行**（走进去就会踢走它）；2 个以上的泡、类型 11/12 的泡、不可通行的地图元素算阻挡；**完全不看危险**；而且**不能往上走进第 0～3 行**。
4. 决策的优先级：技能 → 沿旧路径走（离格心 5 px 内就吸附）→ 追杀被困玩家（不限距离，按角色表顺序取第一个能到的）→ 自己脚下 `danger == 2` 就逃 → 按"距上一批空投的时间" `t` 分时段：
   - `t < 2000` ms：先"踢泡打人"，再"走向直线 4 格内的泡"，再"随机去一个道具"；
   - `4501 ≤ t ≤ 7499` ms：捡道具（AIType 3 的 `0x005B13F5`），否则随机游走到一个安全格；
   - 其他时间（包括开局到第一批空投之前）：**站着不动（9）**。
5. "踢泡打人" `0x005B4561` 有一个原版 bug：它检查泡时用的格子和评估踢向时用的格子行列互换了，见 §3.3。
6. 技能解释器 `0x005B4947`：
   - 游戏时间 ≥ 10000 ms 才运行，**没有总闸**；
   - 第 i 个技能的冷却格是 `N+0x834 + 4i`（**按列表位置**），统一要求 ≥ 30000 ms；
   - 但更新时**按技能号**：10 → 格 0，11 → 格 1，12 → 格 2；
   - 冷却格在创建时清零（只对 RoleID 35/36），所以第一次放技能在 30 s；技能 12 另外要求格 2 ≥ 60000 ms。
   - `competitive_overlay.go` 里关于"冷却格错位"的说法**得到证实**：`{0,11,12}` 正确；压缩成 `{11,12}` 会让技能 11 每次决策都放。
7. 三个技能：
   - **10** = 往 5 个随机空格扔减速胶（场景 43），接收方与技能 1 相同；
   - **11** = Boss 自己 **10 s 免伤**（`+0x318` 免伤组件，`flash2.eff`）；
   - **12** = 在 1～3 个随机的 2×2 空地上直接生成**场景 11 的大泡，威力 8**，主人是 Boss，不飞行。
   - 节奏：10 和 11 每 30 s 一次，12 每 60 s 一次（从 30 s / 60 s 开始）。
8. 受伤、死亡、掉落：规则 2 和规则 1 走的是**同一套代码**。规则 2 虚表槽 4/6/7 就是规则 1 的 `0x005F4A5D`/`0x005F4B16`/`0x005F4A7A`，接触处理器 `0x0060C10B` 也在规则 2 的命令表里。差别只在伤害来源（见 §6）。

---

## 1. 调用链与频率 [汇编]

- 每帧 `0x005C87B0` → 仲裁方对每个 NPC 调 `0x005F43B1(npc)` 跑命令表 → 推进 → 0x0FA2 合包（同 `boss-ai-re.md` §1.2）。
- 规则 2 的命令表 `0x0060BD76` 按顺序注册（构造函数地址）：
  - `0x0061126E` 移动、`0x00610F2A`、`0x0061029C`、`0x006101DF`、`0x00610105`、`0x0060E5B0`、`0x0060E24B`、`0x0060DE3C`、`0x0060DCE3`、`0x0061984E`（空投）、`0x0060D4F7`、`0x00610016`；
  - **`0x0060CF0C` 踢泡（虚表 `0x007A4588`，类型 2）**；
  - `0x0060C5FD`、`0x0060C31A`、**`0x0060C10B` 接触处理器**（虚表 `0x007A4548`，槽 2 = `0x0060C148`）。
  - 没有放泡命令 `0x00610B63`。
- `0x005F43B1` 按"上一帧位置 `+0x3a8/+0x3ac` 与当前位置是否同格"选 SameGrid 列表（`+0x1c`）或 DiffGrid 列表（`+0x2c`），然后总是跑 Gene 列表（`+0xc`）。类型 1 → Gene、2 → SameGrid、3 → DiffGrid 是 [推断]。所以踢泡命令在 Boss 没跨格的帧里每帧检查一次 [推断]。
- 移动命令 `0x006112AF`（`0x0061139C`～`0x006113C6`）：`counter > 5` 时调 `N->vtbl[0x70]` = `0x005B0C1D` → 控制器槽 1 `0x005DF203` → `jmp 0x005B0C35`。**只看返回值的低字节 `al`**：`al == 9` 就停（`0x005C07D6`，计数器不清零）；否则 `0x005C07FE(al)`，计数器清零。前提和 AIType 3 一样：模式 1 或 0x0B，状态机不是 7、8，`S+0x4b48 > 2999`。

> 注意：AIType 5 返回 9 的时间段很长（§2.3）。这些时段里**每一帧**都会完整跑一遍决策（包括技能解释器、追杀被困玩家的 A*）。

---

## 2. 移动决策 `0x005B0C35` [汇编]

### 2.1 主流程

```
decide5():
  col = int(x)/40; row = int(y)/40                  // edi / ebx
  kickSkills()                                      // 0x005B4947，§4
  if followPath5() == 0:                            // 0x005B3F80，§2.4
      return byte[path.end - 4]                     // 路径刚被清空：读到缓冲区前面的堆头，原版是垃圾值；移植建议返回 9
  if chaseTrapped5(): return lastDir()              // 0x005B3A87
  if danger(row, col) == 2: return flee5()          // 0x005B3BA4 → 0x005B3BDA；0x005B3BFD
  t = (uint)(G+0x3838 - G+0x1360)                   // 距上一批空投的时间，见 2.3
  if t < 2000:
      if kickAtPlayer():                            // 0x005B4561，§3.3
          return path.empty ? 0 : (int16)path.back.dir
      if nearBubble(&gc, &gr)                       // 0x005B38D9，§3.2
         && find5(col, row, gc, gr):                // N+0x698 → 0x005E0351
          storePath5(); return lastDir()            // 0x005B3E36
      if randomItem(): return lastDir()             // 0x005B4155，§3.4
  if t >= 7500 || t <= 4500: return 9               // cmp 0x1D4C / jae；cmp 0x1194 / jbe
  if collectItem3(): return lastDir()               // 0x005B13F5（AIType 3 的捡道具，WFPathFinder，见 boss-ai-re.md §3.1）
  if wander5(): return lastDir()                    // 0x005B3CC1，§3.5
  return 9
lastDir() = byte[path.end - 4]                      // 路径 vector 最后一项（当前格）的 dir 低字节
```

`t < 2000` 时三个分支都失败，会落到后面的时段判断；因为 `t < 4501`，结果是 9。

### 2.2 足球寻路器 `0x005E0351`（对象在 `N+0x698`，构造 `0x005E02F6`，`+0x1270` = G）[汇编核对邻格和可通行判定，主体为伪C]

```
find5(sc, sr, gc, gr) -> bool                     // x = 列, y = 行
  if !(int16(gc) < 15 && int16(gr) < 13): return false      // 只检查上界
  清空 195 个结点（每个 0x18 字节：x, y, f, g, h, state, parentX, parentY），open = []
  start = node(sc,sr): g = 0, h = |sc-gc| + |sr-gr|, f = g + h, parent = (-1,-1), state = OPEN
  open.push(start); cnt = 1; cur = start
  do:
    prev = cur
    cur = open 中 f 最小的结点（严格 <，同 f 取 vector 里靠前的）          // 0x005E06FD
    if cur != start:
      if cur == goal:          goal.parent = prev; build(); return true
      if goal.state == OPEN:   build(); return true                    // 等价于"进 open 即成功"
    open.erase(cur); cnt--; cur.state = CLOSED                         // 0x005E0621 / 0x005E066C
    for (nc, nr) in [(c-1,r) 若 c-1 ≥ 0,
                     (c+1,r) 若 c+1 ≤ 14,
                     (c,r-1) 若 r-1 ≥ 4,      // ★ 不能往上走进第 0～3 行
                     (c,r+1) 若 r+1 ≤ 12]:     // 0x005E0760
      if !passable5(nc, nr): continue
      n = node(nc, nr)
      if n.state == CLOSED: continue
      if n.state == 0:  n.parent = cur; n.g = cur.g + 1; n.h = manhattan(n, goal); n.f = n.g + n.h
                        open.push_back(n); cnt++; n.state = OPEN       // 0x005E055C
      elif cur.g + 1 < n.g: n.parent = cur; n.g = cur.g + 1; n.f = n.h + n.g
  while cnt > 0
  return false
```

- 起点等于终点时**一定失败**：起点已经关闭，不会重新进 open。
- 代价恒为 1，**不看危险格**，也不看箱子代价。
- 结果 `0x005E09B7`：从终点沿 parent 走回起点，写成 `{u8 col, u8 row, ?, ?}` 数组（终点在前），最多 195 项。

`passable5(col, row)` = `0x005E08B0` [汇编]：

```
两层地图元素 G+0xE78、G+0xE2C：该格有元素且 0x005D8BC1(row,col,4) 为假 → 不可通行   // 4 的含义推断为"可通行元素"
泡列表 0x005D3BD3(row,col)（泡管理器 G+0xF40）：
   0 个 → 可通行；≥ 2 个 → 不可通行；
   1 个 → 泡类型 +4 是 0x0B 或 0x0C 时不可通行，否则可通行（不看 +0x2c，飞行中的泡也算可通行）
```

写入路径 `0x005B3E36` [汇编]：
- 先清空 `N+0x75C`；
- push `{row, col, dir = 9, marker 0}`（终点）；
- 然后对 k = 1..count-1 依次 push `{row_k, col_k, dir = 0x005B3F15(第 k-1 项, 第 k 项), 0}`。

所以 vector 的布局和 AIType 3 相同：最后一项是当前格，它的 dir 是第一步；终点的 dir 是 9。`0x005B3F15(c1,r1,c2,r2)` 给出从 (c2,r2) 走到 (c1,r1) 的方向：c1-c2 = -1 → 2，+1 → 0，r1-r2 = -1 → 1，+1 → 3，其他 → 0。marker 全为 0（AIType 5 不放泡）。

### 2.3 时间窗：`G+0x1360` 是"上一批空投的时间" [汇编]

- `G+0x1358`（= `S+0x2668`）就是规则 2 的空投组件，构造函数是 `0x005E0B38`：`+8 = -10000`（`0xFFFFD8F0`），这正是 `kickbomb.go` 里的 `kickBatchLastStart`。所以 `G+0x1360` = 组件 `+8`。
- 0x10E1 接收方 `0x006018A7` 在 `0x006018E3` 执行 `S+0x2670 = S+0x4B48`，也就是每处理一批空投就把它设为当前时间。仲裁方自己发的批次也会经本地分发走到这里（`kickbomb.go` 的 `applied()`）。
- 因此 `t = now - lastBatch` 按无符号数计算：
  - 开局到第一批（5000 ms）之前，`t = now + 10000 ≥ 13000` → **站着不动**；
  - 每批之后：0～1999 ms 踢泡；2000～4500 ms 停；4501～7499 ms 捡道具/游走；≥ 7500 ms 停；
  - 批间隔是 `max(4000, 9000 - now/8)`：前期约 8.4 s 一批，两个窗口都有；**40 s 以后**每 4000 ms 一批，只剩 0～2 s 的踢泡窗口，其余 2 s 停。
- "追杀被困玩家"和"脚下 `danger==2` 就逃"排在时间窗之前，任何时候都生效。

### 2.4 沿旧路径走 `0x005B3F80` [汇编]

```
followPath5():                                      // 返回 0 只有一种情况：路径作废
  if path 非空:
     cur = path.back                                // {row, col, dir}
     wp = (cur.col*40+20, cur.row*40+20) + 40*delta[cur.dir]    // 0x005B4118；dir 为 9 时不偏移
     dx = |int(wp.x - x)|, dy = |int(wp.y - y)|
     if dx ≤ 5 && dy ≤ 5:                           // 常数 0x007A1CC8 = 5.0
         snap(wp); path.pop_back()                  // 0x005AD322, 0x005BE23F
         if path 空: N+0x3b8 = 1; return 1
     elif dx ≥ 60 && dy ≥ 60:                       // 0x007A1CCC = 60.0
         path.clear(); return 0
  if danger(row,col) == 2: flee5()                  // 结果不用，只改路径
  return 1
```

### 2.5 追杀被困玩家 `0x005B3A87` [汇编]

```
for role in G+0x1338（全部角色的拷贝，按顺序）:
    if 是 NPC（0x0040A66E → 0x00534AD0）: continue
    if role.+0x3b7 ∉ {3, 0x0A}: continue
    if find5(col, row, role.col, role.row): storePath5(); return 1     // 第一个能到的就要，不比距离、不设上限
return 0
```

碰到被困玩家就是击杀（`0x0060C148` → 0x0FA8 → 0x0FA9，见 `boss-lifecycle-re.md` §3.4），这是 Boss 主要的击杀手段。

### 2.6 逃跑 `0x005B3BFD` 与随机安全格 `0x005B3CC1` [汇编]

```
flee5():
  saved = path（拷贝）
  if wander5(): return path.empty ? 0 : lastDir()
  path = saved; return path.empty ? 0 : lastDir()      // 失败时恢复旧路径；路径为空时返回 0（向右！）

wander5():                                             // 0x005B3CC1，也用作 4501～7499 ms 的游走
  cand = []
  for c in 0..13:                                      // ★ 列 0..13（不含 14）
     for r in 3..11:                                   // ★ 行 3..11（不含 0..2 和 12）
        if danger(r, c) == 0 && passable5(c, r): cand.append((c, r))
  k = RNG() % len(cand)                                // 每次 1 个随机数；cand 为空时原版读空指针（不会发生，留意）
  if find5(col, row, cand[k]): storePath5(); return 1  // 只试一次
  return 0
```

`danger()` = `0x005D37C4`，两种情况返回 2：该格特殊场景对象列表（`G+0xF8C`）非空且规则号不是 12；或者危险表里记的是 2。危险表的 2 由 `0x005D4142`（泡到时，`0x005DF916`）和 `0x005D4937`（爆炸处理）写入，1 由普通放泡 `0x005D4252` 写入。所以 2 ≈ "这一格即将或正在被炸"、1 ≈ "将来会被炸" [推断]。AIType 5 只在自己脚下是 2 时才逃；而游走的目标要求是 0。

---

## 3. 踢泡相关

### 3.1 通用踢泡命令 `0x0060CF49`（玩家和 Boss 共用）[汇编]

```
kickCmd(role):                                         // 规则 2 命令表里每帧执行（SameGrid，推断）
  if !canKick(role): return                            // 0x005AD2AF
  (r, c) = role 的格子
  probe = role.pos + 22 * delta[role.dir]              // 0x16 px
  if cell(probe) == (r, c): return
  (br, bc) = (r, c) + delta[role.dir]                  // 前方格
  if 越界: return
  bubbles = 0x005D3BD3(br, bc)
  if len(bubbles) != 1: return
  b = bubbles[0]; if b.type(+4) ∈ {0x0B, 0x0C} || b.+0x2c != 1: return     // +0x2c == 1：静止（0x005DF6FB 落地时置 1）
  for m = 8 down to 1:                                 // 与 kickbomb.go 的落点规则相同
      L = ((br + 13 + dy*m) % 13, (bc + 15 + dx*m) % 15)
      if 0x005D865C(G+0xE78, L) == 0: break            // 只看第一层地图元素
  else: return
  if L == (br, bc): return
  send 0x0FB4 {u16 role.id, u16 (u16)G+0x3838, u16 br, u16 bc, u16 L.row, u16 L.col,
               u8 role.dir, u16 b.+0x1c(主人), u32 b.+0x24(放置时间), u8 b.+0x1e(威力)}   // 20 字节
  // 0x005A1DE1 + 0x005A2F4C 上行；若本机是仲裁方（G+0x3824 == G+0x3830）再 0x005C8794 本地分发

canKick(role) = 0x005AD2AF:
  role.+0x3b7 == 1 && role.+0x3b8 == 2 &&
  (规则号 == 8 || !rule->vtbl[0xC]() || (role.+0x404 != 0 && role.+0x404->vtbl[0x30]()))
  // 规则 2 的槽 3 = 0x0060C0E0 恒返回 0 → 任何移动中的角色都能踢
```

仲裁方的 0x0FB4 处理 `0x006040CF`：本机是仲裁方，并且 (br, bc) 恰好有 1 个泡，就把它原样回显成 0x139C（Time 改成当前时间），并本地分发。**处理函数不检查踢泡者是谁**，Boss 的 ID 也照常通过。

> 对移植的意义：Boss 不需要专门的踢泡代码。只要 sidecar 的仲裁方每帧对 Boss 跑一遍"通用踢泡"（与 `UniversalKick` 的玩家路径相同：移动中、朝向前方 22 px 处进入另一格、那格恰好 1 个静止的普通泡），再用 Boss ID 发 0x0FB4/0x139C 即可。AI 只负责把 Boss 引向泡所在的格子。

### 3.2 走向直线 4 格内的泡 `0x005B38D9` [汇编]

```
nearBubble(&gc, &gr):
  for k in 1..4:
    if row-k ≥ 2 && hasBubble(row-k, col) && kickable(row-k, col): (gc,gr) = (col, row-k); return 1
    if row+k ≤ 12 && hasBubble(row+k, col) && kickable(...):       (gc,gr) = (col, row+k); return 1
    if col-k ≥ 0  && hasBubble(row, col-k) && kickable(...):       (gc,gr) = (col-k, row); return 1
    if col+k ≤ 14 && hasBubble(row, col+k) && kickable(...):       (gc,gr) = (col+k, row); return 1
  return 0
hasBubble(r,c) = 0x005B42FA: 4 ≤ r ≤ 12 && 0 ≤ c ≤ 14 && 泡列表非空          // ★ 行下限是 4
kickable(r,c)  = 0x005B4506: 界内 && 第一个泡类型 ∉ {0x0B,0x0C} && 第一个泡 +0x2c == 1
```

不看墙，也不检查中间有没有阻挡；寻路失败就落到 `randomItem()`。寻到的路径终点就是泡所在的格，Boss 走过去时会由 §3.1 把泡沿行进方向踢走。

### 3.3 踢泡打人 `0x005B4561` / `0x005B46BB`（含原版 bug）[汇编]

```
kickAtPlayer():
  for k in 0..4:
    // 检查的格子                  传给 aim 的方向（★ 与检查的方向行列互换）
    if row-k ≥ 0  && hasBubble(row-k, col) && kickable(row-k, col) && aim(row, col, 2, k): return 1
    if row+k ≤ 12 && hasBubble(row+k, col) && kickable(row+k, col) && aim(row, col, 0, k): return 1
    if col-k ≥ 0  && hasBubble(row, col-k) && kickable(row, col-k) && aim(row, col, 1, k): return 1
    if col+k ≤ 14 && hasBubble(row, col+k) && kickable(row, col+k) && aim(row, col, 3, k): return 1
  return 0

aim(row, col, d, k):                                   // 0x005B46BB，d 用标准方向码
  (dr, dc) = delta[d]
  B = ((row + 13 + dr*k) % 13, (col + 15 + dc*k) % 15) // 被评估的泡格
  for m = 8 down to 1:                                 // ★ 与真实踢泡不同：每个无元素的 m 都试，不是只看第一个
     L = ((B.r + 13 + dr*m) % 13, (B.c + 15 + dc*m) % 15)
     if 0x005D865C(G+0xE78, L) != 0: continue
     bs = 0x005D3BD3(B); if bs 为空: continue          // B 必须有泡（不检查能不能踢）
     power = int16(bs[0].+0x1e)                        // 线上的威力字段（空投普通泡 2、威力泡 5、技能 12 大泡 8）
     for p in G+0x1338（拷贝）:
        if p 是 NPC || p.+0x3b7 != 1: continue
        if (|p.row - L.r| ≤ power && p.col == L.c) || (|p.col - L.c| ≤ power && p.row == L.r):   // 不看墙
           if find5(col, row, B.c, B.r): storePath5(); return 1
  return 0
```

- **bug**：先确认"上方第 k 格有可踢的泡"，然后却评估"左方第 k 格的泡向左踢"。另外三个方向同样错位（下→右、左→上、右→下）。结果是：只有两个格子上**都有泡**时才会触发，路径走向的是评估的那一格 B，踢出的方向就是走过去的方向 d，这和评估一致。
- k = 0 时 B 就是 Boss 所在格，`find5` 起点等于终点，必然失败，所以 k = 0 永远不会触发。
- 成功时决策直接返回 `(int16)path.back.dir`。

### 3.4 随机去一个道具 `0x005B4155` [汇编]

```
randomItem():
  cand = [(r,c) for c in 0..14 for r in 0..12 if 0x005B275E(r,c)]   // 有可捡道具（不含 41/43）且无地图元素，列优先
  if cand 空: return 0
  repeat 3 次: k = RNG() % len(cand); if find5(col,row, cand[k].c, cand[k].r): storePath5(); return 1
  return 0
```

### 3.5 随机数消耗（仲裁方）

每次决策可能调用 RNG 的地方，按出现顺序：
- 技能 10 的 `0x005B8574`：空格数 - 5 次；
- 技能 12：`0x005E18B0` 1 次，`0x005B8753` 每个大泡 1 次，再加每个"单格"1 次（见 §4.3）；
- `flee5` / `wander5`：每次 1 次（候选非空时）；
- `randomItem`：最多 3 次。

`0x005B13F5`、`find5` 不用 RNG。和 AIType 3 一样，仲裁方的 RNG 会因此与其他端分叉，其他端只能照消息执行。

---

## 4. 技能解释器 `0x005B4947`（技能 10～12）[汇编]

### 4.1 数据与初始化（`0x005AFDFA`，`0x005B0211`～`0x005B02D8`）

- 所有 Boss：`N+0x830 = N+0x884 = Skills 数量`，`N+0x808[10]` 先填 -1 再写入技能表。
- **只有 RoleID 0x23/0x24（35/36）**：`N+0x85C[10]` 先填 -1 再写入同一份技能表；`N+0x834..0x858` 共 10 个冷却格**清零**（`memset(…, 0, 0x28)`）。

### 4.2 伪代码

```
kickSkills():
  now = G+0x3838
  if now < 10000: return                               // cmp 0x2710; jb
  body = 0x18D 字节清零: {u16 ObjectID=N.id, u32 Time=now, u16 PosX=int(x), u16 PosY=int(y),
                          u16 SkillID, u8 Count, Items[{u32 SceneID, u8 row, u8 col}...]}   // 偏移 0,2,6,8,0xA,0xC,0xD
  for i in 0 .. N+0x884 - 1:
    if now - cd[i] < 30000: return                     // cd[i] = N+0x834+4i，★ 按列表位置
    switch skills[i] (= N+0x85C[i]):
    case 10:
      SkillID = 10; Count = 5
      cells = randomFreeCells(5)                       // 0x005B8574，全图（规则 12 才用 vtbl+0xB8 的矩形）
      if len(cells) < 5: return
      Items = [{43, c.row, c.col} for c in cells]
      cd[0] = now                                      // N+0x834，★ 按技能号
      send(0x1169, body)
    case 11:
      SkillID = 11; Count = 0
      cd[1] = now                                      // N+0x838
      send(0x1169, body)                               // Items 区不清，可能带着本轮技能 10 的 5 项，Count=0 所以接收方不看
    case 12:
      if now - cd[2] < 60000: return                   // N+0x83C，0xEA60
      SkillID = 12; cd[2] = now
      n = 0x005E18B0()                                 // RNG % 300 % 3：0→3，1→2，2→1
      0x005B8753(nSingle=n, &single, nBig=n, &big)     // §4.3
      if len(single) < n: n = len(single)
      Count = n; Items = [{11, big[j].row, big[j].col} for j in 0..n-1]   // ★ 取 big，但数量按 single 截断
      send(0x1169, body)
    default: continue                                  // 例如 0：什么都不做，也不更新冷却
send = 0x005A1DE1 + 0x005A2F4C（上行）+ 0x005C8794 + 0x0087960B（本地分发）
```

- 只有冷却没到（30 s 或技能 12 的 60 s）、或技能 10 的空格不足 5 个时才 `return` 结束整轮；`default` 只是跳过。
- 冷却格只在**成功发送时**更新，技能 10 空格不足时不更新。

### 4.3 技能 12 的选格 `0x005B8753(nSingle, &single, nBig, &big)`（`ret 0x10`）[汇编]

```
free = 行优先列出 (row,col)：两层地图元素(G+0xE78, G+0xE2C)都没有、无泡、特殊场景对象(G+0xF8C)为空、地面道具(G+0xF10)为空
repeat nBig 次:
   k = RNG() % len(free); j = k
   loop:
     (r,c) = free[j]
     if r > 0 && c > 0 && (r-1,c-1)、(r-1,c)、(r,c-1) 都在 free 里（0x005B58A9 线性查找）:
        big.push(free[j])
        依次把下标 j, i(r,c-1), i(r-1,c), i(r-1,c-1) 用"末项覆盖、长度-1"删掉；长度到 0 就整体返回
        break
     j = (j+1) % len(free); if j == k: break           // 绕一圈没找到：这次不放
n = min(nSingle, len(free))
repeat n 次: k = RNG() % len(free); single.push(free[k]); free[k] = free[last]; len--  // 长度到 0 返回
```

- 大泡的锚点是 2×2 空地的**右下格**。
- "单格"结果只用来截断数量，没有别的用处。
- 原版隐患：`big` 比 `n` 少时会越界读（`big` 为空时读空指针）。移植时取 `min(n, len(single), len(big))`。

### 4.4 冷却格错位 —— 证实 `competitive_overlay.go` 的说法

检查用 `cd[位置]`，更新用 `cd[技能号 - 10]`：

| 技能表 | 实际节奏 |
|---|---|
| 鲁尼 `{10,11,12}` | 位置和技能号对齐。30 s：放 10、11（12 卡在 60 s 门槛，结束本轮）；60 s：10、11、12；90 s：10、11；120 s：全部…… 10/11 每 30 s，12 每 60 s |
| 小小罗 `{0,11,12}` | 位置 0 是 0：`cd[0]` 永远是 0，30 s 后永远通过，跳过。11 查 `cd[1]` 并更新 `cd[1]`，12 查并更新 `cd[2]`。结果与上一行相同，只是没有减速胶 |
| 压缩成 `{11,12}`（错误） | 11 查 `cd[0]`（永远 0）、更新 `cd[1]` → **每次决策都放 11**；12 查 `cd[1]`（刚被更新）→ 永远放不出来 |

规则 2 一局 240 s：鲁尼约放 10/11 各 7～8 次（30、60……s），12 约 3～4 次（60、120、180……s）。前提是这些时刻有决策调用，而决策至少每 7 帧一次，所以总会有。

---

## 5. 0x1169 接收方 `0x005FDB18` 中的 10/11/12 [伪C，0x0B 分支已用汇编核对]

| SkillID | 效果 |
|---|---|
| 10 | 同技能 1：每项 `0x005CB79A(SceneID=43)` 建场景对象，起点 (PosY/40, PosX/40)，`0x005D9BBF(obj,row,col,400,0)` 以 400 px/s 飞到目标格。落地后是一次性减速胶（`boss-ai-re.md` §7.2/7.3：非 Boss 踩到后速度档 2，持续 10 s；ID 30000～30200 免疫） |
| 11 | `role = 0x005A287F(ObjectID)`（Boss 自己），`role+0x318`（免伤组件）`->0x005B5202(10000)`：`+0x18 = 1`、`+0x1c = 10000`，挂 `effect/flash2.eff`；`0x005B52D1(1)` 是空函数。**所有客户端都执行，不检查仲裁方**。能否受伤看 `0x005AD175`（`+0x318` 未激活），所以这 10 s 内 0x10F4 无效。再次放技能 11 会把剩余时间重置为 10 s |
| 12 | `Count < 0x41` 时，泡类型取第一项的 SceneID（11）。逐项：`0x005CB087/0x005CC58F(11, P+0x274, P+0x278)` 建泡；`+0x1c` = ObjectID（Boss），`+0x24` = Time，**`+0x1e` = 8（威力）**，`+0x20` = Boss 的 `P->vtbl[0x24]()`；`0x005DF590(row,col)` 设起点，`0x005D388A` 入泡管理器，`0x005D3C1D(b,row,col,400,0)` 飞向同一格，所以**原地出现，没有飞行**。这一分支不调用 RNG，各端结果一致 |

关于类型 11 的大泡 [推断]：
- `0x005DF6FB` 在泡落地时置 `+0x2c = 1`，类型 11 用的绘制锚点是 (45,70)，普通泡是 (0,15)。图像向左上多偏约一格，与"锚点是 2×2 的右下格"吻合。
- 大泡不能踢：踢泡命令、`kickable`、`passable5` 都排除了类型 0x0B/0x0C。
- 它在 2×2 范围内怎样阻挡、怎样爆炸（十字从哪里算、是否占 4 格）没有追。sidecar 现在**不支持类型 11**（`kickbomb.go` 第 28 行：空投的大泡"没有做"）。

---

## 6. 规则 2 的 Boss 受伤、死亡、掉落：与规则 1 的差别

全部共用 `boss-lifecycle-re.md` 的代码：
- 0xFA4 → `0x00607DDC` 判定，发 0x10F4；每条固定扣 1 HP，之后 3 s 免伤；
- 0x116B 部分掉落：接收方要求规则号为 1、2 或 12；
- 0x0FA7 死亡和全部掉落。

规则 2 虚表 `0x007A44FC` [汇编，转储]：槽 4 `0x005F4A5D`（不复活）、槽 6 `0x005F4B16`（死亡掉落）、槽 7 `0x005F4A7A`（受击掉落）都与规则 1 相同。出生格同样用 `0x005B653F` 的交替表（规则 2 的槽 1 是 `0x0060BCEC`，它调用 `0x005B653F`、`0x005B63CD`，跳过墙内道具 `0x005B6441`，然后 `0x005E1AAD` 建空投道具池）。

真正不同的只有下面几点：
1. **伤害来源**：Boss 自己不放普通泡，玩家也不能放泡。能炸到 Boss 的只有：空投泡（主人是仲裁方，威力字段 2/5）、被任何人（包括 Boss 自己）踢飞的泡、技能 12 的大泡（主人是 Boss，威力 8，同样能炸到 Boss 自己，判定不看主人）。
2. **技能 11 的 10 s 免伤**：从 30 s 起每 30 s 一次，Boss 大约有 1/3 的时间打不动；它和受击后的 3 s 免伤共用 `+0x318`。
3. **引信**：AIType 5 不调用 `0x005B0BAF`，所以不会把 `G+0xF88` 改成 2500，所有泡都按默认 2990 ms 引爆，各端一致。规则 2 的槽 9（`+0x24` = `0x005F4D51`）也就不会被 AI 调到。
4. **击杀**：接触处理器 `0x0060C10B/0x0060C148` 在规则 2 的命令表里，Boss 碰到被困（3/0x0A）玩家就击杀；而追杀被困玩家是 AIType 5 优先级最高的移动目标，而且不限距离。
5. 没有发现规则 2 专用的 Boss 掉落或死亡分支。规则 2 不下发墙内道具，0x0FA7 里"基础道具 1/2/3 按拾取数"那一部分照常执行（`S+0x51a4 == 0` 时）。

---

## 7. 移植清单（sidecar）

现状：`boss_ai.go` 的 `newBossController` 只把 AIType 3 交给 `bossGeneral`，其余 AIType 都用占位的 `bossWander`；`applyBossSkill` 只实现了 1/4/10。按本文需要：

1. 新控制器 `bossKick`（AIType 5）：
   - 用 §2.1 的流程，每次决策先跑 §4 的技能；
   - 决策节奏用现有的 7 帧计数器（返回 9 时不清零）；
   - 实现 §2.2 的 A*：open 列表用插入顺序的 slice，取最小 f 时同 f 取先插入的；不能上行到第 0～3 行；
   - `t` 取 `kickRule.lastBatch`（初值 -10000，`applied()` 时更新），两者语义一致；
   - `followPath5` 返回 0 时原版的方向是垃圾值，移植返回 9。
2. 踢泡：仲裁方每帧对 Boss 跑与玩家相同的通用踢泡判定（§3.1），用 Boss ID 发 0x0FB4，由现有的仲裁方逻辑回显 0x139C。要确认引擎的 `UniversalKick` 对 NPC 行动者也生效，并且服务端接受仲裁方代 Boss 发的 0x0FB4。
3. 技能：
   - 10：复用减速胶的飞行和落地；
   - 11：给 Boss 10 s 免伤并播 flash2 闪光；
   - 12：需要引擎支持类型 11 的 2×2 大泡（威力 8、主人 Boss、不能踢、阻挡寻路），落点按 §4.3；
   - 冷却格的位置/技能号错位要原样保留，这样服务端目录里的 `{0,11,12}` 才成立。
4. RNG：按 §3.5 的顺序消耗仲裁方本地的发生器（只影响仲裁方，不需要与其他端同步）。

---

## 8. 未证实 / 待查

1. **类型 11 大泡的占格和爆炸**（只确认了锚点 (45,70) 和"右下格"的选格规则）；类型 0x0C 是什么泡也没查。
2. **地图 701/702 的第 0～3 行**：足球寻路器禁止上行进入这几行，选泡目标要求行 ≥ 4，游走候选是行 3～11、列 0～13。推测这些行是不可进入的区域（看台、球门），要用地图数据核对。若 Boss 出生在第 0～3 行，它只能往下走。
3. `0x005D8BC1(row,col,4)`、危险表 1/2 的精确含义（推断见 §2.2、§2.6）。
4. 命令类型 → Gene/SameGrid/DiffGrid 的对应关系（踢泡命令类型 2 被推断为 SameGrid）。
5. `followPath5` 返回 0 后原版实际下发的方向字节（堆头低字节，> 3），以及 `0x005C07FE` 收到非法方向时怎样表现。
6. `0x005B429F`/`0x005B438D`（NPC 踢脚下的泡）没有静态引用。全量字节扫描既找不到 call/jmp rel32，也找不到数据指针，推断是死代码，但不能排除加壳区间接调用。
7. 出生模式：`boss-lifecycle-re.md` §1.5 指出仲裁方对 RoleID 32 不把 `+0x3b7` 恢复为 1。RoleID 35/36 同样不在 24/28/34 之列，**如果模式一直停在 9，移动命令（要求 1/0x0B）和踢泡（要求 1）都不会执行**，Boss 会站着不动。后续默认角色帧执行已确认会经3000ms状态控制器恢复（`boss-birth-controller-re.md`），该段永久停住的猜测不成立；完整Ready时钟与命令顺序的宿主适配仍待完成。
8. 帧长（决策每 7 帧一次换成毫秒）沿用 `boss-ai-re.md` §11 第 1 条的未决问题。

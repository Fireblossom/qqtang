# 聊天房间（游戏类型 4）里的走动与放泡：静态逆向结果

只读调研：没有运行客户端，没有启动 Ghidra，只读了 `~/qqtang-re/decompiled/` 的伪 C，并用 capstone 核对机器码，用 pefile 读了数据。除本文件外没有改动任何文件。

依据标注：**[证实]** 读过伪 C 或反汇编，并且数值已核对；**[推断]** 根据代码结构或旁证得出，还没逐条核对；**[未知]** 没查清楚。

`$C` = `~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched`，`$S` = `/private/tmp/qqtang-codex-boss`。文中的地址都是 Client.exe 的虚拟地址，除非另外注明模块。

---

## 0. 结论摘要

1. **走动和放泡都不是聊天房间独有的。** 它们属于 Client.exe 的"房间场景"对象（构造函数 `0x00457080`，虚表 `0x00799BF8`），每帧更新函数是 `0x00457740`，函数里没有检查房间类型，所以普通房间也一样能走、能放泡。[证实] 房间类型（偏移 `+0x315`，由 `SetRoomType` 写入）只影响两件事：名牌默认显示还是隐藏（§2.6），以及点击头像弹出私聊或踢人的处理（§2.8）。
2. **按键**：方向键走，空格放泡，F8 切换名牌显示。键位来自用户配置 `keyboard_layout2`，默认值见 §2.1。[证实]
3. **速度固定为 0.26 像素/毫秒。** 每帧位移为 `int(dt × 0.26)`，小数部分截掉。可走区域是一个固定矩形：x 在 41..481、y 在 106..466（含边界），与背景图无关。走出界的那一帧整帧撤回。[证实]
4. **放出的泡泡只是装饰。** 泡泡落在 12 列 × 10 行、每格 40 像素的格子里，3000 毫秒后爆炸。相邻的泡泡连锁引爆，火焰是"中心加四个邻格"的十字，持续 400 毫秒，并播放 `x10_01.wav`。爆炸不伤人，没有个数上限，一格最多一个泡泡。[证实]
5. **网络消息只有两种**，都包在 `ROOM_MSG_DATA`（schema 0x0BBA）里：`ROOM_PLAYER_MOVE_INFO` 0x0BB8（17 字节）和 `ROOM_PLAYER_PUT_BOMB` 0x0BB9（9 字节）。发送路径：Client.exe `0x00458FB0` 编码后交给 QQTPPP.dll（`CQQTP2pModule`）。原版客户端在等待房间里走 TCP 0x0065 "房间快速包"（有抓包），服务端再转成 QQTPPP Type-2 UDP 发给其他人。接收路径：QQTSection.dll `0x1000C814`，当状态等于 1（在房间里）时进入 `0x1000C860`，后者解码并执行脚本字符串 `remotePlayerMove(...)` 或 `remotePlayerPutBomb(...)`，脚本再调用原生函数 `RemotePlayerMove` 或 `RemotePlayerPutBomb`。[证实]
6. **远端头像沿着收到的目标点逐个走过去。** 收到的坐标排进队列，头像先走横向、再走纵向，速度同样是固定的 0.26，**收到的 `Speed` 字段不参与移动**。走到最后一个目标点就停下，并转向包里给的朝向。`RemotePlayerPutBomb` **不用包里的坐标**，而是在本地看到的那个头像当前位置放泡。[证实]
7. **`ChangePosition` 和 `Navigate` 与走动无关。** `ChangePosition` 在所有程序里都找不到注册，原版脚本调用它会直接报错；它出现在装备栏拖放（`OnDragItemDrop`）里。`Navigate(n)` 读取 `config/http.ini` 里的 VIP 网址并打开网页。[证实]
8. **服务端已经能转发这两种消息，不需要新增命令。** `internal/server/probe/room_fast_dispatch.go` 会接收 TCP 0x0065 上传的等待房间事件，校验座位后，对每个已登记 Type-1 的收件人发一个 Type-2，Data 就是那条 `ROOM_MSG_DATA`。`room_peer_udp.go` 会原样转发任何房间里的 Type-2。Python 宿主要补的是客户端这一侧：一个 UDP 套接字和 Type-1 登记，发 0x0065，收 Type-2（§4、§5）。只有一点没核实：原版 QQTPPP 是否接受"明文 ROOM_MSG_DATA 直接作为 Type-2 Data"。这是服务端代码的假设，本次没能在受保护的 QQTPPP.dll 里证实。[推断]

---

## 1. 对象和偏移

### 1.1 房间场景对象（`this` = 房间场景，构造函数 `0x00457080`）

| 偏移 | 含义 | 依据 |
|---|---|---|
| `+0x28` | 键盘输入对象（DirectInput），构造时由 `0x00458CC0` 载入键位 | [证实] `0x00457080`、`0x00458163`（`add ecx,0x28`） |
| `+0x22C` | 泡泡和火焰管理器（`0x0045A0F0`，0x1F0 字节，网格 10×12） | [证实] |
| `+0x230 + 4·seat` | 8 个座位的头像对象（`0x00441860`，0x54 字节），没有人时为 0 | [证实] |
| `+0x2A0 + seat` | 是否播放闲置动作（`role != 23`） | [证实] `0x0045862E` |
| `+0x2A8 + 8·seat` / `+0x2AC + 8·seat` | 座位初始点 x / y（`room_setPlayerPos` 写入） | [证实] `0x00458A50` |
| `+0x2E8` | 自己的座位号 0..7（`SetSelfSeatId`） | [证实] `0x00459A50` |
| `+0x2EC/+0x2F0/+0x2F4/+0x2F8` | 可走矩形 minX=41, minY=106, maxX=481, maxY=466 | [证实] `0x0045710F`–`0x00457136` |
| `+0x2FC` | 距上次发送移动包经过的毫秒数（每帧加 dt） | [证实] `0x00457740` 第一行 |
| `+0x300/+0x304/+0x308` | 上次发出的 x、y、方向 | [证实] `0x00457CF0` |
| `+0x30C` | QQTPPP 接口（GUID `13f2169f-6f2e-b14e-afe1-e59b64754768`，CQQTP2pModule） | [证实] GUID 在 QQTPPP.dll 和 QQTSection.dll 里都出现 |
| `+0x310` | 消息编解码接口（GUID `401dbcee-…`，与 QQTSection 的解码器相同） | [证实] |
| `+0x314` | F8 名牌开关的当前值 | [证实] `0x0045816B` |
| `+0x315` | 房间游戏类型（`SetRoomType`），4 表示聊天房间 | [证实] `0x00459A80` |
| `+0x318` | 婚礼特效 | [证实] `0x00459110` |
| `+0x31C` | 0x18 字节对象的列表；两人相距小于 50 像素时在两人中点上方 70 像素处放一个特效，推断是情侣特效 | [推断] `0x00440D70` |

### 1.2 头像对象（`0x00441860`，0x54 字节）

| 偏移 | 含义 |
|---|---|
| `+0x04` | 角色精灵（`0x00424460(role, team)`，0x330 字节） |
| `+0x08` | 是否画名牌 |
| `+0x0C` | 名牌对象（`0x0044C2B0`） |
| `+0x10` | 方向：0 右、1 上、2 左、3 下（精灵朝向角 0/90/180/270，`0x004416D0`） |
| `+0x14` | 速度（float）：走动时为 0.26（`0x3E851EB8`），停下时为 0 |
| `+0x18/+0x1C` | 位置 x/y（屏幕坐标，即 `room_setPlayerPos` 给的那个点） |
| `+0x20/+0x24/+0x28` | 泡泡样式（≥1，默认 1）、泡泡特效 ID、颜色（`room_playerWear(seat,'bomb',idx,effect,color)` → `0x00442040`） |
| `+0x2C..+0x38` | 可走矩形（创建时从房间 `+0x2EC` 复制，`0x00459B90`） |
| `+0x3C/+0x40` | 角色 ID / 队伍色（`room_changePlayer` 的参数） |
| `+0x48` | 远端目标点队列（`std::list`，每项 16 字节：`{dir, x, y, speed}`） |

动作编号（`0x007DA010` 表）：1 stand、4 walk、11 wait，其余见表。`0x00442000(state)` 用于切换动作。[证实]

---

## 2. 本地控制（每帧 `0x00457740(dt)` 的顺序）

每帧按以下顺序执行 [证实]：
1. `+0x2FC += dt`
2. 读键盘：`0x00457E90`
3. 泡泡和火焰计时：`0x00459990(dt)`
4. 对每个头像：闲置动作、名牌开关、移动 `0x00441CF0(dt)`
5. `0x00452D20(dt)`（房间 `+4` 处 8 个对象的更新，用途**[未知]**）
6. 发送移动包：`0x00457CF0`
7. 婚礼特效、`+0x31C` 列表里的特效
8. 只有聊天房间才执行：鼠标点头像 `0x00457AE0`

`dt` 是场景每帧传进来的毫秒数。原版的实际帧间隔 **[未知]**。

### 2.1 按键

输入对象有 0x20 个"槽位"，`0x005A5391(k)` 取槽位 k 的 DIK 码，由 `0x00458CC0` 从 `keyboard_layout2`（26 字节，按 UIN 取）载入。取不到配置时用 `0x0048AE40` 的默认值 [证实]：

| 槽位 | layout 字节 | 默认 VK | 房间里的作用 |
|---|---|---|---|
| 1 | [0] | 0x25 ← | 方向 2（左） |
| 2 | [1] | 0x27 → | 方向 0（右） |
| 3 | [2] | 0x26 ↑ | 方向 1（上） |
| 4 | [3] | 0x28 ↓ | 方向 3（下） |
| 5 | [4] | 0x20 空格 | 放泡（只在**按下的瞬间**触发，`+0x11` 是按下沿，`0x005A5285`） |
| 0x17 | 固定 0x77（F8） | — | 切换名牌，每按一次翻转一次（`+0x23`，`0x005A4C52` 末尾） |

方向键的取法：`0x005A52B2` 和 `0x005A52D7` 维护一个"正按着的方向键"栈，按下时压栈、松开时移除。`0x005A4BEF` 返回**最后按下且还按着**的那个键 [证实]。`0x00457E90` 拿这个键依次和槽位 1..4 比较：
* 匹配到槽位 k：调用 `0x00441B80(dir)`，设置方向、把速度设为 0.26、切到 walk 动作（每帧都调用，动作没变就不重新开始）。
* 一个都没匹配，并且头像当前是 walk 动作：调用 `0x00441BD0`，速度归 0，切回 stand 动作。

聊天输入框有焦点时方向键是否仍然生效 **[未知]**。DirectInput 设备总是开着，代码里没看到判断。Python 宿主建议在编辑框有焦点时忽略这些键。

### 2.2 位移（`0x00441CF0`，本地头像和远端头像共用）

```
disp = (0,0)
if 目标队列为空:                       # 本地头像始终走这一支
    n = int(dt * speed)                # 0x00441810: fild dt; fmul speed; _ftol 向零截断
    disp = {0:(n,0), 1:(0,-n), 2:(-n,0), 3:(0,n)}[dir]
    pos += disp
else:                                  # 远端头像，见 §3.3
    ...
if not (minX <= x <= maxX and minY <= y <= maxY):
    pos -= disp                        # 整帧撤回，不会贴边
set_pos(x, y)                          # 0x00441B10: 精灵 0x00424990(x,y)，名牌放在 (x-30, y-90)
```
[证实，已对照 `0x00441CF0` 的机器码]

* 速度是常数 0.26：`0x00441B80` 里写的立即数是 `0x3E851EB8`，抓包里的 Speed 字段也是 0.26。[证实]
* 每帧向零截断，所以实际速度取决于帧间隔：dt=16 时为 4 像素（0.25），dt=20 时为 5 像素。[证实]
* 矩形是固定常数，换背景（`chat<N>_stand.img`）不会改变。背景控件 `UI.room.chatRoomBg` 的范围是 (11,62)，大小 491×426。[证实]

### 2.3 头像的初始位置和重建

* `room_setPlayerPos(seat,x,y)` 只写入座位初始点表 `+0x2A8`，**不会移动已经存在的头像**。[证实] `0x00458A50`
  脚本在 OnInit 里给出的初始点：上排 `(控件x+70, 控件y+110)`，下排 `(控件x+70, 控件y+90)`，即 1..4 号座位 = (80,185) (202,185) (324,185) (446,185)，5..8 号座位 = (80,435) (202,435) (324,435) (446,435)。
* `room_changePlayer(seat, team, role)` → `0x00458240` [证实]：
  * 头像存在，并且角色和队伍色都没变：只刷新名牌（`0x00441C10`）。
  * 头像存在，但角色或队伍色变了：**记下当前位置**，删掉旧头像，在当前位置重建。
  * 头像不存在：在座位初始点 `+0x2A8` 新建。
  * 新建后：朝下（方向 3）；可走矩形取房间 `+0x2EC`；名牌刷新；然后对该座位身上属于这个角色的物品逐个调用脚本 `useCommItem(...)`。
  * `role == -1` 时删除头像。

### 2.4 放泡（本地）

`0x00457E90` 末尾 [证实]：空格按下的那一帧，如果自己有头像：
```
x, y = 自己头像的位置
bombs.place(x, y, style=+0x20, effect=+0x24, color=+0x28)    # 0x0045A150
send(0x0BB9, {seat=+0x2E8, x, y})                            # 不管上一行是否成功都会发送
```

### 2.5 泡泡和火焰（`+0x22C` 管理器）

* 格子换算 [证实]：`col = (x + 20 - 41) // 40`，`row = (y + 20 - 106) // 40`（`0x0045B130/0x0045B160`，被除数都是正数，截断和向下取整结果相同）。格子的原点为 `cellX = 41 + 40·col`，`cellY = 106 + 40·row`（`0x0045B190/0x0045B1C0`）。合法范围是 col 0..11、row 0..9。可走矩形内的任何点都落在网格里。
* `place`（`0x0045A150`）：越界或该格已经有泡泡时什么都不做。否则新建泡泡 `0x00428D60`：图像为 `object/bomb/bomb<style>_stand.img`（场景对象 ID 为 `3<<24 | style<<8 | 1`）；画在 `(cellX-20, cellY-50)`（`0x00429060`）；泡泡特效由 `(effect, color)` 经接口 `+0x338` 换成特效文件名，不为空时在 `(cellX, cellY)` 处播放（`0x00583FF0`）。[证实]
* 计时（`0x0045A2E0`）：每个泡泡累计 dt，**超过 3000 毫秒**（`0x00429030`：`3000 < t`）就引爆 [证实]。引爆时：
  1. `0x0045B700(row,col)` 递归收集：先把本格加入列表并清空，然后对上下左右四格递归。结果是**所有四邻接相连的泡泡一起爆炸**，范围不限。[证实]
  2. 对每个爆炸的泡泡，在本格放火焰 1（中心），在 row-1 放 7（上），row+1 放 9（下），col-1 放 8（左），col+1 放 6（右）。越界的格子跳过。火焰图像为 `object/flame/flame<k>_stand.img`（40×40，7 帧），画在 `(cellX-15, cellY-35)`。[证实] `0x0045B8D0`、`0x004290C0`
  3. 每次引爆调用一次 `PlaySound("x10_01.wav")`。[证实]
* 火焰（`0x00429240`）计时超过 **400 毫秒** 后删除（`0x004292D0`）。动画按 `dt × 2.5` 推进（`0x007971C0` 处的 double 为 2.5）。[证实] 精灵的基础帧间隔 **[未知]**，可以沿用 battle/scene.py 的 FLAME_FRAME_MS，再乘 2.5 的速度。
* 泡泡图像有 4 帧，循环播放，帧间隔 **[未知]**。
* 不伤头像，也不阻挡走动。房间代码里没有任何地方读取泡泡格子来做碰撞。[证实：可走矩形是唯一的限制]

### 2.6 绘制（`SC_room_drawPlayer` → `0x004575D0`，即 `UI.room.demo` 控件，initlayer 99999）

绘制顺序 [证实]：
1. 先画泡泡（`0x0045A670`），再画火焰（`0x0045A700`）。
2. `0x00452D90`：房间 `+4` 处 8 个对象的绘制，用途**[未知]**。
3. 按座位 0..7 的顺序处理每个头像（**不按 y 排序**）：
   * 把 `UI.room.playerChat<seat>` 移到 `(x-40, y-85)`（UI 接口 `+0x48`）。
   * 画精灵（`0x00425220`）。
   * 名牌开关打开时画名牌（`0x0044C4A0`）。
4. `+0x31C` 列表里的特效。
5. 婚礼特效。

注意：Python 宿主当前的 `room.py BUBBLE_OFFSET=(20,105)` 与原版的 `(x-40, y-85)` 不同。可能是为了配合 `Win_Move2Pos` 和 DYLABEL 的定位方式。没有核对 `+0x48` 是否就是 Move2Pos。[推断]

名牌（`0x0044C4A0`，`lx = x-30`，`ly = y-90`）[证实]：
* `object/misc/misc171_stand.img`（83×25 的底板）画在 `(lx-12, ly+1)`。
* `+0x25` 为真时，`object/misc/misc172_stand.img`（31×31 的图标）画在 `(lx-37, ly-5)`。`+0x25` 来自座位信息结构末尾附近的一个字节；QQTSection `0x1000C9EC` 写入它，值为 `参数 != 1`。推断是 P2P 或网络状态标记，含义 **[未知]**。
* 昵称用 12 号字写两遍：先在 `(lx+0x1C-3·len, ly+6)` 写黑色（0xFF000000），再在 `(lx+0x1B-3·len, ly+7)` 写彩色。自己（`+0x24` 为真）用绿色 0xFF00FF00，其他人用黄色 0xFFFFFF00。`len` 是 GBK 字节数。昵称来自 `GetSeatInfo(seat)`，推断就是 `m_szQQNickname`。

名牌是否显示（`0x00457740`）[证实]：
* 聊天房间（`+0x315 == 4`）：`show = (F8开关 == 0)`，即**默认显示**，按 F8 隐藏。
* 其他房间：`show = F8开关`，即默认隐藏。

### 2.7 闲置动作（所有房间都有，`role != 23`）

[证实] `0x00457740`。闲置计时器初值为 `5000 + rand()%4000`；计时到了，并且 `0x0045BEE0()` 为真（某个设置，**[未知]**），就切到 wait 动作。wait 动作计时器初值为 2500；到点后切回 stand，再把计时器设为 `rand()%4000`。计时用的是全局帧间隔 `0x0042ECD0()`，不是参数 dt。走动时也可能被切成 wait，下一帧方向键又会把它切回 walk。

### 2.8 点击头像（只在聊天房间）

`0x00457AE0` [证实]：取鼠标事件队列的第一个事件。如果鼠标点在某个头像的 `x-25 < mx < x+25` 且 `y-65 < my < y` 范围内（按座位顺序找第一个命中的头像）：
* 事件 0xC（WM_RBUTTONDOWN 右键按下，`0x00435C40`）：消费这个事件，执行 `ShowKickoutDlg(seat)`。
* 事件 0xB（WM_LBUTTONDBLCLK 左键双击）：消费这个事件，执行 `chatWithPlayerInChatRoom(seat)`。

座位控件在聊天房间里被 `forbidPlayer` 禁用，点击会穿过它们，落到这里。

补充（2026-11-17，`docs/section-timer-re.md` §3）：鼠标位置取输入对象 `+0x504/+0x508`；每帧先更新场景（`0x00431ED0` → `0x00441590` → `0x00457740`），后把输入队列交给控件（`0x00431CB0`），所以不管上面有没有控件，人物上的这两种事件都先被这里取走。宿主 `RoomWalk.click` 已实现。

---

## 3. 网络消息

### 3.1 消息结构（QQTMsgData.bin，用 `internal/tooling/msgtable` 解析；内存布局按 1 字节对齐）

`ROOM_MSG_DATA`（schema 0x0BBA，序号 235）：经编解码器序列化后字段为**大端**。

| 偏移 | 大小 | 字段 | 说明 |
|---|---|---|---|
| 0 | 4 | Time | 发送方 `GetTickCount()`，接收方不用 |
| 4 | 2 | DataID | 0x0BB8 移动 / 0x0BB9 放泡 |
| 6 | 2 | DataLen | 17 或 9 |
| 8 | 4 | Sequence | 全局递增计数器 `DAT_007FB50C`，移动和放泡共用，接收方不用 |
| 12 | DataLen | Data | **原始内存结构，小端**（发送方 memcpy 进去，接收方 memcpy 出来） |

`ROOM_PLAYER_MOVE_INFO`（Data，0x0BB8，17 字节）[证实：发送 `0x00457CF0`，接收 `0x1000C943`，与 msgtable 一致]：

| 偏移 | 大小 | 字段 |
|---|---|---|
| 0 | 1 | SeatId（0..7） |
| 1 | 4 | PosX（i32 LE） |
| 5 | 4 | PosY（i32 LE） |
| 9 | 4 | Dir（0 右、1 上、2 左、3 下） |
| 13 | 4 | Speed（float32 LE：0.26 或 0） |

`ROOM_PLAYER_PUT_BOMB`（Data，0x0BB9，9 字节）：`u8 SeatId, i32 PosX, i32 PosY`（LE）。[证实]

抓包实例（`$S/internal/protocol/game/room_fast_packet_test.go`，本次用 LocalKey 解密核对过）：
```
移动 ROOM_MSG_DATA: 15179aa5 0bb8 0011 00000027 | 01 ec000000 1b010000 00000000 b81e853e
                    座位1, x=236, y=283, dir=0, speed=0.26
放泡 ROOM_MSG_DATA: 15179e4e 0bb9 0009 0000002a | 01 34010000 3b010000      座位1, x=308, y=315
```

### 3.2 发送方（Client.exe）

* **移动**（`0x00457CF0`，每帧执行）[证实，已核对机器码]：
  ```
  if 自己有头像:
      if (x,y) != (lastX,lastY) and (elapsed > 300 or dir != lastDir):    # elapsed 按无符号比较
          last = (x,y,dir); send 0x0BB8 {seat, x, y, dir, speed}; elapsed = 0
  ```
  也就是说：走动时最多每 300 毫秒发一次；转向时立即发；停下后，最后位置会在 300 毫秒内补发一次，这一包的 speed 为 0；原地不动就不发。刚进房时会发一次自己的初始位置，前提是 `+0x300/+0x304` 的初值与实际位置不同（**[推断]**，构造函数里没看到初始化这两个字段）。**不会补发给后来进房的人**：后进来的人在别人下一次走动之前，只能看到他们站在座位初始点。[推断]
* **放泡**：见 §2.4，每次按下都发。
* **封装**（`0x00458FB0`）[证实]：先填好 `ROOM_MSG_DATA{Time=GetTickCount, DataID, DataLen, Sequence=DAT_007FB50C++, Data}`，再用 `+0x310` 接口的 `+0x10` 按 schema 0x0BBA 编码，最后调用 `+0x30C`（QQTPPP）的 `+0x24(len, buf, 2)`。后续由 QQTPPP 决定走哪条通道。原版在等待房间里实际发出的是 TCP 0x0065 房间快速包（§4.2，有抓包）[证实：抓包]；是否同时发 Type-2 **[推断：不发]**。

### 3.3 接收方

1. QQTPPP 收到后，调用 QQTSection 接口 `0x10054808` 处的槽位 → `0x100242B0` → `0x1000C814(…, buf, len)`（日志字符串为 `CMySection::OnRecvP2pTcpData, uin=%u`）。[证实]
2. `0x1000C814`：section 状态 `+4 == 1`（在房间里）时进入 `0x1000C860`；否则交给对局模块 `+0x11FF8`。[证实]
3. `0x1000C860`：按 schema 0x0BBA 解码 `buf`。DataID 为 3000 时，执行 `sprintf("remotePlayerMove(%d,%d,%d,%d,%f)", seat, Dir, PosX, PosY, (double)Speed)`；为 3001 时，执行 `sprintf("remotePlayerPutBomb(%d,%d,%d)", seat, PosX, PosY)`。然后通过脚本接口 `+0x10` 执行这个字符串。其他 DataID 丢弃。**注意参数顺序**：Dir 在 PosX 前面，而结构体里 Dir 在 PosY 后面。[证实，已核对 sprintf 的压栈顺序]
4. 脚本 `remotePlayerMove(bSeatID, nDir, nPosX, nPosY, fSpeed)`（`uiRoom.py:695`）打印 `'remotePlayerMove'`，然后调用原生函数 `RemotePlayerMove`。`remotePlayerPutBomb` 直接调用 `RemotePlayerPutBomb`。

原生函数 [证实]：
* `RemotePlayerMove(seat, dir, x, y, speed)`：入口 `0x004595A0` → `0x00458B90`，座位没有头像时忽略；否则 → `0x004421A0`：把 `{dir,x,y,speed}` 与队列最后一项逐字节比较（memcmp 16 字节），不同才追加到队尾。
* `RemotePlayerPutBomb(seat, x, y)`：入口 `0x00459530` → `0x00458AA0`，**忽略 x、y**，在该头像**当前本地位置**放泡，样式取它自己的 `+0x20/+0x24/+0x28`。不发声音，也不发消息。

远端头像每帧 `0x00441CF0`，队列非空时 [证实，已核对机器码]：
```
t = 队首 {tdir, tx, ty, tspeed}
if (x,y) == (tx,ty):
    if 队列长度 == 1: stop()               # speed=0, stand
    dir = tdir; 弹出队首                    # 这一帧不移动
else:
    if x != tx: walk(0 if x < tx else 2)  # walk() = 0x00441B80：设方向、speed=0.26、walk 动作
    else:       walk(3 if y < ty else 1)
    n = int(dt * 0.26); pos += 单位向量(dir) * n
    # 只钳制正在移动的那根轴，防止越过目标点
    if dir==0 and x>tx: x=tx
    elif dir==2 and x<tx: x=tx
    elif dir==3 and y>ty: y=ty
    elif dir==1 and y<ty: y=ty
越界撤回（同 §2.2）；set_pos
```
所以远端头像走的是"先横后纵"的折线，速度由本地固定，包里的 `tspeed` 没有用到。到达最后一个点后停下，并转向包里给的朝向。

---

## 4. 传输层（Python 宿主需要实现的部分）

> 服务端的现有实现：`$S/internal/server/probe/room_fast_dispatch.go`（0x0065 → Type-2）、`room_peer_udp.go`（Type-1 登记、Type-2 原样转发）、`$S/internal/protocol/game/room_fast_packet.go`、`room_message_data.go`、`room_peer_udp.go`。整体协议见 `client/native/docs/match-protocol.md` §5–§7。下面只写聊天房间要用到的部分。

### 4.1 Type-1 登记（UDP，收件前必须先做）

服务端的 UDP 端口与对局用的相同（目录信息里的 `udp_port`）。Python 宿主在**进入房间时**就要登记，不能等到开局。18 字节头部，大端：

| 偏移 | 大小 | 字段 |
|---|---|---|
| 0 | 2 | 总长度 |
| 2 | 1 | Flags = 0 |
| 3 | 4 | PacketNumber（自己递增） |
| 7 | 2 | PlayerID |
| 9 | 4 | UIN |
| 13 | 2 | Type（1 登记 / 2 组播 / 3 房间对等） |
| 15 | 2 | 校验和：对整个报文按 16 位**小端**字求和，折叠进位后取反，按大端写回。整个报文再算一遍应得 0 |
| 17 | 1 | RouteBytes（Type-1 为 6） |
| 18 | 6 | 本机 IPv4 + 端口（大端） |

例：`0018 00 00000001 0002 000f4242 0001 9c25 06 c0a8010a c350`（玩家 2，UIN 1000002，本机地址 192.168.1.10:50000）。服务端回一个 Type-1，里面是它看到的外网地址，收到回复就算登记成功（与 sidecar 的 `online.go:816` 相同）。服务端要求：同一 IP 上有这个 UIN 的 TCP 会话，并且 PlayerID 一致。mode-1 登记不会过期。**对局 sidecar 会用自己的套接字重新登记，回到房间后要重新发一次 Type-1**，否则服务端会继续往 sidecar 已关闭的端口发。[证实：服务端代码；回房后需要重新登记属于推断]

### 4.2 发送：TCP 0x0065 房间快速包（与原版一致，推荐）

原版就是这样发的，服务端也会做座位校验。包体格式（`room_fast_packet.go:103-231`）：

```
u32 总长 | u32 外层序号(0) | u16 路由序号 R | u16 0xFFFF | u32 UIN | u8 1 | u8 STlen=3+2N
| u16 0x0065 | u8 N | N × u16 收件人 PlayerID（房间里除自己以外的人，不重复、不为 0）
| QQTEA(LocalKey = 10 11 … 1F) 加密下面的明文:
    u16 0 | u32 0 | u16 R（必须等于外层 R）| u16 1 | u16 0xFFFF | u16 1        ← 14 字节内层头部，服务端强制检查这些值
    u8 条目数(1..64)
    每个条目: u32 GameTime(0) | u32 Flag(0) | u16 长度 | ROOM_MSG_DATA（§3.1，12+DataLen 字节）
```
抓包里 R 为 0x0352（推断是发送方自己的递增计数），GameTime 和 Flag 都是 0。服务端（`handleRoomFastPacketLocked`）检查以下各项，**不回任何 TCP 包**：信封 UIN 等于会话 UIN；房间处于准备阶段（PhasePreparing）；每条事件只能是 0x0BB8 或 0x0BB9，长度为 17 或 9，并且 SeatId 等于发送者的座位号减 1；收件人在房间里且不是自己。然后对每个已登记 Type-1 的收件人、每条事件发一个 Type-2。没有登记的收件人跳过。注意这个包和普通包的区别：普通包的 ST 是 32 字节的本地 ST（`net/game.py`），这里换成 `0065 N ids…`，`packet[16]==1` 不变，服务端靠这一点（`LooksLikeRoomFastPacket`）分流。

### 4.3 接收：Type-2（UDP）

```
18 字节头部（Type=2，PlayerID/UIN/PacketNumber 是"原发送者"的，RouteBytes=6·M）
| M × {u16 PlayerID, u32 UIN}（收件人）| Data
```
服务端由 0x0065 转换出来的 Type-2，Data 是**一条明文 ROOM_MSG_DATA**（`MarshalRoomMessageData`），PacketNumber 取自服务端为该发送者维护的递增序号。例（玩家 2 → 玩家 1，包含上面那条移动）：
```
0035 00 00000005 0002 000f4242 0002 4047 06 0001000f4241 15179aa50bb800110000002701ec0000001b01000000000000b81e853e
```
处理方法：
1. 校验和与长度。
2. 按发送者 PlayerID 去重：PacketNumber 必须比上一次大（按 32 位回绕比较，与 sidecar 相同）。
3. 解析 Data 为 ROOM_MSG_DATA，按 DataID 拼出脚本调用 `remotePlayerMove(seat, dir, x, y, speed)` 或 `remotePlayerPutBomb(seat, x, y)`，保持原版"执行脚本字符串"的路径。只在房间界面执行，对局中丢弃。

原版的接收方是否认为座位号就是 `SeatId`：是的，原版直接用包里的 SeatId，不核对发送者身份 [证实]。Python 宿主为了稳妥，可以检查 SeatId 是否等于发送者 PlayerID 在房间里的座位号减 1。

**[推断，需要联机验证]** 在原版之间，服务端发出的明文 ROOM_MSG_DATA Type-2 能否被 QQTPPP 接受，本次没有证实。对局里的 Type-2 Data 是用 `"qqt is the best!"` 加密的批量格式（`room_peer_batch.go`）；QQTPPP 的解析代码受保护，这次没能读到。Python 宿主和 Python 宿主之间、以及原版发给 Python 宿主（服务端转换后）这两条路，格式都由服务端决定，是自洽的。只有"Python 宿主 → 原版"这条路会碰到这个问题，这条路与"原版 → 原版"一样经过服务端转换。

### 4.4 备选：Python 宿主直接发 Type-2

可以不发 0x0065，直接向服务端 UDP 端口发 Type-2：Data 用明文 ROOM_MSG_DATA，收件人列表包含其他人的 PlayerID 和 UIN，PacketNumber 自己维护。服务端会在任何房间阶段原样转发（`handleLegacyUDPMulticast`，只要求发送者已经登记 Type-1、收件人在房间里且已登记）。缺点是绕过了座位校验，并且 PacketNumber 不能和服务端在 0x0065 路径里替你分配的序号混用。**两种发法只能选一种。**

---

## 5. 服务端：已有的和还需要的

已有 [证实，`$S/internal/server/probe`]：
* 0x0065 等待房间事件 → 校验 → 每个收件人一个 Type-2（`room_fast_dispatch.go:60-276, 620-741, 1257-1285`）。
* Type-1 登记和回复外网地址（`room_peer_udp.go`）；Type-2 原样转发（`:432-450, 786-893`）。
* `commands.go` 里没有、也不需要单独的命令号。0x0065 是房间快速包的 ST 命令号，不在普通命令表里。`TCPTransferRequest/Notify 0x009C/0x009D`（REQUEST/NOTIFY_TCP_TRANSFER）**没有被实现**。服务端注释说，用 0x009D 包裹这些消息原版不会处理，所以聊天房间不需要它。

需要注意或补充的地方：
1. **不用新增转发。** 只要 Python 宿主按 §4.1、§4.2、§4.3 实现，聊天房间的走动和放泡就能在 Python 宿主之间、以及与原版之间往返。只有 §4.3 末尾那条推断需要联机确认。
2. 可选增强（**原版没有这个行为**）：服务端缓存每个座位最后一条 0x0BB8，有新人进房时补发给他，这样后进来的人能看到大家的实际位置。原版不补发，后进来的人看到的是大家站在座位初始点。
3. 服务端对 0x0065 要求房间处于 `PhasePreparing`。聊天房间不能开局，所以一直满足。

---

## 6. `ChangePosition` 和 `Navigate`

* **`ChangePosition`**：在 Client.exe、QQTSection.dll、gameUI.dll、QQTModules.dll 以及其他所有 DLL 的字节里都搜不到这个名字，`natives.tsv` 里也没有，说明原版**没有注册**它。脚本里唯一的调用在 `uiRoom.py:2810`：房间装备栏 `OnDragItemDrop`，把装备槽 `curEquipIdx` 拖到自己的格子上时调用。原版执行到这里会抛出 NameError，被脚本引擎吞掉。[证实] 它与聊天房间的走动无关，Python 宿主可以不实现，或者注册一个空函数。
* **`Navigate(n)`**：注册在 `0x00532327`，实现是 `0x005317F0`。它从 `config\http.ini` 的 `[public]` 段读取网址，交给网页接口 `+0x24C(url, 0, flag)` 打开 [证实]：

  | n | 键名 | flag |
  |---|---|---|
  | 0 | `urlvip`（默认 `http://qqtang.qq.com/vip/index1.shtml`） | 1 |
  | 1 | `url7`（VIP 入口 QTC7） | 0 |
  | 2 | `url2`（QTC2） | 0 |
  | 3 | `urlgcs` | 1 |

  `uiRoom.py:2601` 在点击 VIP 角色（`vipRole` 已勾选且不是联赛）时调用 `Navigate(1)`。这些网址都已失效，Python 宿主实现成空函数或只记一条日志即可。

---

## 7. Python 宿主实现清单（按依赖顺序）

1. **`room.py` 的原生函数**：
   * `SetRoomType(t)`：保存为 `state.game_type`。
   * `SetSelfSeatId(i)`：保存为 `state.self_seat`。
   * `room_setPlayerPos`：只写初始点表，不再直接改头像坐标。
   * `room_changePlayer`：按 §2.3 新建或重建头像（重建时保留位置）。新建的头像朝下（dir 3），状态为 stand。
   * 新增 `RemotePlayerMove(seat,dir,x,y,speed)`：按 §3.3 入队，与队尾相同时不入队。
   * 新增 `RemotePlayerPutBomb(seat,x,y)`：在该头像当前位置放泡。
   * `Navigate`、`ChangePosition`：空函数。
2. **每帧 tick（dt 为整数毫秒）**，按 §2 的顺序执行：
   * 读键盘：方向键栈只在方向键按下和松开时更新；空格只取按下沿；F8 翻转名牌开关。
   * 泡泡和火焰计时、连锁引爆。
   * 每个头像：闲置动作（可选），然后按 §2.2 / §3.3 移动，最后做越界撤回。
   * 按 §3.2 的条件发移动包。
3. **绘制**（`draw_players`）：
   * 先画泡泡（`bomb<style>_stand.img`，画在 `(cellX-20, cellY-50)`）和火焰（`flame{1,6,7,8,9}_stand.img`，画在 `(cellX-15, cellY-35)`）。
   * 再按座位顺序画头像：walk 动作用 `AvatarCache.get(role, team, "walk", dir, frame)`，6 帧，沿用 `WALK_FRAME_MS`；stand 动作用 `dir` 朝向。头像锚点沿用现有的 `AVATAR_ANCHOR`，它对应的正是 `room_setPlayerPos` 的那个点。
   * 名牌按 §2.6 绘制，聊天房间默认显示。
   * playerChat 气泡跟着头像移动。
4. **聊天房间的点击**：右键按下 → `ShowKickoutDlg(seat)`；左键双击 → `chatWithPlayerInChatRoom(seat)`。命中框为 `|mx-x|<25` 且 `y-65<my<y`，在座位控件被禁用之后才处理（§2.8）。
5. **网络**：
   * 进房后开一个 UDP 套接字，按 §4.1 发 Type-1，没收到回复就每秒重发一次。
   * 发送走 §4.2 的 0x0065。`net/game.py` 需要新增一个编码函数，ST 换成 `0065 N ids`，外层序号为 0，`R` 用连接自己的递增计数，内层 route=1、section=1、command=0。
   * 收到 Type-2 按 §4.3 处理。
   * 从对局回到房间后重新登记 Type-1。
6. **测试向量**：
   * §3.1 的抓包解密结果。
   * §4.1 和 §4.3 的样例报文（校验和已经验算为 0）。
   * 两条换算：放泡 (308,315) → col 7、row 5；dt=16 时每帧位移 4 像素。

---

## 8. 未知和待验证

* 原版 QQTPPP 是否接受明文 ROOM_MSG_DATA 作为 Type-2 Data（§4.3）。等待房间里原版是否只走 0x0065、不发 Type-2。
* 房间场景的帧间隔，以及泡泡、火焰精灵的基础帧时长。
* 名牌上的 misc172 图标（`+0x25`）表示什么；`0x0045BEE0` 控制的设置是什么（决定是否播放闲置动作）。
* 房间 `+4` 处的 8 个对象（更新 `0x00451120`、绘制 `0x004513C0`）和构造函数里那 4 个额外头像（角色 9、10、11、22）的用途。推断与婚礼有关，和走动无关。
* 聊天输入框有焦点时方向键和空格是否仍然生效。

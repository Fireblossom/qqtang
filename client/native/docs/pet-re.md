# 宠物（pet）逆向：宿主原生函数与回调

目标：让 `client/native/qqtclient` 用 Python 实现原版宠物功能需要的全部宿主原生函数、网络处理和脚本回调。原版界面脚本（`generated/scripts/res/uiRes/uiSelRoom.py`、`uiMain.py`、`uiShop.py`）不改。

标注：
- **[证实]**：读过伪 C 或机器码，或者核对过 `QQTMsgData.bin` 的 schema 表、`config/PetCfg.ini`。
- **[推断]**：有间接证据，没有逐条走通。
- **[未知]**：没有查。

地址都是虚拟地址。`Client.exe` 基址 `0x400000`，`QQTSection.dll` 基址 `0x10000000`，`QQTDealer.dll` 基址 `0x10000000`。下文 **S** 指 QQTSection 的 `CMySection` 主对象。

---

## 0. 结论摘要

1. 脚本里的宠物原生函数都注册在 Client.exe（`natives.tsv`，注册处 `0x5519EA`～`0x551AF8`）。包装函数取服务对象 `[0x7FAF04]+0x14(GUID 0x79A908)`，再 QueryInterface GUID `0x79A928`（字节 `c54255f95a93684aa7a4aacc6a1c4fbe`，即 QQTSection 的 `0x100542F0`）得到 **XPet** 接口（`S+0xBF38`，虚表 `0x100548A0`），然后调用虚表槽位 [证实]。
2. 宠物数据全在 **本地缓存**：`S+0xBF3C` 处的 `RESPONSE_PLAYER_PETS_INFO`（1452 字节：基础栏 5 只 + 扩展栏 20 只）。读取类原生函数（`GetPlayerAllPetsId`、`GetPetBaseInfo`、`GetCurActivePetInfo`…）只读缓存，不发包 [证实]。
3. 只有两条网络消息：
   - `0x00D4` PLAYER_PETS（请求 schema `0x0BCF`，应答 `0x0BD0`）：整表覆盖缓存，然后回调 `UpdatePetInfo()`，再按界面状态执行 `UpdateC2CDealDlgAfterBargain()` 或 `updateMyStorageInPawn()` [证实]。
   - `0x00D5` HANDLE_PET（请求 `0x0BCD`，应答 `0x0BCE`）：7 种事件（喂养/携带/休息/放生/改名/学技能/领养）。成功且事件不是 4/7 时，用应答里的宠物替换缓存中同 ID 的那只；**不论成败**都回调 `UpdatePetInfo()`；失败时弹出 `Reason` [证实]。
4. 宠物配置（经验表、技能名和说明、宠物形象编号）读客户端本地的 `config/PetCfg.ini`，不走网络 [证实]。
5. 宠物**只出现在等待房间的人物演示里**（`CRoomDemoNew` 内嵌的 `CPetsManager`，每个座位一只，跟着主人走）。对战场景不创建宠物，宠物不改变对战玩法；技能（糖币/勇气/积分/声望/材料加成）只在服务端结算时生效 [证实：宠物精灵唯一的创建路径是 `room_changePlayer`；加成部分见 Go 服务端]。
6. 背包里宠物道具（26001～29000）数量变化时，QQTSection 回调 `UpdatePetItemUi()` [证实]。
7. 和 Go 服务端对不上的地方，详见 §9。最重要的两条：当铺查看宠物（`RequestPawnPetInfo`，命令 `0x035F`）服务端完全没实现；宿主现在把 `0x00D4` 当成无用应答丢掉了，必须改成更新缓存。

---

## 1. 原生函数到实现的对照 [证实]

| 原生函数 | Client.exe 实现 | XPet 槽位 | QQTSection 实现 | 作用 |
| --- | --- | --- | --- | --- |
| `RaisePet(itemId, petId)` | `0x550080` | `+0x0C` | `0x100304D7` | 用宠物道具：事件 1/6/7 |
| `TakePet(petId)` | `0x5502B0` | `+0x10` | `0x100305E7` | 事件 2 携带 |
| `StopTakePet(petId)` | `0x550390` | `+0x14` | `0x10030691` | 事件 3 休息 |
| `FreePet(petId)` | `0x550470` | `+0x18` | `0x1003073B` | 事件 4 放生 |
| `ChangePetName(petId, name)` | `0x550550` | `+0x2C` 再 `+0x1C` | `0x100307E5` | 事件 5 改名 |
| `RequestPetsInfo()` | `0x550700` | `+0x20` | `0x100308A5` | 发 `0x00D4` 请求 |
| （网络应答写缓存，非原生） | — | `+0x24` | `0x10030A6A` → `0x10034B71` | `SetPetsInfo` |
| `GetPlayerAllPetsId()` | `0x5507E0` | `+0x28` | `0x10030AD0` | 返回缓存指针 |
| `GetCurActivePetInfo()` | `0x5509C0` | `+0x28` | 同上 | 见 §2.3 |
| `GetPetBaseInfo(petId)` | `0x550BA0` | `+0x2C` | `0x10030B18` → `0x10034BDC` | 按 ID 查缓存 |
| `GetPetLevelRateInfo(exp)` | `0x551230` | `+0x30` | `0x10030BBF` → `0x10034C4D` | 经验条 |
| `GetSkillNameById(id)` | `0x551340` | `+0x34` | `0x10030C6A` → `0x10034D84` | 技能名 |
| `GetSkillDescription(id)` | `0x551460` | `+0x38` | `0x10030CD8` → `0x10034E04` | 技能说明 |
| `GetPetResId(typeId, level)` | `0x550CE0` | `+0x3C` | `0x10030D46` → `0x10034E84` | 形象编号 |
| `GetPetAllSkillId(petId)` | `0x550DE0` | `+0x2C` | — | 脚本不用 |
| `GetPetAllSkillName(petId)` | `0x550FC0` | `+0x2C`、`+0x34` | — | 脚本不用 |
| `GetAllMyPetItems()` | `0x5515D0` | 主接口 `+0x170` | `0x1002437F` | 背包里的宠物道具 |
| `RequestPawnPetInfo(saleIndex)` | `0x45F8B0` | 交易接口 | QQTDealer `0x100032FD` | 当铺，见 §7 |
| `GetPawnPetInfo()` | `0x45F910` | 交易接口 | QQTDealer 缓存 `+0x50` | 当铺，见 §7 |

XPet 虚表 `0x100548A0` 的前 3 个槽是 IUnknown；`+0x40` 之后不属于 XPet。

脚本里出现、宿主已有的：`GetPlayerAllPetsId`（`shop.py` 里目前返回 `[]`，要换成读缓存）。

---

## 2. 数据结构

### 2.1 `PET_BASE_INFO`（58 字节）[证实]

boost.python 注册在 `0x4D0380`，成员偏移取自该函数，类型取自 RTTI（`$$BY0M@D` 是 `char[12]`，`$$BY0BE@E` 是 `unsigned char[20]`）。

| 偏移 | 属性 | 类型 | 说明 |
| --- | --- | --- | --- |
| `+0x00` | `m_dwPetId` | u32 | 宠物实例 ID，0 表示无 |
| `+0x04` | `m_dwPetTypeId` | u32 | 宠物种类，PetCfg.ini 的 `[PetNNNNN]` 段号（25001～26000） |
| `+0x08` | `m_dwPetExperience` | u32 | 成长值 |
| `+0x0C` | `m_dwPetLoyalty` | u32 | 体力，脚本按 `/1000` 画条 |
| `+0x10` | `m_wPetLevel` | u16 | 等级 |
| `+0x12` | `m_wPetMood` | u16 | 心情，脚本按 90/60/40/10 分 5 档表情 |
| `+0x14` | `m_wPetState` | u16 | **1 = 携带中**（按钮显示"休息"），**2 = 休息**（按钮显示"携带"），其他值按钮禁用 |
| `+0x16` | `m_szPetName` | char[12] | GBK |
| `+0x22` | `m_dwSkillCount` | u32 | 技能数，>20 时 `GetPetAllSkillId` 返回空表 |
| `+0x26` | `m_szSkills` | u8[20] | 技能 ID，每字节一个 |

宿主给脚本的对象：`types.SimpleNamespace`，以上 10 个属性。注意：
- `m_szPetName` 是 `str`（GBK 解码到第一个 NUL）。
- 脚本写的是 `ord(petInfo.m_szSkills[idx])`，所以 `m_szSkills` 必须是 **`str`**，每字节转成 `chr(b)`（用 latin-1 解码），不能给 `bytes`（Python 3 里 `bytes[i]` 是 int，`ord(int)` 会报错）。补齐到 20 个字符即可。
- 技能 ID ≤ 50 的是天赋（`CHECK_BASESKILL`，显示在 `baseSkillDlg`，3 格），其余是学到的技能（`skillDlg`，10 格）。

### 2.2 `PET_LEVEL_INFO` [证实]

| 偏移 | 属性 | 类型 |
| --- | --- | --- |
| `+0` | `m_fRate` | float，0～1，脚本画 `int(rate*94)` 像素宽 |
| `+4` | `m_unNextLevelExp` | u32，脚本显示 `exp/next` |

### 2.3 本地缓存 [证实]

- `S+0xBF3C`：`RESPONSE_PLAYER_PETS_INFO` 解码后的定长结构：`+0x000` 基础栏数量 `m_cCount`（s8），`+0x001` 5×58 字节；`+0x123` 扩展栏数量（s8），`+0x124` 20×58 字节；共 `0x5AC` 字节。`SetPetsInfo`（`0x10034B71`）在数量超出 5/20 时只记日志，**照样整块复制** [证实]。
- `S+0x1A8` 是本机玩家自己的玩家信息（起首是 UIN，`+0x54` 道具数，`+0x56` 起每条 18 字节的道具，最多 500 条），其中 `+0x23A4` 是一条 `PET_BASE_INFO`，即本机的 **当前携带宠物**（`S+0x254C`）。每次 `0x00D4`/`0x00D5` 应答后由 `0x1003139C` 重算：按"基础栏、扩展栏"的顺序找第一只 `m_dwPetId != 0` 且 `m_wPetState == 1` 的，复制过去；找不到就清零 [证实]。房间座位结构里同一偏移 `+0x23A4` 也是宠物（`room_changePlayer` 读它，见 §8）[推断：两处偏移一致]。

宿主建议：`PetState` 对象里存一个列表 `base`（≤5）和 `extra`（≤20），所有查询按"先 base 后 extra"的顺序遍历。

---

## 3. 读取类原生函数

### `GetPlayerAllPetsId()` → `list[int]` [证实]
`0x5507E0`：先基础栏再扩展栏，逐只把 `m_dwPetId` 追加到列表（不过滤 0 和状态）。脚本用它判断有没有宠物、做左右翻页（`petPan.left/right`），商店用它决定 `UI.shop.lighticon.pet` 亮不亮。

### `GetCurActivePetInfo()` → `PET_BASE_INFO` [证实]
`0x5509C0`：按顺序把每只宠物复制到同一个局部变量，遇到 `m_wPetState == 1` 就停。**没有携带中的宠物时，返回的是列表里的最后一只**（局部变量里留着最后复制的那只）；列表为空才返回全 0。`UpdatePetInfo` 在 `curPetId == 0` 时靠这个行为显示"第一只"可看的宠物，宿主必须照做，否则有宠物但都在休息时面板会显示"还没有宠物"。

注意它和 §2.3 的"当前携带宠物"`S+0x254C` 不同：后者找不到时是全 0。

### `GetPetBaseInfo(petId)` → `PET_BASE_INFO` [证实]
`0x550BA0` → `0x10034BDC`：先基础栏再扩展栏，找 `m_dwPetId == petId` 的；找不到返回全 0（脚本靠 `m_dwPetId == 0` 判断）。

### `GetPetResId(typeId, level)` → int [证实]
`0x10034E84`：PetCfg.ini 段 `Pet<typeId>`，从 `Level<level>` 开始往下找，`level, level-1, …, 1`，第一个非 0 的整数值就是结果；`level` 为 0 或一直找不到返回 0。`level` 按 u16 传。
- 脚本用法：`object/pet/pet%d_stand.img`（宠物面板预览）、`res/uiRes/icon/pet/pet%d.img`（当铺、交易里把道具 ID 25001～26000 当种类 ID，`level` 固定传 10）。
- 例：`[Pet25001] Level1=100, Level4=1, Level7=2`：1～3 级是 100（蛋），4～6 级是 1，7 级以上是 2。

### `GetPetLevelRateInfo(exp)` → `PET_LEVEL_INFO` [证实]
`0x10034C4D`，表来自 `[PetExperience] ExpTable`（逗号分隔，最多 100 项；当前是 `0,180,580,1380,2920,5230,8570,16490,29810,51410`）：

```python
def level_rate(exp, table):
    i = next((k for k, v in enumerate(table) if exp < v), len(table))
    if 0 < i < len(table):
        lo, hi = table[i - 1], table[i]
        if hi == lo:
            return 0.0, hi          # 原版记错误日志，rate 置 0
        return (exp - lo) / (hi - lo), hi
    return 1.0, table[i - 1]       # 超过最后一档：满条，下一级显示最后一档
```
- `exp == 180` 时 `i = 2`，rate 为 0，下一级 580（比较用 `<`）。
- `i == 0` 的分支原版会读到表前一格（越界），但 `exp` 是无符号数且表首是 0，不会走到。
- ini 读失败时原版返回 `(0.0, 0)`。rate 存成 float32。

### `GetSkillNameById(id)` / `GetSkillDescription(id)` → str [证实]
`0x10034D84` / `0x10034E04`：段 `[SkillName]` / `[SkillDescription]`，键 `skill<id>`（`id` 按字节传，取低 8 位）。缓冲区分别是 20、256 字节：最多复制 19/255 字节，恰好截满且最后一个字节 ≥ 0x80 时去掉这个字节（粗略地避免半个汉字）。找不到返回 `""`。

### `GetPetAllSkillId(petId)` / `GetPetAllSkillName(petId)` [证实，脚本不用]
按 `m_dwSkillCount` 返回 `m_szSkills` 里的 ID（int）列表 / 对应技能名列表；计数 > 20 返回空表。宿主可以不实现，或者顺手实现。

### `GetAllMyPetItems()` → `list[ITEM_INFO]` [证实]
`0x5515D0`：取主接口 `+0x170`（`0x1002437F`，返回本机玩家的全部道具：`S+0x1FC` 数量、`S+0x1FE` 起 18 字节一条），保留 `m_iNumOfItem > 0` 且 ID 落在下面任一区间的：
- 26001～27000：宠物食品（`itemCFG.py` 类型 `food`）
- 27001～28000：宠物技能书（`skill`）
- 28001～29000：宠物卡片（`petcard`，领养用）

不看道具状态（不排除回收站里的）。元素和背包道具同类型，脚本读 `m_nItemID`、`m_iNumOfItem`；宿主直接复用 `shop.py` 的 `item_view`。脚本自己按 ID 降序排序、分页（每页 12 格）。

---

## 4. 发包类原生函数

### 4.1 请求格式 [证实]

QQTSection 里的公共头（`0x10030DF5`、`0x100308A5`）：`{uin, 1, 4, schema, command, 0, sectionId}`，和聊天室背景请求（宿主用 route 4）完全相同，所以宠物请求应走 **route 4** [推断；Go 服务端不检查 route]。

`REQUEST_PLAYER_PETS_INFO`（命令 `0x00D4`，schema `0x0BCF`），8 字节，大端：

| 偏移 | 字段 |
| --- | --- |
| 0 | `Uin` u32 |
| 4 | `Time` u32（`0x1004AEB9`，服务器时间；宿主用 `time.time()`） |

`REQUEST_HANDLE_PET`（命令 `0x00D5`，schema `0x0BCD`），36 字节，大端：

| 偏移 | 字段 |
| --- | --- |
| 0 | `Uin` u32 |
| 4 | `Time` u32 |
| 8 | `EventID` u32 |
| 12 | `PetID` u32 |
| 16 | `ParaBuf` char[20]，NUL 填充 |

### 4.2 各原生函数 [证实]

| 原生函数 | 前置检查（Client.exe） | EventID | PetID | ParaBuf |
| --- | --- | --- | --- | --- |
| `RaisePet(itemId, petId)` | `itemId` 在 26001～29000，否则什么都不发 | 26001～27000 → **1** 喂养；27001～28000 → **6** 学技能；28001～29000 → **7** 领养 | `petId`（领养时脚本可能传 0） | `"%d" % itemId` |
| `TakePet(petId)` | 无 | 2 | `petId` | 全 0 |
| `StopTakePet(petId)` | 无 | 3 | `petId` | 全 0 |
| `FreePet(petId)` | 无 | 4 | `petId` | 全 0 |
| `ChangePetName(petId, name)` | 先 `GetPetBaseInfo(petId)`，新名字和缓存里的名字**相同就不发** | 5 | `petId` | `"%s" % name`，GBK；原版用 `sprintf` 写 20 字节缓冲区，无长度检查（输入框 `maxchar = 10`）。宿主截到 19 字节 |
| `RequestPetsInfo()` | 无 | — | — | 发 `0x00D4` |

这些函数都不改本地缓存，也不回调脚本；界面要等应答（§5）才刷新。脚本侧的配合：
- `petStateBtn` 点击后先 `Win_EnableWidget(this, 0)` 再 `TakePet`/`StopTakePet`；`changeNameBtn` 同理；`FreePet` 后 `freePetBtn` 被禁用。应答里的 `UpdatePetInfo()` 会先 `Clear()` 把这些按钮全部重新启用。
- 喂养/学技能/领养：`foodDlg.petItemN.OnRClick` 记下 `curUseItem`，弹确认框，句柄 `-8`；确认后 `ui_OnMsgBoxResult(-8, 1)` 调 `RaisePet(curUseItem, curPetId)`。
- 放生：`freePetBtn` 弹确认框，句柄 `-9`；确认后调 `FreePet(curPetId)`。
- 打开宠物面板：`guideBar.PetBtn`。在 `selRoom` 里直接 `RequestPetsInfo(); UpdatePetInfo()`（先用旧缓存画一遍，应答回来再画）；在 `room` 里先弹框，句柄 `-6`，确认后 `LeaveRoom(uin)` 再同样处理。
- 领养入口 `adoptPetBtn`：置 `mark_shopPetTab = 1` 后进商店，商店 `OnInit` 自动点 `petTab`。

---

## 5. 服务器应答和脚本回调

QQTSection 的网络分发在 `0x100189CE`：命令 `0xD5` → `0x10030F6D`，`0xD4` → `0x1003116C` [证实]。脚本回调接口是 Client.exe 的 `IQQTScrFun`（虚表 `0x79DE84`）：`+0x10` 弹系统消息框（`0x52CB80` → `0x48B130`），`+0x70` 执行 `UpdatePetInfo()`（`0x52D300`），`+0x74` 执行 `UpdatePetItemUi()`（`0x52D330`）[证实]。

### 5.1 `RESPONSE_PLAYER_PETS_INFO`（`0x00D4`，schema `0x0BD0`）[证实]

线上格式（和 Go `buildLocalPlayerPetsPacketWithBuilder` 一致，大端、紧凑）：

```
u8  baseCount            (≤5)
baseCount × PET          (见下)
u8  extraCount           (≤20)
extraCount × PET
PET = u32 PetId, u32 PetTypeId, u32 Experience, u32 Loyalty,
      u16 Level, u16 Mood, u16 State, char[12] Name,
      u32 SkillCount, u8[SkillCount] Skills        (38 + SkillCount 字节)
```

Go 服务端会在两种场合发它：对 `RequestPetsInfo` 的应答；以及**不经请求主动推送**（创建/进入/快速加入房间的结果之前，放生和领养成功之后）。宿主两种都按同一处理：

1. 整表替换缓存（`SetPetsInfo`）。
2. 重算本机"当前携带宠物"（§2.3）。
3. 调脚本 `UpdatePetInfo()`。
4. 当前界面是 `selRoom` 且 `UI.C2CDealDlg` 可见（gameUI 接口 `+0x44`，按名字查控件，[推断] 为"可见"判断）→ 执行 `UpdateC2CDealDlgAfterBargain()`；否则若 `UI.selRoom.pawnshop` 可见 → 执行 `updateMyStorageInPawn()`。

`UpdatePetInfo()` 在面板没打开时也会执行，它只设置控件，不会自己弹出面板 [证实，读脚本]。

### 5.2 `RESPONSE_HANDLE_PET`（`0x00D5`，schema `0x0BCE`）[证实]

解码后的结构（`0x10030F6D` 的局部变量）：`+0` `Result` s16，`+2` `EventID` s32，`+6` `ReasonLen` s16，`+8` `Reason[200]`，`+0xD0` `PetInfo`（`PET_BASE_INFO`）。线上是紧凑的（Go `BuildLocalHandlePetResponseWithReader`）：

```
u16 Result, u32 EventID, u16 ReasonLen, u8[ReasonLen] Reason, PET（38 + SkillCount 字节）
```

处理顺序：
1. `Result == 0` 且 `EventID` 不是 4、7：用 `PetInfo` 替换缓存里同 `m_dwPetId` 的那只（`0x10035022`）。找不到只记日志。**替换时，如果新数据 `m_wPetState == 1`，会把遍历途中遇到的其他 `State == 1` 的宠物改成 2**——但遍历到匹配项就停，排在它后面的不会被改 [证实]。宿主建议把其他所有携带中的都改成 2（Go 服务端本来就保证只有一只携带，结果相同）。
2. 事件 4（放生）、7（领养）成功时**不改缓存**，等后面的 `0x00D4` 推送（Go 会发）。
3. 重算"当前携带宠物"。
4. **不论成败**调 `UpdatePetInfo()`。
5. `Result != 0` 且 `0 ≤ ReasonLen ≤ 200`：`Reason[ReasonLen] = 0`，弹系统消息框：`Win_ShowMsgBox(reason, "", 0, "UI.SysMsgbox", 0)` 后执行 `ui_msgBox(3)`（只有"关闭"按钮）。`ReasonLen == 0` 也会弹一个空框。宿主可直接用 `friend.py` 的 `message_box(text, 3, 0)` 写法。

### 5.3 背包变化 → `UpdatePetItemUi()` [证实]

`0x1002A543`（道具并入背包，由道具增加通知 `0x00D9` 的处理 `0x10023736` 调用）：本次收到的道具里只要有一条 ID 在 26001～29000，就回调 `UpdatePetItemUi()`，不看数量。宿主的 `shop.py:on_item_add` 现在只在商店界面刷新，要加上这一条（任何界面都调；脚本函数只改 `petPan.foodDlg` 里的控件）。

Go 服务端在喂养、学技能、领养成功后发 `0x00D9`，带该道具的剩余数量（可能是 0）。

### 5.4 回调汇总

| 触发 | 回调 |
| --- | --- |
| `0x00D4` 应答或推送 | `UpdatePetInfo()`；然后视界面 `UpdateC2CDealDlgAfterBargain()` / `updateMyStorageInPawn()` |
| `0x00D5` 应答 | `UpdatePetInfo()`；失败再弹框 + `ui_msgBox(3)` |
| `0x00D9` 含宠物道具 | `UpdatePetItemUi()`（以及原有的背包刷新） |
| 当铺宠物应答 `0x035F` 成功 | `Responsepawnpetinfo()`（§7） |
| 消息框结果 | 脚本自己的 `ui_OnMsgBoxResult(-6/-8/-9, isOK)`，宿主只需照常转发 |

原版没有任何 `Notify*` 形式的宠物回调；宠物面板没有自己的定时刷新 [证实：Client.exe 字符串里宠物相关的脚本调用只有这几条]。

---

## 6. `PetCfg.ini` [证实]

位置 `config/PetCfg.ini`（QQTSection 用模块目录拼路径，`0x10034A8F`），GBK。解析器是 QQTSection 自己写的（`0x10009822` 一族），不是 `GetPrivateProfileString`：
- 段名和键名**不分大小写**：查找前把要找的名字转小写（`0x10009E95`），在文件的小写副本里搜（`0x10009E85`）[推断：小写副本的生成没逐条读]。文件里既有 `[Pet25001]` 也有 `[pet25010]`，必须不分大小写。
- 键必须在行首或前面是空白，后面紧跟空白或 `=`；值去掉开头的空格/制表符，到行尾（CR/LF）为止，**末尾空格保留**。
- `#`、`;` 开头的注释行不会被误匹配（前一个字符不是空白），不需要特殊处理。

内容：
- `[PetExperience] ExpTable`：经验表，见 §3。
- `[SkillName]` / `[SkillDescription]`：`skill1`～`skill48` 是天赋，`skill51` 起是技能书学到的。
- `[PetNNNNN]`：`Level1`、`Level4`、`Level7` 三档形象编号。发布包末尾附加了 `Pet25901`～`Pet25913`（"QQTang local restoration"）。形象图在 `object/pet/pet<编号>_stand.img`、`_walk.img`（共 124 个编号，含 100 号蛋）。

宿主用 `resources.read("config/PetCfg.ini")` 读一次缓存即可（原版第一次用到时加载，`S+0xC4E8+0x20` 记是否成功）。

---

## 7. 当铺里查看宠物（`RequestPawnPetInfo` / `GetPawnPetInfo`）[证实]

脚本：`uiSelRoom.pawnshop.waresN.PetInfo.OnClick` 读 `ware.saleIndex` 文本，调 `RequestPawnPetInfo(saleIndex)`；应答后 QQTDealer 执行脚本 `Responsepawnpetinfo()`，它调 `GetPawnPetInfo()`，`m_dwPetTypeId > 0` 时用 `ShowPetInfoDlg(info)` 以只读方式打开宠物面板（按钮全部禁用，`foodDlg` 隐藏）。

- `RequestPawnPetInfo`（`0x45F8B0`）：交易对象 `[0x7FB518]` `+0x24` 取子接口，再 `+0x20(7, &saleIndex)`；QQTDealer `0x100032FD` 发包。
  - 命令 **`0x035F`**，schema `0x0BD5`（`REQUEST_PET_INFO_ONSALE`），12 字节：`Uin` u32、`Time` u32（这里用的是 `GetTickCount()`）、`SaleIndex` u32。头部 `{uin, 2, 4, …}`，和宠物请求不同 [证实]。发往交易服务器（当铺的连接），不是游戏服。
- 应答 schema `0x0BD6`（`RESPONSE_PET_INFO_ONSALE`，QQTDealer `0x100033F0`）：`Uin` u32、`Result` s16、`SaleIndex` u32、`petinfo` PET_BASE_INFO、`ReasonLen` u8、`Reason`。解码偏移：`+4` Result，`+0xA` petinfo，`+0x44` ReasonLen，`+0x45` Reason [证实]；线上是否和 `0x00D5` 一样把 Skills 压缩成按计数的数组 [推断：同一套编码器，应该一样]。
  - `Result == 0`：把 58 字节存到交易对象 `+0x50`，执行 `Responsepawnpetinfo()`。
  - 否则 `ReasonLen > 0` 时弹框（同 §5.2，类型 3）。
- `GetPawnPetInfo()`（`0x45F910`）：交易子接口 `+0x34` 取出存的宠物；`m_dwPetTypeId` 不在 25001～29000 就返回全 0。

当铺整套交易（`GetPawnItemList`、`RequestBuyGoodsInPawnShop` 等）不在本文范围。

---

## 8. 房间和对战里的宠物

### 8.1 等待房间：宠物跟在人物旁边 [证实]

- `CRoomDemoNew`（房间人物演示，构造 `0x457080`，由 `0x54DC00`/`0x54E6E0` 在创建/进入房间时创建，后者随后执行 `ui_createRoomOK()`）里内嵌一个 `CPetsManager`（RTTI `.?AVCPetsManager@@`，虚表 `0x799450`），每座位一个 `CPet`（`0x60` 字节）。
- 原生函数 `room_changePlayer(seat, team, role)`（`0x459360` → `0x458240`，脚本 `uiRoom.py:444/470` 调用）：
  1. 座位已有人物、且 team 和 role 都没变：直接返回（宠物也不动）。
  2. 否则拆掉人物，`DeletePet(seat)`（`0x452F70`，宠物隐藏）。
  3. `role != -1` 时重建人物，对座位里该角色穿戴的每件道具执行 `useCommItem(...)`；然后读座位信息 `+0x23A4` 的 `PET_BASE_INFO`，`m_dwPetId != 0` 就 `AddPets(seat, 人物, m_dwPetTypeId, m_wPetLevel)`（`0x452DF0`）。
- `AddPets` → `CPet::InitPet`（`0x450DC0`）：用 `GetPetResId(type, level)` 取编号，创建场景对象 **类型 `0x24`（"pet"）**、编号 = 形象编号（`0x425400(0x24, resId, 1)`），即 `object/pet/pet<resId>_stand.img` / `_walk.img`；编号没变就复用。
- 只有座位换人/换角色/换队时才重建，房间里不会因为别的消息刷新宠物。房间里也打不开宠物面板（`PetBtn` 会先退出房间）。

宠物数据来源：
- 其他玩家：`PLAYER_INFO_IN_ROOM_OLD` 末尾的宠物（宿主 `net/room.py` 已解析成 `RoomPlayer.pet` 原始字节，38+SkillCount），Go 服务端填的是该玩家**携带中**的宠物（`room_state_adapter.go:projectRoomPlayerPet`），没有就是全 0。
- 本机：原版本机座位用的是本机玩家信息 `S+0x1A8`，其中宠物是 §2.3 重算出的携带宠物 `S+0x254C` [推断]。Go 服务端注释也说"房主直接用 `GetCurActivePetInfo` 构造"，并因此在创建/进入房间结果之前先推送 `0x00D4`。宿主：本机座位取缓存里 `State == 1` 的第一只（没有就不显示），其他座位取 `member.pet`。

宠物行为（`CPet::Update` `0x451120`、`0x451AE0`、`0x451D70`、`0x451520`）[推断：读过伪 C，没逐条核对机器码]：
- 计算宠物到主人的距离平方 `d`。
- `d > 90000`（300 像素）：瞬移到主人身边——按主人朝向放在左侧或右侧，横向偏移 20～`20+50` 像素、纵向 ±20/50 的随机点（`0x451520`）。
- 否则移动速度按 `d` 分档：`d < 3600` 100 像素/秒，`< 10000` 260，`≤ 22499` 300，其余 340（`0x451D70`）。
- 主人在走（状态 4）时：`3600 < d < 10000` 或 `d > 10000` 就朝主人走。
- 主人站立（状态 1）且 `d < 1600` 且宠物没在闲逛：停下，换站立动作（动作 1）。
- 主人没在走、宠物没在闲逛，并且 `d < 401` 或闲置计时用完：在主人附近随机选一点走过去，闲置计时重置为 1000～5000 毫秒的随机值。
- 图像锚点偏移 `(0x32, 0x54)`（`0x44C120`）[推断]。

### 8.2 对战：宠物不出场 [证实]

`CPet` 只由 `AddPets` 构造，`AddPets` 只由 `room_changePlayer` 调用；`CPetsManager` 只内嵌在 `CRoomDemoNew` 里。对战场景没有任何创建宠物的路径，GAME_BEGIN 等对战消息也没有宠物字段。宠物对对战玩法没有影响。

宠物天赋和技能（糖币/勇气/积分/声望加成、材料/积分翻倍几率）只在服务端结算时计算：Go `competitive_item_effects.go` 的 `applyCompetitiveSessionRewardEffects` 用 `carriedPetEffectItemIDs` 找携带宠物的技能。客户端只显示结算结果。

---

## 9. 和 Go 服务端的对照

Go 实现：`internal/protocol/game/pet.go`、`enums.go`（`PetEventID` 1～7、`HandlePetResult`）、`internal/server/probe/pet_dispatch.go`、`login_service.go`（`0x00D4` 请求）、`lobby_dispatch.go`（房间前推送）、`pet_projection.go`。

一致的地方 [证实]：
- 命令号和 schema（`0x00D4`/`0x0BCF`/`0x0BD0`，`0x00D5`/`0x0BCD`/`0x0BCE`），请求长度 8 和 36，事件号 1～7 和原版 `RaisePet` 的区间划分一致，`ParaBuf` 解析十进制道具 ID / GBK 名字。
- 应答布局、容量 5+20、宠物 58 字节和紧凑编码、`State` 1/2 的含义。
- 放生、领养成功后另推一次 `0x00D4`，正好补上原版不更新缓存的空档；创建/进入/快速加入房间前推 `0x00D4`。
- 天赋 ID 1～48 投影到 `Skills` 前面，学到的技能 ≥51，和脚本 `CHECK_BASESKILL(≤50)` 的划分一致。
- 携带一只时把其他都设为休息（`SetPetActive`），和原版缓存逻辑相容。

对不上或需要注意的地方：
1. **当铺查看宠物没有实现**：Go 没有命令 `0x035F`（schema `0x0BD5`/`0x0BD6`）的处理，整个当铺都没有。`RequestPawnPetInfo` 发出去不会有应答；宿主若没有交易连接，可以实现为空操作 [证实：`internal` 下搜不到 `0x035F`/`pawn`]。
2. **宿主丢弃了 `0x00D4`**：`qqtclient/room.py:358` 把 `room.PLAYER_PETS_COMMAND` 注册成 `ignore`，`net/room.py:209` 注释写"not used by the host"。必须改成 §5.1 的处理，且放在会话级（大厅、房间、商店都可能收到），不能只在房间模块里。
3. Go 失败应答也带一条全 0 的最小宠物记录（38 字节）。原版失败时不读宠物，没问题；宿主解析时别因为它报错。
4. 失败原因文本由 Go 给出（"道具数量不足"、"宠物栏已满"等），原版直接弹框显示，宿主照显示即可。`ReasonLen` 是 u16，原版只接受 0～200。
5. `ChangePetName`：原版不检查长度，`sprintf` 写进 20 字节缓冲区；Go 要求名字能编码进 12 字节 GBK 槽，否则失败。宿主截到 19 字节发出，超出 11 字节的名字交给服务端拒绝。
6. Go 的 `0x00D4` 请求解码要求 `UIN` 等于会话 UIN；`Time` 不检查。
7. 原版 `RequestPetsInfo` 只在打开宠物面板时发；登录后不会自动请求。Go 只在进房间前推送，所以在大厅第一次打开面板时，`UpdatePetInfo()` 先用空缓存画"还没有宠物"，应答回来再画正确内容——和原版一样。

---

## 10. 宿主实现清单

新建一个会话级模块（例如 `qqtclient/pet.py`），持有缓存和 PetCfg 配置：

1. **缓存**：`base: list[Pet]`、`extra: list[Pet]`；`all()` 按 base+extra 顺序；`active()` = 第一只 `pet_id != 0 and state == 1`。
2. **网络处理**（注册到 `session.handlers`，覆盖 `room.py` 里的 `ignore`）：
   - `0x00D4`：解析 §5.1 → 替换缓存 → `script("UpdatePetInfo")` → 视界面执行 `UpdateC2CDealDlgAfterBargain` / `updateMyStorageInPawn`（脚本里有才调）。
   - `0x00D5`：解析 §5.2 → 成功且事件不是 4/7 时按 ID 替换（新状态为 1 时其他改 2）→ `script("UpdatePetInfo")` → 失败时 `runtime.show_msgbox(reason, "", 3, "UI.SysMsgbox", 0)` + `script("ui_msgBox", 3)`。
   - 在 `shop.py:on_item_add` 末尾：任一道具 ID 在 26001～29000 时 `script("UpdatePetItemUi")`。
3. **原生函数**（§3、§4）：`RaisePet`、`TakePet`、`StopTakePet`、`FreePet`、`ChangePetName`、`RequestPetsInfo`、`GetPlayerAllPetsId`（替换 `shop.py` 里的版本）、`GetCurActivePetInfo`（保留"返回最后一只"的行为）、`GetPetBaseInfo`、`GetPetResId`、`GetPetLevelRateInfo`、`GetSkillNameById`、`GetSkillDescription`、`GetAllMyPetItems`，可选 `GetPetAllSkillId`、`GetPetAllSkillName`；当铺的 `RequestPawnPetInfo`（无服务端时空操作）、`GetPawnPetInfo`（返回全 0 的宠物对象）。
   - 发包走 `session.send(0x00D4/0x00D5, payload, route=4)`（route 见 §4.1）。
4. **房间演示**（`room.py:room_changePlayer` 和人物行走代码 `roomwalk.py`）：重建人物时先删座位的宠物；新人物建好后，本机座位用 `active()`，其他座位用 `member.pet`，`pet_id != 0` 就创建宠物精灵（`GetPetResId(type, level)` → `object/pet/pet<id>_stand/_walk.img`），按 §8.1 的规则跟随主人。

测试要点：
- 有宠物但全部休息时，打开面板显示最后一只，按钮显示"携带"。
- 携带/休息/改名后按钮被重新启用，状态图标随应答切换。
- 放生后面板先刷新一次（宠物还在），收到 `0x00D4` 后宠物消失。
- 喂养后 `foodDlg` 的数量随 `0x00D9` 更新，用完的道具从格子里消失。
- `GetPetLevelRateInfo(0)` = `(0.0, 180)`，`(180)` = `(0.0, 580)`，`(60000)` = `(1.0, 51410)`。
- `GetPetResId(25010, 5)` = 19（段名小写 `[pet25010]`），`GetPetResId(25001, 2)` = 100，`GetPetResId(99999, 5)` = 0。

---

## 11. 实现状态（2026-11-03）

- `qqtclient/net/pet.py`：两条消息的编解码；`qqtclient/pet.py`：缓存、PetCfg.ini、§3/§4 的全部原生函数、`0x00D4`/`0x00D5` 处理；`shop.py:on_item_add` 遇到宠物道具调 `UpdatePetItemUi()`；`room.py:room_changePlayer` 建座位宠物，`roomwalk.py:RoomPet` 按 §8.1 跟随。
- 当铺两个原生函数是空实现（无交易服务器）。
- 跟随动作的细节（闲逛半径、瞬移的纵向范围、动画帧间隔 100 ms、宠物总画在主人之后）是推断值。
- 测试：`tests/test_pet.py`；实测记录见 README"宠物"一行。

# 商城合成、装扮锻造与砸蛋

更新：2026-11-03。基于本机 v848 客户端的静态反汇编、转换后的原版 UI 脚本，以及本地 58000 测试服联调。未运行 Windows 原版客户端，也没有做历史录像画面对照。

## 1. 原生入口和消息

地址为虚拟地址。`Client.exe` 基址 `0x00400000`，`QQTSection.dll` 基址 `0x10000000`。Client.exe 经 GUID `0x79A908` 取得 XSection 接口，QQTSection 的虚表为 `0x10054928`。

| 原生函数 | Client.exe | XSection 槽位 | 行为 |
| --- | --- | --- | --- |
| BeginComForge | `0x0054B8B0` | `+0x2A4`、`+0x2AC` | 重新读取合成表、构建列表 |
| RequestCombine | `0x0054B920` | `+0x2C0`、`+0x294` | 查产品配方，提交合成，2 秒防重复 |
| IsBook | `0x0054BB50` | `+0x298` | 查询合成书 |
| RequestLearnScroll | `0x0054BBD0` | `+0x29C`、`+0x2A0` | 学习配方、更新本地书的状态 |
| GetCombineType | `0x0054BC50` | `+0x2C8`、`+0x2CC` | 分类名列表 |
| GetCombineItem | `0x0054BE50` | `+0x2D0`、`+0x2D4` | 分类内已学配方、可合成数量 |
| GetCombineMeterial | `0x0054C050` | `+0x2D8`、`+0x2DC` | 产品材料 ID、名称、已有/需要数量 |
| RequestAvatarForge | `0x0054CE10` | `+0x344` | 操作 1 锻造、3 还原、4 拆分 |
| GetAvatarForgeType | `0x0054CEA0` | `+0x348`、`+0x34C` | avatarforge.ini 的类型名 |
| GetAvatarForgeItem | `0x0054D280` | `+0x358` | 两份 ITEM_INFO 列表 |
| GetAvatarForgeMaterial | `0x0054D4F0` | `+0x35C` | 材料列表（`m_stItem`、名称、说明） |
| GetSplitInfo / GetRevertInfo | `0x0054D680` / `0x0054D830` | `+0x360` / `+0x364` | `[材料 ID, 数量, 名称]` |
| RequestBreakEgg | `0x0056D100` | `+0x380` | 脚本参数 `(蛋, 锤子)`，转接口时调换顺序 |

请求均走已经登录的游戏连接，不走商城购买用的短连接。网络字节序为大端；`UIN`、时间、物品 ID 均为 u32。

| 传输命令 | 请求载荷 |
| --- | --- |
| `0x00CF`，schema `0x0825` | UIN、时间、u8 操作 0、产品 ID、来源 ID 0、u16 材料种数、每种材料 ID |
| `0x0353`，schema `0x0BBB` | UIN、时间、u8 操作、装备 ID、材料 ID |
| `0x00FE`，schema `0x0BDB` | UIN、时间、**锤子 ID、蛋 ID** |

**学习合成书用 `0x0085 ITEM_STATUS_CHANGE`，状态 4，角色 0。** QQTSection `0x10035E27` 明确构造这个请求；`0x1002C487` 把本地物品状态设为 4。服务端另有 `0x007D` 的学习兼容处理，但这个版本的 RequestLearnScroll 不走它。原生客户端等服务端绝对背包更新后才确认已学，避免请求失败时提前开放配方。

## 2. 合成表、配方与界面

`config/combineforge.ini` 经 QQTSection `0x1000BEC8` 的逆向 XOR、zlib 解压后是 GB2312 XML，根 `ComForgeFile version=110`。原生客户端直接读取用户的表，不在源码里保存解密后的原版数据。当前有 56 个配方，按 XML 顺序分八类：药水、陷阱、护符、卷轴、工具、食物、锻造宝石、婚礼礼花。

QQTSection `0x1003619D` 构建分类：所有分类均保留，即使该类没有已学配方（`0x10036497`）；每个产品只在对应书已学时加入列表（`0x10036261` → `0x10035A74`，检查书状态 4）。可合成数取各材料 `已有数量 / 需要数量` 的整数商最小值（`0x10036413`）。发送时只列每种材料 ID 一次，数量由配方决定。

合成回复为 `u16 result, u8 type, u32 product, u32 source, u16 material_count, materials[], u16 reason_length, GBK reason`；两个变长字段都是紧凑编码，不能按结构体最大容量跳过。回调 `ui_ReceiveComResult(result, reason)` 会立即重查背包，所以本地服务端在结果前发绝对背包通知（原有服务端行为，本轮未改）。

进入商城→“锻造／合成”→合成页，展开分类并选择产品，点击“开始合成”。学习书的原版路径是材料仓库里右键合成书→确认框（句柄 -5）→RequestLearnScroll。

## 3. 装扮锻造

`config/avatarforge.ini` 是普通 CP936 INI。名单、类型、七种晶石、拆分/还原费用均从该文件读取；不采用本仓库另一份默认表覆盖用户的实际配置。

QQTSection `0x10005C9E` 查询指定分类的有效背包物品、与允许名单取交集。`0x10005E20` 的筛选为：

- 可锻造：`effect == 0`，或 `effect > 0 && color == 0`。
- 可还原/拆分：`effect > 0`。

因此拆分后保留特效、清除颜色的装备同时出现在两份列表。服务端按晶石规则抽取结果；客户端不计算成功率、不预扣材料。

结果为 `u16 result, u8 type, u32 item, u32 material, u8 effect, u8 color, u16 reason_length, GBK reason`。QQTSection `0x100056DC` 即使失败也执行 `ui_UpdateAvatarEffect(item, effect, color, result, reason)`（调用点 `0x10005917`）；字符串长度里包含 NUL。原生客户端随后收到 `0x00D9` 才更新物品、晶石/宝石数量及列表。

当前 UI 材料栏硬编码 `res/uires/icon/item/item20051..57.img`，本机资源实际为 itemCFG 指定的 `res/uires/icon/forge/forge1..7.img`。原生客户端为这些晶石添加缺图时的映射，保留存在的原路径，不改脚本或物品 ID。

**未实现锻造结果的动态预览。** `AvatarForge_wear`（`0x0047DA30` → `0x0047C600`）、`AvatarForge_Pos`（`0x0047DA80` → `0x0047C8D0`）及 `SC_AvatarForge_draw` 尚待移植。当前能锻造、拆分、还原、更新列表和结果文字，结果预览框仍为空，不能称为完整画面复刻。

## 4. 砸蛋

QQTSection `0x10031ED2` 构造 16 字节请求。回复是 `u16 result, u32 show_item, u8 text_length, GBK text`。原生客户端把 `result == 0` 转成脚本的成功布尔值 1，调用 `NotifyBreakEggResult(success, show_item, text)`。脚本清除当前蛋、禁用三把锤子、更新数量和奖品图，播放成功/失败音效。

界面入口为道具箱右键蛋（9001～9004），再点击锤子。材料消耗与奖品完全以服务端背包通知为准；服务端在结果前发三种物品的绝对数量。

## 5. 实现与验收

- 宿主：`qqtclient/craft.py`；协议：`qqtclient/net/craft.py`；通过已有 shop 的 `0x00D9` 处理更新背包，再重建锻造列表与材料栏。
- 协议测试与本地 Go 编码格式逐字段对应，覆盖紧凑长度、GBK/NUL、砸蛋字段顺序；宿主测试覆盖学习确认、材料最小商、拆分后列表归属、绝对更新先于重绘、失败不预扣。测试在 `tests/test_craft.py`。
- 本地 58000，账号 **1000007（糖七）**：GM 准备棉绒帽 19、紫水晶 20057、酷比宝石 1800、医疗包书 24050、材料 30057/30045/30008、金蛋 9003、金锤 9013。学习→合成医疗包→锻造→拆分→再锻造→还原→砸蛋均被服务端接受；材料/奖励绝对数量已核对。
- 初次自动化把产品选择写到了错误的命名空间/分类，结果回调报错；属于测试脚本错误，不算界面验收通过。后续 `live-ui2` 使用实际鼠标点击“工具→医疗包→开始合成”复验通过，成功文字、产品与三种材料余量正确，客户端无脚本异常、服务端零 warn/error；其余锻造操作和砸蛋通过宿主调用验收，完整鼠标路径尚未逐项实测。
- Python 全套 137 项通过（新增 7 项）。本轮无服务端代码改动。
- 测试过程脚本及截图在 `/private/tmp/qqt-craft-re/`，不会提交原版资源、账号数据库或运行日志。

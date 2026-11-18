# 客户端缺失资源清单

2026-11-18 只读审计，对象是客户端 5.2.1.201（itemCFG v848）。完整清单 184 行，见同目录的 `missing-assets.csv`，列为：分类、路径或模式、取值、状态、功能、证据、可达性、严重度、备注、来源。

## 方法

- 引用来源：
  - UI 脚本里的路径字面量和拼接式路径；
  - 各模块字符串里的格式串，取值范围按反编译代码确定；
  - Client.exe 资源 ID 的编码规则：类型<<24 | 编号<<8 | 动作，构造函数在 `0x00425400`，拼路径在 `0x00447BD0`。
- 数据表：itemCFG、commodityCFG、mapDesc、全部 .map 文件、mapElem、.eff 内部引用、player*.ini、PetCfg.ini、PropSet、探险关卡 XML、taskSys.pkg。
- 原版加载图片时不检查文件是否存在（`0x005CB22D`、`0x005C463E`），缺图的那一项直接不画，不会崩溃。

## 汇总（文件数按缺失文件计）

| 分类 | 缺失项 | 正常游戏能看到的 |
|---|---|---|
| 探险 | 1 关卡数据（1647）、11 Boss 头像、2 图标、2 特效、10 技能定义、59 张 NPC 死亡特效、7 张元素炸毁图 | 约 16 个文件 + 10 条技能定义 |
| 机械/箱子 | 火焰 flame10000/10001、8 张玩家死亡特效 misc435～442、midIcon/machine.img | 10 |
| Boss（竞技） | 7 张死亡特效 misc449、451～456；角色 30 取到的 misc450 是错图 | 7 |
| 雕像 | misc247 等 | 1 |
| 比武 | 6 张招式姿势图（6 个姿势槽确定，编号 misc214～219 是推断） | 有人出招时可见 |
| 新手教程 | 4 | 1 |
| 对战通用 | 约 30（Halo_Excite.eff、底栏道具图标 item30020/30027/30028/30030/30100/30101、元素炸毁动画等） | 约 26 |
| 商城/装扮 | eye12（3767 篮球）、frame54/55；另有 namecardbound 两件放错目录 | 6 |
| 宠物 | 48 个宠物道具图标 pet<N>.img | 48 |
| 大厅与任务 | taskSys 缺 5006.xml，两件任务奖励不在 itemCFG | 2 |
| 婚礼 | dlg_marriageConfirm.img | 偶发 |
| 音效音乐 | 0 | 0 |

## 不用找旧客户端就能修的

- 边框名片 12501/12502：文件在 `object/namecard/namecardbound{1,2}_*` 和 `res/uires/icon/namecard/`，程序找的却是 `object/NamecardBound/` 和 `res/uires/icon/namecardbound/`。宿主加一个路径别名即可。
- 旧文档说“名片缺 trigger 动作”不成立：原版代码只请求 stand，145 张都在。

## 找旧客户端时的优先顺序

1. 探险：`battlefield_map1647`（含 GameCFG.ini 段）、新版 errorPPP.ini（补技能 21092、21099～21107）、探险 Boss 头像、pve 图标。
2. 死亡特效 misc435～442、449～456，火焰 flame10000/10001，雕像 misc247。
3. Halo_Excite.eff、底栏道具图标 item300xx、eye12、frame54/55。
4. 宠物图标、5006.xml、midIcon/machine.img、xuli2.eff。

## 推断成分

凡写“推断”的，都是因为相关代码在加壳区，静态读不出来：比武招式表、光环的 k 值、部分 misc 用法。

“原版本来就没有”的判断依据是包里只有个别成品，例如 NPC 死亡特效只有 misc444、misc448。只有更完整的旧客户端能证实。

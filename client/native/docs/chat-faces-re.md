# 聊天表情、分模式等级与几个消息框原生函数

2026-11-17，静态逆向 `gameUI.dll`、`Client.exe`。代码在 `qqtclient/render.py`（表情）、`session.py`（分模式等级）、`natives.py`。

## 聊天表情（`initFaceIconList`，gameUI `0x10018ECF`）

- `uiFunc` 启动时把表情代码表传给 `initFaceIconList`。脚本先后给 `faceIconList` 赋了两次值，生效的是第二次：共 144 个，`/m00`～`/m23` 在前。
- gameUI 按位置加载 `res/uiRes/face/faces/%03d.img`（`0x10018F4F`）。房间表情面板也按同一个位置取图，再用 `Win_FocusOnInsert` 把代码插入输入框。
- 匹配（`0x10019149`）：遇到 `/` 时按表顺序找第一个前缀相同的代码，不是最长匹配。匹配上的表情成为一个字形：宽度取图片宽度，不足 25 时按 20；高度取图片高度。
- 绘制（`0x100191F7`）：
  - 横坐标就是该字形的位置。
  - 图高至少 26 时顶边在行顶 −图高 +16；更矮时在行顶 −4。
  - 动画帧取 `timeGetTime() / 333` 对帧数取模。
- 会把代码画成表情的控件：RICHEDIT（`0x1001975A`，聊天输入框、房间头顶气泡）、MULTIEDIT（`0x10016736`）、DYLABEL（`0x10010B26`）、TEXTLIST（`0x10022CA3`，大厅和房间的聊天记录）。宿主对这四类都按上面的规则绘制，换行时表情不拆开。
- TEXTLIST 的行距是 textsize + rowspace，有没有表情都一样（`0x100236F0`）；绘制前裁剪到列表区域（`0x10025083`），所以高的会员表情会被截掉上半。宿主以前用字体行高，现在 TEXTLIST 改为原版行距，其他列表不变。
- TEXTLIST 还支持 `/<H>`、`/<c=…>`、`/<eft=…>` 等格式标记（`0x100231AA`），宿主没有实现。
- RICHEDIT 里相邻的 `/` 另有判断（`0x1001975A` 开头），宿主也没有照做。
- 实跑：测试服 58000、账号 1000007。大厅和房间聊天里的普通表情与会员表情都能显示并播放动画，输入框里输入代码后也直接显示成表情。

## 分模式等级（`GetPlayerModeInfoByUin` `0x0054F060`、`GetPlayerModeInfoByModeIndex` `0x0054F0F0`）

- 两者都向服务 `0x0079A908` 虚表 `+0x3A4` 要 PATTERN_POINT 列表：1 字节条数，之后每条 11 字节。宿主里每条依次是：积分（4 字节）、升级所需积分（4）、等级（2）、模式（1）。
- 脚本按列表位置 i 填第 i 个面板（包子、比武、雕像），不看记录里的模式号。
- 宿主只保存了自己登录回复里的这份列表，别人的取不到。
- 本地服务端从不填 PatternPoints（只是原样转发档案里的空列表），所以所有人的分模式等级都显示 0。要显示真实等级，服务端得先在特殊规则对局后记录积分；原版的积分规则没有资料。

## 消息框与道具

- `OnMsgBoxResult`（`0x0048B410`）：只转发 Client.exe 自己弹出的框，按句柄最高字节分到三个服务。另外 0x14～0x16 三个句柄是退出流程，其中 0x16 会打开官网。宿主自己弹的框句柄都是 0，原版对 0 不做任何事，所以留空。
- `OperateItem`（`0x0056ADB0`）：商城“删除道具”确认后调用。它在栈上组好请求（类型 5、道具号、操作 1）就返回了，从不发送，所以原版删道具本来就没有效果；服务端也没有删除道具的命令。宿主同样留空，不另造协议。

## 玩家资料里的人物（`SC_selRoom_drawPlayer`、`selRoom_playerWear`）

- 收到找人回复时，`0x0048EB20` 先把详情存起来，再执行 `go2playerInfo`。接着 `0x0054E8B0` 按对方角色（为 0 或大于 22 时用 1）重建资料框里的人物，对每个第 +7 字节等于该角色的道具执行脚本 `playerInfo_useCommItem(id)`，最后执行 `ui_getPlayerDetail()`。
- 脚本据此调用 `selRoom_playerWear`（`0x0054EB90` → `0x00442210`）。部位名表在 `0x007DA160`：cap、fhadorn、bhadorn、thadorn、head、hair、face、mask、eye、mouth、ear、neck、body、cloth、cladorn、npack、fpack、leg、foot、huanying、footprint。
- 穿帽子时去掉头发；换发型时，发型号为 0 则帽子置 0，否则帽子置 100。宿主用 wear 值 -1 表示去掉该部位。
- `SC_selRoom_drawPlayer`（`0x0054EB60`）每帧推进并绘制这个人物。宿主画站立第 0 帧，位置照商城预览居中、脚靠底；原版的具体位置没有追。
- 以前宿主没有注册这个绘制回调，资料框右下角一直是空的。代码在 `qqtclient/profile_avatar.py`。
- 服务端的找人回复已经按装备记录给道具填了角色字节（`projectProfileEquipment`）。当前测试库里没有任何装备记录，所以实跑的两个账号都显示不穿装备的角色 1；穿戴规则由 `tests/test_profile_avatar.py` 覆盖。
- 顺带修复：自己的婚姻信息早于找人回复到达时，`ui_UpdateMyMarriageInfo` 会读到空详情并报错。现在 `GetPlayerDetail` 在没有查询结果时返回全零结构，和原版的全零初值一致。

# 结算模式来自房间GameType

2026-11-04，补齐此前 `Scene+0x51A4` 的来源。它是原包UI的 **SetGameType** 值，不是登录SectionMode，也不是房间free_rule位。宿主现将Session中已由原版UI脚本设置的game_type带入战斗和结算。

## 原版调用链

1. 原包 `ui_SetGameType` / `ui_UpdateGameType` 调用SetGameType；房间普通无道具0、带道具1、探险2。原包UI在1时勾选itemChk。
2. Client.exe注册的SetGameType包装为 `0x0048BCE0`，通过GUID `6073F717-85B7-429B-8F5A-91A5A546018E` 查询UISection接口，再调虚表+31C，参数只读低字节。
3. QQTSection接口映射将该GUID对应到大厅对象+BF30，其虚表10054928。+31C=`0x10001F50 → 0x1002F706`，把字节写入大厅+C8B4，同时写入房间列表使用的+9B90。
4. Client.exe战斗入口 `0x005C862B` 调RoomModule虚表+68；该槽对应QQTSection `0x1000225C → 0x1000DDB0`，读取模块+11FDC所指大厅的+C8B4。
5. `0x005C863D..005C864F` 取返回值低8位写Scene+51A4，大于2则改成0。

实际执行输入0/1/2/3/4/255/256/257：场景值依次0/1/2/0/0/0/0/1。接口getter返回的高位可残留指针，原版明确只取AL。

## 结算分支

原版完整结果绘制在普通规则4、箱子规则8上均得到：GameType1使用misc322，0/2使用misc321；探险规则12始终使用misc322。普通布局图片宿主位置(71,196)，探险(171,196)。misc322原包文字为“勇气值”，在道具场同样出现，保留原版选择，不依据文字含义另改资源。

此前GAME_OVER真实转换记录已证明：普通竞技GameType1的经验百分比是0/0，Score、等级和统计照常保留；探险则不受此分支影响，仍按pvedata勇气积分计算。宿主现同时接通这两个分支。

## 对照与实局

```sh
.venv/bin/python tools/probe_result_modes.py /path/to/client-patched
.venv/bin/python tools/probe_results.py /path/to/client-patched --modes
```

`result_modes_v848.jsonl` 8条，执行原版客户端包装、DLL COM setter/getter和战斗入口片段；只控制MFC模块状态簿记、房间列表刷新及跨DLL的接口边界。场景片段在夹取结束后返回，未把整个战斗初始化跑完。`result_mode_draw_v848.jsonl` 9条，三种规则×三种模式的原生完整图片/文字调用序列。

早期探针中外层字段名section_mode现改为game_type，与实际来源一致；原有数据值未改变。既有单行投影测试现也直接核对模式1的0/0分支。Python194项通过。

58000持久服214带道具场，1000005/1000006正常胜负结算。双方从房间进入时均取得GameType1，经验百分比均为0/0，标题图片实际为322；两张截图已查看。胜/负全身动作、统计与等级正常，自动返回房间，进程均退出0，无脚本异常，服务端220条info、零warn/error。

产物 `/private/tmp/qqt-result-modes-re/live/`。结果控制器的本轮代码缺项已补齐；原版Windows完整窗口与历史录像的逐帧外观对照仍为独立验收项。

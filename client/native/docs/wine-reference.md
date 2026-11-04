# 原版客户端的隔离Wine对照环境

2026-11-04，在Apple Silicon / macOS26.7.2上实际搭建。运行原版补丁Client.exe、GFX.dll、gameUI.dll及Python2脚本，连接现有本地测试服；Mac重写客户端可与它同局。

## 环境与运行边界

- 使用CodeWeavers官方CrossOver26.3.0试用包，构建标记 `cxoffice-26.3.0rc2`。这是独立授权的软件，试用受厂商条款限制；工具不购买、注册或延长试用。
- ZIP来源为官方 `media.codeweavers.com/pub/crossover/cxmac/demo/crossover-26.3.0.zip`；387192026字节，SHA256为 `8688e0848c4e5f79f1cc351cb52d32447da00c6c00cfd3b4bb2d164d44589a26`，与Homebrew包元数据一致。
- 程序、专用Win10_64 Bottle、客户端副本、日志和截图都位于忽略目录 `client/native/generated/wine-reference/`。客户端源目录保持独立，macOS用写时复制建立副本。
- 只在副本中配置本地地址/端口、60帧上限及发行版已有的combineforge.xml别名。Client.exe沿用发行版已完成的静态兼容补丁。
- 启动与登录复用仓库的 `qqt-launch -tp-free -high-resolution-timer` 和 `qqt-login-immediate`：保留已有的启动阶段/本地SSO兼容流程，不注入Mac重写玩法代码。
- Wine进程使用 `LANG/LC_ALL=zh_CN.UTF-8`。实际GetACP/GetOEMCP为936，系统LCID2052；Windows版本报告10.0.19045，中文界面正常，无需另装字体或DirectX包。

官方试用说明：[CodeWeavers下载页](https://www.codeweavers.com/crossover/download/)。32位程序可使用64位Bottle，见[官方用户指南](https://support.codeweavers.com/en_US/crossover-mac-user-guide)。此环境验证的是补丁客户端在Wine下的行为；Windows实机图形、音频及精细调度仍需独立复核。

## 可重复工具

从仓库根目录运行：

```sh
client/native/.venv/bin/python client/native/tools/wine_reference.py prepare \
  --client-root /path/to/QQTang-Local/runtime/client-patched \
  --fetch-runtime --go /path/to/go

client/native/.venv/bin/python client/native/tools/wine_reference.py start
client/native/.venv/bin/python client/native/tools/wine_reference.py status
client/native/.venv/bin/python client/native/tools/wine_reference.py capture --out /tmp/qqt-original.png
```

已安装CrossOver时，可用 `--crossover-app /Applications/CrossOver.app`，省去下载。默认测试服端口为认证/游戏58000、目录58080、兼容TCP58443和CA57000，可用对应CLI参数指定；`start`的认证端口应与prepare一致。`--trace`可指定发行包已有的SSO失败消息模板。

窗口控制通过同Bottle内的32位Windows辅助程序，使用EnumWindows识别进程/QQTangWinClass，再发送Win32鼠标和键盘消息。操作要求窗口归属与记录的Windows PID一致，文本从stdin读取，不打印密码。坐标是原版800×600客户端坐标：

```sh
client/native/.venv/bin/python client/native/tools/wine_reference.py click --activate --x 625 --y 271
client/native/.venv/bin/python client/native/tools/wine_reference.py text --activate  # 从stdin输入，结束后Ctrl-D
client/native/.venv/bin/python client/native/tools/wine_reference.py key --key 13
```

默认仅发送窗口消息；部分原版控件在后台不执行动作，此时加 `--activate`，这会切换焦点和移动宿主鼠标。实际登录/离开按钮已验证需要前台时可使用它。`close`发送WM_CLOSE，仍由原版窗口处理退出；本轮观察到窗口隐藏后进程可能驻留，故另提供 `stop`，同时核对Windows PID、窗口归属及完整Client.exe路径后才结束该实例。登录后选区、进房、角色与准备均由原版控件和脚本处理，服务端报文用于确认操作效果。

prepare、start、status、capture、click、close、stop及停止后重新启动已实际验证。prepare保留已有客户端副本；更换发行资源版本时使用新的 `--root`，避免覆盖对照状态。正式工具未包含临时GPU抓图或任意进程内存操作。

## 截图权限与采集

macOS屏幕录制和辅助功能是两项独立权限。此会话进程实际从iTerm启动，因此应在“隐私与安全性→屏幕与系统音频录制”开启iTerm；“Codex Computer Use”已有权限不能替代这条命令行链的授权。用户选择稍后重启后，新截图进程已立即可用，本次未重启iTerm。

`status`使用只读系统API报告权限和可见Client.exe窗口。`request-screen-access`仅触发官方授权提示，由用户决定；工具不修改TCC数据库。`capture`通过 `screencapture -l <window-id>` 只截指定游戏窗口，多个Client.exe窗口时要求显式 `--cg-window-id`。本机逻辑窗口800×600，Retina截图为1600×1200，对照时应区分逻辑坐标和截图像素。

最初GDI/PrintWindow和Direct3D表面读回实验均得到黑图，不能作为原版实际画面的证据。用户看到正常画面后，检查发现宿主屏幕录制未授权；授权后窗口截图成功。首个试验性GPU探针因接口嵌套偏移错误使隔离客户端退出，已重启恢复；这些探针未并入正式工具。保留的工具采用窗口级系统截图，不安装图形钩子。

## 已完成验证

原版客户端实际显示登录、选区、大厅、房间，账号1000007完成本地挑战应答认证、进区及建房。SSO辅助状态为 `lobby_prepared`，对应服务端真实登录、房间及UDP登记报文；不是只创建了空窗口。

普通211混合局：Mac账号1000005担任仲裁，原版账号1000007加入；双方完成UDP登记和握手，Mac在23820ms记录自然结束，服务器向双方发送队伍2胜利及各自成绩（1=2、3=0）。Mac退出0，无Traceback，完整采集日志370条info、零warn/error。原版随后实际返回大厅。该首局未及时录下原版战斗/结算画面，不把服务端发送成功扩展成逐帧画面验收。证据目录 `generated/wine-reference/logs/mixed-211/`。

登录目录连接还出现一个0x0131报文被未认证业务门拒绝的警告，随后正常进区成功。此项保留为原版兼容尾项，不将包含它的初次登录记录记为零告警。

2026-11-05后续：已实现REQUEST_KINGDOM目录应答并修正退出后RST分类；首次登录、免密码重新选区、目录更新后冷启动均实测零warn/error，详见 `wine-directory-login-re.md`。上面的初次兼容日志保留为历史证据。

混合探险180秒：Mac账号1000005担任仲裁，原版1000007加入；原版角色选择/准备由窗口消息完成，双方先后进入1601（40×17）和1602（40×40），每关均完成UDP握手。Mac记录144个状态快照（第一关43、第二关101），无Traceback、退出0；服务器确认14次NPC死亡及1601→1602换关，采集期间1261条info、零warn/error。

每两秒仅抓原版窗口，共75张连续截图，已查看原版房间、第一关及第二关实际画面。180秒到时尚未自然结算，Mac按计划退出；服务端把仲裁权从玩家1交给原版玩家3，原版仍继续显示/运行探险。随后使用原版离开按钮清理对局，另记录了一条退出阶段 `tcp_read_failed`，发生在logout成功之后；不将整个清理过程写成零错误或自然胜负验收。完整阶段日志分别为 `logs/mixed-pve/server.jsonl` 和 `server-through-cleanup.jsonl`。

最后通过正式prepare/start命令重新启动，得到新的原版登录窗口，登录辅助为armed、窗口级截图成功；对照环境留在可继续使用的状态。上述混合实测不增加NPC独立函数基准计数，仍为9850条。

# 交接：macOS 原生客户端（2026-10-31）

接续 `2026-10-30-001-native-mac-client.md`（项目背景、环境）和 `2026-10-30-002-native-mac-client.md`（夺宝、玩家栏、测试机器人、联调脚本）。三份都要读，再读 `client/native/docs/checklist.md` 和 `client/native/docs/lessons.md`。

目标不变：让 QQ堂 在 Apple Silicon Mac 上原生可玩。Python 3 宿主程序（`client/native/qqtclient`，pygame-ce）运行原版界面脚本，连本仓库的 Go 服务端；对战规则由 Go 辅助进程 `cmd/qqt-battle-sidecar` 演算。用户用中文交流，回复和文档都用中文，汇报要用大白话，少用地址和术语。

## 分支和提交

- 本地分支 `native-mac-client`，**没有推送，也没有合并到 `main`**，等用户决定。
- 本轮提交：`04a6cab` 抢包子可玩，并按原版画法画。工作区干净（只有本文件未提交）。
- `client/native/generated/`（腾讯资源）和 `.chat_history` 绝不能提交。

## 这一轮完成的内容

**抢包子（规则 3）可玩**，规则写在 `cmd/qqt-battle-sidecar/bun.go` 开头和 README 的"已确认的原版约定"里：

- 开局在两个目标格建 3×3 包子店；进对方店拿、回自己店放，由仲裁方核对后发 0x0FB7、0x0FB9；被击杀时包子归空手的击杀者，否则掉在地上；阵亡 10 秒后复活，不等出生格。
- 与教程录像 `Tutorial2.qbv`（地图 806）核对过：复活正好 10 秒，拿放、击杀掉包子的顺序和我们的规则一致，录像在"一方把对方包子全部搬回家"后立即结束，和服务端的判胜条件一致。
- 两个机器人在 803、819 上各跑完一局：拿、放、判胜正常。

**画面按原版**（`client/native/qqtclient/battle/scene.py`）：

- 包子店：misc151（第一座，红顶）、misc152（青顶），4 帧动画；登记到锚格时替换地图文件原有的元素（地图 806 自带 8009、8010 两座房子）。
- 店里的包子数画在屋顶窗口里：本色一排，抢来的一排低 10 像素，间距 8 像素。上一轮按伪 C 记成"两排起点相同"是错的：建店函数末尾 `0x006152BF` 给两个图像对象设了不同的基准点 (−43, 17)、(−43, 7)，伪 C 把调用对象丢了。
- **场景道具整体上移 15 像素**（所有玩法都受影响）：原版道具基准点 (0, 15)。
- 以上都用 2021 年的原版对局录像逐像素核对过（B 站 BV1Fh411m7Cx，地图 819，8 人局）。坐标换算的结论：原版精灵和特效坐标换到我们的场地坐标都是"减 (8, 21)"。

**收到结果后停止对局**（`battle/online.py`、`scene.py` 的 `over`）：原版收到 0x0FBB 后进入状态 2（`0x005C874F`），只走时钟、不再演算和发消息。抢包子的胜负在对局中途宣布，之前宿主会在结算画面后面继续发消息，服务端报一串警告。现在已没有这些警告。

**其他：**

- 客户端新增 `--shot 路径@毫秒`，可以在运行中途截图，可以写多次。
- 用户说"地上的包子太小"，其实那是店里的计数图标：当时店没画出来，图标露在草地上。经验已写进 `docs/lessons.md`。

## 没做的（已列进 checklist）

- 中途离开时手上的包子回到店里（槽位 8）。夺宝也没做离开处理；1 对 1 时服务端直接结束对局，3 人以上才用得上。
- 地上的包子被火烧到怎么办，没查。
- 店的动画每帧 100 毫秒是推断（`effect/default_MapElement.eff` 是 Python 字面量文本，字段含义没完全确认）。
- 香蕉皮、胶水这类放置物的位置没和录像核对，没跟着道具上移。
- 复活免伤的闪烁效果（`.eff`）仍没画。现在知道 `.eff` 是文本格式，可以顺手解。
- 已知的无害警告：服务端宣布结果后几毫秒内，仲裁方可能还转发一条拿放消息，服务端拒收一次。原版也有这个时间差，没处理。

## 下一步（用户定的顺序）

踢炸弹（规则 2，有教程录像 `Tutorial3.qbv`）→ 雕像 → 其他，见 `client/native/docs/other-rules.md` 第 4 节。

建议做法和抢包子一样：先读伪 C 理清规则，用教程录像核对消息顺序，再到 B 站找原版录像核对画面（用户喜欢这种核对方式，也主动提供过参考链接），最后用两个机器人联调。

## 用户的决定和偏好（新增的）

- 用户说过没时间亲自测，联调一律用两个机器人（`--autoplay`）扮演两名真人。
- 画面拿不准时，用户希望去网上找原版的历史图片或录像核对。用户提供过两个链接，都没有对战画面：`qqt.95hyc.cn`（"QQ堂怀念版"，只还原界面；首页链接了 B 站 up 主"怀念QQ堂"的视频和"QQ堂游戏素材库" `qqqt.ysepan.com`）和 GitHub `atxxfs/QQTPVE`（用原版素材做的闯关小游戏，没有特殊玩法）。B 站搜"QQ堂 抢包子""QQ堂 踢炸弹"能找到很多停服前的原版录像。
- 旧决定仍然有效：引擎扩展点默认关闭、补测试、单独说明；暂不装 Wine；可以用 Unicorn 黑盒跑受保护的函数；Boss 局只在客户端拦截。

## 用原版录像核对画面（方法）

详细步骤也写在 `docs/lessons.md`。要点：

1. 搜视频：先 `curl -c cj.txt https://www.bilibili.com/` 拿 cookie，再请求 `https://api.bilibili.com/x/web-interface/search/type?search_type=video&keyword=<关键词>`（带 Referer `https://search.bilibili.com/`）。
2. 取视频流：`x/player/pagelist?bvid=` 拿 cid，`x/player/playurl?bvid=&cid=&qn=80&fnval=16` 拿 DASH 地址。不登录只有 480p。
3. 下载：主节点（akamai）在这里几乎下不动；用 `backupUrl`，必须带完整的浏览器 UA、`Referer: https://www.bilibili.com/`、`Origin: https://www.bilibili.com`，并且用分段请求（`curl -r 起点-终点`，每段 2 MB）。不分段会返回 567（EdgeOne 拦截页）。命令都要关沙箱。
4. 截帧和匹配：临时环境 `/tmp/qqt-web/venv`（`uv venv` + `uv pip install --system-certs opencv-python-headless numpy`，公司网络要加 `--system-certs`），脚本在 `/tmp/qqt-web/scripts/`（`frames.py` 截帧和缩略图，`match.py`/`match2.py`/`rows.py` 带透明蒙版的模板匹配，`side.py` 我们的截图和录像并排放大）。下载的文件放 `/tmp/qqt-web/` 下单独的目录，用 `python -I` 运行。
5. 640×480 的录像放大到 800×600 就是原版屏幕坐标；先拿几个地图元素校准整段录屏的偏移（BV1Fh411m7Cx 是 (−2, −6)），素材图从宿主的 `SpriteCache` 导出成带透明的 PNG 再匹配。

`/tmp` 重启后会清空，上面的环境和脚本都要重建。

## 测试环境

和上一份交接相同（原版客户端 `$C`、测试服务端 `/tmp/qqt-out/qqt-server-onnx`、Go 在 `/tmp/qqt-go`、测试账号 1000003/1000004）。本轮写了一个可以换地图的联调脚本 `/tmp/qqt-tools/live/run_map.sh <输出目录名> <毫秒> <地图>`，环境变量 `SHOTS="18000 20000 …"` 指定中途截图时间（默认 30000 到 160000 的几个点）。内容如下（zsh，要关沙箱运行）：

```sh
#!/bin/zsh
# run_map.sh <name> <ms> <map>: two autoplay clients; 1000003 owns the room (arbitrator), 1000004 joins team 2.
C=~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched
LOG=~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/logs/server-local.jsonl
OUT=/tmp/qqt-tools/live/$1
MS=${2:-75000}
mkdir -p $OUT
wc -l < $LOG > $OUT/server-start-line
shots() { for t in ${=SHOTS:-30000 45000 60000 80000 100000 130000 160000}; do [ $t -lt $MS ] && echo "--shot" "$OUT/$1-$t.png@$(( t - $2 ))"; done }
cd /Users/cduan/qqtang/client/native
.venv/bin/python -m qqtclient --client-root $C --headless --autoplay --screen login \
  --call "Login(1000003,0,'123456')@500" --call "request_LoginSection(1000003,0,0,0)@2000" \
  --call "CreateRoom('t',0,1,'')@4000" --call "UserSelMapInRoom(${3:-1101})@5500" --call "UserReady()@14000" \
  $(shots owner 0) --exit-after $MS --screenshot $OUT/owner.png > $OUT/owner.log 2>&1 &
sleep 3
.venv/bin/python -m qqtclient --client-root $C --headless --autoplay --screen login \
  --call "Login(1000004,0,'123456')@500" --call "request_LoginSection(1000004,0,0,0)@2000" \
  --call "EnterRoom(1)@5000" --call "UserSelTeamInRoom(0,2)@6000" --call "UserReady()@7000" \
  $(shots guest 3000) --exit-after $(( MS - 3000 )) --screenshot $OUT/guest.png > $OUT/guest.log 2>&1
wait
tail -n +$(( $(cat $OUT/server-start-line) + 1 )) $LOG > $OUT/server.jsonl
echo done
```

- 抢包子 1 对 1 每边只有 1 个包子，开局几秒就可能分出胜负；要看对局中的画面，截图时间排在 18～24 秒（开局大约在第 16 秒）。
- 抢包子地图是 801～829（目标格见 `mapdata` 目录里的 `ObjectiveCells`）；803 两座店在左右两边，819 和录像是同一张图。
- 教程录像解析工具（临时）：`/tmp/qqt-tools/bunreplay/main.go`，用 `go run -overlay /tmp/qqt-tools/bunreplay/overlay.json ./cmd/qqt-bun-replay <qbv 路径>` 打印拿放、击杀、复活消息；加参数 `tail` 打印录像结尾。改一改就能用来看 `Tutorial3.qbv`。
- 测试：`go test ./internal/game/... ./cmd/qqt-battle-sidecar`（地图测试要临时建 `runtime/client-patched` 符号链接，用完删掉）；Python `cd client/native && .venv/bin/python -m unittest discover -s tests`（现在 80 个，全部通过）。

## 坑（新增的）

- 伪 C 里没有调用对象的 `FUN_xxx(常量, 常量)`（比如设基准点的 `0x005C47DC`），要用 `pe dis` 看前一条 `mov ecx, …` 才知道作用在哪个对象上。
- 有些地图元素在开局时由程序建（包子店），地图文件里没有。画面缺东西时先查开局建了什么。
- `uv` 在公司网络下要加 `--system-certs`，否则报证书错误。
- B 站接口直接用 curl 会被拦，要先拿首页 cookie、带 Referer；网页要加 `--compressed`。

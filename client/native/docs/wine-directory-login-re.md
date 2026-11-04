# 原版目录0x0131与退出连接重置

2026-11-05，接续 `wine-reference.md` 的两个真实兼容问题。已更新本地测试服，原版窗口完成登录、退出、免密码重新选区以及目录更新后的冷启动验证。

## 0x0131是大区目录查询

两次原版捕获均显示：同一TCP连接先发0x0133，再发82字节0x0131。后者使用Route6、Marker0xFFFF、Section0xFE，路由/内层序号为2；有效负载8字节，分别为Uin与Time。它不是游戏房间业务。

原版 `NetCenter.dll 0x10004F6D` 的伪C错误删掉了发送分支；汇编确认它以schema041E编码、命令0x0131发送。接收入口 `0x100044A2` 对0x0131使用schema0806。QQTMsgData中分别为REQUEST_KINGDOM与RESPONSE_KINGDOM。

此前服务端只在目录入口处理0x0133，0x0131落入游戏业务鉴权；目录连接虽然持有已验证的导航凭据，尚未发送REQUEST_LOGIN、没有session.UIN，因此被记为未认证业务并关闭。

现在目录入口验证0x0131的完整路由、负载长度及负载/包络UIN，要求该连接持有同账号的导航凭据或主游戏连接所有权。不会按命令号开放未认证业务。请求只读取启动时构建的目录；不创建游戏登录、房间、物品或余额操作。导航身份不能使用REQUEST_LOGOUT等游戏业务，回归对此有显式断言。

## 真实编解码器校验

原版结构如下：

| 结构 | 原生大小/内容 |
|---|---|
| RESPONSE_KINGDOM | 15710字节；Result、KingdomStamp、KingdomNumber及最多10个Kingdom |
| KINGDOM_INFO | 1570字节；固定40字节名称、ID、可变DNS及最多20个目录服务器 |
| DIR_SERVER_INFO | 74字节；服务器ID、可变DNS、IPv4、最多10个TCP端口 |

当前本地应答提供一个RealmID1“东部电信”、一个保留目录服务器254及配置的IPv4/主服务端口；KingdomStamp保持0，与当前Hall应答一致。RealmID与Hall的LocationID0x32E9、游戏服务器ID1是不同字段。IPv4按原版DWORD数值编码，解码后的内存字节直接适合Winsock。

通过原版服务注册表查询NetCenter的IID（RVA0x2F290），调用真实QQTEncoder的+0x10编码和+0x14解码：15710字节原生对象编码为76字节，再逐字节还原整个对象，两个返回码均为0。只使用临时缓冲，没有向UI派发或修改对象/虚表。独立76字节输出保存为协议黄金夹具；请求夹具取自前述真实82字节抓包。

可重复的Windows/386探针：

```sh
GOOS=windows GOARCH=386 CGO_ENABLED=0 go build \
  -o client/native/generated/wine-reference/helpers/kingdom-codec-probe.exe \
  client/native/tools/probe_directory_kingdom.go
```

在专用Bottle内运行该exe并指定 `-pid <Windows客户端PID>`；`-out`可保存JSON，`-ip/-port`可选择仅用于编码验证的目录地址。探针不发送网络报文。

## 退出RST的会话边界

原版发出REQUEST_LOGOUT、收到成功应答后，约109ms发生连接重置。原处理把它与活动连接异常一律记为tcp_read_failed/error。

现在只有实际成功处理退出的连接才记录completedLogout；重新登录即清除。随后若读错误确为连接重置，记为connection_reset_after_logout/info。其他读错误、未退出的RST、重新登录后的RST仍保留错误。macOS使用ECONNRESET，Windows使用Winsock WSAECONNRESET；真实TCP回归覆盖三种会话阶段，并在Wine中运行Windows/386版测试通过。

退出保留的是导航凭据而非密码凭据。新选区连接可消费既有导航能力并重新绑定，撤销旧选择器的能力；后续游戏登录仍只消费一次，不恢复旧房间/比赛快照。异主机、异UIN、辅助连接、伪路由和业务越权均有回归。

## 验证及部署

完整directory/game协议、server/probe和game/match测试通过；Windows/386版退出/RST测试在Wine实际通过；正式原版编解码探针再次往返成功。

原版账号1000007实测：

- 首次目录更新及进区：31条info、零warn/error，原来的0x0131拒绝消失。DirCfg变为一个有效Realm/目录、TS0、127.0.0.1:58000。
- 大厅退出、直接重新选自由1区：24条info、零warn/error，实际出现connection_reset_after_logout；未重新输入密码即进区。
- 从更新后的DirCfg冷启动：原版正常显示选区并进入大厅，29条info、零warn/error。目录版本一致时不再发0x0131，保留正常缓存行为。验证脚本最初把“必须每次出现0x0131”当成条件而超时，随后依据真实报文及选区画面完成验证，没有因此改动服务端。

产物 `/private/tmp/qqt-wine-debug/`，选区/大厅截图已查看。没有把以上采样窗口外的日志或普通游戏胜负算入本次验证。NPC独立函数基准仍为9850条。

测试服为 `/Users/cduan/qqtang-test/qqt-server-directory-kingdom`，沿用配置、数据库和ONNX模型；启动成功，PID记录在 `server/directory-kingdom.pid`。旧二进制保留，所有端口保持原配置。原版参考客户端已重新进入本地大厅。

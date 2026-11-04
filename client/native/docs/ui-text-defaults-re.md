# 控件默认文字色

2026-11-04。宿主未声明drawcolor时原先取黑色，现恢复原版默认不透明白色。脚本模板显式声明的颜色继续优先，例如TEdit/TLabel的黑字声明；None保留原生默认值。

`probe_ui_text_defaults.py` 实际执行默认颜色初始化 `0x10026279`，以及WIDGET/BUTTON/CHECK/LABEL完整构造。四者 `+0xCC` 均为 `0xFFFFFFFF`；`0x1002650D` 写入这个全局默认值。静态属性加载与导出代码也确认 `+0xCC` 为drawcolor。

另实际执行基础文字入口 `0x10026E0E`，记录传给受控字体排版出口的ARGB：默认色、显式黑色、(32,64,128,153)三组，各取控件透明度0/0.5/0.8/1，共12次调用。例如默认色0.5传 `0x7FFFFFFF`，0.8传 `0xCCFFFFFF`。与前四个构造合计16条结果，已逐项对照正式Widget/Renderer文字入口的RGB和透明度；字体轮廓/排版出口受控，不作原生字体像素栅格结论。

Python202项通过，旧列表文本用例的默认色期望同步纠正为白色。账号1000007正常登录、进区、建房及角色按钮悬停/离开验证通过，截图已查看，客户端退出0、无Traceback；本次UI验证期间服务端41条info、零warn/error。

```sh
.venv/bin/python tools/probe_ui_text_defaults.py /path/to/client-patched
```

产物 `/private/tmp/qqt-ui-text-defaults-re/`。该16条为独立UI观测，不加到NPC/共用模型8865条中。

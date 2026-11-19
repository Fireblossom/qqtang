# 交接：在 42 集群上训练 QQ堂 对战 AI

写给在集群容器里接手训练的 Claude 实例（2026-11-18）。先读完本文和 `training/README.md`（接口规格，以及 Model、Worker、Trainer 三节），再动手。

## 目标

1. 先训练普通模式（规则 1）的新模型，目标是在 1v1 和 2v2 下稳定赢过当前服务端用的 U2392（`configs/models/qqtang-rule1.onnx`）。
2. 之后扩展到其他模式，顺序见文末“之后的模式”。

用户希望最终每种模式都能和 AI 玩。离线陪玩现在用脚本机器人和 U2392 顶着（`client/native/docs/offline-ai-re.md`），学习型模型会逐个模式替换它们。

## 现状（都已在 main）

| 部分 | 位置 | 状态 |
|---|---|---|
| 环境 worker（Go） | `cmd/qqt-train-worker`、`internal/game/battleenv` | 观测在 170 张普通图上与服务端推理逐位一致（`TestWorkerObservationMatchesServerInference`）；确定性；支持 `decision_cadence = "server"`，复现线上服务端的提前决策 |
| 网络（PyTorch） | `training/qqtrain/model.py`、`export.py` | 逐节点复现 U2392，与原模型误差约 1e-5；导出的 ONNX 能被服务端 Go 加载器读入；value 头是新加的 |
| 训练器 | `training/qqtrain/` | 带记忆的 PPO，league 对手池（最新策略、历史快照、U2392），定期与 U2392 评估并导出最佳 ONNX，可断点续训 |
| 集群脚本 | `training/scripts/cluster_run.sh`、`configs/h100.toml` | 只做过语法检查，**从没在集群上跑过** |

测试：在 `training/` 里执行 `uv run pytest`，共 61 项，在 Mac 上全部通过。真实 worker 的测试在找不到二进制或游戏数据时会自动跳过。

## 在集群上部署

游戏数据属于腾讯，**不能进 git**。用户会把一个私有数据包 `qqtang-rl-bundle.tar.gz` 拷到 `/mlde`，里面有：

- `qqtang-local/runtime/client-patched/{map,config,object/mapElem}` 和 `qqtang-local/data/qqt_competitive_map_rules.json`：worker 读的地图、配置和元素表；
- `repo/training/bin/qqt-train-worker-linux-amd64`：静态编译的 worker，集群上不需要 Go；
- `runs/mac-ft1/`：Mac 上 3 小时微调的检查点、指标和评估记录（结果见下文）。

步骤：

```bash
mkdir -p /mlde/qqtang-rl && cd /mlde/qqtang-rl
git clone https://github.com/Fireblossom/qqtang.git repo      # 已有则 git -C repo pull
tar -xzf /mlde/qqtang-rl-bundle.tar.gz -C /mlde/qqtang-rl      # 补上 qqtang-local/、worker 二进制、runs/mac-ft1
git -C repo config user.email 16226671+Fireblossom@users.noreply.github.com   # 用户要求提交只用 noreply 邮箱
bash repo/training/scripts/cluster_run.sh                       # 建 uv 环境（/mlde/venv），在 tmux 里启动 configs/h100.toml
bash repo/training/scripts/cluster_run.sh --status              # 看进度
bash repo/training/scripts/cluster_run.sh --stop                # 做完当前更新、写检查点后退出
```

- 一切持久数据都在 `/mlde/qqtang-rl/runs/<RUN>/`。容器被回收后重跑 `cluster_run.sh` 会自动续训（`run.resume = "auto"`）。
- 想接着 Mac 的结果训练：第一次启动时设 `--set run.resume=/mlde/qqtang-rl/runs/mac-ft1/checkpoints/ckpt_000152.pt`。容器回收后重跑时**去掉这个参数**，否则会回到 Mac 的检查点，而不是本次 run 的最新检查点。
- PyTorch 固定为 2.11.0：U2392 就是用它的导出器生成的。Linux 上通过 uv source 从 PyTorch cu126 索引安装，见 `pyproject.toml`。
- 集群的连接方式（`ssh 42`、`42-up` 等）是用户的事。容器回收、换容器、输入密码都要先问用户。

## Mac 上的试跑结果

2026-11-18，Apple M4 Max（MPS），`configs/mac.toml`，从 U2392 起步。目录 `runs/mac-ft1/`，已放进数据包。

- **有效训练只有约 23 分钟**：18:45 开跑，19:09 拔了电源后 Mac 进入空闲睡眠，之后只在系统维护唤醒时断续跑。共 152 轮更新、225 万次学习决策，其中前 111 轮是正常速度。以后在 Mac 上长跑要接电源，并用 `caffeinate -i` 启动。
- **速度（实测）**：每秒约 2000 次学习决策，每轮更新 7.5 秒；随着 league 快照增多，逐渐降到约 1000 次/秒、14 秒一轮。瓶颈在 MPS 上的网络计算，不在 worker。
- **指标**：value 头从零学起，explained_var 从 0 升到约 0.85～0.89；KL 0.001～0.004；熵 0.62～0.87；没有出现数值异常。
- **评估（实测，只评了一次）**：第 100 轮对 U2392 打 1v1 64 局，31 胜 10 平 23 负，得分 0.56，95% 区间 0.45～0.67，**和 U2392 没有显著差别**。导出在 `export/best.onnx`，不要拿它替换 U2392。
- **结论**：训练循环在真实对局上是通的，可以上集群。Mac 的算力只够验证流程，不够把模型训强。
- 检查点：`checkpoints/ckpt_000152.pt` 可用来续训，但训练量很小，直接从 U2392 重新开始也一样。

## 第一次上集群要做的

1. **短跑校准**（30～60 分钟）：确认 CUDA、bf16 和 worker 都正常，记录每秒学习决策数和每轮更新时间；按 `h100.toml` 里的估算调 `worker.num_workers` 和 `ppo.epochs`；试 `model.compile = true`。
2. **长跑**：盯住 `eval.jsonl` 里对 U2392 的胜率和 Elo，以及 `metrics.jsonl` 里的 KL、explained_var、熵。得分创新高时会自动导出 `export/best.onnx` 和配套 `.json`。
3. **成果交付**：新模型想上服务端，要替换 `configs/models/qqtang-rule1.onnx` 和配套 json。替换前先在本地跑 Go 加载测试（`training/tools/check-onnx-go.sh`，需要 ORT 库），再和 U2392 打足够多的评估局。推送到 GitHub 前问用户。

## 已知问题和改进方向

- **原训练配方已丢失**：U2392 是怎么训出来的不知道，新训练不一定能超过它。始终以对 U2392 的评估为准。
- **观测带宽是瓶颈**：每个环境步传 182 KB，2 人局里空座位的 f16 观测也照传，浪费约一半。可以只传有效座位，或改用共享内存。
- **评估期间训练会暂停**：可改成另开进程评估。
- **还没开课程**：worker 支持旧环境的各种课程（对决、封锁、连锁收割、逃生、开局发育、残局回放等），比例可配，但训练配置里都还是 0。
- **CUDA 上的 bf16 和 `torch.compile` 都没实测过。**
- **少了一层服务端保护**：服务端会取消“多个 AI 同帧放泡、合起来困住自己”的放泡（`suppressMarginallyUnsafeVirtualBombs`），worker 没有复现。部署时服务端会兜底，训练分布与部署略有差别。

## 之后的模式（参考调研结论）

- 顺序建议：抢包子（离规则 1 最近，不需要新动作）→ 比武、夺宝 → 雕像（要扩动作：48 号黑泡、66 号碎片）→ 踢炸弹。机械、箱子、糖客战、探险建议长期用脚本机器人。
- 每个模式都要做：
  - 在 `battleenv` 里加一个 `RuleModule`（`rules.go`）。规则逻辑现在在 `cmd/qqt-battle-sidecar` 里，各规则需要什么见 `training/README.md` 的 Worker 一节末尾。
  - 扩张量 schema（目标物和比分通道）、设计该模式的奖励；
  - 服务端按规则选模型。现在服务端只有一个模型路径，也只在规则 1 无道具图上补 AI。
- 注意：蓄力招式属于机械模式（规则 7），不是比武，见 `client/native/docs/wrestle-skills-re.md`。

## 约定

- 提交只用 noreply 邮箱；推送前问用户。
- 游戏数据和二进制不进 git（`training/.gitignore` 已忽略 `bin/`、`runs/`、`.venv/`）。
- 文档用中文写；说明里区分“实测”和“估计”。

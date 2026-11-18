"""PyTorch reimplementation of the deployed ``unified-candidate-v1`` policy.

The architecture is recovered node by node from the U2392 ONNX graph
(``configs/models/qqtang-rule1.onnx``), not only from the prose in
``docs/onnx-network-architecture.md``. Parameter names reproduce the exporter's
module names (``model.spatial_encoder.0.weight`` ...), so U2392's initializers
load directly; the 31 initializers whose names the exporter dropped are mapped
by their position in the graph (see :func:`load_onnx_state_dict`).

Training additions that are not part of the deployment graph live outside the
``model.`` prefix (currently only ``value_head``).

Observation layout facts used by the graph (battleenv tensor version 18):

* spatial channels 7..8: self (free, trapped); 9..10: allies; 11..12: enemies;
  64..71: public behaviour rates of the actor standing on a cell.
* scalars 94..110: bubble-placement consequence facts; 112..136 / 137..161:
  known-danger reachability per movement candidate (stay, up, right, down,
  left) x 5 horizons, without / with a new bubble; 162..181: refuge
  found/time per candidate (current found, current time, bubble found,
  bubble time).
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import torch
import torch.nn.functional as F
from torch import Tensor, nn

CHANNELS = 117
HEIGHT = 13
WIDTH = 15
SCALARS = 187
ACTIONS = 45
HIDDEN = 256
FEATURE = 192

# Illegal actions get float32's lowest finite value, exactly like the ONNX
# graph (Where(not legal, -3.4028234663852886e38, logits)). Finite values keep
# log-softmax and entropy free of NaN.
ILLEGAL_LOGIT = torch.finfo(torch.float32).min

SELF_CHANNELS = slice(7, 9)
ALLY_CHANNELS = slice(9, 11)
ENEMY_CHANNELS = slice(11, 13)
BEHAVIOUR_CHANNELS = slice(64, 72)
BOMB_FACT_SCALARS = slice(94, 111)
CURRENT_REACH_SCALARS = slice(112, 137)
BOMB_REACH_SCALARS = slice(137, 162)
REFUGE_SCALARS = slice(162, 182)

# linspace(-1, 1, n) exactly as the float32 constants baked into U2392 (they
# are symmetric with an exact 0; torch.linspace on arm64 is off by up to 4e-8).
ROW_COORDINATES = [
    -1.0, -0.8333333134651184, -0.6666666269302368, -0.5, -0.3333333134651184, -0.16666662693023682, 0.0,
    0.16666662693023682, 0.3333333134651184, 0.5, 0.6666666269302368, 0.8333333134651184, 1.0,
]  # fmt: skip
COL_COORDINATES = [
    -1.0, -0.8571428656578064, -0.7142857313156128, -0.5714285373687744, -0.4285714030265808,
    -0.2857142686843872, -0.14285707473754883, 0.0, 0.14285707473754883, 0.2857142686843872,
    0.4285714030265808, 0.5714285373687744, 0.7142857313156128, 0.8571428656578064, 1.0,
]  # fmt: skip

# Joint action -> movement candidate (0 stay, 1 up, 2 right, 3 down, 4 left).
ACTION_DIRECTION = [0, 1, 2, 3, 4, 0, 1, 2, 3, 4] + [0] * 7 + [d for _ in range(7) for d in (1, 2, 3, 4)]
# Joint action -> bubble intent (1 for place-bubble actions 5..9).
ACTION_BOMB = [0] * 5 + [1] * 5 + [0] * 35
# Joint action -> held-item intent (0 none, 1..7 the seven fixed item actions).
ACTION_ITEM = [0] * 10 + list(range(1, 8)) + [i for i in range(1, 8) for _ in range(4)]


def _conv_stack() -> nn.Sequential:
    return nn.Sequential(
        nn.Conv2d(CHANNELS + 2, 24, 3, padding=1),
        nn.SiLU(),
        nn.Conv2d(24, 32, 3, padding=1),
        nn.SiLU(),
        nn.Conv2d(32, 32, 3, padding=1),
        nn.SiLU(),
    )


class DilatedResidualBlock(nn.Module):
    def __init__(self, channels: int, dilation: int) -> None:
        super().__init__()
        self.depthwise = nn.Conv2d(channels, channels, 3, padding=dilation, dilation=dilation, groups=channels)
        self.expand = nn.Conv2d(channels, 2 * channels, 1)
        self.project = nn.Conv2d(2 * channels, channels, 1)

    def _depthwise(self, x: Tensor) -> Tensor:
        conv = self.depthwise
        if x.device.type != "cpu" or torch.onnx.is_in_onnx_export():
            return conv(x)
        # CPU runs grouped dilated convolutions (and their backward) through
        # slow_conv_dilated2d, about 10x slower for this model than these nine
        # shifted multiply-adds, which compute the same depthwise convolution.
        # (On MPS the native kernel is 2.5x faster than the shifts.)
        d = conv.dilation[0]
        height, width = x.shape[-2:]
        padded = F.pad(x, (d, d, d, d))
        weight = conv.weight.view(1, -1, 3, 3)
        out = conv.bias.view(1, -1, 1, 1)
        for i in range(3):
            for j in range(3):
                window = padded[:, :, i * d : i * d + height, j * d : j * d + width]
                out = out + window * weight[:, :, i : i + 1, j : j + 1]
        return out

    def forward(self, x: Tensor) -> Tensor:
        y = F.silu(self._depthwise(x))
        y = F.silu(self.expand(y))
        return x + self.project(y)


def _refiner() -> nn.Sequential:
    return nn.Sequential(*(DilatedResidualBlock(32, d) for d in (1, 2, 4, 1)))


def _agent_encoder() -> nn.Sequential:
    return nn.Sequential(
        nn.Linear(32 * HEIGHT * WIDTH + SCALARS, FEATURE),
        nn.LayerNorm(FEATURE),
        nn.SiLU(),
        nn.Linear(FEATURE, FEATURE),
        nn.SiLU(),
    )


@dataclass
class Observation:
    """Memory-independent intermediate values for one flattened batch."""

    encoded: Tensor  # [N,192] agent encoding (before the temporal residual)
    router: Tensor  # [N,5] style weights
    multiplayer_gate: Tensor  # [N,1]
    multiplayer_context: Tensor  # [N,45] gate * context actor logits
    candidate_static: tuple[Tensor, Tensor, Tensor]  # scalar [N,1,24], local [N,45,24], consequence [N,45,24]
    consequences: Tensor  # [N,45,32]


class UnifiedCandidateNet(nn.Module):
    """The deployed network (all parameters of U2392, nothing else)."""

    def __init__(self) -> None:
        super().__init__()
        self.spatial_encoder = _conv_stack()
        self.spatial_refiner = _refiner()
        self.agent_encoder = _agent_encoder()
        self.actor = nn.Linear(FEATURE, ACTIONS)
        self.expert_residuals = nn.ModuleList(nn.Linear(FEATURE, ACTIONS) for _ in range(5))
        self.behavior_router = nn.Sequential(nn.Linear(8, 32), nn.SiLU(), nn.Linear(32, 5))
        self.multiplayer_residual = nn.Linear(FEATURE, ACTIONS)
        self.multiplayer_context_spatial_encoder = _conv_stack()
        self.multiplayer_context_spatial_refiner = _refiner()
        self.multiplayer_context_agent_encoder = _agent_encoder()
        self.multiplayer_context_actor = nn.Linear(FEATURE, ACTIONS)
        self.tactical_residual = nn.Linear(FEATURE, ACTIONS)
        self.tactical_gate = nn.Linear(FEATURE, 1)
        self.temporal_cell = nn.GRUCell(FEATURE, HIDDEN)
        self.temporal_residual = nn.Linear(HIDDEN, FEATURE)
        self.candidate_state_projection = nn.Linear(FEATURE, 24)
        self.candidate_local_projection = nn.Linear(CHANNELS, 24)
        self.candidate_scalar_projection = nn.Linear(SCALARS, 24)
        self.candidate_consequence_projection = nn.Linear(14, 24)
        self.candidate_action_embedding = nn.Parameter(torch.empty(ACTIONS, 24))
        self.candidate_score = nn.Linear(24, 1)
        self.direct_candidate_state_projection = nn.Linear(FEATURE, 48)
        self.direct_candidate_consequence_projection = nn.Linear(32, 48)
        self.direct_candidate_action_embedding = nn.Parameter(torch.empty(ACTIONS, 48))
        self.direct_candidate_score = nn.Linear(48, 1)
        self.interaction_candidate_state_projection = nn.Linear(FEATURE, 96)
        self.interaction_candidate_consequence_projection = nn.Linear(32, 96)
        self.interaction_candidate_action_embedding = nn.Parameter(torch.empty(ACTIONS, 96))
        self.interaction_candidate_mlp = nn.Sequential(nn.Linear(288, 128), nn.SiLU(), nn.Linear(128, 96), nn.SiLU())
        self.interaction_candidate_score = nn.Linear(96, 1)
        self.factorized_direction = nn.Linear(FEATURE, 5)
        self.factorized_bomb = nn.Linear(FEATURE, 2)
        self.factorized_item = nn.Linear(FEATURE, 8)
        for embedding in (
            self.candidate_action_embedding,
            self.direct_candidate_action_embedding,
            self.interaction_candidate_action_embedding,
        ):
            nn.init.normal_(embedding, std=0.02)

        # Constants of the graph, kept as non-persistent buffers so they follow
        # .to(device) but never enter checkpoints.
        rows = torch.tensor(ROW_COORDINATES, dtype=torch.float32).view(1, 1, HEIGHT, 1)
        cols = torch.tensor(COL_COORDINATES, dtype=torch.float32).view(1, 1, 1, WIDTH)
        self.register_buffer("row_coordinates", rows, persistent=False)
        self.register_buffer("col_coordinates", cols, persistent=False)
        self.register_buffer("action_direction", torch.tensor(ACTION_DIRECTION), persistent=False)
        self.register_buffer("action_bomb", torch.tensor(ACTION_BOMB), persistent=False)
        self.register_buffer("action_item", torch.tensor(ACTION_ITEM), persistent=False)
        is_bomb = torch.tensor(ACTION_BOMB, dtype=torch.float32).view(1, ACTIONS, 1)
        self.register_buffer("is_bomb", is_bomb, persistent=False)

    # ------------------------------------------------------------------ encoder

    def _with_relative_coordinates(self, spatial: Tensor) -> Tensor:
        """Append row/column offsets relative to the self cell (117 -> 119)."""
        mask = (spatial[:, SELF_CHANNELS].sum(1, keepdim=True) > 0).to(spatial.dtype)
        count = mask.sum((-2, -1)).clamp(min=1.0)[:, :, None, None]
        row_center = (mask * self.row_coordinates).sum((-2, -1), keepdim=True) / count
        col_center = (mask * self.col_coordinates).sum((-2, -1), keepdim=True) / count
        batch = spatial.shape[0]
        rows = (self.row_coordinates - row_center).expand(batch, 1, HEIGHT, WIDTH)
        cols = (self.col_coordinates - col_center).expand(batch, 1, HEIGHT, WIDTH)
        return torch.cat((spatial, rows, cols), 1)

    @staticmethod
    def _encode(planes: Tensor, scalars: Tensor, convs: nn.Module, refiner: nn.Module, mlp: nn.Module) -> Tensor:
        x = refiner(convs(planes))
        return mlp(torch.cat((x.flatten(1), scalars), -1))

    def _router(self, spatial: Tensor) -> Tensor:
        """Style weights from the mean public behaviour of visible enemies."""
        enemies = (spatial[:, ENEMY_CHANNELS].sum(1, keepdim=True) > 0).to(spatial.dtype)
        count = enemies.sum((-2, -1)).clamp(min=1.0)
        behaviour = (spatial[:, BEHAVIOUR_CHANNELS] * enemies).sum((-2, -1)) / count
        return torch.softmax(self.behavior_router(behaviour), -1)

    @staticmethod
    def _multiplayer_gate(spatial: Tensor) -> Tensor:
        """1 when an ally is visible or more than one enemy cell is occupied."""
        allies = spatial[:, ALLY_CHANNELS].sum((1, 2, 3)) > 0
        enemy_cells = (spatial[:, ENEMY_CHANNELS].sum(1) > 0).long().sum((1, 2)) > 1
        return (allies | enemy_cells).to(spatial.dtype).unsqueeze(-1)

    @staticmethod
    def _local_features(spatial: Tensor) -> Tensor:
        """Mean spatial features at the self cell and its 4 neighbours: [N,5,117]."""
        mask = (spatial[:, SELF_CHANNELS].sum(1, keepdim=True) > 0).to(spatial.dtype)
        padded = F.pad(mask, (1, 1, 1, 1))
        windows = torch.cat(
            (
                padded[:, :, 1 : HEIGHT + 1, 1 : WIDTH + 1],  # self
                padded[:, :, 2 : HEIGHT + 2, 1 : WIDTH + 1],  # cell above self
                padded[:, :, 1 : HEIGHT + 1, 0:WIDTH],  # cell right of self
                padded[:, :, 0:HEIGHT, 1 : WIDTH + 1],  # cell below self
                padded[:, :, 1 : HEIGHT + 1, 2 : WIDTH + 2],  # cell left of self
            ),
            1,
        )  # [N,5,H,W]
        count = windows.sum((-2, -1)).clamp(min=1.0).unsqueeze(-1)
        sums = torch.matmul(windows.flatten(2), spatial.flatten(2).transpose(1, 2))  # [N,5,117]
        return sums / count

    def _direction_tables(self, scalars: Tensor) -> tuple[Tensor, Tensor, Tensor]:
        current = scalars[:, CURRENT_REACH_SCALARS].reshape(-1, 5, 5)
        bomb = scalars[:, BOMB_REACH_SCALARS].reshape(-1, 5, 5)
        refuge = scalars[:, REFUGE_SCALARS].reshape(-1, 4, 5).transpose(1, 2)
        return current, bomb, refuge

    def _consequences(self, scalars: Tensor) -> Tensor:
        """32 per-action consequence features: [N,45,32]."""
        current, bomb, refuge = self._direction_tables(scalars)
        direction = self.action_direction
        current = current[:, direction]
        bomb = bomb[:, direction]
        refuge = refuge[:, direction]
        refuge_now, refuge_bomb = refuge[..., :2], refuge[..., 2:]
        is_bomb = self.is_bomb
        selected = torch.where(is_bomb > 0.5, bomb, current)  # lerp(current, bomb, is_bomb)
        selected_refuge = torch.where(is_bomb > 0.5, refuge_bomb, refuge_now)
        facts = scalars[:, BOMB_FACT_SCALARS].unsqueeze(1) * is_bomb
        batch = scalars.shape[0]
        return torch.cat(
            (selected, selected_refuge, current, refuge_now, is_bomb.expand(batch, -1, -1), facts),
            -1,
        )

    def observe(self, spatial: Tensor, scalars: Tensor) -> Observation:
        """Everything that does not depend on recurrent memory."""
        planes = self._with_relative_coordinates(spatial)
        encoded = self._encode(planes, scalars, self.spatial_encoder, self.spatial_refiner, self.agent_encoder)
        context = self._encode(
            planes,
            scalars,
            self.multiplayer_context_spatial_encoder,
            self.multiplayer_context_spatial_refiner,
            self.multiplayer_context_agent_encoder,
        )
        gate = self._multiplayer_gate(spatial)
        direction = self.action_direction
        local = self.candidate_local_projection(self._local_features(spatial)[:, direction])
        current, bomb, refuge = self._direction_tables(scalars)
        table = torch.cat((current, bomb, refuge), -1)[:, direction]
        return Observation(
            encoded=encoded,
            router=self._router(spatial),
            multiplayer_gate=gate,
            multiplayer_context=gate * self.multiplayer_context_actor(context),
            candidate_static=(
                self.candidate_scalar_projection(scalars).unsqueeze(1),
                local,
                self.candidate_consequence_projection(table),
            ),
            consequences=self._consequences(scalars),
        )

    # ------------------------------------------------------------------ memory

    def recurrent_step(self, encoded: Tensor, memory: Tensor, reset: Tensor) -> Tensor:
        memory = torch.where(reset.unsqueeze(-1), torch.zeros_like(memory), memory)
        return self.temporal_cell(encoded, memory)

    # ------------------------------------------------------------------ heads

    def logits(self, temporal: Tensor, obs: Observation, legal: Tensor) -> Tensor:
        """Joint-action logits from the 192-dim temporal feature."""
        logits = self.actor(temporal)
        experts = torch.stack([head(temporal) for head in self.expert_residuals], -2)
        logits = logits + (obs.router.unsqueeze(-1) * experts).sum(-2)
        logits = logits + obs.multiplayer_gate * self.multiplayer_residual(temporal)
        logits = logits + obs.multiplayer_context
        logits = logits + torch.sigmoid(self.tactical_gate(temporal)) * self.tactical_residual(temporal)

        scalar_part, local_part, consequence_part = obs.candidate_static
        candidate = self.candidate_state_projection(temporal).unsqueeze(1) + scalar_part
        candidate = candidate + local_part + consequence_part + self.candidate_action_embedding
        logits = logits + self.candidate_score(F.silu(candidate)).squeeze(-1)

        direct = self.direct_candidate_state_projection(temporal).unsqueeze(1)
        direct = direct + self.direct_candidate_consequence_projection(obs.consequences)
        direct = direct + self.direct_candidate_action_embedding
        logits = logits + self.direct_candidate_score(F.silu(direct)).squeeze(-1)

        batch = temporal.shape[0]
        interaction = torch.cat(
            (
                self.interaction_candidate_state_projection(temporal).unsqueeze(1).expand(-1, ACTIONS, -1),
                self.interaction_candidate_consequence_projection(obs.consequences),
                self.interaction_candidate_action_embedding.unsqueeze(0).expand(batch, -1, -1),
            ),
            -1,
        )
        interaction = self.interaction_candidate_score(self.interaction_candidate_mlp(interaction)).squeeze(-1)
        logits = logits + interaction

        factors = (
            self.factorized_direction(temporal)[:, self.action_direction]
            + self.factorized_bomb(temporal)[:, self.action_bomb]
            + self.factorized_item(temporal)[:, self.action_item]
        )
        logits = (logits + factors).float()
        return logits.masked_fill(~legal, ILLEGAL_LOGIT)


class QQTPolicy(nn.Module):
    """Deployed policy plus a PPO value head.

    ``forward(spatial, scalars, legal, memory, reset) -> (logits, value, next_memory)``
    with spatial [B,117,13,15], scalars [B,187], legal [B,45] bool, memory
    [B,256], reset [B] bool. Illegal logits are ``ILLEGAL_LOGIT``.
    """

    def __init__(self, value_hidden: int = 128) -> None:
        super().__init__()
        self.model = UnifiedCandidateNet()
        self.value_head = nn.Sequential(nn.Linear(FEATURE, value_hidden), nn.SiLU(), nn.Linear(value_hidden, 1))
        nn.init.orthogonal_(self.value_head[2].weight, gain=0.01)
        nn.init.zeros_(self.value_head[2].bias)

    @classmethod
    def from_onnx(cls, path: str | Path, **kwargs) -> "QQTPolicy":
        policy = cls(**kwargs)
        policy.model.load_state_dict(load_onnx_state_dict(path))
        return policy

    def forward(
        self, spatial: Tensor, scalars: Tensor, legal: Tensor, memory: Tensor, reset: Tensor
    ) -> tuple[Tensor, Tensor, Tensor]:
        net = self.model
        spatial = spatial.float()
        obs = net.observe(spatial, scalars)
        next_memory = net.recurrent_step(obs.encoded, memory, reset.bool())
        temporal = obs.encoded + net.temporal_residual(next_memory)
        logits = net.logits(temporal, obs, legal.bool())
        value = self.value_head(temporal).squeeze(-1).float()
        return logits, value, next_memory.float()

    def forward_sequence(
        self,
        spatial: Tensor,
        scalars: Tensor,
        legal: Tensor,
        memory: Tensor,
        reset: Tensor,
    ) -> tuple[Tensor, Tensor, Tensor]:
        """Evaluate a rollout segment for recurrent PPO.

        Inputs are time-major: spatial [T,B,117,13,15], scalars [T,B,187],
        legal [T,B,45], reset [T,B] (memory is cleared *before* step t where
        reset[t] is true), memory [B,256] is the state before step 0.
        Returns logits [T,B,45], values [T,B] and the memory after the last
        step [B,256]. Equal to calling ``forward`` step by step; the
        convolutional encoder and all heads run once on the flattened T*B
        batch, only the GRU recurrence loops over T.
        """
        steps, batch = spatial.shape[:2]
        net = self.model
        obs = net.observe(spatial.flatten(0, 1).float(), scalars.flatten(0, 1))
        encoded = obs.encoded.view(steps, batch, FEATURE)
        reset = reset.bool()
        states = []
        for step in range(steps):
            memory = net.recurrent_step(encoded[step], memory, reset[step])
            states.append(memory)
        hidden = torch.stack(states).flatten(0, 1)
        temporal = obs.encoded + net.temporal_residual(hidden)
        logits = net.logits(temporal, obs, legal.flatten(0, 1).bool())
        values = self.value_head(temporal).squeeze(-1).float()
        return logits.view(steps, batch, ACTIONS), values.view(steps, batch), memory.float()

    def initial_memory(self, batch: int, device: torch.device | str | None = None) -> Tensor:
        return torch.zeros(batch, HIDDEN, device=device)


class DeploymentPolicy(nn.Module):
    """The exported contract: (spatial, scalars, legal, memory, reset) -> (logits, next_memory)."""

    def __init__(self, net: UnifiedCandidateNet) -> None:
        super().__init__()
        self.model = net

    def forward(self, spatial: Tensor, scalars: Tensor, legal: Tensor, memory: Tensor, reset: Tensor):
        net = self.model
        obs = net.observe(spatial, scalars)
        next_memory = net.recurrent_step(obs.encoded, memory, reset)
        temporal = obs.encoded + net.temporal_residual(next_memory)
        return net.logits(temporal, obs, legal), next_memory


# ---------------------------------------------------------------------- ONNX


def load_onnx_state_dict(path: str | Path) -> dict[str, Tensor]:
    """Read U2392-style initializers into ``UnifiedCandidateNet`` names.

    Named initializers keep their module path. Linear layers applied to the
    [batch, time, features] activations were exported as MatMul + Add with the
    transposed weight under an ``onnx::MatMul_*`` name; each one is identified
    by the named bias that the following Add consumes. The three action
    embeddings are the [1,45,d] constants added (or expanded) in the candidate
    branches and are identified by their width.
    """
    import onnx
    from onnx import numpy_helper

    graph = onnx.load(str(path)).graph
    arrays = {t.name: numpy_helper.to_array(t) for t in graph.initializer}
    consumers: dict[str, list] = {}
    for node in graph.node:
        for name in node.input:
            consumers.setdefault(name, []).append(node)

    state: dict[str, Tensor] = {}
    embeddings = {24: "candidate_action_embedding", 48: "direct_candidate_action_embedding", 96: "interaction_candidate_action_embedding"}
    for name, array in arrays.items():
        if name.startswith("model."):
            state[name.removeprefix("model.")] = torch.from_numpy(array.copy())
            continue
        if array.ndim == 3 and array.shape[:2] == (1, ACTIONS):
            state[embeddings[array.shape[2]]] = torch.from_numpy(array[0].copy())
            continue
        users = consumers.get(name, [])
        if len(users) != 1 or users[0].op_type != "MatMul":
            raise ValueError(f"cannot place unnamed initializer {name} {array.shape}")
        product = users[0].output[0]
        adds = [n for n in consumers.get(product, []) if n.op_type == "Add"]
        biases = [i for n in adds for i in n.input if i.startswith("model.") and i.endswith(".bias")]
        if len(biases) != 1:
            raise ValueError(f"cannot find the bias that follows {name}")
        state[biases[0].removeprefix("model.").removesuffix(".bias") + ".weight"] = torch.from_numpy(array.T.copy())
    return state


def parameter_count(module: nn.Module) -> int:
    return sum(p.numel() for p in module.parameters())

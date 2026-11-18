"""Everything the trainer needs from a policy, behind one small adapter.

The binding interface (training/README.md) is

    logits, value, next_memory = policy(spatial, scalars, legal, memory, reset)

R2's ``qqtrain.model`` provides the real network, the U2392 loader and the
ONNX export. Until it lands (or when a config asks for it) the trainer uses
``StandInPolicy``, a small network with the exact same interface, and
``RandomPolicy`` stands in for the frozen baseline.

Sequence forward for truncated BPTT: if the policy has a ``forward_sequence``
method it is used, else ``sequence_forward`` loops over time calling the
single-step interface. ``forward_sequence`` takes time-major tensors
``spatial [L,B,117,13,15]``, ``scalars [L,B,187]``, ``legal [L,B,45]``,
``memory [B,256]`` (state before step 0) and ``reset [L,B]`` and returns
``logits [L,B,45]``, ``value [L,B]``, ``next_memory [B,256]``.
"""

from __future__ import annotations

import importlib
import logging
from pathlib import Path
from typing import Callable

import torch
import torch.nn as nn

CHANNELS, HEIGHT, WIDTH = 117, 13, 15
SCALARS = 187
ACTIONS = 45
MEMORY = 256

log = logging.getLogger(__name__)


class StandInPolicy(nn.Module):
    """Small recurrent policy with the deployment interface plus a value head.

    It splits a non-recurrent encoder (run on the whole [L*B] batch) from the
    GRU, the same structure ``unified-candidate-v1`` has, so ``forward_sequence``
    only loops over the cheap GRU cell.
    """

    def __init__(self, conv_channels: int = 16, hidden: int = 128, memory: int = MEMORY):
        super().__init__()
        self.memory_size = memory
        self.conv = nn.Sequential(
            nn.Conv2d(CHANNELS, conv_channels, 3, padding=1),
            nn.SiLU(),
            nn.Conv2d(conv_channels, conv_channels, 3, padding=1, stride=2),
            nn.SiLU(),
        )
        flat = conv_channels * 7 * 8
        self.mlp = nn.Sequential(
            nn.Linear(flat + SCALARS, hidden), nn.LayerNorm(hidden), nn.SiLU(), nn.Linear(hidden, hidden), nn.SiLU()
        )
        self.gru = nn.GRUCell(hidden, memory)
        self.back = nn.Linear(memory, hidden)
        self.policy_head = nn.Linear(hidden, ACTIONS)
        self.value_head = nn.Linear(hidden, 1)
        nn.init.zeros_(self.policy_head.weight)
        nn.init.zeros_(self.policy_head.bias)

    def encode(self, spatial: torch.Tensor, scalars: torch.Tensor) -> torch.Tensor:
        x = self.conv(spatial.float()).flatten(1)
        return self.mlp(torch.cat([x, scalars.float()], dim=1))

    def heads(self, z: torch.Tensor, h: torch.Tensor, legal: torch.Tensor):
        feat = z + self.back(h)
        logits = self.policy_head(feat).float()
        logits = logits.masked_fill(~legal.bool(), float("-inf"))
        return logits, self.value_head(feat).squeeze(-1).float()

    def forward(self, spatial, scalars, legal, memory, reset):
        z = self.encode(spatial, scalars)
        memory = memory * (~reset.bool()).unsqueeze(-1).to(memory.dtype)
        h = self.gru(z, memory.to(z.dtype))
        logits, value = self.heads(z, h, legal)
        return logits, value, h.float()

    def forward_sequence(self, spatial, scalars, legal, memory, reset):
        L, B = reset.shape
        z = self.encode(spatial.flatten(0, 1), scalars.flatten(0, 1)).view(L, B, -1)
        h = memory
        hs = []
        keep = (~reset.bool()).unsqueeze(-1)
        for t in range(L):
            h = self.gru(z[t], (h * keep[t].to(h.dtype)).to(z.dtype))
            hs.append(h)
        hseq = torch.stack(hs)
        logits, value = self.heads(z.flatten(0, 1), hseq.flatten(0, 1), legal.flatten(0, 1))
        return logits.view(L, B, ACTIONS), value.view(L, B), h.float()


class RandomPolicy(nn.Module):
    """Uniform over legal actions; stand-in for the frozen U2392 baseline."""

    def __init__(self):
        super().__init__()
        self.register_buffer("_dummy", torch.zeros(1))

    def forward(self, spatial, scalars, legal, memory, reset):
        legal = legal.bool()
        logits = torch.zeros(legal.shape, device=legal.device).masked_fill(~legal, float("-inf"))
        return logits, torch.zeros(legal.shape[0], device=legal.device), memory


# --------------------------------------------------------------------------- factories


def _real_model_module():
    try:
        return importlib.import_module("qqtrain.model")
    except ImportError:
        return None


def _call_first(module, names: list[str], *args, **kwargs):
    for name in names:
        fn = getattr(module, name, None)
        if callable(fn):
            return fn(*args, **kwargs)
    raise AttributeError(f"{module.__name__} has none of {names}")


def make_policy(cfg: dict) -> nn.Module:
    """Build a fresh learner network according to ``cfg['model']``."""
    mcfg = cfg["model"]
    factory = mcfg.get("factory", "auto")
    kwargs = dict(mcfg.get("kwargs", {}))
    if factory in ("auto", "qqtrain.model"):
        mod = _real_model_module()
        if mod is not None:
            return _call_first(mod, ["QQTPolicy", "build_policy", "make_policy"], **kwargs)
        if factory == "qqtrain.model":
            raise ImportError("qqtrain.model is not available")
        log.warning("qqtrain.model not found; using the stand-in policy")
    return StandInPolicy(**kwargs)


def policy_factory(cfg: dict) -> Callable[[], nn.Module]:
    return lambda: make_policy(cfg)


def load_baseline(cfg: dict, factory: str | None = None) -> nn.Module:
    """The frozen U2392 policy (R2's ONNX loader); RandomPolicy as stand-in."""
    factory = factory or cfg["model"].get("factory", "auto")
    path = cfg["baseline"].get("onnx", "")
    if factory != "standin":
        mod = _real_model_module()
        if mod is not None and path and Path(path).exists():
            kwargs = dict(cfg["baseline"].get("kwargs", {}))
            cls = getattr(mod, "QQTPolicy", None)
            if cls is not None and hasattr(cls, "from_onnx"):
                return cls.from_onnx(path, **kwargs)
            return _call_first(mod, ["load_u2392", "load_baseline", "from_onnx"], path, **kwargs)
        if factory == "qqtrain.model":
            raise FileNotFoundError(f"cannot load the baseline from {path!r}")
    log.warning("baseline U2392 unavailable (model module or ONNX missing); using a random policy")
    return RandomPolicy()


def export_onnx(policy: nn.Module, path: str | Path) -> bool:
    """Export through R2's ``qqtrain.export.export_onnx``; False when unavailable."""
    try:
        mod = importlib.import_module("qqtrain.export")
    except ImportError:
        log.warning("qqtrain.export not available; skipping ONNX export of %s", path)
        return False
    if isinstance(policy, StandInPolicy):
        log.warning("stand-in policy is not exportable to the deployment contract; skipping")
        return False
    fn = getattr(mod, "export_onnx")
    fn(policy, str(path))
    return True


# --------------------------------------------------------------------------- forward helpers


def sequence_forward(policy: nn.Module, spatial, scalars, legal, memory, reset):
    """Time-major replay used by PPO (see module docstring)."""
    fn = getattr(policy, "forward_sequence", None)
    if fn is None:
        inner = getattr(policy, "_orig_mod", None)  # torch.compile wrapper
        fn = getattr(inner, "forward_sequence", None) if inner is not None else None
    if fn is not None:
        return fn(spatial, scalars, legal, memory, reset)
    logits, values = [], []
    h = memory
    for t in range(reset.shape[0]):
        lg, v, h = policy(spatial[t], scalars[t], legal[t], h, reset[t])
        logits.append(lg)
        values.append(v)
    return torch.stack(logits), torch.stack(values), h


def masked_log_probs(logits: torch.Tensor, legal: torch.Tensor) -> torch.Tensor:
    """log-softmax over legal actions; illegal entries are -inf."""
    logits = logits.float().masked_fill(~legal.bool(), float("-inf"))
    return torch.log_softmax(logits, dim=-1)


def masked_entropy(logp: torch.Tensor, legal: torch.Tensor) -> torch.Tensor:
    # masked_fill before the product: 0 * -inf would poison the gradient with NaN.
    safe = logp.masked_fill(~legal.bool(), 0.0)
    return -(safe.exp() * safe).masked_fill(~legal.bool(), 0.0).sum(-1)


def sample_actions(
    logits: torch.Tensor,
    legal: torch.Tensor,
    min_p: float = 0.0,
    temperature: float = 1.0,
    generator: torch.Generator | None = None,
    greedy: bool = False,
):
    """Sample from the masked softmax.

    Training (learner) uses ``min_p=0, temperature=1``: the full masked softmax,
    so the recorded log-probability is exactly the behaviour distribution PPO
    needs. The deployed server decodes with MinP 0.1 / temperature 1.0 (see
    configs/models/*.onnx.json); opponents and evaluation use that decoder.
    Returns (actions int64 [B], log-probabilities of the sampled actions under
    the *undecoded* full masked softmax [B]).
    """
    legal = legal.bool()
    logp_full = masked_log_probs(logits, legal)
    if greedy:
        actions = logp_full.argmax(-1)
    else:
        logp = logp_full
        if temperature != 1.0:
            logp = torch.log_softmax(logits.float().masked_fill(~legal, float("-inf")) / temperature, dim=-1)
        if min_p > 0.0:
            p = logp.exp()
            keep = p >= min_p * p.max(dim=-1, keepdim=True).values
            logp = logp.masked_fill(~keep, float("-inf"))
        probs = torch.softmax(logp, dim=-1)
        actions = torch.multinomial(probs, 1, generator=generator).squeeze(-1)
    return actions, logp_full.gather(-1, actions.unsqueeze(-1)).squeeze(-1)


# Scalar layout used by the server's refuge filter (internal/game/battleai/policy.go).
_BOMB_PROJECTION_VALID = 95
_CURRENT_REFUGE = 162
_BOMB_REFUGE = 172


def refuge_mask(scalars: torch.Tensor) -> torch.Tensor:
    """Per action, whether its engine projection keeps a whole-cell refuge.

    Mirrors ``battleai.refugeActions``: actions 0-4 (stay/move) use
    scalars[162:167], 5-9 (bubble + stay/move) use scalars[172:177] and need
    scalars[95] (bubble projection valid); item actions are always open.
    """
    s = scalars.float()
    safe = torch.ones(s.shape[0], ACTIONS, dtype=torch.bool, device=s.device)
    safe[:, 0:5] = s[:, _CURRENT_REFUGE : _CURRENT_REFUGE + 5] > 0.5
    safe[:, 5:10] = (s[:, _BOMB_REFUGE : _BOMB_REFUGE + 5] > 0.5) & (s[:, _BOMB_PROJECTION_VALID : _BOMB_PROJECTION_VALID + 1] > 0.5)
    return safe


def deployed_sample(
    logits: torch.Tensor,
    legal: torch.Tensor,
    scalars: torch.Tensor,
    min_p: float = 0.1,
    temperature: float = 1.0,
    generator: torch.Generator | None = None,
) -> torch.Tensor:
    """The server's decoder (``battleai.SampledCandidatePolicy``).

    The best legal action is always eligible; any other legal action is
    eligible when ``exp((logit - best) / T) >= min_p`` and it keeps a refuge
    (``refuge_mask``); the choice is sampled proportionally to those ratios.
    Used for frozen opponents (U2392 as deployed) and in evaluation.
    """
    legal = legal.bool()
    s = logits.float().masked_fill(~legal, float("-inf"))
    best = s.argmax(-1, keepdim=True)
    t = temperature if temperature > 0 else 1.0
    ratio = torch.exp((s - s.gather(-1, best)) / t)
    keep = legal & (ratio >= min_p) & refuge_mask(scalars)
    keep.scatter_(-1, best, True)
    weights = torch.where(keep, ratio, torch.zeros_like(ratio))
    return torch.multinomial(weights, 1, generator=generator).squeeze(-1)

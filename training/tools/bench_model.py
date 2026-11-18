"""Forward+backward throughput of QQTPolicy (one PPO-like update step).

    training/.venv/bin/python training/tools/bench_model.py --device cpu mps --batch 1024 4096

Each measured step runs forward_sequence on a [T, B/T] segment (recurrent
PPO layout), a clipped-surrogate-like loss on logits and values, backward and
an Adam step. Reported numbers are samples (T*B/T = B) per second.
"""

from __future__ import annotations

import argparse
import time

import torch

from qqtrain.model import QQTPolicy
from qqtrain.synthetic import random_inputs


def bench(device: str, batch: int, steps: int, seq: int, dtype: torch.dtype | None, compile_model: bool) -> float:
    torch.manual_seed(0)
    policy = QQTPolicy().to(device)
    optimizer = torch.optim.Adam(policy.parameters(), lr=1e-4)
    forward = torch.compile(policy.forward_sequence) if compile_model else policy.forward_sequence
    inputs = random_inputs(batch, seed=0)
    lanes = batch // seq

    def tensor(name: str) -> torch.Tensor:
        value = torch.from_numpy(inputs[name])
        return value.view(seq, lanes, *value.shape[1:]).to(device)

    spatial, scalars, legal, reset = tensor("spatial"), tensor("scalars"), tensor("legal"), tensor("reset")
    memory = torch.from_numpy(inputs["memory"][:lanes]).to(device)
    actions = torch.zeros(seq, lanes, dtype=torch.long, device=device)
    advantages = torch.randn(seq, lanes, device=device)

    def step() -> None:
        with torch.autocast(device_type=device.split(":")[0], dtype=dtype, enabled=dtype is not None):
            logits, values, _ = forward(spatial, scalars, legal, memory, reset)
        log_probs = torch.log_softmax(logits.float(), -1)
        chosen = log_probs.gather(-1, actions.unsqueeze(-1)).squeeze(-1)
        entropy = -(log_probs.exp() * log_probs).sum(-1)
        loss = -(chosen * advantages).mean() + 0.5 * (values.float() - advantages).pow(2).mean() - 0.01 * entropy.mean()
        optimizer.zero_grad(set_to_none=True)
        loss.backward()
        optimizer.step()

    def synchronize() -> None:
        if device.startswith("cuda"):
            torch.cuda.synchronize()
        elif device == "mps":
            torch.mps.synchronize()

    for _ in range(3):
        step()
    synchronize()
    start = time.perf_counter()
    for _ in range(steps):
        step()
    synchronize()
    return batch * steps / (time.perf_counter() - start)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--device", nargs="+", default=["cpu"])
    parser.add_argument("--batch", nargs="+", type=int, default=[1024, 4096])
    parser.add_argument("--seq", type=int, default=16, help="sequence length T of each segment")
    parser.add_argument("--steps", type=int, default=5)
    parser.add_argument("--bf16", action="store_true", help="autocast to bfloat16")
    parser.add_argument("--compile", action="store_true", help="torch.compile forward_sequence")
    options = parser.parse_args()
    for device in options.device:
        for batch in options.batch:
            rate = bench(device, batch, options.steps, options.seq, torch.bfloat16 if options.bf16 else None, options.compile)
            print(f"{device:5s} batch {batch:5d} (T={options.seq}): {rate:9.0f} samples/s fwd+bwd+adam", flush=True)


if __name__ == "__main__":
    main()

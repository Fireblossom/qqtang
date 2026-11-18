"""Export the deployment ONNX graph (contract v2) and its metadata sidecar."""

from __future__ import annotations

import copy
import hashlib
import json
import warnings
from pathlib import Path
from typing import Any

import numpy as np
import torch

from .model import (
    ACTIONS,
    CHANNELS,
    HEIGHT,
    HIDDEN,
    SCALARS,
    WIDTH,
    DeploymentPolicy,
    QQTPolicy,
    UnifiedCandidateNet,
)
from .synthetic import random_inputs

OPSET = 18
INPUT_NAMES = ["spatial", "scalars", "legal", "memory", "reset"]
OUTPUT_NAMES = ["logits", "next_memory"]

DEFAULT_METADATA: dict[str, Any] = {
    "format": "qqtang-decentralized-actor-onnx",
    "contract_version": 2,
    "model_architecture_version": 2,
    "model_variant": "unified-candidate-v1",
    "tensor_version": 18,
    "channels": CHANNELS,
    "scalars": SCALARS,
    "actions": ACTIONS,
    "height": HEIGHT,
    "width": WIDTH,
    "batch_dynamic": True,
    "recurrent_hidden_size": HIDDEN,
    "recurrent_reset": "match_or_episode_start",
    # U2392's live decoding; a new model may need a different min_p.
    "sampling": {"enabled": True, "min_p": 0.1, "temperature": 1.0},
}
# Key order of configs/models/qqtang-rule1.onnx.json.
METADATA_ORDER = [
    "format",
    "contract_version",
    "model_architecture_version",
    "model_variant",
    "tensor_version",
    "channels",
    "scalars",
    "actions",
    "height",
    "width",
    "batch_dynamic",
    "recurrent_hidden_size",
    "recurrent_reset",
    "onnx_sha256",
    "maximum_parity_error",
    "mean_parity_error",
    "sampling",
]


def _network(policy: QQTPolicy | UnifiedCandidateNet) -> UnifiedCandidateNet:
    return policy.model if isinstance(policy, QQTPolicy) else policy


def sha256(path: str | Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def parity_errors(
    net: UnifiedCandidateNet, onnx_path: str | Path, batch: int = 256, seed: int = 2392
) -> tuple[float, float]:
    """Max / mean absolute difference between PyTorch and ONNX Runtime (CPU)
    over the legal logits of synthetic inputs. Masked logits must match
    exactly and next_memory within 1e-4."""
    import onnxruntime as ort

    session = ort.InferenceSession(str(onnx_path), providers=["CPUExecutionProvider"])
    inputs = random_inputs(batch, seed=seed)
    onnx_logits, onnx_memory = session.run(OUTPUT_NAMES, inputs)
    reference = DeploymentPolicy(copy.deepcopy(net).cpu().float()).eval()
    with torch.no_grad():
        logits, memory = reference(*(torch.from_numpy(inputs[name]) for name in INPUT_NAMES))
    legal = inputs["legal"]
    if not np.array_equal(logits.numpy()[~legal], onnx_logits[~legal]):
        raise AssertionError("illegal-action masking differs between PyTorch and ONNX")
    memory_error = float(np.abs(memory.numpy() - onnx_memory).max())
    if memory_error > 1e-4:
        raise AssertionError(f"next_memory differs between PyTorch and ONNX by {memory_error}")
    errors = np.abs(logits.numpy()[legal] - onnx_logits[legal])
    return float(errors.max()), float(errors.mean())


def export_onnx(
    policy: QQTPolicy | UnifiedCandidateNet,
    path: str | Path,
    metadata: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Write the deployment graph to ``path`` and its metadata to ``path + '.json'``.

    The graph has U2392's interface: inputs spatial f32 [batch,117,13,15],
    scalars f32 [batch,187], legal bool [batch,45], memory f32 [batch,256],
    reset bool [batch]; outputs logits f32 [batch,45] and next_memory f32
    [batch,256]; dynamic batch, opset 18, no value output. ``metadata``
    overrides fields of the sidecar (for example ``sampling``). Returns the
    metadata that was written.
    """
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    net = copy.deepcopy(_network(policy)).cpu().float().eval()
    deploy = DeploymentPolicy(net).eval()
    example = random_inputs(2, seed=0)
    args = tuple(torch.from_numpy(example[name]) for name in INPUT_NAMES)
    # U2392 was written by the TorchScript exporter (torch 2.11); the same
    # exporter keeps module names on initializers and needs no onnxscript.
    with torch.no_grad(), warnings.catch_warnings():
        warnings.simplefilter("ignore", category=UserWarning)
        warnings.simplefilter("ignore", category=DeprecationWarning)
        torch.onnx.export(
            deploy,
            args,
            str(path),
            input_names=INPUT_NAMES,
            output_names=OUTPUT_NAMES,
            dynamic_axes={name: {0: "batch"} for name in INPUT_NAMES + OUTPUT_NAMES},
            opset_version=OPSET,
            do_constant_folding=True,
            dynamo=False,
        )
    maximum, mean = parity_errors(net, path)
    values = {**DEFAULT_METADATA, **(metadata or {})}
    values["onnx_sha256"] = sha256(path)
    values["maximum_parity_error"] = maximum
    values["mean_parity_error"] = mean
    ordered = {key: values[key] for key in METADATA_ORDER if key in values}
    ordered.update({key: value for key, value in values.items() if key not in ordered})
    with open(f"{path}.json", "w", encoding="utf-8") as handle:
        json.dump(ordered, handle, indent=2)
        handle.write("\n")
    return ordered


def main() -> None:
    import argparse

    parser = argparse.ArgumentParser(description="Export a qqtrain checkpoint or ONNX model to the deployment contract")
    parser.add_argument("source", help="a .onnx model (weights are reloaded) or a .pt state_dict of QQTPolicy")
    parser.add_argument("output", help="output .onnx path; metadata goes to OUTPUT.json")
    parser.add_argument("--min-p", type=float, default=None, help="sampling min_p for the sidecar")
    options = parser.parse_args()
    if options.source.endswith(".onnx"):
        policy = QQTPolicy.from_onnx(options.source)
    else:
        policy = QQTPolicy()
        state = torch.load(options.source, map_location="cpu")
        policy.load_state_dict(state.get("policy", state))
    metadata = None
    if options.min_p is not None:
        metadata = {"sampling": {"enabled": True, "min_p": options.min_p, "temperature": 1.0}}
    print(json.dumps(export_onnx(policy, options.output, metadata), indent=2))


if __name__ == "__main__":
    main()

"""Policy network: U2392 parity, value head, sequence evaluation, ONNX export."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from pathlib import Path

import numpy as np
import onnx
import onnxruntime as ort
import pytest
import torch

from qqtrain.dump import load_dump
from qqtrain.export import INPUT_NAMES, export_onnx, sha256
from qqtrain.model import ACTIONS, HIDDEN, ILLEGAL_LOGIT, QQTPolicy, UnifiedCandidateNet, parameter_count
from qqtrain.synthetic import random_inputs

REPO = Path(__file__).resolve().parents[2]
U2392 = REPO / "configs/models/qqtang-rule1.onnx"
REAL_OBSERVATIONS = Path(__file__).parent / "data/real_observations.npz"
ORT_LIBRARY = Path(
    os.environ.get(
        "QQTANG_ONNXRUNTIME_LIB",
        Path.home() / "Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/onnxruntime/darwin-arm64/libonnxruntime.1.29.0.dylib",
    )
)
TOLERANCE = 2e-5  # U2392 metadata: maximum_parity_error 1.0e-5 (PyTorch vs ORT)


@pytest.fixture(scope="module")
def policy() -> QQTPolicy:
    return QQTPolicy.from_onnx(U2392).eval()


@pytest.fixture(scope="module")
def u2392_session() -> ort.InferenceSession:
    return ort.InferenceSession(str(U2392), providers=["CPUExecutionProvider"])


def run_torch(policy: QQTPolicy, inputs: dict[str, np.ndarray]):
    with torch.no_grad():
        logits, value, memory = policy(*(torch.from_numpy(inputs[name]) for name in INPUT_NAMES))
    return logits.numpy(), value.numpy(), memory.numpy()


def assert_logits_close(ours: np.ndarray, reference: np.ndarray, legal: np.ndarray, tolerance: float = TOLERANCE):
    assert np.array_equal(ours[~legal], reference[~legal])
    assert np.abs(ours[legal] - reference[legal]).max() < tolerance


def test_parameters_match_u2392():
    net = UnifiedCandidateNet()
    assert parameter_count(net) == 3_241_805
    graph = onnx.load(str(U2392)).graph
    assert len(graph.initializer) == 137
    from qqtrain.model import load_onnx_state_dict

    state = load_onnx_state_dict(U2392)
    assert set(state) == set(net.state_dict())
    for name, tensor in net.state_dict().items():
        assert state[name].shape == tensor.shape, name


@pytest.mark.parametrize("batch", [1, 5, 300])
def test_parity_random_inputs(policy, u2392_session, batch):
    inputs = random_inputs(batch, seed=batch)
    reference_logits, reference_memory = u2392_session.run(None, inputs)
    logits, _, memory = run_torch(policy, inputs)
    assert_logits_close(logits, reference_logits, inputs["legal"])
    assert np.abs(memory - reference_memory).max() < TOLERANCE
    assert (logits[~inputs["legal"]] == ILLEGAL_LOGIT).all()


def test_parity_real_observations(policy, u2392_session):
    """Observations recorded from the server inference path on real maps
    (training/tools/dump_observations), with Go ONNX Runtime outputs."""
    data = load_dump(REAL_OBSERVATIONS)
    inputs = {name: data[name] for name in INPUT_NAMES}
    logits, _, memory = run_torch(policy, inputs)
    assert_logits_close(logits, data["logits"], data["legal"])
    assert np.abs(memory - data["next_memory"]).max() < TOLERANCE
    reference_logits, _ = u2392_session.run(None, inputs)
    assert_logits_close(logits, reference_logits, data["legal"])


def test_value_head_shapes_and_gradients(policy):
    inputs = random_inputs(6, seed=3)
    model = QQTPolicy()
    logits, value, memory = model(*(torch.from_numpy(inputs[name]) for name in INPUT_NAMES))
    assert logits.shape == (6, ACTIONS) and logits.dtype == torch.float32
    assert value.shape == (6,) and memory.shape == (6, HIDDEN)
    (torch.log_softmax(logits, -1).exp() * torch.log_softmax(logits, -1)).sum().backward(retain_graph=True)
    value.sum().backward()
    assert model.value_head[0].weight.grad is not None
    assert torch.isfinite(model.model.agent_encoder[0].weight.grad).all()
    # The value head is not part of the deployed parameters.
    assert not any(name.startswith("value_head") for name in policy.model.state_dict())


def test_reset_clears_memory(policy):
    inputs = random_inputs(4, seed=11)
    inputs["reset"][:] = True
    first = run_torch(policy, inputs)
    inputs["memory"] = np.random.default_rng(0).normal(size=inputs["memory"].shape).astype(np.float32)
    second = run_torch(policy, inputs)
    np.testing.assert_array_equal(first[0], second[0])
    np.testing.assert_array_equal(first[2], second[2])


def test_sequence_matches_step_by_step():
    torch.manual_seed(0)
    model = QQTPolicy().eval()
    steps, batch = 7, 5
    frames = [random_inputs(batch, seed=100 + t) for t in range(steps)]
    spatial = torch.stack([torch.from_numpy(f["spatial"]) for f in frames])
    scalars = torch.stack([torch.from_numpy(f["scalars"]) for f in frames])
    legal = torch.stack([torch.from_numpy(f["legal"]) for f in frames])
    reset = torch.stack([torch.from_numpy(f["reset"]) for f in frames])
    memory = torch.from_numpy(frames[0]["memory"])
    with torch.no_grad():
        seq_logits, seq_values, seq_memory = model.forward_sequence(spatial, scalars, legal, memory, reset)
        state = memory
        for t in range(steps):
            logits, value, state = model(spatial[t], scalars[t], legal[t], state, reset[t])
            torch.testing.assert_close(seq_logits[t], logits, rtol=0, atol=1e-5)
            torch.testing.assert_close(seq_values[t], value, rtol=0, atol=1e-6)
    torch.testing.assert_close(seq_memory, state, rtol=0, atol=1e-6)
    assert seq_logits.shape == (steps, batch, ACTIONS) and seq_values.shape == (steps, batch)


def test_sequence_matches_real_trajectory(policy):
    """Chain memory through one real recorded stream and compare with Go ORT."""
    data = load_dump(REAL_OBSERVATIONS)
    rows = np.nonzero(data["stream"] == data["stream"][0])[0]
    assert data["reset"][rows[0]]
    take = lambda name: torch.from_numpy(data[name][rows]).unsqueeze(1)  # noqa: E731
    with torch.no_grad():
        logits, _, last = policy.forward_sequence(
            take("spatial"), take("scalars"), take("legal"), torch.from_numpy(data["memory"][rows[:1]]), take("reset")
        )
    assert_logits_close(logits[:, 0].numpy(), data["logits"][rows], data["legal"][rows])
    assert np.abs(last.numpy()[0] - data["next_memory"][rows[-1]]).max() < TOLERANCE


@pytest.fixture(scope="module")
def reexported(policy, tmp_path_factory) -> Path:
    path = tmp_path_factory.mktemp("export") / "u2392.onnx"
    export_onnx(policy, path)
    return path


def test_export_contract_and_metadata(reexported):
    model = onnx.load(str(reexported))
    onnx.checker.check_model(model)
    assert [(o.domain, o.version) for o in model.opset_import] == [("", 18)]
    reference = onnx.load(str(U2392))

    def signature(values):
        return [
            (v.name, v.type.tensor_type.elem_type, [d.dim_param or d.dim_value for d in v.type.tensor_type.shape.dim])
            for v in values
        ]

    assert signature(model.graph.input) == signature(reference.graph.input)
    assert signature(model.graph.output) == signature(reference.graph.output)
    metadata = json.loads(Path(f"{reexported}.json").read_text())
    expected = json.loads(Path(f"{U2392}.json").read_text())
    assert list(metadata) == list(expected)
    for key in expected:
        if key not in ("onnx_sha256", "maximum_parity_error", "mean_parity_error"):
            assert metadata[key] == expected[key], key
    assert metadata["onnx_sha256"] == sha256(reexported)
    assert 0 < metadata["maximum_parity_error"] < TOLERANCE


def test_reexport_reproduces_u2392(reexported, u2392_session):
    session = ort.InferenceSession(str(reexported), providers=["CPUExecutionProvider"])
    data = load_dump(REAL_OBSERVATIONS)
    for inputs in (random_inputs(257, seed=9), {name: data[name] for name in INPUT_NAMES}):
        logits, memory = session.run(None, inputs)
        reference_logits, reference_memory = u2392_session.run(None, inputs)
        assert_logits_close(logits, reference_logits, inputs["legal"])
        assert np.abs(memory - reference_memory).max() < TOLERANCE


def test_export_of_trained_weights_round_trips(tmp_path):
    torch.manual_seed(1)
    model = QQTPolicy()
    with torch.no_grad():
        for parameter in model.model.parameters():
            parameter.add_(0.01 * torch.randn_like(parameter))
    path = tmp_path / "fresh.onnx"
    metadata = export_onnx(model, path, {"sampling": {"enabled": False, "min_p": 0.1, "temperature": 1.0}})
    assert metadata["sampling"]["enabled"] is False
    assert QQTPolicy.from_onnx(path).model.state_dict().keys() == model.model.state_dict().keys()
    reloaded = QQTPolicy.from_onnx(path).eval()
    inputs = random_inputs(9, seed=5)
    expected = run_torch(model.eval(), inputs)
    actual = run_torch(reloaded, inputs)
    np.testing.assert_array_equal(expected[0], actual[0])
    np.testing.assert_array_equal(expected[2], actual[2])


def _go() -> str | None:
    candidates = [os.environ.get("QQTANG_GO"), shutil.which("go"), str(Path.home() / "sdk/go1.26.10/bin/go")]
    return next((c for c in candidates if c and Path(c).exists()), None)


@pytest.mark.skipif(_go() is None or not ORT_LIBRARY.exists(), reason="Go toolchain or ONNX Runtime library not found")
def test_go_server_loader_accepts_export(reexported):
    go = _go()
    environment = {
        **os.environ,
        "PATH": f"{Path(go).parent}:{os.environ.get('PATH', '')}",
        "GOTOOLCHAIN": "local",
        "CGO_ENABLED": "1",
    }
    result = subprocess.run(
        [str(REPO / "training/tools/check-onnx-go.sh"), str(reexported), f"{reexported}.json", str(ORT_LIBRARY)],
        cwd=REPO,
        env=environment,
        capture_output=True,
        text=True,
        timeout=600,
    )
    assert result.returncode == 0, result.stdout + result.stderr
    assert "--- PASS: TestONNXRuntimeRunnerLoadsContractAndRuns" in result.stdout

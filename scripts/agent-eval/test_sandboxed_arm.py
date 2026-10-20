"""sandboxed_arm.py: import smoke + argv shape, against modelbench's sandbox.py by path.

Run: python3 -m pytest scripts/agent-eval/test_sandboxed_arm.py
Skips when modelbench is not checked out (the SANDBOX.md import contract).
"""
import importlib.util
import json
import os
import sys

import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
REVIEW = os.environ.get("MODELBENCH_REVIEW") or os.path.expanduser("~/personal_projects/modelbench/main/bench/review")
pytestmark = pytest.mark.skipif(not os.path.isfile(os.path.join(REVIEW, "sandbox.py")), reason="needs modelbench sandbox.py")


@pytest.fixture(scope="module")
def arm():
    spec = importlib.util.spec_from_file_location("sandboxed_arm", os.path.join(HERE, "sandboxed_arm.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod, mod.load_sandbox()


def test_sandbox_contract_names_exist(arm):
    _, S = arm
    for name in ("sandbox", "claude_binary", "scrub", "sandbox_env", "CLAUDE_IN", "CONFIG_DIR", "DISALLOWED_TOOLS"):
        assert hasattr(S, name), f"modelbench sandbox.py no longer exports {name}"


def test_without_arm_has_no_runecho_and_no_web_tools(arm):
    mod, S = arm
    assert mod.mcp_config(False, None, None) == {"mcpServers": {}}
    cmd = mod.build_cmd(S, "Q?", "sonnet", "high", 4, False, None, None)
    assert cmd[0] == S.CLAUDE_IN and "--strict-mcp-config" in cmd
    assert cmd[cmd.index("--mcp-config") + 1] == f"{S.CONFIG_DIR}/mcp.json"
    assert "WebSearch,WebFetch" in cmd[cmd.index("--disallowedTools") + 1]
    assert "env" not in cmd[:1], "the without arm must not get RUNECHO_HOME or runecho on PATH"


def test_with_arm_scopes_runecho_to_the_registry(arm):
    mod, S = arm
    cfg = mod.mcp_config(True, "/b/runecho-mcp", "/h/reg")
    assert cfg["mcpServers"]["runecho"] == {"command": "/b/runecho-mcp", "args": [], "env": {"RUNECHO_HOME": "/h/reg"}}
    cmd = mod.build_cmd(S, "Q?", "sonnet", "high", 4, True, "/h/reg", "/b")
    assert cmd[:3] == ["env", "RUNECHO_HOME=/h/reg", f"PATH=/b:{S.sandbox_env()['PATH']}"]
    assert S.CLAUDE_IN in cmd


def test_refuses_to_run_without_the_benchmark_token(arm, monkeypatch, tmp_path):
    mod, _ = arm
    monkeypatch.delenv("CLAUDE_CODE_OAUTH_TOKEN", raising=False)
    with pytest.raises(SystemExit) as e:
        mod.main(["--repo", str(tmp_path), "--label", "x", "--question", "q", "--out", str(tmp_path / "o")])
    assert "CLAUDE_CODE_OAUTH_TOKEN" in str(e.value)


def test_run_all_headless_arms_no_longer_call_claude_on_the_host():
    src = open(os.path.join(HERE, "run-all.sh")).read()
    head = src[src.index("headless() {"):src.index('if [ "$MODE" = headless ]')]
    assert "claude -p" not in head and "sandboxed_arm.py" in head

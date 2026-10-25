"""ShopFlow release readiness report and check lifecycle API."""

import json
import os
import re
import tempfile
import threading
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.responses import Response
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Histogram, generate_latest
from pydantic import BaseModel, ConfigDict

app = FastAPI(title="ShopFlow release readiness reporting")
MODEL = "qwen3:4b-instruct"
RESULTS_DIR = Path(os.getenv("RESULTS_DIR", "/results"))
SKILL_PATH = Path(os.getenv("SKILL_PATH", "/SKILL.md"))
EXPECTED_TOOLS = {
    "get_release": ("release-mcp", "release"),
    "get_checks": ("release-mcp", "checks"),
    "get_environment": ("ops-mcp", "environment"),
    "get_runbook": ("ops-mcp", "runbook"),
}
SAFE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$")

CHECKS = Counter("release_checks_total", "Release checks completed", ["status"])
DURATION = Histogram(
    "release_check_duration_seconds", "Release check wall time in seconds",
    buckets=(0.5, 1, 2, 5, 10, 20, 30, 60, 120),
)
REPORTS = Counter("release_reports_total", "Reports written", ["decision"])
for status in ("success", "error"):
    CHECKS.labels(status)
for decision in ("approved", "blocked"):
    REPORTS.labels(decision)


@dataclass
class CheckState:
    release_id: str
    environment: str
    started: float
    status: str | None = None
    response: dict | None = None


_check_lock = threading.Lock()
_checks: dict[str, CheckState] = {}


class StartRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    release_id: str
    environment: str
    execution_id: str


class ReportRequest(StartRequest):
    summary: str
    evidence: dict
    tool_calls: list[dict]
    model: str
    decision: str
    reasons: list[str]


class FailRequest(StartRequest):
    error: str


def safe_key(value: str) -> str:
    if not SAFE_ID.fullmatch(value):
        raise HTTPException(status_code=422, detail="Invalid identifier")
    return value


def validate_evidence(release_id: str, environment: str, evidence: dict) -> tuple[bool, list[str]]:
    if set(evidence) != {"release", "checks", "environment", "runbook"}:
        raise ValueError("Four evidence records are required")
    release, checks, env, runbook = (evidence[key] for key in
                                     ("release", "checks", "environment", "runbook"))
    if any(not isinstance(item, dict) for item in (release, checks, env, runbook)):
        raise ValueError("Evidence records must be objects")
    if (release.get("release_id") != release_id or checks.get("release_id") != release_id
            or env.get("environment") != environment or runbook.get("service") != release.get("service")):
        raise ValueError("Evidence does not match the requested release and environment")
    if not isinstance(release.get("service"), str) or not release["service"]:
        raise ValueError("Release service is required for runbook scope")
    all_checks = checks.get("checks")
    if not isinstance(all_checks, list) or any(not isinstance(item, dict) for item in all_checks):
        raise ValueError("Checks must be a list of objects")
    required = [check for check in all_checks if check.get("required") is True]
    if not required:
        raise ValueError("No required checks in evidence")
    reasons = []
    for check in required:
        if check.get("status") != "passed":
            reasons.append(f"Required check {check.get('name', 'unknown')} did not pass")
    for field in ("image_signed", "digest_pinned", "rollback_ready"):
        if release.get(field) is not True:
            reasons.append(f"Release {field} is not true")
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", str(release.get("image_digest", ""))):
        reasons.append("Image digest is invalid")
    if env.get("status") != "healthy" or env.get("shop_api_healthy") is not True:
        reasons.append("Staging or shop-api is unhealthy")
    capacity = env.get("free_capacity_percent")
    if not isinstance(capacity, (int, float)) or isinstance(capacity, bool) or capacity < 30:
        reasons.append("Staging free capacity is below 30%")
    if runbook.get("rollback_supported") is not True or not runbook.get("rollback_steps"):
        reasons.append("Rollback runbook is unavailable")
    return not reasons, reasons


def validate_tool_calls(report: ReportRequest) -> None:
    if len(report.tool_calls) != len(EXPECTED_TOOLS):
        raise HTTPException(status_code=422, detail="Report must include exactly four MCP tool calls")
    seen: set[str] = set()
    for call in report.tool_calls:
        if not isinstance(call, dict) or set(call) != {"server", "tool", "arguments", "result"}:
            raise HTTPException(status_code=422, detail="Invalid MCP tool call record")
        name = call["tool"]
        if name not in EXPECTED_TOOLS or name in seen:
            raise HTTPException(status_code=422, detail="Duplicate or unknown MCP tool call")
        seen.add(name)
        server, evidence_key = EXPECTED_TOOLS[name]
        expected_arguments = {
            "get_release": {"release_id": report.release_id},
            "get_checks": {"release_id": report.release_id},
            "get_environment": {"environment": report.environment},
            "get_runbook": {"service": report.evidence["release"].get("service")},
        }[name]
        if (call["server"] != server or call["arguments"] != expected_arguments
                or call["result"] != report.evidence[evidence_key]):
            raise HTTPException(status_code=422, detail=f"MCP call does not match evidence: {name}")
    if seen != set(EXPECTED_TOOLS):
        raise HTTPException(status_code=422, detail="Report must include all four MCP tool calls")


def atomic_write(path: Path, body: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as temp:
        temp.write(body)
        temp.flush()
        os.fsync(temp.fileno())
        temporary = Path(temp.name)
    os.replace(temporary, path)


def matching_state(request: StartRequest) -> CheckState:
    state = _checks.get(request.execution_id)
    if state is None:
        raise HTTPException(status_code=404, detail="Check execution was not started")
    if (state.release_id, state.environment) != (request.release_id, request.environment):
        raise HTTPException(status_code=409, detail="Execution ID belongs to another check")
    return state


@app.get("/health")
def health() -> dict:
    return {"status": "ok"}


@app.get("/metrics")
def metrics() -> Response:
    return Response(generate_latest(), media_type=CONTENT_TYPE_LATEST)


@app.post("/checks/start")
def start_check(request: StartRequest) -> dict:
    safe_key(request.release_id)
    safe_key(request.environment)
    safe_key(request.execution_id)
    try:
        skill = SKILL_PATH.read_text(encoding="utf-8")
    except OSError as exc:
        raise HTTPException(status_code=500, detail=f"Skill read failed: {exc}") from exc
    with _check_lock:
        state = _checks.get(request.execution_id)
        if state is None:
            _checks[request.execution_id] = CheckState(
                request.release_id, request.environment, time.monotonic()
            )
        elif (state.release_id, state.environment) != (request.release_id, request.environment):
            raise HTTPException(status_code=409, detail="Execution ID belongs to another check")
    return {**request.model_dump(), "skill": skill, "model": MODEL}


@app.post("/reports")
def reports(report: ReportRequest) -> dict:
    release_id = safe_key(report.release_id)
    safe_key(report.environment)
    safe_key(report.execution_id)
    if report.model != MODEL:
        raise HTTPException(status_code=422, detail=f"Model must be {MODEL}")
    if not report.summary.strip():
        raise HTTPException(status_code=422, detail="Summary is required")
    if report.decision not in {"approved", "blocked"}:
        raise HTTPException(status_code=422, detail="Decision must be approved or blocked")
    try:
        ready, gate_reasons = validate_evidence(release_id, report.environment, report.evidence)
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    validate_tool_calls(report)
    expected = "approved" if ready else "blocked"
    if report.decision != expected:
        raise HTTPException(status_code=422, detail=f"Decision conflicts with evidence; expected {expected}")
    if not ready and not report.reasons:
        raise HTTPException(status_code=422, detail=f"Blocked decision needs reasons: {gate_reasons}")

    with _check_lock:
        state = matching_state(report)
        if state.status == "success":
            return state.response
        if state.status is not None:
            raise HTTPException(status_code=409, detail="Check already failed")
        report_path = RESULTS_DIR / f"{release_id}.json"
        markdown_path = RESULTS_DIR / f"{release_id}.md"
        markdown = (f"# ShopFlow release readiness: {release_id}\n\n"
                    f"Decision: **{report.decision}**\n\n"
                    f"Environment: {report.environment}\n\n"
                    f"n8n execution: {report.execution_id}\n\n"
                    f"Summary: {report.summary}\n\n"
                    "Reasons:\n" + ("".join(f"- {reason}\n" for reason in report.reasons)
                                     if report.reasons else "- No blocking checks\n")
                    + "\nEvidence and tool calls: see the adjacent JSON report.\n")
        try:
            atomic_write(markdown_path, markdown)
            duration = round(time.monotonic() - state.started, 3)
            data = {**report.model_dump(), "duration_seconds": duration,
                    "reported_at": datetime.now(timezone.utc).isoformat()}
            atomic_write(report_path, json.dumps(data, ensure_ascii=False, indent=2) + "\n")
        except OSError as exc:
            raise HTTPException(status_code=500, detail=f"Report write failed: {exc}") from exc
        CHECKS.labels("success").inc()
        DURATION.observe(duration)
        REPORTS.labels(report.decision).inc()
        response = {"release_id": release_id, "decision": report.decision,
                    "reasons": report.reasons, "report": str(markdown_path),
                    "tool_calls": 4, "execution_id": report.execution_id,
                    "duration_seconds": duration}
        state.status = "success"
        state.response = response
        return response


@app.post("/checks/fail")
def fail_check(request: FailRequest) -> dict:
    safe_key(request.release_id)
    safe_key(request.environment)
    safe_key(request.execution_id)
    with _check_lock:
        state = matching_state(request)
        if state.status == "error":
            return state.response
        if state.status is not None:
            raise HTTPException(status_code=409, detail="Check already succeeded")
        duration = round(time.monotonic() - state.started, 3)
        failure = {**request.model_dump(), "duration_seconds": duration,
                   "failed_at": datetime.now(timezone.utc).isoformat()}
        path = RESULTS_DIR / "failures" / f"{request.execution_id}.json"
        try:
            atomic_write(path, json.dumps(failure, ensure_ascii=False, indent=2) + "\n")
        except OSError as exc:
            raise HTTPException(status_code=500, detail=f"Failure write failed: {exc}") from exc
        CHECKS.labels("error").inc()
        DURATION.observe(duration)
        response = {"execution_id": request.execution_id, "release_id": request.release_id,
                    "environment": request.environment, "error": request.error,
                    "duration_seconds": duration, "failure": str(path)}
        state.status = "error"
        state.response = response
        return response


@app.post("/alerts")
def receive_alerts(payload: dict) -> dict:
    """Save a small allowlisted subset of Alertmanager data without annotations or secrets."""
    alerts = payload.get("alerts", [])
    if not isinstance(alerts, list):
        raise HTTPException(status_code=422, detail="alerts must be a list")
    record = {"received_at": datetime.now(timezone.utc).isoformat(),
              "status": payload.get("status"), "alerts": [
                  {"name": alert.get("labels", {}).get("alertname"),
                   "status": alert.get("status"), "starts_at": alert.get("startsAt")}
                  for alert in alerts if isinstance(alert, dict)
              ]}
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    with (RESULTS_DIR / "alerts.jsonl").open("a", encoding="utf-8") as output:
        output.write(json.dumps(record, ensure_ascii=False) + "\n")
    return {"saved": len(record["alerts"])}


@app.get("/alerts")
def recent_alerts() -> dict:
    path = RESULTS_DIR / "alerts.jsonl"
    if not path.exists():
        return {"alerts": []}
    with path.open(encoding="utf-8") as source:
        rows = source.readlines()[-20:]
    return {"alerts": [json.loads(row) for row in rows]}

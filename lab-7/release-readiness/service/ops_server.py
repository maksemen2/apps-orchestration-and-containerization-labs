"""Operations evidence MCP server; records come from local mock JSON fixtures."""

from mcp.server.fastmcp import FastMCP
from mcp.server.transport_security import TransportSecuritySettings
from prometheus_client import CONTENT_TYPE_LATEST, Counter, generate_latest
from starlette.requests import Request
from starlette.responses import Response

from fixture_store import lookup

TOOL_CALLS = Counter("mcp_tool_calls_total", "MCP tool calls", ["server", "tool", "status"])
for tool in ("get_environment", "get_runbook"):
    for status in ("success", "error"):
        TOOL_CALLS.labels("ops-mcp", tool, status)

mcp = FastMCP(
    "shopflow-operations-evidence",
    host="0.0.0.0",
    port=8000,
    stateless_http=True,
    json_response=True,
    transport_security=TransportSecuritySettings(
        enable_dns_rebinding_protection=True,
        allowed_hosts=["ops-mcp:*", "localhost:*", "127.0.0.1:*"],
        allowed_origins=["http://ops-mcp:*", "http://localhost:*", "http://127.0.0.1:*"],
    ),
)


@mcp.tool()
def get_environment(environment: str) -> dict:
    """Get health and spare capacity for an environment (mock fixture)."""
    try:
        result = lookup("environments", environment)
    except Exception:
        TOOL_CALLS.labels("ops-mcp", "get_environment", "error").inc()
        raise
    TOOL_CALLS.labels("ops-mcp", "get_environment", "success").inc()
    return result


@mcp.tool()
def get_runbook(service: str) -> dict:
    """Get rollback instructions for a service (mock fixture)."""
    try:
        result = lookup("runbooks", service)
    except Exception:
        TOOL_CALLS.labels("ops-mcp", "get_runbook", "error").inc()
        raise
    TOOL_CALLS.labels("ops-mcp", "get_runbook", "success").inc()
    return result


@mcp.custom_route("/metrics", methods=["GET"])
async def metrics(request: Request) -> Response:
    return Response(generate_latest(), media_type=CONTENT_TYPE_LATEST)


if __name__ == "__main__":
    mcp.run(transport="streamable-http")

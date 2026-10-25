"""Release evidence MCP server; records come from local mock JSON fixtures."""

from mcp.server.fastmcp import FastMCP
from mcp.server.transport_security import TransportSecuritySettings
from prometheus_client import CONTENT_TYPE_LATEST, Counter, generate_latest
from starlette.requests import Request
from starlette.responses import Response

from fixture_store import lookup

TOOL_CALLS = Counter("mcp_tool_calls_total", "MCP tool calls", ["server", "tool", "status"])
for tool in ("get_release", "get_checks"):
    for status in ("success", "error"):
        TOOL_CALLS.labels("release-mcp", tool, status)

mcp = FastMCP(
    "shopflow-release-evidence",
    host="0.0.0.0",
    port=8000,
    stateless_http=True,
    json_response=True,
    transport_security=TransportSecuritySettings(
        enable_dns_rebinding_protection=True,
        allowed_hosts=["release-mcp:*", "localhost:*", "127.0.0.1:*"],
        allowed_origins=["http://release-mcp:*", "http://localhost:*", "http://127.0.0.1:*"],
    ),
)


@mcp.tool()
def get_release(release_id: str) -> dict:
    """Get signed image and rollback metadata for one ShopFlow release (mock fixture)."""
    try:
        result = lookup("releases", release_id)
    except Exception:
        TOOL_CALLS.labels("release-mcp", "get_release", "error").inc()
        raise
    TOOL_CALLS.labels("release-mcp", "get_release", "success").inc()
    return result


@mcp.tool()
def get_checks(release_id: str) -> dict:
    """Get required test and migration checks for one ShopFlow release (mock fixture)."""
    try:
        result = lookup("checks", release_id)
    except Exception:
        TOOL_CALLS.labels("release-mcp", "get_checks", "error").inc()
        raise
    TOOL_CALLS.labels("release-mcp", "get_checks", "success").inc()
    return result


@mcp.custom_route("/metrics", methods=["GET"])
async def metrics(request: Request) -> Response:
    return Response(generate_latest(), media_type=CONTENT_TYPE_LATEST)


if __name__ == "__main__":
    mcp.run(transport="streamable-http")

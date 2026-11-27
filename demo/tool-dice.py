#!/usr/bin/env python3
"""dice.py: a stdio MCP server that rolls a die from 1 to 1,000,000.

Standard library only. Speaks newline-delimited JSON-RPC 2.0 over stdin/stdout.
Randomness comes from the `secrets` module (OS CSPRNG), uniform over the range.
"""
import json
import secrets
import sys

PROTOCOL_VERSION = "2024-11-05"
SERVER_INFO = {"name": "dice", "version": "1.0.0"}
MAX_FACE = 1_000_000

TOOL = {
    "name": "roll_dice",
    "description": "Roll a random integer from 1 to 1,000,000 inclusive.",
    "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
}


def roll() -> int:
    return secrets.randbelow(MAX_FACE) + 1


def respond(msg_id, result=None, error=None):
    out = {"jsonrpc": "2.0", "id": msg_id}
    if error is not None:
        out["error"] = error
    else:
        out["result"] = result
    sys.stdout.write(json.dumps(out) + "\n")
    sys.stdout.flush()


def handle(msg):
    method = msg.get("method")
    msg_id = msg.get("id")
    params = msg.get("params") or {}

    # Notifications carry no id and need no reply.
    if msg_id is None:
        return

    if method == "initialize":
        respond(msg_id, {
            "protocolVersion": PROTOCOL_VERSION,
            "capabilities": {"tools": {}},
            "serverInfo": SERVER_INFO,
        })
    elif method == "ping":
        respond(msg_id, {})
    elif method == "tools/list":
        respond(msg_id, {"tools": [TOOL]})
    elif method == "tools/call":
        name = params.get("name")
        if name != TOOL["name"]:
            respond(msg_id, error={"code": -32602, "message": f"unknown tool: {name}"})
            return
        value = roll()
        respond(msg_id, {
            "content": [{"type": "text", "text": str(value)}],
            "isError": False,
        })
    else:
        respond(msg_id, error={"code": -32601, "message": f"method not found: {method}"})


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            respond(None, error={"code": -32700, "message": "parse error"})
            continue
        handle(msg)


if __name__ == "__main__":
    main()

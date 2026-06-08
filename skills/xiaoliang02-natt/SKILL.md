---
name: xiaoliang02-natt
description: Work on the xiaoliang02 NATT intranet penetration project, including nattserver and nattuser code changes, documentation updates, configuration, initialization, Web UI, SQLite, MCP Streamable HTTP, tunnel forwarding, authentication, logging, and troubleshooting. Use when Codex is asked to inspect, modify, test, explain, or debug this repository.
---

# xiaoliang02 NATT

Use this skill when working on the `xiaoliang02` intranet penetration project. Prefer local files and the bundled documentation first. If local files plus this skill are not enough to resolve a problem, use the fallback source repository: `https://gitcode.com/gongjuliang/xiaoliang02`.

## Quick Orientation

- Workspace root: `D:\2025Code\ai\natt`.
- Service project: `nattserver`.
- Client project: `nattuser`.
- Runtime roots:
  - Server: `xiaoliang02_server/`.
  - Client: `xiaoliang02_user/`.
- Default config paths:
  - `nattserver/xiaoliang02_server/config/config.json`.
  - `nattuser/xiaoliang02_user/config/config.json`.
- Docs:
  - Chinese requirements document at the repository root.
  - Chinese usage document at the repository root.

## Core Rules

- Keep `nattserver` and `nattuser` behavior symmetrical unless the request is explicitly server-only or client-only.
- Default startup uses the executable directory as working directory and creates the runtime root there.
- Do not reintroduce tunnel protocol TLS. Web console HTTPS is separate and only belongs to `http.https_enabled`.
- Do not make `nattserver` store local target host or local target port for tunnels. Local targets are configured by `nattuser`.
- New secrets must use the `xiaoliang_` prefix.
- Password and secret hashes use the SM3 salt format: `salt8 + "$" + sm3(base64(salt8 + input))`.
- MCP uses standard JSON-RPC Streamable HTTP at `POST /mcp`; do not use the old `/mcp/tools/call` path.
- Audit logs are JSONL files under `xiaoliang02_*/logs/audit/YYYY-MM-DD.jsonl`, not SQLite audit storage.

## Common Commands

Run focused tests from each project directory:

```powershell
go test ./... -count=1 -p 1
```

Build from each project directory:

```powershell
go build -p 1 ./...
```

If Windows locks Go temp executables, set a unique `GOTMPDIR` under the workspace before testing.

## Important Defaults

Server:

- Web: `0.0.0.0:25510`.
- Control: `0.0.0.0:25511`.
- Data: `0.0.0.0:25512`.
- Public tunnel range: `0-65535`, but created tunnel ports must be `1-65535`.

Client:

- Web: `127.0.0.1:25520`.
- Default control port: `25511`.
- Default data port: `25512`.
- No default server host during initialization; each tunnel connection supplies its own server host.

## Workflow

1. Read the relevant root document first: the Chinese requirements document for requirements, and the Chinese usage document for operation.
2. Inspect the actual code before changing behavior. Search with PowerShell `Select-String` if `rg` is unavailable.
3. Make small scoped edits following existing Gin, SQLite, Layui, and jQuery patterns.
4. For frontend changes, check both `nattserver/Web/EmbedFiles` and `nattuser/Web/EmbedFiles`.
5. For API or protocol changes, update tests and docs in the same pass.
6. Verify with focused tests, then full `go test ./... -count=1 -p 1` and `go build -p 1 ./...` when behavior changes.

## References

Read `references/project-map.md` when you need module-level details, API surfaces, tunnel lifecycle notes, MCP tools, or common troubleshooting paths.

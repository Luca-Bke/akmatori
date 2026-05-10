// Inject a POST /update route into QMD's MCP HTTP server (dist/mcp/server.js).
//
// The akmatori API server POSTs to http://qmd:8181/update after every runbook
// CRUD to keep the search index current. Upstream QMD doesn't expose this
// route, so without this patch every call returns 404 and the index drifts.
//
// The handler runs `qmd update` (refresh lex index) followed by `qmd embed`
// (refresh vector index). Without the embed step, freshly-added runbooks are
// invisible to vec/hyde search until the container restarts. qmd embed is
// idempotent and ETag-cached, so unchanged docs are a near-no-op.
//
// We anchor the insertion immediately before the existing "/health" handler.
// The injected handler shells out via execFile("qmd", [...]) — fixed argv,
// no shell, no user input.

const fs = require("node:fs");
const path = require("node:path");

const target = process.argv[2];
if (!target) {
    console.error("usage: node patch-server.js <path-to-server.js>");
    process.exit(2);
}

const src = fs.readFileSync(target, "utf8");

if (src.includes('pathname === "/update"')) {
    console.log(`patch-server.js: /update route already present in ${path.basename(target)}, skipping`);
    process.exit(0);
}

const anchor = 'if (pathname === "/health" && nodeReq.method === "GET") {';
const idx = src.indexOf(anchor);
if (idx === -1) {
    console.error(`patch-server.js: anchor not found in ${target}`);
    console.error("expected literal: " + anchor);
    process.exit(1);
}

// Match the indentation of the anchor line so the injection is properly nested.
const lineStart = src.lastIndexOf("\n", idx) + 1;
const indent = src.slice(lineStart, idx);

const injection =
    `if (pathname === "/update" && nodeReq.method === "POST") {\n` +
    `${indent}    const { execFile } = await import("node:child_process");\n` +
    `${indent}    execFile("qmd", ["update"], { timeout: 300000 }, (updErr, updStdout, updStderr) => {\n` +
    `${indent}        if (updErr) {\n` +
    `${indent}            nodeRes.writeHead(500, { "Content-Type": "application/json" });\n` +
    `${indent}            nodeRes.end(JSON.stringify({ error: String(updErr), stderr: updStderr, stage: "update" }));\n` +
    `${indent}            log(\`\${ts()} POST /update FAILED at update stage (\${Date.now() - reqStart}ms)\`);\n` +
    `${indent}            return;\n` +
    `${indent}        }\n` +
    `${indent}        execFile("qmd", ["embed"], { timeout: 600000 }, (embErr, embStdout, embStderr) => {\n` +
    `${indent}            if (embErr) {\n` +
    `${indent}                nodeRes.writeHead(500, { "Content-Type": "application/json" });\n` +
    `${indent}                nodeRes.end(JSON.stringify({ error: String(embErr), stderr: embStderr, stage: "embed", updateOutput: updStdout.trim() }));\n` +
    `${indent}                log(\`\${ts()} POST /update FAILED at embed stage (\${Date.now() - reqStart}ms)\`);\n` +
    `${indent}                return;\n` +
    `${indent}            }\n` +
    `${indent}            nodeRes.writeHead(200, { "Content-Type": "application/json" });\n` +
    `${indent}            nodeRes.end(JSON.stringify({ status: "ok", updateOutput: updStdout.trim(), embedOutput: embStdout.trim() }));\n` +
    `${indent}            log(\`\${ts()} POST /update (\${Date.now() - reqStart}ms)\`);\n` +
    `${indent}        });\n` +
    `${indent}    });\n` +
    `${indent}    return;\n` +
    `${indent}}\n` +
    `${indent}`;

const patched = src.slice(0, lineStart) + indent + injection + src.slice(idx);
fs.writeFileSync(target, patched);
console.log(`patch-server.js: injected /update route into ${path.basename(target)}`);

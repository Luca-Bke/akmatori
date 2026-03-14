# Pi-Coding-Agent SDK Upgrade Plan: 0.55.3 → 0.58.0

## Current State

| Package | Current | Latest |
|---------|---------|--------|
| `@mariozechner/pi-coding-agent` | 0.55.3 | 0.58.0 |
| `@mariozechner/pi-ai` | 0.55.3 | 0.58.0 |
| `@mariozechner/pi-agent-core` | 0.55.3 | 0.58.0 |

New dependency in 0.58.0: `@mariozechner/pi-tui` (^0.58.0) — only needed for TUI features, not our headless usage.

---

## Part 1: Version-by-Version Changelog Summary (Relevant to Akmatori)

### 0.55.4 (2026-03-02) — Dynamic Tool Registration
- **Runtime tool registration** via `pi.registerTool()` now applies immediately without `/reload`
- **`promptSnippet`** and **`promptGuidelines`** on `ToolDefinition` — tools can inject text into the system prompt
- Fixed `session.prompt()` returning before retry completion

### 0.56.0 (2026-03-04) — Breaking: OAuth Imports
- **BREAKING**: OAuth exports moved from `@mariozechner/pi-ai` to `@mariozechner/pi-ai/oauth` (we don't use OAuth, no impact)
- **BREAKING**: Scoped model thinking semantics changed — scoped entries without `:<thinking>` suffix inherit session thinking level
- OpenCode Go provider added
- Compaction fixes for non-reasoning models

### 0.56.1 (2026-03-05) — Patch
- Extension alias resolution fix (no impact)

### 0.56.2 (2026-03-05) — GPT-5.4 Support
- GPT-5.4 model available across openai/openai-codex/azure providers
- Mistral native conversations integration

### 0.56.3 (2026-03-06) — Claude Sonnet 4.6
- Claude Sonnet 4.6 model added
- Auto-compaction resilience improvements
- Fixed parallel processes failing with false "No API key found" errors (relevant for us!)

### 0.57.0 (2026-03-07) — Extension Hooks
- **`before_provider_request`** extension hook for intercepting/modifying provider payloads
- **BREAKING**: RPC mode uses strict LF-delimited JSONL framing (we don't use RPC mode, no impact)

### 0.57.1 (2026-03-07) — Session Directory Events
- **`session_directory`** extension event for customizing session directory paths
- Digit keybindings (TUI only, no impact)

### 0.58.0 (2026-03-14) — 1M Context Windows
- **Claude Opus 4.6, Sonnet 4.6 context window expanded to 1M tokens** (huge for incident investigation)
- **Extension tool calls execute in parallel** by default
- Tool interception moved to agent-core `beforeToolCall`/`afterToolCall` hooks
- `GOOGLE_CLOUD_API_KEY` environment variable support for google-vertex
- Extensions can supply deterministic session IDs via `newSession()`
- Fixed retry regex to match `server_error` and `internal_error` error types
- Fixed usage statistics for OpenAI-compatible providers

---

## Part 2: Breaking Changes Impact Assessment

| Breaking Change | Version | Impact on Akmatori | Action Required |
|---|---|---|---|
| OAuth exports moved to `@mariozechner/pi-ai/oauth` | 0.56.0 | **None** — we don't use OAuth | No action |
| Scoped model thinking semantics change | 0.56.0 | **Low** — we set thinking level explicitly | Verify our `mapThinkingLevel()` still works correctly |
| RPC mode strict JSONL framing | 0.57.0 | **None** — we don't use RPC mode | No action |
| Extension tool interception API changed | 0.58.0 | **None** — we don't use extension tool interception | No action |

**Assessment: No breaking changes directly affect our integration.** The upgrade should be mostly a version bump.

---

## Part 3: Upgrade Steps

### Step 1: Update Dependencies
```bash
cd agent-worker
npm install @mariozechner/pi-coding-agent@^0.58.0 @mariozechner/pi-ai@^0.58.0 @mariozechner/pi-agent-core@^0.58.0
```

### Step 2: Verify TypeScript Compilation
```bash
npx tsc --noEmit
```
Check for any type errors from API changes. Key areas to verify:
- `createAgentSession()` options — the interface may have new optional fields
- `AgentSessionEvent` — new event types added (auto_compaction, auto_retry)
- `createBashTool()` — verify spawnHook signature unchanged
- `DefaultResourceLoader` options — verify `skillsOverride` signature unchanged

### Step 3: Verify Event Handling
Our `agent-runner.ts` subscribes to events. New event types added since 0.55.3:
- `auto_compaction_start` / `auto_compaction_end`
- `auto_retry_start` / `auto_retry_end`

These are additive and won't break our existing switch statement, but we should consider handling them for better observability.

### Step 4: Run Tests
```bash
make test-agent
```

### Step 5: Integration Test
- Start the stack with `docker-compose up`
- Trigger an incident investigation
- Verify agent session creation, tool calling, and output streaming still work
- Test with Claude Opus 4.6 to verify 1M context window

### Step 6: Update Dockerfile
Verify the Docker build still works with the new dependencies. No Dockerfile changes expected unless new system dependencies are required.

---

## Part 4: New Features We Can Adopt

### Priority 1: High Value, Low Effort

#### 1.1 — Dynamic Tool Registration (`promptSnippet` / `promptGuidelines`)
**Version**: 0.55.4+
**What**: Tool definitions can now include `promptSnippet` (appears in "Available tools" section) and `promptGuidelines` (appended as guidelines to system prompt).
**How we can use it**: Instead of injecting `TOOL_CALLING_INSTRUCTIONS` as a big string via `appendSystemPrompt`, we can attach prompt snippets directly to our custom bash tool. This makes the system prompt more modular and tools self-documenting.

**Implementation**:
```typescript
// In agent-runner.ts, when creating the bash tool:
const bashTool = createBashTool(workDir, {
  spawnHook: (ctx) => ({ /* existing spawnHook */ }),
});

// Attach prompt guidelines directly to the tool
bashTool.promptGuidelines = `
- Use python3 -c "from ssh import ..." for SSH operations
- Use python3 -c "from zabbix import ..." for Zabbix operations
- Always pass tool_instance_id from SKILL.md
`;
```

**Effort**: Small — refactor `TOOL_CALLING_INSTRUCTIONS` into per-tool snippets.
**Benefit**: Cleaner system prompt, tools carry their own documentation.

#### 1.2 — 1M Context Window for Claude Models
**Version**: 0.58.0
**What**: Claude Opus 4.6 and Sonnet 4.6 now support 1M token context windows.
**How we can use it**: Longer investigations without hitting context limits. Especially valuable for incidents with large log dumps, multiple tool calls, and extended multi-turn conversations.
**Implementation**: Free — comes automatically with the model registry update.
**Effort**: Zero.
**Benefit**: Significant — agents can handle much longer investigations before needing compaction.

#### 1.3 — Improved Auto-Compaction and Retry
**Version**: 0.56.3, 0.58.0
**What**: Auto-compaction is now resilient to persistent API errors, and retry logic correctly matches more error types.
**How we can use it**: More reliable long-running investigations. Agents recover gracefully from transient API failures.
**Implementation**: Free — comes with the upgrade.
**Effort**: Zero.
**Benefit**: Improved reliability for long investigations.

#### 1.4 — New Event Types for Observability
**Version**: 0.58.0
**What**: `auto_compaction_start/end` and `auto_retry_start/end` events.
**How we can use it**: Stream compaction and retry status back to the UI. Users can see when the agent is compacting its context or retrying after an error.

**Implementation**:
```typescript
// In agent-runner.ts event handler:
case "auto_compaction_start":
  onOutput(`[COMPACTION] Starting context compaction (reason: ${event.reason})\n`);
  break;
case "auto_compaction_end":
  if (event.aborted) {
    onOutput(`[COMPACTION] Compaction aborted\n`);
  } else {
    onOutput(`[COMPACTION] Compaction complete\n`);
  }
  break;
case "auto_retry_start":
  onOutput(`[RETRY] Attempt ${event.attempt}/${event.maxAttempts} after error: ${event.errorMessage}\n`);
  break;
case "auto_retry_end":
  if (!event.success) {
    onOutput(`[RETRY] All retries exhausted: ${event.finalError}\n`);
  }
  break;
```

**Effort**: Small.
**Benefit**: Better visibility into agent behavior during long investigations.

### Priority 2: Medium Value, Medium Effort

#### 2.1 — `before_provider_request` Extension Hook
**Version**: 0.57.0
**What**: Extensions can intercept and modify provider request payloads before they're sent.
**How we can use it**: Could be used for:
  - Request logging/auditing (log all LLM API calls)
  - Token budget enforcement (reject requests that would exceed a budget)
  - Request modification (inject additional context, modify temperature)
  - Proxy header injection

**Implementation**: Requires creating an extension. We'd need to explore the extension API more deeply.
**Effort**: Medium — need to understand extension lifecycle in headless mode.
**Benefit**: Better observability and control over LLM API calls.

#### 2.2 — `session_directory` Extension Event
**Version**: 0.57.1
**What**: Extensions can customize session directory paths before session manager creation.
**How we can use it**: Currently we construct `workDir` as `${WORKSPACE_DIR}/${incidentId}`. With this event, we could have the session manager auto-organize sessions by date, severity, or other metadata.
**Effort**: Medium.
**Benefit**: Better session organization, easier cleanup.

#### 2.3 — Deterministic Session IDs
**Version**: 0.58.0
**What**: Extensions can supply deterministic session IDs via `newSession()`.
**How we can use it**: Use the incident ID as the session ID, making it trivial to look up sessions by incident. Currently session IDs are random UUIDs and we have to track the mapping separately.
**Implementation**: Via extension `newSession()` hook that returns the incident ID as session ID.
**Effort**: Medium.
**Benefit**: Simplified session lookup, no separate mapping needed.

#### 2.4 — Parallel Extension Tool Execution
**Version**: 0.58.0
**What**: Extension tool calls now execute in parallel by default.
**How we can use it**: If we implement custom tools as extension tools (rather than Python wrappers), they'd automatically benefit from parallel execution. E.g., the agent could query multiple Zabbix instances or run SSH commands on multiple servers simultaneously.
**Effort**: Medium-High — would require rearchitecting tool invocation from Python wrappers to native extension tools.
**Benefit**: Faster investigations when multiple tool calls are needed.

### Priority 3: Future Consideration

#### 3.1 — Native Tool Definitions (replacing Python wrappers)
**What**: Instead of teaching the agent to run `python3 -c "from ssh import ..."` commands via bash, register SSH and Zabbix operations as native `ToolDefinition` objects with proper input schemas.
**How we can use it**:
  - Better type safety — tools have JSON Schema input definitions
  - Automatic prompt documentation via `promptSnippet`/`promptGuidelines`
  - Parallel execution support
  - Tool result images/structured data support
  - No Python runtime dependency in agent container

**Implementation**:
```typescript
const sshTool: ToolDefinition = {
  name: "ssh_execute",
  description: "Execute a command on a remote server via SSH",
  inputSchema: {
    type: "object",
    properties: {
      command: { type: "string", description: "Command to execute" },
      tool_instance_id: { type: "number", description: "SSH tool instance ID" },
    },
    required: ["command"],
  },
  async execute(input) {
    // Call MCP Gateway directly via HTTP
    const result = await mcpClient.call("ssh.execute_command", input);
    return { output: result };
  },
  promptSnippet: "Execute commands on remote servers via SSH",
  promptGuidelines: "Use for system diagnostics, log analysis, service management",
};
```

**Effort**: High — requires rearchitecting the tool layer, updating SKILL.md generation in Go, updating tests.
**Benefit**: Significantly cleaner architecture, better agent behavior, parallel tool execution, no Python dependency.

#### 3.2 — `beforeToolCall` / `afterToolCall` Hooks
**Version**: 0.58.0
**What**: Agent-core level hooks for intercepting tool calls.
**How we can use it**:
  - Audit logging of all tool invocations
  - Rate limiting tool calls (prevent agent from flooding external APIs)
  - Permission enforcement (block dangerous commands)
  - Tool call metrics collection

**Effort**: Medium — need to implement via `AgentSessionConfig.baseToolsOverride` or extension hooks.
**Benefit**: Better security and observability.

#### 3.3 — GPT-5.4 / New Model Support
**Version**: 0.56.2+
**What**: Latest model support in the registry.
**How we can use it**: Users selecting these models in the Akmatori UI automatically get the correct model metadata (context window, pricing, etc.) from the updated registry.
**Implementation**: Free — comes with the upgrade.
**Effort**: Zero.
**Benefit**: Users can use latest models immediately.

---

## Part 5: Implementation Phases

### Phase 1: Core Upgrade (1-2 hours)
1. Bump all three pi packages to `^0.58.0`
2. Run `npm install` and verify lockfile
3. Run `npx tsc --noEmit` — fix any type errors
4. Update event handler in `agent-runner.ts` to handle new event types
5. Run `make test-agent`
6. Docker build + integration test

### Phase 2: Quick Wins (2-4 hours)
1. Add auto-compaction/retry event streaming to the UI
2. Refactor `TOOL_CALLING_INSTRUCTIONS` into `promptGuidelines` on the bash tool
3. Verify 1M context window works with Claude models

### Phase 3: Architecture Improvements (1-2 days, optional)
1. Implement deterministic session IDs via extension
2. Explore `before_provider_request` for request auditing
3. Prototype native tool definitions (replacing one Python wrapper as a proof of concept)

### Phase 4: Full Tool Migration (3-5 days, future)
1. Migrate all Python tool wrappers to native `ToolDefinition` objects
2. Update Go SKILL.md generation to not include Python examples
3. Remove Python runtime from agent container Dockerfile
4. Update tests

---

## Part 6: Risks and Mitigations

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| Type incompatibilities in SDK | Low | `tsc --noEmit` check before deploying |
| Behavioral changes in tool execution | Low | Integration test with real incident |
| New dependencies causing Docker build issues | Low | Test Docker build in CI |
| Auto-compaction changes affecting investigation quality | Medium | Monitor first few investigations post-upgrade |
| Extension API not working well in headless mode | Medium | Test extension hooks in headless before committing to architecture |

---

## Part 7: Files Requiring Changes

### Phase 1 (Core Upgrade)
- `agent-worker/package.json` — version bump
- `agent-worker/package-lock.json` — regenerated
- `agent-worker/src/agent-runner.ts` — new event types in switch statement

### Phase 2 (Quick Wins)
- `agent-worker/src/agent-runner.ts` — refactor TOOL_CALLING_INSTRUCTIONS to promptGuidelines
- `agent-worker/src/orchestrator.ts` — forward new event types to API

### Phase 3+ (Architecture)
- `agent-worker/src/agent-runner.ts` — extension hooks, native tools
- `agent-worker/Dockerfile` — potentially remove Python if tools fully migrated
- `internal/services/skill_service.go` — update SKILL.md generation
- `agent-worker/tools/` — deprecate Python wrappers

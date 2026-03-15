/**
 * Gateway tool definitions for the pi-mono coding agent.
 *
 * Registers `gateway_call` as a custom tool that replaces the Python wrapper
 * pattern. The agent calls tools on the MCP Gateway directly via the
 * TypeScript GatewayClient instead of shelling out to Python scripts.
 */

import { Type, type TSchema, type Static } from "@sinclair/typebox";
import type { GatewayClient, CallResult } from "./gateway-client.js";

// Re-export the ToolDefinition type from pi-coding-agent for convenience.
// We define our own compatible interface to avoid tight coupling with the
// extension system's full type surface (renderCall, renderResult, etc.).
// The `customTools` option on createAgentSession accepts this shape.

export interface GatewayToolContext {
  client: GatewayClient;
}

// ---------------------------------------------------------------------------
// gateway_call tool schema
// ---------------------------------------------------------------------------

export const GatewayCallParams = Type.Object({
  tool_name: Type.String({ description: "The MCP tool name to call (e.g. 'ssh.execute_command', 'zabbix.get_problems')" }),
  args: Type.Record(Type.String(), Type.Unknown(), { description: "Arguments to pass to the tool" }),
  instance: Type.Optional(Type.String({ description: "Logical name of the tool instance to use (optional, falls back to default)" })),
});

export type GatewayCallInput = Static<typeof GatewayCallParams>;

// ---------------------------------------------------------------------------
// Tool definition factory
// ---------------------------------------------------------------------------

/**
 * Create the `gateway_call` tool definition for registration with pi-mono.
 *
 * The returned object conforms to the `ToolDefinition` interface expected by
 * `createAgentSession({ customTools: [...] })`.
 */
export function createGatewayCallTool(ctx: GatewayToolContext) {
  return {
    name: "gateway_call",
    label: "Gateway Call",
    description:
      "Call a tool on the MCP Gateway. Use this to execute infrastructure tools " +
      "(SSH commands, Zabbix queries, VictoriaMetrics queries, etc.) by name. " +
      "Each skill's SKILL.md lists the available tools and their logical instance names.",
    promptGuidelines: [
      "Use gateway_call to invoke infrastructure tools instead of Python wrappers.",
      "Each skill's SKILL.md lists assigned tools with their logical names and available operations. Read SKILL.md first.",
      "Example: gateway_call({ tool_name: \"ssh.execute_command\", args: { command: \"uptime\", servers: [\"web-01\"] }, instance: \"prod-ssh\" })",
      "Example: gateway_call({ tool_name: \"zabbix.get_problems\", args: { severity_min: 3 }, instance: \"prod-zabbix\" })",
      "For large result sets, the output is automatically saved to a file and a preview is returned.",
    ],
    parameters: GatewayCallParams,
    execute: async (
      _toolCallId: string,
      params: GatewayCallInput,
      _signal: AbortSignal | undefined,
      _onUpdate: unknown,
    ) => {
      try {
        const result: CallResult = await ctx.client.call(
          params.tool_name,
          params.args as Record<string, unknown>,
          params.instance,
        );

        let text: string;
        if (typeof result.data === "string") {
          text = result.data;
        } else {
          text = JSON.stringify(result.data, null, 2);
        }

        if (result.outputFile) {
          text += `\n\n[Full output saved to: ${result.outputFile}]`;
        }

        return {
          content: [{ type: "text" as const, text }],
          details: {},
        };
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        return {
          content: [{ type: "text" as const, text: `Error: ${message}` }],
          details: {},
        };
      }
    },
  };
}

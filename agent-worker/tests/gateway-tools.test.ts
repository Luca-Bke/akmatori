import { describe, it, expect, vi, beforeEach } from "vitest";
import { createGatewayCallTool, GatewayCallParams, type GatewayCallInput } from "../src/gateway-tools.js";
import type { GatewayClient, CallResult } from "../src/gateway-client.js";

// ---------------------------------------------------------------------------
// Mock GatewayClient
// ---------------------------------------------------------------------------

function createMockClient(overrides?: Partial<GatewayClient>): GatewayClient {
  return {
    call: vi.fn(async () => ({ data: { status: "ok" } } as CallResult)),
    searchTools: vi.fn(async () => ({ tools: [] })),
    getToolDetail: vi.fn(async () => ({
      name: "ssh.execute_command",
      description: "Execute SSH command",
      params: {},
      instances: [],
    })),
    ...overrides,
  } as unknown as GatewayClient;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("createGatewayCallTool", () => {
  let mockClient: GatewayClient;

  beforeEach(() => {
    mockClient = createMockClient();
  });

  describe("tool definition", () => {
    it("should have correct name and description", () => {
      const tool = createGatewayCallTool({ client: mockClient });

      expect(tool.name).toBe("gateway_call");
      expect(tool.label).toBe("Gateway Call");
      expect(tool.description).toContain("MCP Gateway");
    });

    it("should have correct parameter schema", () => {
      const tool = createGatewayCallTool({ client: mockClient });

      expect(tool.parameters).toBe(GatewayCallParams);
      // Verify schema has the expected properties
      expect(tool.parameters.properties.tool_name).toBeDefined();
      expect(tool.parameters.properties.args).toBeDefined();
      expect(tool.parameters.properties.instance).toBeDefined();
    });

    it("should have promptGuidelines", () => {
      const tool = createGatewayCallTool({ client: mockClient });

      expect(tool.promptGuidelines).toBeDefined();
      expect(Array.isArray(tool.promptGuidelines)).toBe(true);
      expect(tool.promptGuidelines!.length).toBeGreaterThan(0);
      expect(tool.promptGuidelines!.some((g: string) => g.includes("gateway_call"))).toBe(true);
    });
  });

  describe("execute handler", () => {
    it("should call GatewayClient.call with correct arguments", async () => {
      const tool = createGatewayCallTool({ client: mockClient });

      const params: GatewayCallInput = {
        tool_name: "ssh.execute_command",
        args: { command: "uptime", servers: ["web-01"] },
        instance: "prod-ssh",
      };

      await tool.execute("tc-1", params, undefined, undefined);

      expect(mockClient.call).toHaveBeenCalledWith(
        "ssh.execute_command",
        { command: "uptime", servers: ["web-01"] },
        "prod-ssh",
      );
    });

    it("should call without instance when not provided", async () => {
      const tool = createGatewayCallTool({ client: mockClient });

      const params: GatewayCallInput = {
        tool_name: "zabbix.get_problems",
        args: { severity_min: 3 },
      };

      await tool.execute("tc-2", params, undefined, undefined);

      expect(mockClient.call).toHaveBeenCalledWith(
        "zabbix.get_problems",
        { severity_min: 3 },
        undefined,
      );
    });

    it("should return JSON-stringified object results", async () => {
      const client = createMockClient({
        call: vi.fn(async () => ({
          data: { hosts: ["web-01", "web-02"], count: 2 },
        })) as any,
      });
      const tool = createGatewayCallTool({ client });

      const result = await tool.execute(
        "tc-3",
        { tool_name: "zabbix.get_hosts", args: {} },
        undefined,
        undefined,
      );

      expect(result.content).toHaveLength(1);
      expect(result.content[0].type).toBe("text");
      const parsed = JSON.parse(result.content[0].text);
      expect(parsed.hosts).toEqual(["web-01", "web-02"]);
      expect(parsed.count).toBe(2);
    });

    it("should return string results directly", async () => {
      const client = createMockClient({
        call: vi.fn(async () => ({
          data: "uptime: 42 days, 3 users",
        })) as any,
      });
      const tool = createGatewayCallTool({ client });

      const result = await tool.execute(
        "tc-4",
        { tool_name: "ssh.execute_command", args: { command: "uptime" } },
        undefined,
        undefined,
      );

      expect(result.content[0].text).toBe("uptime: 42 days, 3 users");
    });

    it("should include output file path when result was truncated", async () => {
      const client = createMockClient({
        call: vi.fn(async () => ({
          data: "truncated preview...",
          outputFile: "/workspace/tool_outputs/ssh_1234.json",
        })) as any,
      });
      const tool = createGatewayCallTool({ client });

      const result = await tool.execute(
        "tc-5",
        { tool_name: "ssh.execute_command", args: { command: "ls -la" } },
        undefined,
        undefined,
      );

      expect(result.content[0].text).toContain("truncated preview...");
      expect(result.content[0].text).toContain("/workspace/tool_outputs/ssh_1234.json");
    });

    it("should handle errors gracefully", async () => {
      const client = createMockClient({
        call: vi.fn(async () => {
          throw new Error("MCP Error -32000: Connection refused");
        }) as any,
      });
      const tool = createGatewayCallTool({ client });

      const result = await tool.execute(
        "tc-6",
        { tool_name: "ssh.execute_command", args: { command: "uptime" } },
        undefined,
        undefined,
      );

      expect(result.content[0].text).toContain("Error:");
      expect(result.content[0].text).toContain("Connection refused");
    });

    it("should handle non-Error thrown values", async () => {
      const client = createMockClient({
        call: vi.fn(async () => {
          throw "unexpected string error";
        }) as any,
      });
      const tool = createGatewayCallTool({ client });

      const result = await tool.execute(
        "tc-7",
        { tool_name: "ssh.execute_command", args: {} },
        undefined,
        undefined,
      );

      expect(result.content[0].text).toContain("Error:");
      expect(result.content[0].text).toContain("unexpected string error");
    });
  });
});

describe("GatewayCallParams schema", () => {
  it("should require tool_name as string", () => {
    expect(GatewayCallParams.properties.tool_name.type).toBe("string");
  });

  it("should require args as record", () => {
    expect(GatewayCallParams.properties.args).toBeDefined();
  });

  it("should have optional instance field", () => {
    // Optional fields in TypeBox are wrapped in Type.Optional
    const instanceProp = GatewayCallParams.properties.instance;
    expect(instanceProp).toBeDefined();
  });
});

import { describe, expect, it } from "vitest";
import { daemonSetupCommands } from "./daemon-setup-commands";

describe("daemonSetupCommands", () => {
  it("uses deployment URLs for self-host setup", () => {
    expect(
      daemonSetupCommands("https://api.example.com/", "https://app.example.com/"),
    ).toEqual({
      setupCmd:
        "multica setup self-host --server-url https://api.example.com --app-url https://app.example.com",
      tokenCmd: `multica config set server_url https://api.example.com
multica config set app_url https://app.example.com
multica login --token <YOUR_TOKEN>
multica daemon start`,
    });
  });

  it("does not invent public Multica Cloud addresses", () => {
    const commands = daemonSetupCommands("", "");
    expect(commands.setupCmd).toBe("multica setup");
    expect(commands.tokenCmd).toBe(`multica login --token <YOUR_TOKEN>
multica daemon start`);
    expect(commands.setupCmd).not.toContain("multica.ai");
    expect(commands.tokenCmd).not.toContain("multica.ai");
    expect(commands.tokenCmd).not.toContain("api.multica.ai");
  });
});

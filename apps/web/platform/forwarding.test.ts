import { afterEach, expect, it } from "vitest";
import { browserForwarding, createForwardStorage } from "./forwarding";

afterEach(() => {
  window.history.replaceState({}, "", "/");
  localStorage.clear();
});

it("only enables browser forwarding for the configuration page", () => {
  for (const path of ["/dingtalk/configure", "/forward/pre/workspaces", "/forward/pre/dingtalk/configure/extra", "/forward/Pre/dingtalk/configure", "/forward/pre_blue/dingtalk/configure"]) {
    window.history.replaceState({}, "", path);
    expect(browserForwarding()).toBeNull();
  }
});

it("reads, writes and deletes only the captured target's storage namespace", () => {
  localStorage.setItem("multica_token", "production");
  const storage = createForwardStorage("mf_pre_");
  expect(storage.getItem("multica_token")).toBeNull();
  storage.setItem("multica_token", "preview");
  expect(storage.getItem("multica_token")).toBe("preview");
  window.history.replaceState({}, "", "/dingtalk/configure");
  storage.removeItem("multica_token");
  expect(localStorage.getItem("multica_token")).toBe("production");
  expect(localStorage.getItem("mf_pre_multica_token")).toBeNull();
});

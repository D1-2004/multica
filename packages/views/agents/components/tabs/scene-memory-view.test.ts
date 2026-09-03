import { describe, expect, it } from "vitest";
import {
  displayMatterTitle,
  isEmptyMemoryBody,
  parseMemorySections,
  partitionSceneMemories,
  sceneDisplayTitle,
  scenePreview,
  visibleMemorySections,
} from "./scene-memory-view";

describe("scene memory view helpers", () => {
  it("splits Host markdown into titled sections", () => {
    const sections = parseMemorySections(
      "## 场域定位\n钉钉群聊。\n\n## 稳定知识与约定\n（暂无）\n",
    );
    expect(sections).toEqual([
      { heading: "场域定位", body: "钉钉群聊。" },
      { heading: "稳定知识与约定", body: "（暂无）" },
    ]);
    expect(isEmptyMemoryBody("（暂无）")).toBe(true);
  });

  it("uses the first locating line when the scene has no title", () => {
    expect(
      sceneDisplayTitle(
        {
          scene_title: "",
          memory_text: "## 场域定位\n场域回归-R7A。\n成员：SixSix、东翔测试号。\n",
        },
        "未命名群聊",
      ),
    ).toBe("场域回归-R7A");
  });

  it("skips a generic 钉钉群聊 filler and names the group from members", () => {
    expect(
      sceneDisplayTitle(
        {
          scene_title: "",
          memory_text: "## 场域定位\n钉钉群聊。\n成员：SixSix、东翔测试号。\n",
        },
        "未命名群聊",
      ),
    ).toBe("SixSix、东翔测试号");
    expect(
      sceneDisplayTitle(
        {
          scene_title: "",
          memory_text: "## 场域定位\n群聊。已知成员：SixSix、东翔测试号。\n",
        },
        "未命名群聊",
      ),
    ).toBe("SixSix、东翔测试号");
  });

  it("splits direct messages from groups", () => {
    expect(
      partitionSceneMemories([
        { scene_kind: "dm" },
        { scene_kind: "group" },
        { scene_kind: "dm" },
      ]),
    ).toEqual({
      dms: [{ scene_kind: "dm" }, { scene_kind: "dm" }],
      groups: [{ scene_kind: "group" }],
    });
  });

  it("shows the deliverable without the 委托 wrapper", () => {
    expect(
      displayMatterTitle("冬翔委托：向dxxh确认明天上午有没有空", "未命名事项"),
    ).toBe("向dxxh确认明天上午有没有空");
    expect(
      displayMatterTitle("<@abc> 向dxxh确认明天有空", "未命名事项"),
    ).toBe("向dxxh确认明天有空");
  });

  it("keeps an explicit title and strips markdown from the list preview", () => {
    expect(
      sceneDisplayTitle(
        { scene_title: "冬翔", memory_text: "## 稳定知识\nGoalMate 是工具" },
        "未命名场域",
      ),
    ).toBe("冬翔");
    expect(
      scenePreview({
        memory_text: "## 稳定知识与约定\n- GoalMate 是工具，不是数字员工",
      }),
    ).toContain("GoalMate 是工具");
  });

  it("does not use a locating dump as the list title", () => {
    expect(
      sceneDisplayTitle(
        {
          scene_title: "群聊。已知成员：SixSix、东翔测试号",
          memory_text:
            "## 场域定位\n群聊。已知成员：SixSix、东翔测试号。\n本会话是内部群。\n",
        },
        "未命名群聊",
      ),
    ).toBe("SixSix、东翔测试号");
  });

  it("hides locating and empty buckets from the owner-facing preview", () => {
    const text =
      "## 场域定位\n冬翔\n成员：冬翔。\n\n## 稳定知识与约定\n（暂无）\n\n## 近期事实\n- 约了dxxh\n";
    expect(visibleMemorySections(text)).toEqual([
      { heading: "近期事实", body: "- 约了dxxh" },
    ]);
    expect(scenePreview({ memory_text: text })).toContain("约了dxxh");
    expect(scenePreview({ memory_text: text })).not.toContain("冬翔");
  });
});

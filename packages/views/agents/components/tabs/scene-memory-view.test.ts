import { describe, expect, it } from "vitest";
import {
  isEmptyMemoryBody,
  parseMemorySections,
  sceneDisplayTitle,
  scenePreview,
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
          memory_text: "## 场域定位\n钉钉群聊。成员：SixSix、东翔测试号。\n",
        },
        "未命名场域",
      ),
    ).toBe("钉钉群聊。成员：SixSix、东翔测试号。");
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
});

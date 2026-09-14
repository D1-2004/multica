"use client";

import { useMemo, type CSSProperties } from "react";
import { MultiFileDiff } from "@pierre/diffs/react";
import { useTheme } from "@multica/ui/components/common/theme-provider";

export default function SourceFileDiff({ path, before, after, expandUnchanged }: {
  path: string;
  before: string;
  after: string;
  expandUnchanged: boolean;
}) {
  const { resolvedTheme } = useTheme();
  const oldFile = useMemo(() => ({ name: path, contents: before }), [path, before]);
  const newFile = useMemo(() => ({ name: path, contents: after }), [path, after]);
  // Pierre renders a file with one empty side in a single column. Keep that
  // column under its version heading instead of letting it span both sides.
  return <div className={before === "" ? "ml-auto w-1/2 border-l" : after === "" ? "w-1/2 border-r" : undefined}><MultiFileDiff
    oldFile={oldFile}
    newFile={newFile}
    options={{
      diffStyle: "split",
      diffIndicators: "classic",
      lineDiffType: "word-alt",
      themeType: resolvedTheme === "dark" ? "dark" : resolvedTheme === "light" ? "light" : "system",
      disableFileHeader: true,
      overflow: "wrap",
      expandUnchanged,
      hunkSeparators: "line-info",
    }}
    style={{ "--diffs-font-family": "var(--font-mono)", "--diffs-font-size": "var(--text-caption)" } as CSSProperties}
  /></div>;
}

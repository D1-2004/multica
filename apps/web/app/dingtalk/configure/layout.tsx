import type { Metadata } from "next";
import type { ReactNode } from "react";

// The configure page is opened inside DingTalk, whose navigation bar shows
// the document title: name the page itself, without the workbench suffix.
export const metadata: Metadata = {
  title: { absolute: "QwenTag配置" },
};

export default function ContextConfigureLayout({ children }: { children: ReactNode }) {
  return children;
}

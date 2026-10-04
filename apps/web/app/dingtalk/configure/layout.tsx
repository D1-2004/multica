import type { Metadata } from "next";
import type { ReactNode } from "react";

// The configure page is opened inside DingTalk, whose navigation bar shows
// the document title: name the page itself, without the workbench suffix.
export const metadata: Metadata = {
  title: { absolute: "QwenTag配置" },
  icons: {
    icon: `${process.env.MULTICA_FORWARD_ASSET_PREFIX ?? ""}/favicon.svg`,
    shortcut: `${process.env.MULTICA_FORWARD_ASSET_PREFIX ?? ""}/favicon.svg`,
  },
};

export default function ContextConfigureLayout({ children }: { children: ReactNode }) {
  return children;
}

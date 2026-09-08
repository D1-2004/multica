"use client";

import { use } from "react";
import { DshPluginDetailPage } from "@multica/views/dsh-plugins";

export default function Page({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return <DshPluginDetailPage pluginId={id} />;
}

"use client";

import { useCallback, useState } from "react";
import { Loader2, Route } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import type { AgentTask, DSHTrajectoryArtifact } from "@multica/core/types/agent";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { DSHTrajectoryDialog } from "./dsh-trajectory-dialog";

export function DSHTrajectoryButton({
  task,
  className,
  title,
}: {
  task: AgentTask;
  className?: string;
  title?: string;
}) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [artifact, setArtifact] = useState<DSHTrajectoryArtifact | null>(null);
  const label = title ?? t(($) => $.trajectory.open);

  const handleClick = useCallback(
    (event: React.MouseEvent) => {
      event.preventDefault();
      event.stopPropagation();
      if (artifact) {
        setOpen(true);
        return;
      }
      setLoading(true);
      api
        .getDSHTrajectory(task.id)
        .then((next) => {
          setArtifact(next);
          setOpen(true);
        })
        .catch((error) => {
          toast.error(
            error instanceof Error
              ? error.message
              : t(($) => $.trajectory.load_failed),
          );
        })
        .finally(() => setLoading(false));
    },
    [artifact, task.id, t],
  );

  if (!task.dsh_trajectory_available) return null;

  return (
    <>
      <Tooltip>
        <TooltipTrigger
          render={<button type="button" />}
          onClick={handleClick}
          disabled={loading}
          aria-label={label}
          className={cn(
            "flex items-center justify-center rounded p-1 text-sky-600 transition-colors hover:bg-sky-500/10 hover:text-sky-700 disabled:opacity-50 dark:text-sky-400 dark:hover:text-sky-300",
            className,
          )}
        >
          {loading ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Route className="h-3.5 w-3.5" />
          )}
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
      {artifact && (
        <DSHTrajectoryDialog
          open={open}
          onOpenChange={setOpen}
          artifact={artifact}
        />
      )}
    </>
  );
}

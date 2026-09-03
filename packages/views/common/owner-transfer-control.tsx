"use client";

import { useMemo, useState } from "react";
import type { MemberWithUser } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { ActorAvatar } from "./actor-avatar";
import { useT } from "../i18n";

interface OwnerTransferControlProps {
  ownerId: string | null;
  owner: MemberWithUser | null;
  members: MemberWithUser[];
  canTransfer: boolean;
  descriptionFor: (ownerName: string) => string;
  onTransfer: (userId: string) => Promise<void>;
}

export function OwnerTransferControl({
  ownerId,
  owner,
  members,
  canTransfer,
  descriptionFor,
  onTransfer,
}: OwnerTransferControlProps) {
  const { t } = useT("common");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const [pending, setPending] = useState<MemberWithUser | null>(null);
  const [transferring, setTransferring] = useState(false);

  const filteredMembers = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return members;
    return members.filter((m) => m.name.toLowerCase().includes(q));
  }, [members, filter]);

  const label = owner
    ? owner.name
    : ownerId
      ? t(($) => $.owner_transfer.left)
      : t(($) => $.owner_transfer.none);

  const value = (
    <span className="flex min-w-0 items-center gap-1.5">
      {owner ? (
        <ActorAvatar actorType="member" actorId={owner.user_id} size="xs" />
      ) : null}
      <span
        className={
          owner ? "truncate text-foreground" : "truncate text-muted-foreground"
        }
      >
        {label}
      </span>
    </span>
  );

  const pick = (member: MemberWithUser) => {
    setOpen(false);
    setFilter("");
    if (member.user_id === ownerId) return;
    setPending(member);
  };

  const confirm = async () => {
    if (!pending) return;
    setTransferring(true);
    try {
      await onTransfer(pending.user_id);
      setPending(null);
    } finally {
      setTransferring(false);
    }
  };

  return (
    <>
      {canTransfer ? (
        <Popover
          open={open}
          onOpenChange={(next) => {
            setOpen(next);
            if (!next) setFilter("");
          }}
        >
          <PopoverTrigger
            render={
              <button
                type="button"
                className="inline-flex min-w-0 items-center text-left hover:text-foreground"
              >
                {value}
              </button>
            }
          />
          <PopoverContent align="start" className="w-52 p-0">
            <div className="border-b px-2 py-1.5">
              <input
                type="text"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder={t(($) => $.owner_transfer.search)}
                className="w-full bg-transparent text-body outline-none placeholder:text-muted-foreground"
              />
            </div>
            <div className="max-h-60 overflow-y-auto p-1">
              {filteredMembers.map((m) => (
                <button
                  type="button"
                  key={m.user_id}
                  onClick={() => pick(m)}
                  className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-body transition-colors hover:bg-accent"
                >
                  <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
                  <span className="truncate">{m.name}</span>
                </button>
              ))}
              {filteredMembers.length === 0 && (
                <div className="px-2 py-3 text-center text-body text-muted-foreground">
                  {t(($) => $.owner_transfer.no_results)}
                </div>
              )}
            </div>
          </PopoverContent>
        </Popover>
      ) : (
        value
      )}

      <Dialog
        open={pending !== null}
        onOpenChange={(next) => {
          if (!next && !transferring) setPending(null);
        }}
      >
        <DialogContent className="max-w-sm" showCloseButton={false}>
          <DialogHeader>
            <DialogTitle className="text-body font-semibold">
              {t(($) => $.owner_transfer.title)}
            </DialogTitle>
            <DialogDescription className="text-caption">
              {descriptionFor(pending?.name ?? "")}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              disabled={transferring}
              onClick={() => setPending(null)}
            >
              {t(($) => $.owner_transfer.cancel)}
            </Button>
            <Button size="sm" disabled={transferring} onClick={() => void confirm()}>
              {t(($) => $.owner_transfer.confirm)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

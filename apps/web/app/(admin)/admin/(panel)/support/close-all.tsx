"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useState } from "react";
import { useSWRConfig } from "swr";

import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

/**
 * Closes every open and pending ticket at once — after a mass incident has
 * been answered, say. It asks for confirmation and is audited with the count.
 */
export function CloseAllTickets() {
  const translate = useTranslations("admin.support");
  const { can } = useSession();
  const { mutate } = useSWRConfig();
  const { run, pending } = useOperatorAction();
  const [open, setOpen] = useState(false);
  if (!can("support.write")) {
    return null;
  }
  return (
    <>
      <Button
        className="self-end"
        disabled={pending}
        onClick={() => setOpen(true)}
        size="sm"
        variant="outline"
      >
        {translate("closeAll")}
      </Button>
      <ConfirmDialog
        cancelLabel={translate("closeAllCancel")}
        confirmLabel={translate("closeAll")}
        confirmationPhrase="CLOSE"
        confirmationPrompt={translate("closeAllPrompt")}
        description={translate("closeAllDescription")}
        destructive
        onConfirm={async () => {
          setOpen(false);
          if (
            await run("/v1/panel/support/tickets/close-all", {
              method: "POST",
              body: {},
              reason: "close all tickets",
            })
          ) {
            toast.success(translate("closeAllDone"));
            await mutate((key) => typeof key === "string" && key.startsWith("/v1/panel/support"));
          }
        }}
        onOpenChange={setOpen}
        open={open}
        title={translate("closeAll")}
      />
    </>
  );
}

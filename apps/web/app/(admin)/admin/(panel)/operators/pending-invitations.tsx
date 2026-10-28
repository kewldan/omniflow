"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { SkeletonTable } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useState } from "react";
import type { KeyedMutator } from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { apiFetch } from "@/lib/api";

import type { Invitation } from "./roles";

/**
 * Invitations nobody has redeemed yet.
 *
 * Expired ones stay on the list, marked, because a link that lapsed unseen is
 * the case the inviter needs to notice; inviting the same address again
 * replaces it. The link itself is never shown here — the server does not have
 * it.
 */
export function PendingInvitations({
  editable,
  error,
  invitations,
  loading,
  mutate,
}: {
  editable: boolean;
  error: boolean;
  invitations: Invitation[];
  loading: boolean;
  mutate: KeyedMutator<{ items: Invitation[] }>;
}) {
  const translate = useTranslations("admin.operators.pending");
  const translateRole = useTranslations("admin.roles");
  // Fixed at mount: a link that lapses while the page is open is relabelled
  // on the next visit, and the server refuses it either way.
  const [now] = useState(() => Date.now());
  const [revoking, setRevoking] = useState<Invitation | null>(null);
  const [pending, setPending] = useState(false);

  async function revoke(invitation: Invitation) {
    setPending(true);
    try {
      await apiFetch(`/v1/panel/admins/invitations/${invitation.id}`, { method: "DELETE" });
      toast.success(translate("revoked", { email: invitation.email }));
      setRevoking(null);
      await mutate();
    } catch {
      toast.error(translate("revokeFailed"));
    } finally {
      setPending(false);
    }
  }

  return (
    <Card className="overflow-hidden">
      <div className="border-border border-b px-4 py-3">
        <h2 className="font-semibold text-base">{translate("title")}</h2>
        <p className="text-muted-foreground text-sm">{translate("description")}</p>
      </div>
      {loading ? (
        <SkeletonTable columns={4} rows={2} />
      ) : error ? (
        <div className="p-4">
          <StateNotice
            action={
              <Button onClick={() => mutate()} size="sm" variant="outline">
                {translate("retry")}
              </Button>
            }
            description={translate("errorDescription")}
            title={translate("error")}
            variant="danger"
          />
        </div>
      ) : invitations.length === 0 ? (
        <div className="p-4">
          <StateNotice description={translate("emptyDescription")} title={translate("empty")} />
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("columns.email")}</TableHead>
              <TableHead>{translate("columns.roles")}</TableHead>
              <TableHead>{translate("columns.expires")}</TableHead>
              <TableHead className="hidden sm:table-cell">
                {translate("columns.invitedBy")}
              </TableHead>
              {editable ? <TableHead className="w-px" /> : null}
            </TableRow>
          </TableHeader>
          <TableBody>
            {invitations.map((invitation) => {
              const expiresAt = new Date(invitation.expiresAt);
              const expired = expiresAt.getTime() <= now;
              return (
                <TableRow key={invitation.id}>
                  <TableCell className="font-mono text-[12px]">{invitation.email}</TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {invitation.roles.map((role) => (
                        <Badge key={role} variant="neutral">
                          {translateRole(role)}
                        </Badge>
                      ))}
                    </div>
                  </TableCell>
                  <TableCell>
                    {expired ? (
                      <Badge variant="warning">{translate("expired")}</Badge>
                    ) : (
                      <time dateTime={invitation.expiresAt}>
                        {translate("expiresAt", { date: expiresAt })}
                      </time>
                    )}
                  </TableCell>
                  <TableCell className="hidden sm:table-cell">{invitation.invitedByName}</TableCell>
                  {editable ? (
                    <TableCell>
                      <Button onClick={() => setRevoking(invitation)} size="sm" variant="outline">
                        {translate("revoke")}
                      </Button>
                    </TableCell>
                  ) : null}
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}

      <ConfirmDialog
        cancelLabel={translate("cancel")}
        confirmLabel={translate("revoke")}
        description={translate("confirmDescription", { email: revoking?.email ?? "" })}
        destructive
        onConfirm={() => revoking && revoke(revoking)}
        onOpenChange={(open) => !open && setRevoking(null)}
        open={Boolean(revoking)}
        pending={pending}
        title={translate("confirmTitle")}
      />
    </Card>
  );
}

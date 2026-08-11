"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { CursorPagination } from "@omniflow/ui/pagination";
import { SkeletonTable } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { ShieldCheck, ShieldOff } from "lucide-react";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { RequirePermission, StateNotice } from "@/components/admin/state-notice";
import { ApiError, apiFetch, fetcher, toQuery } from "@/lib/api";
import { useSession } from "@/lib/session";
import { useUrlFilters } from "@/lib/use-url-filters";

type Operator = {
  id: string;
  email: string;
  displayName: string;
  status: "active" | "suspended" | "disabled";
  roles: string[];
  totpEnabled: boolean;
  lastLoginAt?: string;
};

type OperatorPage = { items: Operator[]; nextCursor?: string };

export default function OperatorsPage() {
  return (
    <RequirePermission permissions={["admins.read"]}>
      <OperatorList />
    </RequirePermission>
  );
}

function OperatorList() {
  const translate = useTranslations("admin.operators");
  // Role names are shared across the panel, so they live one level up.
  const translateRole = useTranslations("admin.roles");
  const { can, session } = useSession();
  const { filters, cursorStack, goNext, goPrevious, hasPrevious } = useUrlFilters([]);
  const [pendingSuspend, setPendingSuspend] = useState<Operator | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const query = toQuery({ cursor: filters.cursor, pageSize: 25 });
  const { data, error, isLoading, isValidating, mutate } = useSWR<OperatorPage, ApiError>(
    `/v1/panel/admins${query}`,
    fetcher,
    { keepPreviousData: true },
  );

  const operators = data?.items ?? [];

  async function changeStatus(operator: Operator, status: Operator["status"]) {
    setSubmitting(true);
    try {
      await apiFetch(`/v1/panel/admins/${operator.id}/status`, {
        body: JSON.stringify({ status }),
        method: "POST",
      });
      toast.success(
        status === "active"
          ? translate("toasts.reinstated", { name: operator.displayName })
          : translate("toasts.suspended", { name: operator.displayName }),
      );
      await mutate();
      setPendingSuspend(null);
    } catch (caught) {
      // The server refuses to leave an installation with no active owner. That
      // is the one failure an operator most needs stated plainly.
      if (caught instanceof ApiError && caught.code === "last_owner") {
        toast.error(translate("toasts.lastOwner"));
      } else {
        toast.error(translate("toasts.statusFailed"));
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <header className="flex flex-col gap-1">
        <h1 className="font-bold text-2xl tracking-tight">{translate("title")}</h1>
        <p className="max-w-2xl text-muted-foreground text-sm">{translate("description")}</p>
      </header>

      <Card className="overflow-hidden">
        {isLoading && !data ? (
          <SkeletonTable columns={4} rows={6} />
        ) : error ? (
          <div className="p-6">
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
        ) : operators.length === 0 ? (
          <div className="p-6">
            <StateNotice description={translate("emptyDescription")} title={translate("empty")} />
          </div>
        ) : (
          <>
            <div aria-busy={isValidating || undefined} className={isValidating ? "opacity-60" : ""}>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{translate("columns.operator")}</TableHead>
                    <TableHead>{translate("columns.roles")}</TableHead>
                    <TableHead>{translate("columns.status")}</TableHead>
                    <TableHead>{translate("columns.twoFactor")}</TableHead>
                    {can("admins.write") && <TableHead className="w-px" />}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {operators.map((operator) => {
                    const isSelf = operator.id === session?.account.id;
                    return (
                      <TableRow key={operator.id}>
                        <TableCell>
                          <div className="flex flex-col">
                            <span className="font-medium">{operator.displayName}</span>
                            <span className="font-mono text-[11px] text-subtle-foreground">
                              {operator.email}
                            </span>
                          </div>
                        </TableCell>
                        <TableCell>
                          <div className="flex flex-wrap gap-1">
                            {operator.roles.map((role) => (
                              <Badge key={role} variant="neutral">
                                {translateRole(role)}
                              </Badge>
                            ))}
                          </div>
                        </TableCell>
                        <TableCell>
                          <Badge variant={operator.status === "active" ? "success" : "warning"}>
                            {translate(`statuses.${operator.status}`)}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          {operator.totpEnabled ? (
                            <span className="inline-flex items-center gap-1.5 text-success text-xs">
                              <ShieldCheck className="size-3.5" />
                              {translate("twoFactor.on")}
                            </span>
                          ) : (
                            <span className="inline-flex items-center gap-1.5 text-muted-foreground text-xs">
                              <ShieldOff className="size-3.5" />
                              {translate("twoFactor.off")}
                            </span>
                          )}
                        </TableCell>
                        {can("admins.write") && (
                          <TableCell>
                            {/*
                              Suspending yourself would end your own session
                              mid-action, so the control is not offered.
                            */}
                            {!isSelf &&
                              (operator.status === "active" ? (
                                <Button
                                  onClick={() => setPendingSuspend(operator)}
                                  size="sm"
                                  variant="outline"
                                >
                                  {translate("actions.suspend")}
                                </Button>
                              ) : (
                                <Button
                                  disabled={submitting}
                                  onClick={() => changeStatus(operator, "active")}
                                  size="sm"
                                  variant="outline"
                                >
                                  {translate("actions.reinstate")}
                                </Button>
                              ))}
                          </TableCell>
                        )}
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            </div>

            <div className="border-border border-t px-2">
              <CursorPagination
                hasNext={Boolean(data?.nextCursor)}
                hasPrevious={hasPrevious}
                labels={{ next: translate("next"), previous: translate("previous") }}
                onNext={() => goNext(data?.nextCursor)}
                onPrevious={goPrevious}
                pending={isValidating}
                summary={translate("shown", {
                  count: operators.length,
                  page: cursorStack.length + 1,
                })}
              />
            </div>
          </>
        )}
      </Card>

      {/*
        Suspension ends every one of that operator's live sessions immediately,
        so it is gated behind typing their name rather than a single click.
      */}
      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("actions.suspend")}
        confirmationPhrase={pendingSuspend?.displayName}
        confirmationPrompt={translate("confirm.prompt", {
          name: pendingSuspend?.displayName ?? "",
        })}
        description={translate("confirm.description")}
        destructive
        onConfirm={() => pendingSuspend && changeStatus(pendingSuspend, "suspended")}
        onOpenChange={(open) => !open && setPendingSuspend(null)}
        open={Boolean(pendingSuspend)}
        pending={submitting}
        title={translate("confirm.title")}
      />
    </div>
  );
}

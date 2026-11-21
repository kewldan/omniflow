"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";
import { useEnumLabel } from "@/components/admin/enum-label";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, type PlanSummary, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type Controls = {
  restrictions: { kind: string; reason: string; createdAt: string }[];
  autoRenew?: { enabled: boolean; consented: boolean; provider?: string; funding: string };
  lastActivityAt?: string;
};

const RESTRICTION_KINDS = ["topup", "purchase", "support"] as const;

/**
 * The operator controls on the customer card: give days, a trial, or another
 * plan without a charge; buy a plan with the customer's own balance; stop top-
 * ups or purchases with a reason; switch auto-renew; reset the trial; and the
 * irreversible actions, anonymise and delete. Every button is behind the
 * permission the API checks, and every action is audited there.
 */
export function CustomerControlsPanel({ customerId }: { customerId: string }) {
  const translate = useTranslations("admin.customers.controls");
  const { can } = useSession();
  const base = `/v1/panel/customers/${customerId}`;
  const { data, mutate } = useSWR<Controls, ApiError>(`${base}/controls`, fetcher);
  const { data: plans } = useSWR<Listing<PlanSummary>, ApiError>(
    can("catalog.read") ? "/v1/panel/catalog/plans" : null,
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const ids = { mode: useId(), plan: useId(), days: useId(), reason: useId() };
  const [mode, setMode] = useState("days");
  const [planId, setPlanId] = useState("");
  const [days, setDays] = useState("30");
  const [reasons, setReasons] = useState<Record<string, string>>({});
  const [confirm, setConfirm] = useState<"anonymize" | "deletion" | null>(null);
  const livePlans = (plans?.items ?? []).filter((plan) => !plan.archivedAt);

  async function act(path: string, body: unknown, method = "POST", done = translate("done")) {
    if (await run(`${base}${path}`, { method, body, reason: "customer card" })) {
      toast.success(done);
      await mutate();
      return true;
    }
    return false;
  }

  return (
    <div className="flex flex-col gap-4">
      <Card className="flex flex-wrap items-center gap-3 p-4 text-sm">
        <span className="text-muted-foreground">{translate("lastActivity")}</span>
        {data?.lastActivityAt ? <Timestamp value={data.lastActivityAt} /> : translate("never")}
      </Card>

      {can("subscriptions.write") && (
        <Card className="flex flex-col gap-3 p-4">
          <div className="font-medium">{translate("grant.title")}</div>
          <p className="text-muted-foreground text-sm">{translate("grant.description")}</p>
          <div className="grid gap-3 sm:grid-cols-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.mode}>{translate("grant.mode")}</Label>
              <Select onValueChange={setMode} value={mode}>
                <SelectTrigger id={ids.mode}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {["days", "trial", "change_plan"].map((value) => (
                    <SelectItem key={value} value={value}>
                      {translate(`grant.modes.${value}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.plan}>{translate("grant.plan")}</Label>
              <Select onValueChange={setPlanId} value={planId}>
                <SelectTrigger id={ids.plan}>
                  <SelectValue
                    placeholder={translate(mode === "trial" ? "grant.trialPlan" : "grant.pickPlan")}
                  />
                </SelectTrigger>
                <SelectContent>
                  {livePlans.map((plan) => (
                    <SelectItem key={plan.id} value={plan.id}>
                      {plan.code}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {mode !== "change_plan" && (
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.days}>{translate("grant.days")}</Label>
                <Input
                  id={ids.days}
                  inputMode="numeric"
                  onChange={(event) => setDays(event.target.value)}
                  value={days}
                />
              </div>
            )}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={pending || (mode !== "trial" && !planId)}
              onClick={() =>
                act("/grants", {
                  mode,
                  planId: planId || undefined,
                  days: Number(days) || 0,
                  idempotencyKey: crypto.randomUUID(),
                })
              }
            >
              {translate("grant.submit")}
            </Button>
            {can("finance.write") && (
              <Button
                disabled={pending || !planId}
                onClick={() =>
                  act("/purchases", {
                    planId,
                    operation: "purchase",
                    idempotencyKey: crypto.randomUUID(),
                  })
                }
                variant="outline"
              >
                {translate("grant.buyFromBalance")}
              </Button>
            )}
            <Button
              disabled={pending}
              onClick={() => act("/trial-reset", {}, "POST", translate("trialReset"))}
              variant="ghost"
            >
              {translate("resetTrial")}
            </Button>
          </div>
        </Card>
      )}

      {can("customers.write") && (
        <Card className="flex flex-col gap-3 p-4">
          <div className="font-medium">{translate("restrictions.title")}</div>
          <p className="text-muted-foreground text-sm">{translate("restrictions.description")}</p>
          {RESTRICTION_KINDS.map((kind) => {
            const current = data?.restrictions.find((item) => item.kind === kind);
            return (
              <div className="flex flex-wrap items-end gap-2" key={kind}>
                <div className="flex min-w-48 flex-1 flex-col gap-1">
                  <Label htmlFor={`${ids.reason}-${kind}`}>
                    {translate(`restrictions.${kind}`)}
                  </Label>
                  {current ? (
                    <span className="text-sm">
                      <Badge variant="warning">{translate("restrictions.active")}</Badge>{" "}
                      {current.reason}
                    </span>
                  ) : (
                    <Input
                      id={`${ids.reason}-${kind}`}
                      onChange={(event) => setReasons({ ...reasons, [kind]: event.target.value })}
                      placeholder={translate("restrictions.reason")}
                      value={reasons[kind] ?? ""}
                    />
                  )}
                </div>
                {current ? (
                  <Button
                    disabled={pending}
                    onClick={() =>
                      act(`/restrictions/${kind}`, { restricted: false, reason: "" }, "PUT")
                    }
                    size="sm"
                    variant="outline"
                  >
                    {translate("restrictions.lift")}
                  </Button>
                ) : (
                  <Button
                    disabled={pending || !(reasons[kind] ?? "").trim()}
                    onClick={() =>
                      act(
                        `/restrictions/${kind}`,
                        { restricted: true, reason: reasons[kind] },
                        "PUT",
                      )
                    }
                    size="sm"
                  >
                    {translate("restrictions.set")}
                  </Button>
                )}
              </div>
            );
          })}
        </Card>
      )}

      {can("subscriptions.write") && data?.autoRenew && (
        <Card className="flex flex-col gap-2 p-4">
          <div className="flex items-center gap-3">
            <Switch
              checked={data.autoRenew.enabled}
              disabled={pending || (!data.autoRenew.enabled && !data.autoRenew.consented)}
              id={`${ids.mode}-renew`}
              onCheckedChange={(enabled) => act("/auto-renew", { enabled }, "PUT")}
            />
            <Label htmlFor={`${ids.mode}-renew`}>{translate("autoRenew.label")}</Label>
          </div>
          <p className="text-muted-foreground text-xs">{translate("autoRenew.hint")}</p>
        </Card>
      )}

      {can("customers.write") && (
        <Card className="flex flex-col gap-3 border-destructive/40 p-4">
          <div className="font-medium">{translate("danger.title")}</div>
          <p className="text-muted-foreground text-sm">{translate("danger.description")}</p>
          <div className="flex flex-wrap gap-2">
            <Button onClick={() => setConfirm("anonymize")} variant="destructive">
              {translate("danger.anonymize")}
            </Button>
            <Button onClick={() => setConfirm("deletion")} variant="outline">
              {translate("danger.delete")}
            </Button>
          </div>
        </Card>
      )}

      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error.message}
        </p>
      )}

      <ConfirmDialog
        cancelLabel={translate("cancel")}
        confirmLabel={translate(confirm === "anonymize" ? "danger.anonymize" : "danger.delete")}
        confirmationPhrase={confirm === "anonymize" ? "ANONYMIZE" : "DELETE"}
        confirmationPrompt={translate("danger.prompt")}
        description={translate(
          confirm === "anonymize" ? "danger.anonymizeWarning" : "danger.deleteWarning",
        )}
        destructive
        onConfirm={() => {
          const action = confirm;
          setConfirm(null);
          if (action) {
            act(`/${action === "anonymize" ? "anonymize" : "deletion"}`, undefined);
          }
        }}
        onOpenChange={(open) => !open && setConfirm(null)}
        open={confirm !== null}
        title={translate("danger.title")}
      />
    </div>
  );
}

/** The customer's own action log: which screens and buttons, when. */
export function ActivityPanel({ customerId, active }: { customerId: string; active: boolean }) {
  const translate = useTranslations("admin.customers.controls");
  const label = useEnumLabel();
  const { data } = useSWR<
    { items: { surface: string; action: string; occurredAt: string }[] },
    ApiError
  >(active ? `/v1/panel/customers/${customerId}/activity` : null, fetcher);
  const items = data?.items ?? [];
  if (items.length === 0) {
    return <p className="text-muted-foreground text-sm">{translate("activityEmpty")}</p>;
  }
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{translate("activityWhen")}</TableHead>
          <TableHead>{translate("activitySurface")}</TableHead>
          <TableHead>{translate("activityAction")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {items.map((item) => (
          <TableRow key={`${item.occurredAt}-${item.surface}-${item.action}`}>
            <TableCell>
              <Timestamp value={item.occurredAt} />
            </TableCell>
            <TableCell>{label("surface", item.surface)}</TableCell>
            <TableCell className="font-mono text-xs">{item.action}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

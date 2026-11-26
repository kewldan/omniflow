"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { useTranslations } from "next-intl";
import { useState } from "react";

import { ReasonDialog } from "@/components/admin/reason-dialog";
import { FormField } from "@/components/forms/field";
import { type PlanSummary, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

/**
 * The plan-level settings that change what a customer on the plan gets,
 * saved apart from visibility and ordering.
 *
 * They apply to every version of the plan, so they are not part of the
 * version editor: a tag changes nothing about the price a customer paid.
 */
export function PlanOptionsEditor({ onSaved, plan }: { onSaved: () => void; plan: PlanSummary }) {
  const translate = useTranslations("admin.catalog.planOptions");
  const translateForm = useTranslations("common.form");
  const [tag, setTag] = useState(plan.remnawaveTag ?? "");
  const { can } = useSession();
  const { run, pending, error } = useOperatorAction();

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <FormField hint={translate("tagHint")} label={translate("tag")}>
          {(props) => (
            <Input
              {...props}
              maxLength={16}
              onChange={(event) => setTag(event.target.value.toUpperCase())}
              value={tag}
            />
          )}
        </FormField>
        {error && (
          <p className="text-danger-foreground text-sm" role="alert">
            {error.message}
          </p>
        )}
        <Button
          className="self-start"
          disabled={pending}
          onClick={async () => {
            const ok = await run(`/v1/panel/catalog/plans/${plan.id}/options`, {
              body: { remnawaveTag: tag.trim() },
              method: "PUT",
            });
            if (ok) {
              onSaved();
            }
          }}
          size="sm"
        >
          {pending ? translateForm("saving") : translate("save")}
        </Button>
        {can("subscriptions.write") && <ApplySquads planId={plan.id} />}
      </CardContent>
    </Card>
  );
}

/**
 * Moves the plan's live subscriptions onto its newest version's squads.
 *
 * A new version only reaches new purchases; this is the deliberate step that
 * takes existing customers along. A customer's own choices and squad add-ons
 * are kept, and pressing it twice queues nothing more.
 */
function ApplySquads({ planId }: { planId: string }) {
  const translate = useTranslations("admin.catalog.planOptions");
  const presets = useTranslations("admin.reasons.planApplySquads");
  const [confirming, setConfirming] = useState(false);
  const [applied, setApplied] = useState(false);
  const { run, pending, error, reset } = useOperatorAction();

  return (
    <div className="flex flex-col gap-2 border-border border-t pt-3">
      <p className="font-medium text-sm">{translate("applySquads")}</p>
      <p className="text-muted-foreground text-xs">{translate("applySquadsHint")}</p>
      {applied && (
        <p className="text-sm" role="status">
          {translate("applySquadsDone")}
        </p>
      )}
      <Button
        className="self-start"
        disabled={pending}
        onClick={() => {
          reset();
          setConfirming(true);
        }}
        size="sm"
        variant="outline"
      >
        {translate("applySquadsAction")}
      </Button>
      <ReasonDialog
        confirmLabel={translate("applySquadsAction")}
        description={translate("applySquadsHint")}
        error={error?.message}
        onConfirm={async (reason) => {
          const ok = await run(`/v1/panel/catalog/plans/${planId}/apply-squads`, {
            method: "POST",
            reason,
          });
          if (ok) {
            setConfirming(false);
            setApplied(true);
          }
        }}
        onOpenChange={setConfirming}
        open={confirming}
        pending={pending}
        presets={["newServers", "retiredSquad", "blockedLocation", "fixedVersion", "load"].map(
          (key) => presets(key),
        )}
        title={translate("applySquads")}
      />
    </div>
  );
}

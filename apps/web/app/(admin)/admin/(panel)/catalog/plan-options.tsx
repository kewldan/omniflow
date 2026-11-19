"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { useTranslations } from "next-intl";
import { useState } from "react";

import { FormField } from "@/components/forms/field";
import { type PlanSummary, useOperatorAction } from "@/lib/operations";

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
      </CardContent>
    </Card>
  );
}

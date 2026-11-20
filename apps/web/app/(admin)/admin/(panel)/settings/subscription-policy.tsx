"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Checkbox } from "@omniflow/ui/checkbox";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { FormField } from "@/components/forms/field";
import { type ApiError, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";

/** The installation-wide rules for what a paid change does to a subscription. */
export type SubscriptionPolicy = {
  upgradesEnabled: boolean;
  downgradesEnabled: boolean;
  resetTrafficOnRenewal: boolean;
  resetTrafficOnPlanChange: boolean;
  resetDevicesOnRenewal: boolean;
  trialRemainder: string;
  trialTag: string;
  paidTag: string;
  trialDisabledMethods?: string[];
  trialWarningHours?: number;
  maxDeviceLimit?: number | null;
  updatedAt?: string;
};

const TRIAL_REMAINDER = ["policy", "carry", "reset"] as const;

/** Free-text fields: Remnawave user tags, upper-cased as the panel requires. */
type TextField = "trialTag" | "paidTag";
const TEXT_FIELDS: TextField[] = ["trialTag", "paidTag"];

/** The ways of signing in a trial can be refused by. */
const SIGN_IN_METHODS = ["telegram", "email", "oidc"] as const;

type Toggle =
  | "upgradesEnabled"
  | "downgradesEnabled"
  | "resetTrafficOnRenewal"
  | "resetTrafficOnPlanChange"
  | "resetDevicesOnRenewal";

const TOGGLES: Toggle[] = [
  "upgradesEnabled",
  "downgradesEnabled",
  "resetTrafficOnRenewal",
  "resetTrafficOnPlanChange",
  "resetDevicesOnRenewal",
];

/**
 * Plan changes and what a paid period resets, for the whole catalogue.
 *
 * A plan version's own upgrade and downgrade policies still apply; these sit
 * above them. The server re-reads them when an order is priced and when it
 * settles, so this card decides behaviour rather than only describing it.
 */
export function SubscriptionPolicyCard({ editable }: { editable: boolean }) {
  const { data, isLoading, mutate } = useSWR<SubscriptionPolicy, ApiError>(
    "/v1/panel/settings/subscription-policy",
    fetcher,
  );
  if (isLoading || !data) {
    return <Skeleton className="h-48 w-full" />;
  }
  return (
    <PolicyForm editable={editable} key={data.updatedAt} onSaved={() => mutate()} policy={data} />
  );
}

function PolicyForm({
  editable,
  onSaved,
  policy,
}: {
  editable: boolean;
  onSaved: () => void;
  policy: SubscriptionPolicy;
}) {
  const translate = useTranslations("admin.settings.subscriptionPolicy");
  const translateSettings = useTranslations("admin.settings");
  const [draft, setDraft] = useState<SubscriptionPolicy>(policy);
  const [hours, setHours] = useState(String(policy.trialWarningHours ?? 0));
  const [maxDevices, setMaxDevices] = useState(String(policy.maxDeviceLimit ?? ""));
  const { run, pending, error } = useOperatorAction();
  const remainderId = useId();

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {TOGGLES.map((toggle) => (
          <PolicySwitch
            checked={draft[toggle]}
            disabled={!editable}
            hint={translate(`${toggle}Hint`)}
            key={toggle}
            label={translate(toggle)}
            onChange={(checked) => setDraft((current) => ({ ...current, [toggle]: checked }))}
          />
        ))}
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={remainderId}>{translate("trialRemainder")}</Label>
          <Select
            disabled={!editable}
            onValueChange={(value) =>
              setDraft((current) => ({ ...current, trialRemainder: value }))
            }
            value={draft.trialRemainder}
          >
            <SelectTrigger id={remainderId}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {TRIAL_REMAINDER.map((value) => (
                <SelectItem key={value} value={value}>
                  {translate(`trialRemainderValue.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <span className="text-muted-foreground text-xs">{translate("trialRemainderHint")}</span>
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          {TEXT_FIELDS.map((field) => (
            <FormField hint={translate(`${field}Hint`)} key={field} label={translate(field)}>
              {(props) => (
                <Input
                  {...props}
                  maxLength={16}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      [field]: event.target.value.toUpperCase(),
                    }))
                  }
                  readOnly={!editable}
                  value={draft[field]}
                />
              )}
            </FormField>
          ))}
        </div>
        <fieldset className="flex flex-col gap-2">
          <legend className="font-medium text-sm">{translate("trialDisabledMethods")}</legend>
          <div className="flex flex-wrap gap-4">
            {SIGN_IN_METHODS.map((method) => (
              <MethodCheckbox
                checked={(draft.trialDisabledMethods ?? []).includes(method)}
                disabled={!editable}
                key={method}
                label={translate(`signInMethod.${method}`)}
                onChange={(checked) =>
                  setDraft((current) => {
                    const methods = (current.trialDisabledMethods ?? []).filter(
                      (entry) => entry !== method,
                    );
                    return {
                      ...current,
                      trialDisabledMethods: checked ? [...methods, method] : methods,
                    };
                  })
                }
              />
            ))}
          </div>
          <span className="text-muted-foreground text-xs">
            {translate("trialDisabledMethodsHint")}
          </span>
        </fieldset>
        <FormField hint={translate("maxDeviceLimitHint")} label={translate("maxDeviceLimit")}>
          {(props) => (
            <Input
              {...props}
              inputMode="numeric"
              onChange={(event) => setMaxDevices(event.target.value)}
              readOnly={!editable}
              value={maxDevices}
            />
          )}
        </FormField>
        <FormField hint={translate("trialWarningHoursHint")} label={translate("trialWarningHours")}>
          {(props) => (
            <Input
              {...props}
              inputMode="numeric"
              onChange={(event) => setHours(event.target.value)}
              readOnly={!editable}
              value={hours}
            />
          )}
        </FormField>
        {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
        {editable && (
          <Button
            className="self-start"
            disabled={pending}
            onClick={async () => {
              const ok = await run("/v1/panel/settings/subscription-policy", {
                body: {
                  ...draft,
                  maxDeviceLimit:
                    Number.parseInt(maxDevices, 10) > 0 ? Number.parseInt(maxDevices, 10) : null,
                  trialWarningHours: Number.parseInt(hours || "0", 10) || 0,
                },
                method: "PUT",
              });
              if (ok) {
                onSaved();
              }
            }}
            size="sm"
          >
            {translateSettings("save")}
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

function PolicySwitch({
  checked,
  disabled,
  hint,
  label,
  onChange,
}: {
  checked: boolean;
  disabled: boolean;
  hint: string;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-start gap-3">
      <Switch checked={checked} disabled={disabled} id={id} onCheckedChange={onChange} />
      <div className="flex flex-col gap-0.5">
        <Label htmlFor={id}>{label}</Label>
        <span className="text-muted-foreground text-xs">{hint}</span>
      </div>
    </div>
  );
}

function MethodCheckbox({
  checked,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  disabled: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-center gap-2">
      <Checkbox
        checked={checked}
        disabled={disabled}
        id={id}
        onCheckedChange={(state) => onChange(state === true)}
      />
      <Label htmlFor={id}>{label}</Label>
    </div>
  );
}

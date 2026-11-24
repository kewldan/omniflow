"use client";

import { Button } from "@omniflow/ui/button";
import { Checkbox } from "@omniflow/ui/checkbox";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { useTranslations } from "next-intl";
import { useId } from "react";

import { MinorAmountInput } from "@/components/forms/minor-amount-input";

import type { ReferralProgram } from "./types";

const percent = (bps?: number | null) => (bps ? String(bps / 100) : "");
const bps = (value: string) => Math.round((Number(value.replace(",", ".")) || 0) * 100);

/**
 * The commission half of the referral programme: a share of every payment a
 * referee makes, a separate rate for the first one, rates that grow with the
 * number of friends invited, a cap on how many payments earn, a minimum
 * payment, and which payments count.
 */
export function ReferralCommissionFields({
  editable,
  program,
  update,
}: {
  editable: boolean;
  program: ReferralProgram;
  update: (patch: Partial<ReferralProgram>) => void;
}) {
  const translate = useTranslations("admin.marketing.referrals.commission");
  const translateReferrals = useTranslations("admin.marketing.referrals");
  const ids = { rate: useId(), first: useId(), cap: useId(), minimum: useId() };
  const triggers = program.commissionTriggers ?? ["subscription"];
  const tiers = program.commissionTiers ?? [];

  function toggleTrigger(trigger: string, on: boolean) {
    const next = on
      ? [...new Set([...triggers, trigger])]
      : triggers.filter((item) => item !== trigger);
    update({ commissionTriggers: next });
  }

  return (
    <fieldset className="flex flex-col gap-3 rounded-md border bg-well p-3">
      <legend className="px-1 font-medium text-sm">{translate("title")}</legend>
      <p className="text-muted-foreground text-xs">{translate("hint")}</p>
      <div className="grid gap-3 sm:grid-cols-4">
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.rate}>{translate("rate")}</Label>
          <Input
            disabled={!editable}
            id={ids.rate}
            inputMode="decimal"
            onChange={(event) => update({ commissionBps: bps(event.target.value) })}
            value={percent(program.commissionBps)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.first}>{translate("first")}</Label>
          <Input
            disabled={!editable}
            id={ids.first}
            inputMode="decimal"
            onChange={(event) =>
              update({
                firstPaymentCommissionBps:
                  event.target.value.trim() === "" ? undefined : bps(event.target.value),
              })
            }
            placeholder={translate("sameAsRate")}
            value={percent(program.firstPaymentCommissionBps)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.cap}>{translate("cap")}</Label>
          <Input
            disabled={!editable}
            id={ids.cap}
            inputMode="numeric"
            onChange={(event) =>
              update({
                commissionPaymentCap:
                  Number(event.target.value) > 0 ? Number(event.target.value) : undefined,
              })
            }
            placeholder={translate("noCap")}
            value={program.commissionPaymentCap ? String(program.commissionPaymentCap) : ""}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.minimum}>{translate("minimum")}</Label>
          <MinorAmountInput
            currency={program.currency}
            disabled={!editable}
            id={ids.minimum}
            onChange={(event) =>
              update({ commissionMinPaymentMinor: Number(event.target.value) || 0 })
            }
            value={String(program.commissionMinPaymentMinor ?? 0)}
          />
        </div>
      </div>
      <TriggerBox
        checked={Boolean(program.allowRewardChoice)}
        disabled={!editable}
        label={translateReferrals("allowRewardChoice")}
        onChange={(allowRewardChoice) => update({ allowRewardChoice })}
      />
      <div className="flex flex-wrap gap-4">
        {["subscription", "topup"].map((trigger) => (
          <TriggerBox
            checked={triggers.includes(trigger)}
            disabled={!editable}
            key={trigger}
            label={translate(`triggers.${trigger}`)}
            onChange={(on) => toggleTrigger(trigger, on)}
          />
        ))}
      </div>
      <div className="flex flex-col gap-2">
        <span className="text-sm">{translate("tiers")}</span>
        {tiers.map((tier, index) => (
          <div className="flex flex-wrap items-center gap-2" key={`tier-${tier.referrals}`}>
            <Input
              aria-label={translate("tierReferrals")}
              className="w-28"
              disabled={!editable}
              inputMode="numeric"
              onChange={(event) =>
                update({
                  commissionTiers: tiers.map((item, at) =>
                    at === index ? { ...item, referrals: Number(event.target.value) || 1 } : item,
                  ),
                })
              }
              value={String(tier.referrals)}
            />
            <span className="text-muted-foreground text-xs">{translate("tierArrow")}</span>
            <Input
              aria-label={translate("tierRate")}
              className="w-24"
              disabled={!editable}
              inputMode="decimal"
              onChange={(event) =>
                update({
                  commissionTiers: tiers.map((item, at) =>
                    at === index ? { ...item, bps: bps(event.target.value) } : item,
                  ),
                })
              }
              value={percent(tier.bps)}
            />
            <span className="text-muted-foreground text-xs">%</span>
            {editable && (
              <Button
                onClick={() => update({ commissionTiers: tiers.filter((_, at) => at !== index) })}
                size="sm"
                type="button"
                variant="outline"
              >
                {translate("removeTier")}
              </Button>
            )}
          </div>
        ))}
        {editable && (
          <Button
            className="self-start"
            onClick={() => update({ commissionTiers: [...tiers, { referrals: 10, bps: 1500 }] })}
            size="sm"
            type="button"
            variant="outline"
          >
            {translate("addTier")}
          </Button>
        )}
      </div>
    </fieldset>
  );
}

function TriggerBox({
  checked,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  disabled: boolean;
  label: string;
  onChange: (value: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-center gap-2">
      <Checkbox
        checked={checked}
        disabled={disabled}
        id={id}
        onCheckedChange={(value) => onChange(value === true)}
      />
      <Label htmlFor={id}>{label}</Label>
    </div>
  );
}

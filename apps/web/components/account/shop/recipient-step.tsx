"use client";

import { Button } from "@omniflow/ui/button";
import { Checkbox } from "@omniflow/ui/checkbox";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Switch } from "@omniflow/ui/switch";
import { BadgeCheck, TriangleAlert } from "lucide-react";
import { useTranslations } from "next-intl";
import { type FormEvent, useId, useState } from "react";

import { useShopProblem } from "@/components/account/shop/problem";
import type { ShopRecipient } from "@/components/account/shop/types";
import { apiFetch } from "@/lib/api";

/** The handle the customer has looked at and agreed to, exactly as it will be sent. */
export type ConfirmedRecipient = { handle: string; checked: boolean };

/**
 * The recipient step.
 *
 * This is the one place in the panel where a typo cannot be undone by anybody,
 * including an operator: the gateway that fronts Fragment sends the goods to
 * the string it is given and there is no route back. So a typed username never
 * reaches payment directly. It is normalised by the server first — pasted
 * links, a leading @, a trailing slash all fall away — and the exact string
 * that will be handed to the gateway is shown back and has to be agreed to on
 * its own.
 *
 * The confirmation is a checkbox naming that string rather than a general "I
 * agree", because what is being confirmed is this handle and not the purchase.
 * Editing the field afterwards drops the agreement, since it was about the
 * previous string.
 *
 * `checked: false` is the normal answer today: no shipping adapter can ask
 * Telegram whether a handle exists. The copy says that plainly instead of
 * implying a verification that did not happen.
 */
export function RecipientStep({
  confirmed,
  disabled,
  forSelf,
  onConfirmedChange,
  onForSelfChange,
  productId,
}: {
  confirmed: ConfirmedRecipient | null;
  disabled: boolean;
  forSelf: boolean;
  onConfirmedChange: (recipient: ConfirmedRecipient | null) => void;
  onForSelfChange: (forSelf: boolean) => void;
  productId: string;
}) {
  const translate = useTranslations("account.shop");
  const describe = useShopProblem();
  const fieldId = useId();
  const confirmId = useId();
  const selfId = useId();

  const [input, setInput] = useState("");
  const [reviewed, setReviewed] = useState<ShopRecipient | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setReviewed(null);
    setError(null);
    onConfirmedChange(null);
  }

  async function review(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const result = await apiFetch<ShopRecipient>("/v1/account/shop/recipient", {
        body: JSON.stringify({ productId, recipient: input }),
        method: "POST",
      });
      setReviewed(result);
      // A fresh review is never pre-agreed. The customer has to look at the
      // normalised handle and say yes to it, which is the entire point of the
      // step, so the confirmation always starts empty.
      onConfirmedChange(null);
    } catch (reviewError) {
      setReviewed(null);
      onConfirmedChange(null);
      setError(describe(reviewError));
    } finally {
      setBusy(false);
    }
  }

  const agreed = Boolean(reviewed && confirmed && confirmed.handle === reviewed.recipient);

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <h2 className="font-semibold text-[15px] tracking-[-0.01em]">
        {translate("recipient.title")}
      </h2>

      <form className="space-y-2" onSubmit={review}>
        <Label htmlFor={fieldId}>{translate("recipient.label")}</Label>
        <div className="flex gap-2">
          <Input
            aria-describedby={error ? `${fieldId}-error` : undefined}
            aria-invalid={error ? true : undefined}
            autoCapitalize="none"
            autoComplete="off"
            disabled={disabled || busy}
            id={fieldId}
            onChange={(event) => {
              setInput(event.target.value);
              if (reviewed || confirmed) {
                reset();
              }
            }}
            placeholder={translate("recipient.placeholder")}
            spellCheck={false}
            value={input}
          />
          <Button disabled={disabled || busy || input.trim() === ""} type="submit">
            {busy ? translate("recipient.checking") : translate("recipient.check")}
          </Button>
        </div>
        {error && (
          <p className="text-[12.5px] text-destructive leading-relaxed" id={`${fieldId}-error`}>
            {error}
          </p>
        )}
      </form>

      {reviewed && (
        <div className="space-y-3 rounded-md border border-border bg-background p-3">
          <div>
            <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
              {translate("recipient.reviewed")}
            </p>
            <p className="mt-1 break-all font-mono font-semibold text-[16px]">
              @{reviewed.recipient}
            </p>
          </div>

          <p className="flex items-start gap-2 text-[12.5px] text-muted-foreground leading-relaxed">
            {reviewed.checked ? (
              <BadgeCheck aria-hidden className="mt-0.5 size-4 shrink-0 text-success" />
            ) : (
              <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-warning" />
            )}
            {reviewed.checked ? translate("recipient.checked") : translate("recipient.unchecked")}
          </p>

          <p className="text-[12.5px] text-foreground leading-relaxed">
            {translate("recipient.warning")}
          </p>

          <div className="flex items-start gap-2.5">
            <Checkbox
              checked={agreed}
              className="mt-0.5"
              disabled={disabled}
              id={confirmId}
              onCheckedChange={(state) =>
                onConfirmedChange(
                  state === true ? { checked: reviewed.checked, handle: reviewed.recipient } : null,
                )
              }
            />
            <Label className="text-[13px] leading-snug" htmlFor={confirmId}>
              {translate("recipient.confirm", { handle: reviewed.recipient })}
            </Label>
          </div>
        </div>
      )}

      {/* Omniflow stores no Telegram handle for a web customer, so this cannot
          be derived and decides nothing about delivery. It annotates the order
          for the customer's own history, which is why it sits with the handle
          rather than near the price. */}
      <div className="flex items-center gap-2.5">
        <Switch
          checked={forSelf}
          disabled={disabled}
          id={selfId}
          onCheckedChange={onForSelfChange}
        />
        <Label className="text-[13px]" htmlFor={selfId}>
          {translate("recipient.forSelf")}
        </Label>
      </div>
    </section>
  );
}

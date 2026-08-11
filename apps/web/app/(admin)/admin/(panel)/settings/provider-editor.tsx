"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";

import type { ProviderSettings } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";

/**
 * Credentials and capability controls for one payment provider.
 *
 * A secret that has been stored is never sent back to the browser — the list
 * reports only whether one is set. An empty field therefore means "leave what
 * is stored alone", not "clear it", which is why saving with both fields blank
 * is a valid way to change only the display order or the enabled flag.
 */
export function ProviderEditor({
  onSaved,
  provider,
}: {
  onSaved: () => void;
  provider: ProviderSettings;
}) {
  const translate = useTranslations("admin.settings");
  const merchantFieldId = useId();
  const orderFieldId = useId();
  const credentialsFieldId = useId();
  const webhookFieldId = useId();
  const enabledFieldId = useId();
  const reasonFieldId = useId();

  const [merchantId, setMerchantId] = useState(provider.merchantId);
  const [enabled, setEnabled] = useState(provider.enabled);
  const [displayOrder, setDisplayOrder] = useState(String(provider.displayOrder));
  const [credentials, setCredentials] = useState("");
  const [webhookSecret, setWebhookSecret] = useState("");
  const [reason, setReason] = useState("");
  const [probe, setProbe] = useState("");

  const { run, pending, error } = useOperatorAction();
  const base = `/v1/panel/settings/providers/${provider.provider}`;
  const ready = reason.trim().length > 0 && !pending;

  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={merchantFieldId}>{translate("providers.merchantId")}</Label>
          <Input
            id={merchantFieldId}
            onChange={(event) => setMerchantId(event.target.value)}
            value={merchantId}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={orderFieldId}>{translate("providers.displayOrder")}</Label>
          <Input
            id={orderFieldId}
            inputMode="numeric"
            onChange={(event) => setDisplayOrder(event.target.value)}
            value={displayOrder}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={credentialsFieldId}>{translate("providers.credentialsField")}</Label>
          <Input
            autoComplete="off"
            id={credentialsFieldId}
            onChange={(event) => setCredentials(event.target.value)}
            placeholder={translate(
              provider.credentialsSet ? "providers.keepStored" : "providers.credentialsHint",
            )}
            type="password"
            value={credentials}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={webhookFieldId}>{translate("providers.webhookSecret")}</Label>
          <Input
            autoComplete="off"
            id={webhookFieldId}
            onChange={(event) => setWebhookSecret(event.target.value)}
            placeholder={translate(
              provider.webhookSecretSet ? "providers.keepStored" : "providers.webhookHint",
            )}
            type="password"
            value={webhookSecret}
          />
        </div>
      </div>

      <div className="flex items-center gap-3">
        <Switch checked={enabled} id={enabledFieldId} onCheckedChange={setEnabled} />
        <Label htmlFor={enabledFieldId}>{translate("providers.enabledLabel")}</Label>
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={reasonFieldId}>{translate("providers.reason")}</Label>
        <Input
          id={reasonFieldId}
          onChange={(event) => setReason(event.target.value)}
          placeholder={translate("providers.reasonPlaceholder")}
          value={reason}
        />
      </div>

      <p className="text-muted-foreground text-xs">{translate("providers.secretNotice")}</p>
      {error && <p className="text-danger-foreground text-sm">{error.message}</p>}

      <div className="flex flex-wrap items-center gap-2">
        <Button
          disabled={!ready}
          onClick={async () => {
            const ok = await run(base, {
              body: {
                // An empty field is absence, not an instruction to clear: the
                // stored secret is untouched unless a replacement is typed.
                credentials: credentials === "" ? null : credentials,
                displayOrder: Number(displayOrder) || 0,
                enabled,
                merchantId: merchantId.trim(),
                webhookSecret: webhookSecret === "" ? null : webhookSecret,
              },
              method: "PUT",
              reason: reason.trim(),
            });
            if (ok) {
              setCredentials("");
              setWebhookSecret("");
              setReason("");
              onSaved();
            }
          }}
          size="sm"
        >
          {translate("providers.save")}
        </Button>

        <Button
          disabled={pending || !provider.credentialsSet}
          onClick={async () => {
            setProbe("");
            const ok = await run(`${base}/test`, {
              body: { merchantId: merchantId.trim() },
              method: "POST",
              reason: reason.trim() || translate("providers.testReason"),
            });
            setProbe(ok ? "done" : "");
            if (ok) {
              onSaved();
            }
          }}
          size="sm"
          variant="outline"
        >
          {translate("providers.test")}
        </Button>

        {/* The probe reads from the provider's API rather than creating
            anything, so an operator can run it as often as they like without
            producing a payment. */}
        <span className="text-muted-foreground text-xs">
          {probe === "done"
            ? translate("providers.testRecorded")
            : translate("providers.testExplains")}
        </span>
      </div>

      {provider.connectionErrorCode && (
        <p className="text-sm">
          <Badge variant="danger">
            {translate(`providers.probeError.${provider.connectionErrorCode}`)}
          </Badge>
        </p>
      )}

      <RecurringControl merchantId={merchantId} onSaved={onSaved} provider={provider} />
    </Card>
  );
}

/**
 * Records a recurring capability test for one merchant account.
 *
 * Two facts have to agree before automatic charging can be enabled: the adapter
 * must be able to bind a payment method at all, and this merchant account must
 * have been granted the capability by the provider. The second cannot be
 * discovered — a merchant either has recurring permission or does not — so it
 * is recorded as an operator attestation with a stored outcome, and the server
 * refuses to enable charging without a passing one.
 */
function RecurringControl({
  merchantId,
  onSaved,
  provider,
}: {
  merchantId: string;
  onSaved: () => void;
  provider: ProviderSettings;
}) {
  const translate = useTranslations("admin.settings");
  const reasonId = useId();
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();

  if (!provider.adapterRecurring) {
    return (
      <p className="text-muted-foreground text-xs">
        {translate("providers.recurringUnsupportedLong")}
      </p>
    );
  }

  async function record(passed: boolean, enable: boolean) {
    const ok = await run(`/v1/panel/settings/providers/${provider.provider}/recurring`, {
      body: { enable, merchantId: merchantId.trim(), passed },
      method: "POST",
      reason: reason.trim(),
    });
    if (ok) {
      setReason("");
      onSaved();
    }
  }

  const ready = reason.trim().length > 0 && !pending;

  return (
    <div className="flex flex-col gap-2 border-border border-t pt-3">
      <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {translate("providers.recurring")}
      </p>
      <p className="text-muted-foreground text-xs">{translate("providers.recurringExplains")}</p>
      <Input
        id={reasonId}
        onChange={(event) => setReason(event.target.value)}
        placeholder={translate("providers.recurringReasonPlaceholder")}
        value={reason}
      />
      {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
      <div className="flex flex-wrap gap-2">
        <Button disabled={!ready} onClick={() => record(true, true)} size="sm">
          {translate("providers.recurringEnable")}
        </Button>
        <Button disabled={!ready} onClick={() => record(true, false)} size="sm" variant="outline">
          {translate("providers.recurringPassOnly")}
        </Button>
        <Button
          disabled={!ready}
          onClick={() => record(false, false)}
          size="sm"
          variant="destructive"
        >
          {translate("providers.recurringFail")}
        </Button>
      </div>
    </div>
  );
}

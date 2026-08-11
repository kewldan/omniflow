"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { Fragment, useEffect, useId, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { type ApiError, fetcher } from "@/lib/api";
import {
  type CommerceSettings,
  type Listing,
  type ProviderSettings,
  useOperatorAction,
} from "@/lib/operations";
import { useSession } from "@/lib/session";
import { useUnsavedChanges } from "@/lib/use-unsaved-changes";

import { ProviderEditor } from "./provider-editor";

/**
 * Wallet top-up, subscription concurrency, and payment-provider status.
 *
 * These were environment variables until v0.7. The installation seeds the row
 * from its own environment the first time the panel starts, so an operator
 * upgrading finds the limits they already configured rather than schema
 * defaults — and from then on this page is the only thing that changes them.
 */
export function CommerceSettingsForm() {
  const translate = useTranslations("admin.settings");
  const { can } = useSession();

  const { data, isLoading, mutate } = useSWR<CommerceSettings, ApiError>(
    "/v1/panel/settings/commerce",
    fetcher,
  );

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      {isLoading || !data ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <>
          <TopUpCard editable={can("settings.write")} onSaved={() => mutate()} settings={data} />
          <SubscriptionCard
            editable={can("settings.write")}
            onSaved={() => mutate()}
            settings={data}
          />
        </>
      )}
      <ProviderCard />
    </div>
  );
}

function TopUpCard({
  editable,
  onSaved,
  settings,
}: {
  editable: boolean;
  onSaved: () => void;
  settings: CommerceSettings;
}) {
  const translate = useTranslations("admin.settings");
  const minimumId = useId();
  const maximumId = useId();
  const windowId = useId();
  const limitId = useId();
  const presetsId = useId();

  const [form, setForm] = useState({
    enabled: settings.topUp.enabled,
    maximum: String(settings.topUp.maximumMinor),
    minimum: String(settings.topUp.minimumMinor),
    presets: (settings.topUp.presetsMinor ?? []).join(", "),
    windowHours: String(Math.round(settings.topUp.windowSeconds / 3600)),
    windowLimit: String(settings.topUp.windowLimitMinor),
  });
  const [dirty, setDirty] = useState(false);
  const { run, pending, error } = useOperatorAction();

  // A half-typed limit that the operator navigates away from is a limit that
  // was never applied, so the browser asks before the page is abandoned.
  useUnsavedChanges(dirty, translate("unsavedChanges"));

  useEffect(() => {
    setDirty(false);
  }, []);

  function update(patch: Partial<typeof form>) {
    setForm((current) => ({ ...current, ...patch }));
    setDirty(true);
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("topUp.title")}</CardTitle>
        <CardDescription>{translate("topUp.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex items-center gap-3">
          <Switch
            checked={form.enabled}
            disabled={!editable}
            id="topup-enabled"
            onCheckedChange={(enabled) => update({ enabled })}
          />
          <Label htmlFor="topup-enabled">{translate("topUp.enabled")}</Label>
          <Badge variant="neutral">{settings.topUp.currency}</Badge>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <Field
            id={minimumId}
            label={translate("topUp.minimum")}
            onChange={(minimum) => update({ minimum })}
            readOnly={!editable}
            value={form.minimum}
          />
          <Field
            id={maximumId}
            label={translate("topUp.maximum")}
            onChange={(maximum) => update({ maximum })}
            readOnly={!editable}
            value={form.maximum}
          />
          <Field
            id={windowId}
            label={translate("topUp.windowHours")}
            onChange={(windowHours) => update({ windowHours })}
            readOnly={!editable}
            value={form.windowHours}
          />
          <Field
            id={limitId}
            label={translate("topUp.windowLimit")}
            onChange={(windowLimit) => update({ windowLimit })}
            readOnly={!editable}
            value={form.windowLimit}
          />
        </div>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={presetsId}>{translate("topUp.presets")}</Label>
          <Input
            id={presetsId}
            onChange={(event) => update({ presets: event.target.value })}
            placeholder="10000, 30000, 50000"
            readOnly={!editable}
            value={form.presets}
          />
          <p className="text-muted-foreground text-xs">{translate("topUp.presetsHint")}</p>
        </div>

        {error && <p className="text-danger-foreground text-sm">{error.message}</p>}

        {editable && (
          <Button
            className="self-start"
            disabled={pending || !dirty}
            onClick={async () => {
              const ok = await run("/v1/panel/settings/commerce/topup", {
                body: {
                  currency: settings.topUp.currency,
                  enabled: form.enabled,
                  maximumMinor: Number(form.maximum),
                  minimumMinor: Number(form.minimum),
                  presetsMinor: form.presets
                    .split(",")
                    .map((value) => Number(value.trim()))
                    .filter((value) => Number.isFinite(value) && value > 0),
                  windowLimitMinor: Number(form.windowLimit),
                  windowSeconds: Number(form.windowHours) * 3600,
                },
                method: "PUT",
              });
              if (ok) {
                setDirty(false);
                onSaved();
              }
            }}
            size="sm"
          >
            {translate("save")}
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

function SubscriptionCard({
  editable,
  onSaved,
  settings,
}: {
  editable: boolean;
  onSaved: () => void;
  settings: CommerceSettings;
}) {
  const translate = useTranslations("admin.settings");
  const maxId = useId();
  const [multiEnabled, setMultiEnabled] = useState(settings.subscriptions.multiEnabled);
  const [maxPerCustomer, setMaxPerCustomer] = useState(
    String(settings.subscriptions.maxPerCustomer),
  );
  const { run, pending, error } = useOperatorAction();

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("subscriptions.title")}</CardTitle>
        <CardDescription>{translate("subscriptions.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex items-center gap-3">
          <Switch
            checked={multiEnabled}
            disabled={!editable}
            id="multi-enabled"
            onCheckedChange={setMultiEnabled}
          />
          <Label htmlFor="multi-enabled">{translate("subscriptions.multiEnabled")}</Label>
        </div>
        <Field
          id={maxId}
          label={translate("subscriptions.maxPerCustomer")}
          onChange={setMaxPerCustomer}
          readOnly={!editable}
          value={maxPerCustomer}
        />
        {/* Turning concurrency off never closes anything: customers who already
            hold several keep them, and the ceiling applies to new purchases. */}
        <p className="text-muted-foreground text-xs">{translate("subscriptions.disableNote")}</p>
        {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
        {editable && (
          <Button
            className="self-start"
            disabled={pending}
            onClick={async () => {
              const ok = await run("/v1/panel/settings/commerce/subscriptions", {
                body: { maxPerCustomer: Number(maxPerCustomer), multiEnabled },
                method: "PUT",
              });
              if (ok) {
                onSaved();
              }
            }}
            size="sm"
          >
            {translate("save")}
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * Payment providers, with what each adapter actually declares beside what the
 * operator configured.
 *
 * A recurring switch that is unavailable is explained rather than merely
 * refused: either the integration has no way to bind a payment method, or the
 * operator's merchant account has not passed a capability test.
 */
function ProviderCard() {
  const translate = useTranslations("admin.settings");
  const { can } = useSession();
  const [editing, setEditing] = useState("");
  const { data, isLoading, mutate } = useSWR<Listing<ProviderSettings>, ApiError>(
    "/v1/panel/settings/providers",
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-40 w-full" />;
  }
  const items = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("providers.title")}</CardTitle>
        <CardDescription>{translate("providers.description")}</CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("providers.provider")}</TableHead>
              <TableHead>{translate("providers.enabled")}</TableHead>
              <TableHead>{translate("providers.credentials")}</TableHead>
              <TableHead>{translate("providers.connection")}</TableHead>
              <TableHead>{translate("providers.webhook")}</TableHead>
              <TableHead>{translate("providers.recurring")}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((provider) => {
              const key = `${provider.provider}:${provider.merchantId}`;
              return (
                <Fragment key={key}>
                  <TableRow>
                    <TableCell className="font-mono text-[12px]">
                      {provider.provider}
                      {provider.merchantId && ` · ${provider.merchantId}`}
                    </TableCell>
                    <TableCell>
                      <Badge variant={provider.enabled ? "success" : "neutral"}>
                        {translate(provider.enabled ? "yes" : "no")}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      {translate(provider.credentialsSet ? "providers.set" : "providers.unset")}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={
                          provider.connectionStatus === "healthy"
                            ? "success"
                            : provider.connectionStatus === "failing"
                              ? "danger"
                              : "neutral"
                        }
                      >
                        {provider.connectionStatus}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={
                          provider.webhookStatus === "healthy"
                            ? "success"
                            : provider.webhookStatus === "failing"
                              ? "danger"
                              : "neutral"
                        }
                      >
                        {provider.webhookStatus}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-sm">
                      {!provider.adapterRecurring
                        ? translate("providers.recurringUnsupported")
                        : provider.recurringEnabled
                          ? translate("providers.recurringOn")
                          : translate(`providers.recurringTest.${provider.recurringTestStatus}`)}
                    </TableCell>
                    <TableCell>
                      {can("settings.write") && (
                        <Button
                          onClick={() => setEditing(editing === key ? "" : key)}
                          size="sm"
                          variant="ghost"
                        >
                          {translate(editing === key ? "providers.close" : "providers.configure")}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                  {editing === key && (
                    <TableRow>
                      <TableCell className="p-2" colSpan={7}>
                        <ProviderEditor onSaved={() => mutate()} provider={provider} />
                      </TableCell>
                    </TableRow>
                  )}
                </Fragment>
              );
            })}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

function Field({
  id,
  label,
  onChange,
  readOnly,
  value,
}: {
  id: string;
  label: string;
  onChange: (value: string) => void;
  readOnly: boolean;
  value: string;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        inputMode="numeric"
        onChange={(event) => onChange(event.target.value)}
        readOnly={readOnly}
        value={value}
      />
    </div>
  );
}

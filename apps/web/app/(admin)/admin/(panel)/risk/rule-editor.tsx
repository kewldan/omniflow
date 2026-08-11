"use client";

import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import { type AnomalyRule, type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

const METRICS = ["purchase", "refund", "referral", "traffic"] as const;

/**
 * Anomaly thresholds, one card per metric.
 *
 * Thresholds are in each metric's own unit — minor units for purchase and
 * refund, a plain count for referral, bytes for traffic — because converting
 * them to a shared scale would mean showing an operator a number they did not
 * type. The unit is stated on every field for the same reason.
 */
export function RuleEditor({ active }: { active: boolean }) {
  const translate = useTranslations("admin.risk");
  const { can } = useSession();
  const { data, isLoading, mutate } = useSWR<Listing<AnomalyRule>, ApiError>(
    active ? "/v1/panel/risk/rules" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-40 w-full" />;
  }

  const stored = new Map((data?.items ?? []).map((rule) => [rule.metric, rule]));

  return (
    <div className="flex flex-col gap-3">
      <Card className="p-3 text-muted-foreground text-sm">{translate("rules.explainer")}</Card>
      <div className="grid gap-3 lg:grid-cols-2">
        {METRICS.map((metric) => (
          <RuleCard
            editable={can("risk.write")}
            key={metric}
            metric={metric}
            onSaved={() => mutate()}
            rule={stored.get(metric)}
          />
        ))}
      </div>
    </div>
  );
}

/**
 * A metric with no stored rule is simply not evaluated, so the card starts
 * disabled with empty thresholds rather than inventing defaults an operator did
 * not choose.
 */
function RuleCard({
  editable,
  metric,
  onSaved,
  rule,
}: {
  editable: boolean;
  metric: string;
  onSaved: () => void;
  rule?: AnomalyRule;
}) {
  const translate = useTranslations("admin.risk");
  const warnId = useId();
  const alertId = useId();
  const windowId = useId();
  const sampleId = useId();

  const [form, setForm] = useState({
    alert: String(rule?.alertThreshold ?? ""),
    enabled: rule?.enabled ?? false,
    sample: String(rule?.minimumSample ?? 3),
    warn: String(rule?.warnThreshold ?? ""),
    windowMinutes: String(Math.round((rule?.windowSeconds ?? 3600) / 60)),
  });
  const { run, pending, error } = useOperatorAction();

  function update(patch: Partial<typeof form>) {
    setForm((current) => ({ ...current, ...patch }));
  }

  // The same coherence rule the evaluator applies, checked here so the operator
  // is told rather than having the rule silently skipped at evaluation time.
  const coherent =
    Number(form.warn) > 0 &&
    Number(form.alert) >= Number(form.warn) &&
    Number(form.windowMinutes) >= 5;

  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="flex items-center justify-between gap-3">
        <span className="font-medium">{translate(`metrics.${metric}`)}</span>
        <Switch
          checked={form.enabled}
          disabled={!editable}
          onCheckedChange={(enabled) => update({ enabled })}
        />
      </div>
      <p className="text-muted-foreground text-xs">{translate(`rules.units.${metric}`)}</p>

      <div className="grid grid-cols-2 gap-3">
        <Field
          id={warnId}
          label={translate("rules.warn")}
          onChange={(warn) => update({ warn })}
          readOnly={!editable}
          value={form.warn}
        />
        <Field
          id={alertId}
          label={translate("rules.alert")}
          onChange={(alert) => update({ alert })}
          readOnly={!editable}
          value={form.alert}
        />
        <Field
          id={windowId}
          label={translate("rules.windowMinutes")}
          onChange={(windowMinutes) => update({ windowMinutes })}
          readOnly={!editable}
          value={form.windowMinutes}
        />
        <Field
          id={sampleId}
          label={translate("rules.minimumSample")}
          onChange={(sample) => update({ sample })}
          readOnly={!editable}
          value={form.sample}
        />
      </div>

      {!coherent && (
        <p className="text-warning-foreground text-xs">{translate("rules.incoherent")}</p>
      )}
      {error && <p className="text-danger-foreground text-xs">{error.message}</p>}

      {editable && (
        <Button
          className="self-start"
          disabled={pending || !coherent}
          onClick={async () => {
            const ok = await run(`/v1/panel/risk/rules/${metric}`, {
              body: {
                alertThreshold: Number(form.alert),
                enabled: form.enabled,
                minimumSample: Number(form.sample),
                warnThreshold: Number(form.warn),
                windowSeconds: Number(form.windowMinutes) * 60,
              },
              method: "PUT",
            });
            if (ok) {
              onSaved();
            }
          }}
          size="sm"
        >
          {translate("rules.save")}
        </Button>
      )}
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

/** Adds or edits a blocklist source. */
export function SourceEditor({ onSaved }: { onSaved: () => void }) {
  const translate = useTranslations("admin.risk");
  const slugId = useId();
  const nameId = useId();
  const urlId = useId();
  const authId = useId();
  const intervalId = useId();

  const [form, setForm] = useState({
    auth: "",
    displayName: "",
    enabled: true,
    intervalHours: "24",
    slug: "",
    subjectKind: "telegram_id",
    url: "",
  });
  const { run, pending, error } = useOperatorAction();

  function update(patch: Partial<typeof form>) {
    setForm((current) => ({ ...current, ...patch }));
  }

  return (
    <Card className="flex flex-col gap-3 p-4">
      <p className="font-medium text-sm">{translate("sources.addTitle")}</p>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={slugId}>{translate("sources.slug")}</Label>
          <Input
            id={slugId}
            onChange={(event) => update({ slug: event.target.value.toLowerCase() })}
            placeholder="community-list"
            value={form.slug}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={nameId}>{translate("sources.displayName")}</Label>
          <Input
            id={nameId}
            onChange={(event) => update({ displayName: event.target.value })}
            value={form.displayName}
          />
        </div>
        <div className="flex flex-col gap-1.5 sm:col-span-2">
          <Label htmlFor={urlId}>{translate("sources.url")}</Label>
          <Input
            id={urlId}
            onChange={(event) => update({ url: event.target.value })}
            placeholder="https://example.org/blocklist.txt"
            value={form.url}
          />
          {/* Plain HTTP would let anyone on the path decide who this
              installation refuses to serve. */}
          <p className="text-muted-foreground text-xs">{translate("sources.httpsOnly")}</p>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={authId}>{translate("sources.authHeader")}</Label>
          <Input
            id={authId}
            onChange={(event) => update({ auth: event.target.value })}
            placeholder="Bearer …"
            type="password"
            value={form.auth}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={intervalId}>{translate("sources.intervalHours")}</Label>
          <Input
            id={intervalId}
            inputMode="numeric"
            onChange={(event) => update({ intervalHours: event.target.value })}
            value={form.intervalHours}
          />
        </div>
      </div>

      {error && <p className="text-danger-foreground text-sm">{error.message}</p>}

      <Button
        className="self-start"
        disabled={pending || !form.slug || !form.displayName || !form.url.startsWith("https://")}
        onClick={async () => {
          const ok = await run("/v1/panel/risk/sources", {
            body: {
              // An empty header means "keep what is stored", so a re-save never
              // has to echo a credential back into the form.
              authHeader: form.auth.trim() === "" ? null : form.auth.trim(),
              displayName: form.displayName,
              enabled: form.enabled,
              refreshIntervalSeconds: Math.max(Number(form.intervalHours) * 3600, 300),
              slug: form.slug,
              subjectKind: form.subjectKind,
              url: form.url,
            },
            method: "PUT",
          });
          if (ok) {
            update({ auth: "", displayName: "", slug: "", url: "" });
            onSaved();
          }
        }}
        size="sm"
      >
        {translate("sources.save")}
      </Button>
    </Card>
  );
}

/** The empty-state used when no source exists yet. */
export function NoSources() {
  const translate = useTranslations("admin.risk");
  return (
    <StateNotice
      description={translate("empty.sourcesDescription")}
      title={translate("empty.sources")}
      variant="empty"
    />
  );
}

"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import type { AnalyticsSettingsView, AnalyticsVerification } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

/**
 * The operator's own advertising measurement.
 *
 * Two things this screen says out loud rather than leaving to be discovered.
 *
 * It is off until somebody turns it on, and nothing about it is ever reported
 * to the Omniflow project — this is the operator's analytics of their own
 * storefront, and the page says so where the switch is rather than in
 * documentation nobody opens.
 *
 * And a counter is a number, not a snippet. There is no textarea here on
 * purpose: a settings field that accepted a script would be a way to run
 * arbitrary code in every customer's browser, and a customer's browser holds
 * subscription links. The script that ends up on the page is written in the
 * repository and takes exactly one operator-supplied identifier.
 */
export function AnalyticsSettings() {
  const translate = useTranslations("admin.analytics");
  const { can } = useSession();
  const { run, pending } = useOperatorAction();
  const enabledId = useId();

  const { data, error, isLoading, mutate } = useSWR<AnalyticsSettingsView, ApiError>(
    "/v1/panel/analytics",
    fetcher,
  );

  const [enabled, setEnabled] = useState(false);
  const [counters, setCounters] = useState<Record<string, string>>({});
  const [verifications, setVerifications] = useState<AnalyticsVerification[]>([]);

  useEffect(() => {
    if (!data) {
      return;
    }
    setEnabled(data.enabled ?? false);
    setCounters(data.counters ?? {});
    setVerifications(data.verifications ?? []);
  }, [data]);

  if (isLoading) {
    return <Skeleton className="h-96 w-full" />;
  }
  if (error || !data) {
    return <StateNotice title={translate("failed")} variant="danger" />;
  }

  const writable = can("settings.write");

  async function save() {
    if (
      await run("/v1/panel/analytics", {
        body: {
          counters,
          enabled,
          verifications: verifications.filter((tag) => tag.name && tag.content),
          version: data?.version ?? 1,
        },
        method: "PUT",
      })
    ) {
      toast.success(translate("saved"));
      mutate();
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>{translate("measurement")}</CardTitle>
          <CardDescription>{translate("measurementAbout")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div className="flex items-center justify-between gap-4">
            <Label className="font-normal" htmlFor={enabledId}>
              {translate("enabled")}
            </Label>
            <Switch
              checked={enabled}
              disabled={!writable}
              id={enabledId}
              onCheckedChange={setEnabled}
            />
          </div>
          <p className="text-subtle-foreground text-sm">{translate("consentNote")}</p>

          {(data.providers ?? []).map((provider) => (
            <CounterField
              key={provider}
              onChange={(value) => setCounters({ ...counters, [provider]: value })}
              provider={provider}
              value={counters[provider] ?? ""}
              writable={writable}
            />
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("verification")}</CardTitle>
          <CardDescription>{translate("verificationAbout")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {verifications.length === 0 && (
            <p className="text-subtle-foreground text-sm">{translate("noVerifications")}</p>
          )}
          {verifications.map((tag, index) => (
            <div className="flex items-end gap-2" key={tag.name}>
              <div className="flex flex-1 flex-col gap-1.5">
                <Label className="font-mono text-xs">{tag.name}</Label>
                <Input
                  disabled={!writable}
                  onChange={(event) =>
                    setVerifications(
                      verifications.map((current, position) =>
                        position === index ? { ...current, content: event.target.value } : current,
                      ),
                    )
                  }
                  value={tag.content}
                />
              </div>
              {writable && (
                <Button
                  aria-label={translate("remove")}
                  onClick={() =>
                    setVerifications(verifications.filter((_, position) => position !== index))
                  }
                  variant="ghost"
                >
                  <Trash2 aria-hidden className="size-4" />
                </Button>
              )}
            </div>
          ))}

          {writable && (
            <div className="flex flex-wrap gap-2">
              {(data.verificationNames ?? [])
                .filter((name) => !verifications.some((tag) => tag.name === name))
                .map((name) => (
                  <Button
                    key={name}
                    onClick={() => setVerifications([...verifications, { content: "", name }])}
                    variant="secondary"
                  >
                    {name}
                  </Button>
                ))}
            </div>
          )}
        </CardContent>
      </Card>

      {writable && (
        <div>
          <Button disabled={pending} onClick={save}>
            {translate("save")}
          </Button>
        </div>
      )}
    </div>
  );
}

/**
 * One counter identifier.
 *
 * The hint says what the value looks like, because the API refuses anything
 * else and "invalid" arriving after a save is a worse way to learn the shape
 * than reading it before typing.
 */
function CounterField({
  onChange,
  provider,
  value,
  writable,
}: {
  onChange: (value: string) => void;
  provider: string;
  value: string;
  writable: boolean;
}) {
  const translate = useTranslations("admin.analytics");
  const fieldId = useId();

  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={fieldId}>{translate(`provider.${provider}` as never)}</Label>
      <Input
        disabled={!writable}
        id={fieldId}
        onChange={(event) => onChange(event.target.value)}
        placeholder={translate(`placeholder.${provider}` as never)}
        value={value}
      />
    </div>
  );
}

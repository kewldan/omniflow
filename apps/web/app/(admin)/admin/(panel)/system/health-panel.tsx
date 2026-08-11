"use client";

import { Badge } from "@omniflow/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { type HealthReport, type MaintenanceState, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

/**
 * Dependency health and maintenance mode.
 *
 * The dependency probes are the same registry `/readyz` reports on, so the
 * panel and the load balancer can never disagree about whether PostgreSQL is
 * up. Provider status comes from the database instead, because "configured but
 * never reached" is a different problem from "reached and failing" and a live
 * probe would collapse the two.
 */
export function HealthPanel({ active }: { active: boolean }) {
  const translate = useTranslations("admin.system");
  const locale = useLocale();
  const { data, isLoading } = useSWR<HealthReport, ApiError>(
    active ? "/v1/panel/overview/health" : null,
    fetcher,
    // Health that silently ages is worse than health that is obviously stale.
    { refreshInterval: active ? 30_000 : 0 },
  );

  if (isLoading || !data) {
    return <Skeleton className="h-48 w-full" />;
  }

  return (
    <div className="flex flex-col gap-4">
      <MaintenanceControl active={active} />

      <Card>
        <CardHeader>
          <CardTitle>{translate("health.dependencies")}</CardTitle>
          <CardDescription>{translate("health.dependenciesDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          {(data.dependencies ?? []).length === 0 ? (
            <p className="text-muted-foreground text-sm">{translate("health.noProbes")}</p>
          ) : (
            (data.dependencies ?? []).map((check) => (
              <div className="flex items-center justify-between gap-3" key={check.name}>
                <span className="flex items-center gap-2">
                  <Badge variant={check.healthy ? "success" : "danger"}>
                    {translate(check.healthy ? "health.up" : "health.down")}
                  </Badge>
                  <span className="font-mono text-[12px]">{check.name}</span>
                </span>
                <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                  {check.latencyMs}ms · {new Date(check.checkedAt).toLocaleTimeString(locale)}
                  {/* A classification, never the driver's message: a probe
                      response must not leak a connection string. */}
                  {check.error ? ` · ${check.error}` : ""}
                </span>
              </div>
            ))
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("health.providers")}</CardTitle>
          <CardDescription>{translate("health.providersDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          {(data.paymentProviders ?? []).length === 0 &&
          (data.goodsProviders ?? []).length === 0 ? (
            <p className="text-muted-foreground text-sm">{translate("health.noProviders")}</p>
          ) : (
            <>
              {(data.paymentProviders ?? []).map((provider) => (
                <div
                  className="flex flex-wrap items-center justify-between gap-3"
                  key={`${provider.provider}:${provider.merchantId ?? ""}`}
                >
                  <span className="font-mono text-[12px]">
                    {provider.provider}
                    {provider.merchantId ? ` · ${provider.merchantId}` : ""}
                  </span>
                  <span className="flex items-center gap-2">
                    {!provider.enabled && (
                      <Badge variant="neutral">{translate("health.disabled")}</Badge>
                    )}
                    <Badge variant={statusVariant(provider.connectionStatus)}>
                      {translate("health.connection")}: {provider.connectionStatus}
                    </Badge>
                    <Badge variant={statusVariant(provider.webhookStatus)}>
                      {translate("health.webhook")}: {provider.webhookStatus}
                    </Badge>
                  </span>
                </div>
              ))}
              {(data.goodsProviders ?? []).map((provider) => (
                <div className="flex items-center justify-between gap-3" key={provider.slug}>
                  <span className="font-mono text-[12px]">{provider.slug}</span>
                  <span className="flex items-center gap-2">
                    {provider.lowBalance && (
                      <Badge variant="danger">{translate("health.lowBalance")}</Badge>
                    )}
                    <Badge variant={statusVariant(provider.status)}>{provider.status}</Badge>
                  </span>
                </div>
              ))}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function statusVariant(status: string): "success" | "danger" | "neutral" {
  if (status === "healthy") {
    return "success";
  }
  return status === "failing" ? "danger" : "neutral";
}

/**
 * Maintenance mode, with the source that engaged it.
 *
 * A manual activation stays until somebody clears it; automatic detection
 * clears itself on recovery. Showing which one is in force is what stops an
 * operator waiting for a recovery that will never come.
 *
 * Neither cancels, refunds, or expires anything already paid for.
 */
function MaintenanceControl({ active }: { active: boolean }) {
  const translate = useTranslations("admin.system");
  const reasonId = useId();
  const ruId = useId();
  const enId = useId();
  const { can } = useSession();

  const { data, isLoading, mutate } = useSWR<MaintenanceState, ApiError>(
    active ? "/v1/panel/overview/maintenance" : null,
    fetcher,
  );
  const [reason, setReason] = useState("");
  const [noticeRu, setNoticeRu] = useState("");
  const [noticeEn, setNoticeEn] = useState("");
  const { run, pending, error } = useOperatorAction();

  if (isLoading || !data) {
    return <Skeleton className="h-40 w-full" />;
  }

  const engaging = !data.active;
  const editable = can("system.write");

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {translate("health.maintenance")}
          <Badge variant={data.active ? "warning" : "success"}>
            {translate(data.active ? "health.maintenanceOn" : "health.maintenanceOff")}
          </Badge>
          {data.active && (
            <Badge variant="neutral">{translate(`health.source.${data.source}`)}</Badge>
          )}
        </CardTitle>
        <CardDescription>{translate("health.maintenanceDescription")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {data.active && data.reason && (
          <p className="text-sm">
            <span className="text-muted-foreground">{translate("health.reason")}: </span>
            {data.reason}
          </p>
        )}
        {data.active && data.source !== "manual" && (
          // Clearing an automatic activation by hand would re-enable purchases
          // while the dependency is still down.
          <p className="text-muted-foreground text-xs">{translate("health.automaticNote")}</p>
        )}

        {editable && (
          <>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={reasonId}>{translate("health.reason")}</Label>
              <Input
                id={reasonId}
                onChange={(event) => setReason(event.target.value)}
                placeholder={translate("health.reasonPlaceholder")}
                value={reason}
              />
            </div>

            {engaging && (
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={ruId}>{translate("health.noticeRu")}</Label>
                  <Input
                    id={ruId}
                    onChange={(event) => setNoticeRu(event.target.value)}
                    value={noticeRu}
                  />
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor={enId}>{translate("health.noticeEn")}</Label>
                  <Input
                    id={enId}
                    onChange={(event) => setNoticeEn(event.target.value)}
                    value={noticeEn}
                  />
                </div>
                {/* Both languages, because a customer who reads only one and
                    gets a blank screen is worse served than one who gets a
                    notice they can read. */}
                <p className="text-muted-foreground text-xs sm:col-span-2">
                  {translate("health.noticesRequired")}
                </p>
              </div>
            )}

            {error && <p className="text-danger-foreground text-sm">{error.message}</p>}

            <div className="flex items-center gap-3">
              <Switch
                checked={data.active}
                disabled={
                  pending ||
                  reason.trim().length === 0 ||
                  (engaging && (noticeRu.trim() === "" || noticeEn.trim() === ""))
                }
                id="maintenance-active"
                onCheckedChange={async (next) => {
                  const ok = await run("/v1/panel/overview/maintenance", {
                    body: {
                      active: next,
                      noticeEn: next ? noticeEn : "",
                      noticeRu: next ? noticeRu : "",
                    },
                    method: "PUT",
                    reason: reason.trim(),
                  });
                  if (ok) {
                    setReason("");
                    await mutate();
                  }
                }}
              />
              <Label htmlFor="maintenance-active">{translate("health.maintenanceToggle")}</Label>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

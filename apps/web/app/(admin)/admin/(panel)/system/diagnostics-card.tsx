"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { useSession } from "@/lib/session";

/**
 * The support bundle: what this installation is, and what it is telling anybody
 * else.
 *
 * It lives on the system screen rather than under settings, which is where the
 * API has always put it — `/v1/panel/settings/diagnostics` sits behind
 * `system.read` rather than `settings.read`, with the comment that a bundle
 * describes the running installation rather than configuring it. The panel
 * disagreed with its own API and rendered it at the foot of the settings page,
 * where an operator holding `system.read` but not `settings.read` could not
 * reach it at all.
 *
 * Nothing is fetched until it is asked for: this is the thing an operator
 * emails to somebody for help, not something a screen should collect on every
 * visit.
 */
export function DiagnosticsCard() {
  const translate = useTranslations("admin.installationSettings");
  const { can } = useSession();
  const [requested, setRequested] = useState(false);
  const { data, isLoading } = useSWR<Record<string, unknown>, ApiError>(
    requested ? "/v1/panel/settings/diagnostics" : null,
    fetcher,
  );

  if (!can("system.read")) {
    return null;
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("diagnostics.title")}</CardTitle>
        {/* Assembled from an allowlist rather than by dumping state and
            filtering, so a field added later is absent until somebody adds it
            deliberately. */}
        <CardDescription>{translate("diagnostics.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <Button className="self-start" onClick={() => setRequested(true)} type="button">
          {translate("diagnostics.generate")}
        </Button>
        {requested && isLoading ? <Skeleton className="h-40 w-full" /> : null}
        {data ? (
          <pre className="max-h-96 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">
            {JSON.stringify(data, null, 2)}
          </pre>
        ) : null}
      </CardContent>
    </Card>
  );
}

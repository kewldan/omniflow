"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { ArrowRight, Route } from "lucide-react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import useSWR from "swr";

import type { ApiError } from "@/lib/api";
import { fetcher } from "@/lib/api";
import { useSession } from "@/lib/session";

const ENDPOINT = "/v1/panel/settings/smart-subscription";

/**
 * The smart subscription at a glance on the dashboard: whether it is on and
 * how many customers made it their own, with the way in. It renders nothing
 * for an operator who cannot read settings.
 */
export function SmartSubscriptionCard() {
  const translate = useTranslations("admin.dashboard.smart");
  const { can } = useSession();
  const allowed = can("settings.read");
  const { data: settings } = useSWR<{ enabled: boolean }, ApiError>(
    allowed ? ENDPOINT : null,
    fetcher,
  );
  const { data: stats } = useSWR<
    { customized: number; whitelist: number; pinned: { count: number }[] },
    ApiError
  >(allowed && settings?.enabled ? `${ENDPOINT}/stats` : null, fetcher);
  if (!allowed || !settings) {
    return null;
  }
  const pinned = (stats?.pinned ?? []).reduce((sum, item) => sum + item.count, 0);
  return (
    <Card className="p-4">
      <div className="flex items-center gap-3">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-secondary">
          <Route aria-hidden className="size-[18px]" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="font-medium text-sm">{translate("title")}</span>
            <Badge variant={settings.enabled ? "success" : "neutral"}>
              {translate(settings.enabled ? "on" : "off")}
            </Badge>
          </div>
          <p className="text-muted-foreground text-xs">
            {settings.enabled && stats
              ? translate("summary", {
                  customized: stats.customized,
                  pinned,
                  whitelist: stats.whitelist,
                })
              : translate("offHint")}
          </p>
        </div>
        <Button asChild size="sm" variant="ghost">
          <Link href="/admin/settings/smart-subscription">
            {translate("open")}
            <ArrowRight aria-hidden />
          </Link>
        </Button>
      </div>
    </Card>
  );
}

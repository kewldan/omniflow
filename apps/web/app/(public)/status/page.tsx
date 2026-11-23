"use client";

import { cn } from "@omniflow/ui/lib/utils";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useTranslations } from "next-intl";
import useSWR from "swr";

import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";

type ServerStatus = {
  servers: { name: string; countryCode?: string; online: boolean }[];
  checkedAt: string;
};

/**
 * Which servers are up, read from Remnawave by the API and cached for a
 * minute. It exists so a customer can tell whether a problem is theirs or the
 * service's before writing to support. The operator turns it on; without
 * APP_SERVER_STATUS_ENABLED the API answers 404 and this page says so.
 */
export default function ServerStatusPage() {
  const translate = useTranslations("common.serverStatus");
  const { data, error, isLoading } = useSWR<ServerStatus, ApiError>("/v1/status/servers", fetcher, {
    refreshInterval: 60_000,
  });

  return (
    <main className="mx-auto w-full max-w-xl space-y-4 px-4 py-10">
      <h1 className="font-semibold text-xl">{translate("title")}</h1>
      {isLoading ? (
        <Skeleton className="h-40 w-full" />
      ) : error || !data ? (
        <p className="rounded-xl border border-border bg-card p-4 text-muted-foreground text-sm">
          {error?.status === 404 ? translate("disabled") : translate("unavailable")}
        </p>
      ) : (
        <>
          <p className="text-muted-foreground text-sm">
            {data.servers.every((server) => server.online)
              ? translate("allUp")
              : translate("someDown", {
                  count: data.servers.filter((server) => !server.online).length,
                })}
          </p>
          <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
            {data.servers.map((server) => (
              <li className="flex items-center justify-between gap-3 px-4 py-3" key={server.name}>
                <span className="truncate text-sm">
                  {server.countryCode ? (
                    <span className="mr-2 text-muted-foreground text-xs">{server.countryCode}</span>
                  ) : null}
                  {server.name}
                </span>
                <span
                  className={cn(
                    "flex items-center gap-2 text-xs",
                    server.online ? "text-success" : "text-destructive",
                  )}
                >
                  <span
                    aria-hidden
                    className={cn(
                      "size-2 rounded-full",
                      server.online ? "bg-success" : "bg-destructive",
                    )}
                  />
                  {server.online ? translate("up") : translate("down")}
                </span>
              </li>
            ))}
          </ul>
          <p className="text-[12.5px] text-subtle-foreground">
            {translate("checked")} <Timestamp value={data.checkedAt} />
          </p>
        </>
      )}
    </main>
  );
}

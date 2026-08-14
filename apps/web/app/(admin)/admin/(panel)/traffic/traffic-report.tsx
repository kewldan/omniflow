"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Download } from "lucide-react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import type { TrafficReport } from "@/lib/operations";

const ENDPOINT = "/v1/panel/reports/traffic";

/**
 * Traffic, read live from Remnawave.
 *
 * Nothing on this screen is stored by Omniflow. Remnawave owns traffic, nodes,
 * and connections; this reads them on each request and adds the one thing
 * Omniflow can add — which customer holds a given Remnawave user. There is no
 * table here for a node or for a byte somebody used, and there is deliberately
 * no history: keeping one would be the first step towards Omniflow having an
 * opinion about traffic.
 */
export function TrafficReportScreen() {
  const translate = useTranslations("admin.traffic");
  const { data, error, isLoading } = useSWR<TrafficReport, ApiError>(ENDPOINT, fetcher);

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />

      <div>
        <Button asChild size="sm" variant="secondary">
          <a href={`${ENDPOINT}/export`}>
            <Download aria-hidden />
            {translate("export")}
          </a>
        </Button>
      </div>

      {error ? (
        <StateNotice description={error.message} title={translate("failed")} variant="danger" />
      ) : null}

      {isLoading || !data ? (
        <Skeleton className="h-96 w-full" />
      ) : (
        <>
          <NodeCard report={data} />
          <ConsumerCard report={data} />
        </>
      )}
    </div>
  );
}

/**
 * Nodes, sorted by pressure.
 *
 * A panel that does not expose its node listing produces a stated absence
 * rather than an empty table. "No nodes are full" and "we could not ask" look
 * identical as an empty list and mean opposite things.
 */
function NodeCard({ report }: { report: TrafficReport }) {
  const translate = useTranslations("admin.traffic");

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("nodes.title")}</CardTitle>
        <CardDescription>{translate("nodes.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {!report.nodesReported ? (
          <StateNotice
            description={translate(`nodes.detail.${report.nodesDetail ?? "nodes_unavailable"}`)}
            title={translate("nodes.unavailable")}
            variant="empty"
          />
        ) : report.nodes.length === 0 ? (
          <StateNotice description={translate("nodes.noneHint")} title={translate("nodes.none")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("nodes.node")}</TableHead>
                <TableHead>{translate("nodes.state")}</TableHead>
                <TableHead className="text-right">{translate("nodes.used")}</TableHead>
                <TableHead className="text-right">{translate("nodes.limit")}</TableHead>
                <TableHead className="text-right">{translate("nodes.share")}</TableHead>
                <TableHead className="text-right">{translate("nodes.online")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {report.nodes.map((node) => (
                <TableRow key={node.name}>
                  <TableCell>
                    {node.name}
                    {node.countryCode ? (
                      <span className="text-subtle-foreground"> · {node.countryCode}</span>
                    ) : null}
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant={node.disabled ? "neutral" : node.connected ? "success" : "danger"}
                    >
                      {translate(
                        node.disabled
                          ? "nodes.disabled"
                          : node.connected
                            ? "nodes.up"
                            : "nodes.down",
                      )}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{bytes(node.usedBytes)}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {node.limitBytes > 0 ? bytes(node.limitBytes) : translate("nodes.noLimit")}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    <Share share={node.usedShare} />
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {node.usersOnline ?? "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

/** A saturation figure, coloured only once it means something. */
function Share({ share }: { share?: number }) {
  if (share === undefined || share === null) {
    return <span className="text-subtle-foreground">—</span>;
  }
  const tone = share >= 0.9 ? "text-destructive" : share >= 0.75 ? "text-warning" : "";
  return <span className={`font-medium ${tone}`}>{Math.round(share * 100)}%</span>;
}

function ConsumerCard({ report }: { report: TrafficReport }) {
  const translate = useTranslations("admin.traffic");

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("consumers.title")}</CardTitle>
        <CardDescription>
          {/* A truncated ranking presented as a complete one is worse than no
              ranking, so the scan says how far it got whenever it did not
              reach the end. */}
          {report.scanned < report.total
            ? translate("consumers.partial", { scanned: report.scanned, total: report.total })
            : translate("consumers.description")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {report.consumers.length === 0 ? (
          <StateNotice
            description={translate("consumers.emptyHint")}
            title={translate("consumers.empty")}
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("consumers.user")}</TableHead>
                <TableHead>{translate("consumers.customer")}</TableHead>
                <TableHead className="text-right">{translate("consumers.used")}</TableHead>
                <TableHead className="text-right">{translate("consumers.lifetime")}</TableHead>
                <TableHead className="text-right">{translate("consumers.limit")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {report.consumers.map((consumer) => (
                <TableRow key={consumer.remnawaveId}>
                  <TableCell className="font-mono text-xs">{consumer.username}</TableCell>
                  <TableCell>
                    {consumer.customerId ? (
                      <Link
                        className="underline underline-offset-2"
                        href={`/admin/customers/${consumer.customerId}`}
                      >
                        {consumer.label || translate("consumers.open")}
                      </Link>
                    ) : (
                      // A Remnawave user Omniflow did not create is a real
                      // state, not a data error: somebody provisioned it in the
                      // panel, or an import has not run.
                      <span className="text-subtle-foreground">
                        {translate("consumers.unattributed")}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {bytes(consumer.usedBytes)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums text-subtle-foreground">
                    {bytes(consumer.lifetimeBytes)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {consumer.limitBytes > 0
                      ? bytes(consumer.limitBytes)
                      : translate("nodes.noLimit")}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * Bytes in binary units, labelled as such.
 *
 * GiB rather than GB because that is what the figure is: Remnawave counts
 * bytes and the conversion here divides by 1024. Calling it GB would be off by
 * seven percent at this scale, which is enough to make an operator's arithmetic
 * against their host's invoice fail to reconcile.
 */
function bytes(value: number): string {
  if (value <= 0) {
    return "0";
  }
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let index = 0;
  let scaled = value;
  while (scaled >= 1024 && index < units.length - 1) {
    scaled /= 1024;
    index += 1;
  }
  return `${scaled < 10 && index > 0 ? scaled.toFixed(1) : Math.round(scaled)} ${units[index]}`;
}

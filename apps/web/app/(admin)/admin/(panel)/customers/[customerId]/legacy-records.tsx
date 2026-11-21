"use client";

import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";

type LegacyRecord = {
  kind: string;
  sourceId: string;
  occurredAt?: string;
  data: Record<string, unknown>;
};

type LegacyRecordList = {
  items: LegacyRecord[];
  kinds: { kind: string; count: number }[];
  total: number;
};

const PAGE = 50;

/** Fields every row has and nobody needs to read. */
const NOISE = new Set(["id", "user_id", "created_at", "updated_at"]);

function value(raw: unknown): string {
  if (raw === null || raw === undefined || raw === "") {
    return "—";
  }
  if (typeof raw === "object") {
    return JSON.stringify(raw);
  }
  return String(raw);
}

/**
 * What the previous bot recorded about this customer and Omniflow keeps only
 * as history: wallet transactions, referral earnings, subscription events,
 * promo code uses, notifications. Rows are shown as they were, by the
 * previous bot's own field names; nothing in Omniflow acts on them.
 */
export function LegacyRecords({ active, customerId }: { active: boolean; customerId: string }) {
  const translate = useTranslations("admin.customers.legacy");
  const [kind, setKind] = useState("");
  const [skip, setSkip] = useState(0);
  const [open, setOpen] = useState<string | null>(null);
  const query = new URLSearchParams();
  if (kind) {
    query.set("kind", kind);
  }
  if (skip > 0) {
    query.set("before", String(skip));
  }
  const { data, isLoading } = useSWR<LegacyRecordList, ApiError>(
    active ? `/v1/panel/customers/${customerId}/legacy-records?${query.toString()}` : null,
    fetcher,
    { keepPreviousData: true },
  );

  const kindName = (code: string) =>
    translate.has(`kinds.${code}`)
      ? translate(`kinds.${code}` as never)
      : code.replaceAll("_", " ");

  if (isLoading && !data) {
    return <Skeleton className="h-24 w-full" />;
  }
  if (!data || data.kinds.length === 0) {
    return null;
  }

  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="flex flex-col gap-0.5">
        <span className="font-medium">{translate("title")}</span>
        <span className="text-muted-foreground text-xs">{translate("description")}</span>
      </div>
      <div className="flex flex-wrap gap-1.5">
        <Button
          onClick={() => {
            setKind("");
            setSkip(0);
          }}
          size="sm"
          variant={kind === "" ? "secondary" : "ghost"}
        >
          {translate("all")}
        </Button>
        {data.kinds.map((entry) => (
          <Button
            key={entry.kind}
            onClick={() => {
              setKind(entry.kind);
              setSkip(0);
            }}
            size="sm"
            variant={kind === entry.kind ? "secondary" : "ghost"}
          >
            {kindName(entry.kind)}
            <span className="text-muted-foreground tabular-nums">{entry.count}</span>
          </Button>
        ))}
      </div>
      <ul className="flex flex-col divide-y divide-border">
        {data.items.map((record) => {
          const key = `${record.kind}:${record.sourceId}`;
          const fields = Object.entries(record.data).filter(
            ([field, raw]) => !NOISE.has(field) && raw !== null && raw !== "",
          );
          const expanded = open === key;
          return (
            <li className="flex flex-col gap-1 py-2 text-sm" key={key}>
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <span className="font-medium">{kindName(record.kind)}</span>
                <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                  {record.occurredAt ? <Timestamp value={record.occurredAt} /> : "—"}
                </span>
              </div>
              <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
                {(expanded ? fields : fields.slice(0, 4)).map(([field, raw]) => (
                  <div className="contents" key={field}>
                    <dt className="text-muted-foreground">{field}</dt>
                    <dd className="min-w-0 break-words font-mono">{value(raw)}</dd>
                  </div>
                ))}
              </dl>
              {fields.length > 4 && (
                <button
                  className="self-start text-muted-foreground text-xs underline-offset-2 hover:underline"
                  onClick={() => setOpen(expanded ? null : key)}
                  type="button"
                >
                  {translate(expanded ? "less" : "more", { count: fields.length - 4 })}
                </button>
              )}
            </li>
          );
        })}
      </ul>
      {(skip > 0 || skip + PAGE < data.total) && (
        <div className="flex items-center justify-between gap-2">
          <Button
            disabled={skip === 0}
            onClick={() => setSkip(Math.max(0, skip - PAGE))}
            size="sm"
            variant="outline"
          >
            {translate("newer")}
          </Button>
          <span className="text-muted-foreground text-xs tabular-nums">
            {translate("range", {
              from: skip + 1,
              to: Math.min(skip + PAGE, data.total),
              total: data.total,
            })}
          </span>
          <Button
            disabled={skip + PAGE >= data.total}
            onClick={() => setSkip(skip + PAGE)}
            size="sm"
            variant="outline"
          >
            {translate("older")}
          </Button>
        </div>
      )}
    </Card>
  );
}

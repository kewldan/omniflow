"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Skeleton } from "@omniflow/ui/skeleton";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useRef, useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import type { Listing } from "@/lib/operations";
import { useSession } from "@/lib/session";

type CustomerImport = {
  id: string;
  source: string;
  status: string;
  total: number;
  valid: number;
  conflicts: number;
  invalid: number;
  errorSummary: Record<string, unknown>;
  resumable: boolean;
  startedAt: string;
  completedAt?: string;
};

/**
 * Customer imports and the customer export.
 *
 * An import is previewed before it is applied, and the preview is where the
 * duplicate detection lands: a record that collides with somebody who already
 * exists is counted as a conflict rather than silently merged or silently
 * skipped. The operator sees how many are clean, how many collide, and how many
 * cannot be read at all, before anything is written.
 */
export function ImportPanel() {
  const translate = useTranslations("admin.imports");
  const { can } = useSession();

  const { data, isLoading, mutate } = useSWR<Listing<CustomerImport>, ApiError>(
    "/v1/panel/imports",
    fetcher,
    // A run in progress moves while an operator watches it.
    { refreshInterval: 5000 },
  );
  const items = data?.items ?? [];
  const writable = can("customers.write");
  const fileId = useId();
  const fileInput = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [applying, setApplying] = useState<CustomerImport | null>(null);

  async function step(path: string, body: unknown): Promise<CustomerImport | null> {
    try {
      return await apiFetch<CustomerImport>(path, { method: "POST", body: JSON.stringify(body) });
    } catch {
      toast.error(translate("actions.failed"));
      return null;
    }
  }

  // A preview reads Remnawave a page at a time; the panel keeps asking until
  // the run is ready, so an operator presses one button for the whole base.
  async function previewRemnawave(resume?: string) {
    setBusy(true);
    let run = await step("/v1/panel/imports/remnawave/preview", {
      importId: resume ?? "",
      pageSize: 500,
    });
    while (run && run.status === "previewing") {
      run = await step("/v1/panel/imports/remnawave/preview", { importId: run.id, pageSize: 500 });
    }
    setBusy(false);
    await mutate();
  }

  async function upload(file: File) {
    setBusy(true);
    const form = new FormData();
    form.append("file", file);
    try {
      await apiFetch("/v1/panel/imports/file", { method: "POST", body: form });
      toast.success(translate("actions.uploaded"));
    } catch (failure) {
      toast.error((failure as ApiError).message || translate("actions.failed"));
    } finally {
      setBusy(false);
      if (fileInput.current) {
        fileInput.current.value = "";
      }
      await mutate();
    }
  }

  async function apply(run: CustomerImport) {
    setBusy(true);
    let current: CustomerImport | null = run;
    while (current && current.status !== "completed") {
      current = await step(`/v1/panel/imports/${run.id}/apply`, { limit: 500 });
    }
    setBusy(false);
    if (current) {
      toast.success(translate("actions.applied"));
    }
    await mutate();
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {/* Its own permission (owner and administrator), not customers.read:
            an export is the whole customer table leaving the installation. */}
        {can("customers.export") && (
          <Button asChild className="self-start" size="sm" variant="outline">
            <a href="/v1/panel/customers/export">{translate("export")}</a>
          </Button>
        )}
        <p className="text-muted-foreground text-xs">{translate("exportNotice")}</p>

        {writable ? (
          <div className="flex flex-wrap items-center gap-2 border-border border-t pt-3">
            <Button disabled={busy} onClick={() => previewRemnawave()} size="sm">
              {translate("actions.previewRemnawave")}
            </Button>
            <Button
              disabled={busy}
              onClick={() => fileInput.current?.click()}
              size="sm"
              variant="outline"
            >
              {translate("actions.upload")}
            </Button>
            <label className="sr-only" htmlFor={fileId}>
              {translate("actions.upload")}
            </label>
            <input
              accept=".csv,text/csv"
              className="hidden"
              id={fileId}
              onChange={(event) => {
                const file = event.target.files?.[0];
                if (file) {
                  upload(file);
                }
              }}
              ref={fileInput}
              type="file"
            />
            <span className="text-muted-foreground text-xs">{translate("actions.fileHint")}</span>
          </div>
        ) : null}

        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : items.length === 0 ? (
          <StateNotice
            description={translate("empty.description")}
            title={translate("empty.title")}
            variant="empty"
          />
        ) : (
          <div className="flex flex-col gap-2 border-border border-t pt-3">
            {items.map((run) => (
              <div className="flex flex-col gap-1" key={run.id}>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <span className="flex items-center gap-2">
                    <span className="font-mono text-[11px]">{run.id.slice(0, 8)}</span>
                    <Badge
                      variant={
                        run.status === "completed"
                          ? "success"
                          : run.status === "failed"
                            ? "danger"
                            : "neutral"
                      }
                    >
                      {translate(`status.${run.status}`)}
                    </Badge>
                    {/* A stored cursor is what makes a run resumable: it names
                        where the source was left off, so a run interrupted at
                        record 40,000 does not start again at one. */}
                    {run.resumable && <Badge variant="warning">{translate("resumable")}</Badge>}
                    {writable && run.status === "previewing" && run.resumable ? (
                      <Button
                        disabled={busy}
                        onClick={() => previewRemnawave(run.id)}
                        size="sm"
                        variant="ghost"
                      >
                        {translate("actions.resume")}
                      </Button>
                    ) : null}
                    {writable && (run.status === "ready" || run.status === "applying") ? (
                      <Button
                        disabled={busy || run.valid === 0}
                        onClick={() => setApplying(run)}
                        size="sm"
                      >
                        {translate("actions.apply", { count: run.valid })}
                      </Button>
                    ) : null}
                  </span>
                  <span className="flex items-center gap-3 text-sm tabular-nums">
                    <span>{translate("counts.total", { count: run.total })}</span>
                    <span className="text-success-foreground">
                      {translate("counts.valid", { count: run.valid })}
                    </span>
                    <span className="text-warning-foreground">
                      {translate("counts.conflicts", { count: run.conflicts })}
                    </span>
                    <span className="text-danger-foreground">
                      {translate("counts.invalid", { count: run.invalid })}
                    </span>
                  </span>
                </div>
                <span className="font-mono text-[11px] text-muted-foreground">
                  <Timestamp value={run.startedAt} />
                  {run.completedAt ? ` → $<Timestamp value={run.completedAt} />` : ""}
                </span>
                {Object.keys(run.errorSummary ?? {}).length > 0 && (
                  // The validation errors are a class-and-count summary rather
                  // than a list of rows: an operator needs to know that 812
                  // records had no Telegram identifier, not to scroll past 812
                  // identical lines.
                  <span className="text-danger-foreground text-xs">
                    {Object.entries(run.errorSummary)
                      .map(([code, count]) => `${translate(`error.${code}`)}: ${String(count)}`)
                      .join(" · ")}
                  </span>
                )}
              </div>
            ))}
          </div>
        )}
        <ConfirmDialog
          cancelLabel={translate("actions.cancel")}
          confirmLabel={translate("actions.applyConfirm")}
          description={translate("actions.applyDescription", {
            valid: applying?.valid ?? 0,
            conflicts: applying?.conflicts ?? 0,
          })}
          onConfirm={() => {
            const run = applying;
            setApplying(null);
            if (run) {
              apply(run);
            }
          }}
          onOpenChange={(open) => {
            if (!open) {
              setApplying(null);
            }
          }}
          open={applying !== null}
          title={translate("actions.applyTitle")}
        />
      </CardContent>
    </Card>
  );
}

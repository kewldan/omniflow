"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useRef, useState } from "react";
import useSWR from "swr";
import { useEnumLabel } from "@/components/admin/enum-label";
import { ReasonDialog } from "@/components/admin/reason-dialog";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { formatBytes, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

const RESTORE_PHRASE = "RESTORE";

type Backup = { id: string; kind: string; status: string; sizeBytes: number; createdAt: string };
type BackupRequest = {
  id: string;
  action: string;
  status: string;
  errorCode?: string;
  createdAt: string;
};

/**
 * Backups from the panel: take one now, restore any completed one, delete
 * one, or upload an encrypted file to restore from. The worker carries out
 * each request within seconds; the list below shows how each ended.
 */
export function BackupControls() {
  const translate = useTranslations("admin.backupControls");
  const label = useEnumLabel();
  const { can } = useSession();
  const { data: history, mutate: refreshHistory } = useSWR<{ backups: Backup[] | null }, ApiError>(
    can("settings.read") ? "/v1/panel/settings/backups" : null,
    fetcher,
    // A deletion is carried out by the worker, so the list catches up on its own.
    { refreshInterval: 10000 },
  );
  const { data: requests, mutate: refreshRequests } = useSWR<{ items: BackupRequest[] }, ApiError>(
    can("system.read") ? "/v1/panel/system/backups/requests" : null,
    fetcher,
    { refreshInterval: 5000 },
  );
  const presets = useTranslations("admin.reasons.backupRestore");
  const { run, pending, error, reset } = useOperatorAction();
  const [restoring, setRestoring] = useState<Backup | null>(null);
  const [deleting, setDeleting] = useState<Backup | null>(null);
  const [typed, setTyped] = useState("");
  const phraseId = useId();
  const fileInput = useRef<HTMLInputElement>(null);
  const fileId = useId();

  if (!can("system.write")) {
    return null;
  }

  async function request(action: string, backupId?: string, why = ""): Promise<boolean> {
    if (
      await run("/v1/panel/system/backups/requests", {
        method: "POST",
        body: { action, backupId: backupId ?? "", reason: why },
        reason: why || `backup ${action}`,
      })
    ) {
      toast.success(translate("requested"));
      await refreshRequests();
      return true;
    }
    return false;
  }

  async function upload(file: File) {
    const form = new FormData();
    form.append("file", file);
    try {
      await apiFetch("/v1/panel/system/backups/upload", { method: "POST", body: form });
      toast.success(translate("uploaded"));
      await refreshHistory();
    } catch (failure) {
      toast.error((failure as ApiError).message || translate("uploadFailed"));
    }
  }

  const completed = (history?.backups ?? []).filter((item) => item.status === "completed");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap gap-2">
          <Button disabled={pending} onClick={() => request("create")} size="sm">
            {translate("backupNow")}
          </Button>
          <Button onClick={() => fileInput.current?.click()} size="sm" variant="outline">
            {translate("upload")}
          </Button>
          <label className="sr-only" htmlFor={fileId}>
            {translate("upload")}
          </label>
          <input
            className="hidden"
            id={fileId}
            onChange={(event) => {
              const file = event.target.files?.[0];
              if (file) {
                upload(file);
              }
              event.target.value = "";
            }}
            ref={fileInput}
            type="file"
          />
        </div>
        {completed.length > 0 ? (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("when")}</TableHead>
                <TableHead>{translate("kind")}</TableHead>
                <TableHead className="text-right">{translate("size")}</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {completed.map((item) => (
                <TableRow key={item.id}>
                  <TableCell className="text-xs">
                    <Timestamp value={item.createdAt} />
                  </TableCell>
                  <TableCell>{label("backupKind", item.kind)}</TableCell>
                  <TableCell className="text-right">{formatBytes(item.sizeBytes)}</TableCell>
                  <TableCell className="flex justify-end gap-1">
                    <Button
                      onClick={() => {
                        reset();
                        setTyped("");
                        setRestoring(item);
                      }}
                      size="sm"
                      variant="outline"
                    >
                      {translate("restore")}
                    </Button>
                    <Button
                      disabled={pending}
                      onClick={() => setDeleting(item)}
                      size="sm"
                      variant="ghost"
                    >
                      {translate("delete")}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : null}
        {(requests?.items ?? []).length > 0 ? (
          <ul className="flex flex-col gap-1 text-sm">
            {(requests?.items ?? []).map((item) => (
              <li className="flex flex-wrap items-center gap-2" key={item.id}>
                <Timestamp value={item.createdAt} />
                <span>{translate(`actions.${item.action}`)}</span>
                <Badge variant={item.status === "failed" ? "danger" : "neutral"}>
                  {label("backupStatus", item.status)}
                </Badge>
                {item.errorCode ? (
                  <span className="font-mono text-xs">{item.errorCode}</span>
                ) : null}
              </li>
            ))}
          </ul>
        ) : null}
        {/* Typing the phrase stays: a restore replaces the live database, and
            a mis-aimed click on a busy row must not be enough to start one. */}
        <ReasonDialog
          confirmLabel={translate("restore")}
          description={translate("restoreWarning")}
          destructive
          disabled={typed.trim() !== RESTORE_PHRASE}
          error={error?.message}
          onConfirm={async (reason) => {
            if (restoring && (await request("restore", restoring.id, reason))) {
              setRestoring(null);
            }
          }}
          onOpenChange={(open) => !open && setRestoring(null)}
          open={restoring !== null}
          pending={pending}
          presets={["dataLoss", "failedMigration", "operatorMistake", "moving", "drill"].map(
            (key) => presets(key),
          )}
          title={translate("restoreTitle")}
        >
          <div className="flex flex-col gap-2">
            <Label htmlFor={phraseId}>{translate("restorePrompt")}</Label>
            <Input
              autoComplete="off"
              id={phraseId}
              onChange={(event) => setTyped(event.target.value)}
              value={typed}
            />
          </div>
        </ReasonDialog>
        <ConfirmDialog
          cancelLabel={translate("cancel")}
          confirmLabel={translate("delete")}
          description={translate("deleteWarning")}
          destructive
          onConfirm={() => {
            const target = deleting;
            setDeleting(null);
            if (target) {
              request("delete", target.id);
            }
          }}
          onOpenChange={(open) => !open && setDeleting(null)}
          open={deleting !== null}
          title={translate("deleteTitle")}
        />
      </CardContent>
    </Card>
  );
}

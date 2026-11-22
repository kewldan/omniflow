"use client";

import { Alert, AlertDescription } from "@omniflow/ui/alert";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type OperatorTopic = {
  kind: string;
  chatId: number;
  topicId?: number;
  status: "pending" | "bound" | "failed";
  errorCode?: string;
  repairing?: boolean;
  updatedAt: string;
};

type OperatorGroupSection = { document?: { chatId?: string; createTopics?: boolean } };

/** Error codes the bot records when it cannot create a topic. */
const KNOWN_ERRORS = new Set([
  "missing_manage_topics_permission",
  "chat_not_found",
  "forum_topics_disabled",
  "bot_not_in_group",
]);

const STATUS_VARIANT = { bound: "success", pending: "info", failed: "danger" } as const;

/**
 * The operator group's topics, as the bot made them.
 *
 * The operator enters the group in the section above; the bot creates one
 * forum topic per notice stream there and saves each thread ID in the routing
 * on its own, so nobody types a thread ID. This card shows what it made, why a
 * topic is missing, and lets an operator repair the topics in place (a plain
 * name and an icon, history kept) or ask for them to be made again.
 */
export function OperatorTopics() {
  const translate = useTranslations("admin.operatorTopics");
  const { can } = useSession();
  const readable = can("settings.read");
  const { data: section } = useSWR<OperatorGroupSection, ApiError>(
    readable ? "/v1/panel/settings/operator_group" : null,
    fetcher,
  );
  const [waiting, setWaiting] = useState(false);
  const { data, isLoading, mutate } = useSWR<{ items: OperatorTopic[] | null }, ApiError>(
    readable ? "/v1/panel/settings/operator-group/topics" : null,
    fetcher,
    {
      // While the bot is creating or repairing topics, follow it; it takes
      // seconds to a minute.
      refreshInterval: (latest) =>
        waiting ||
        (latest?.items ?? []).some((topic) => topic.status === "pending" || topic.repairing)
          ? 3000
          : 0,
    },
  );
  const { run, pending, error } = useOperatorAction();
  const [confirming, setConfirming] = useState<"recreate" | "repair" | null>(null);

  if (!readable) {
    return null;
  }

  const topics = data?.items ?? [];
  const chatId = String(section?.document?.chatId ?? "").trim();
  const flat = section?.document?.createTopics === false;
  const stale = chatId !== "" && topics.some((topic) => String(topic.chatId) !== chatId);
  const bound = topics.filter((topic) => topic.status === "bound").length;
  const failed = topics.filter((topic) => topic.status === "failed");
  const creating = waiting || stale || topics.some((topic) => topic.status === "pending");
  const repairing = topics.some((topic) => topic.repairing);

  async function request(action: "recreate" | "repair") {
    setConfirming(null);
    const done = await run(`/v1/panel/settings/operator-group/topics/${action}`, {
      method: "POST",
      reason: translate(action === "repair" ? "repairReason" : "reason"),
    });
    if (done) {
      setWaiting(true);
      toast.success(translate(action === "repair" ? "repairRequested" : "requested"));
      await mutate();
      // Stop following once the bot had time for a pass or two.
      window.setTimeout(() => setWaiting(false), 30000);
    }
  }

  function label(kind: string) {
    return translate.has(`kinds.${kind}`) ? translate(`kinds.${kind}`) : kind;
  }

  function problem(code?: string) {
    if (!code) {
      return null;
    }
    return KNOWN_ERRORS.has(code)
      ? translate(`errors.${code}`)
      : translate("errors.other", { code });
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {chatId === "" ? (
          <Alert variant="info">
            <AlertDescription>{translate("noGroup")}</AlertDescription>
          </Alert>
        ) : null}
        {flat ? (
          <Alert variant="info">
            <AlertDescription>{translate("flat")}</AlertDescription>
          </Alert>
        ) : null}
        {chatId !== "" && !flat ? (
          <p className="text-sm">
            {translate("summary", { group: chatId, bound, total: topics.length })}
            {creating ? ` · ${translate("creating")}` : ""}
            {repairing ? ` · ${translate("repairing")}` : ""}
          </p>
        ) : null}
        {failed.length > 0 ? (
          <Alert variant="warning">
            <AlertDescription>{problem(failed[0].errorCode)}</AlertDescription>
          </Alert>
        ) : null}

        {error ? (
          <Alert variant="danger">
            <AlertDescription>{translate("failed", { detail: error.message })}</AlertDescription>
          </Alert>
        ) : null}
        {isLoading ? <Skeleton className="h-24 w-full" /> : null}
        {!isLoading && topics.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : null}
        {topics.length > 0 ? (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("stream")}</TableHead>
                <TableHead>{translate("status")}</TableHead>
                <TableHead className="text-right">{translate("thread")}</TableHead>
                <TableHead>{translate("updated")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {topics.map((topic) => (
                <TableRow key={topic.kind}>
                  <TableCell>{label(topic.kind)}</TableCell>
                  <TableCell>
                    <Badge
                      title={problem(topic.errorCode) ?? undefined}
                      variant={STATUS_VARIANT[topic.status]}
                    >
                      {translate(
                        topic.repairing ? "statuses.repairing" : `statuses.${topic.status}`,
                      )}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right font-mono text-xs">
                    {topic.topicId ?? "—"}
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs">
                    <Timestamp value={topic.updatedAt} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : null}

        {can("settings.write") && chatId !== "" && !flat ? (
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={pending || bound === 0}
              onClick={() => setConfirming("repair")}
              type="button"
              variant="outline"
            >
              {translate("repair")}
            </Button>
            <Button
              disabled={pending}
              onClick={() => setConfirming("recreate")}
              type="button"
              variant="outline"
            >
              {translate("recreate")}
            </Button>
          </div>
        ) : null}
      </CardContent>
      <ConfirmDialog
        cancelLabel={translate("cancel")}
        confirmLabel={translate(confirming === "repair" ? "repair" : "recreate")}
        description={translate(confirming === "repair" ? "repairWarning" : "recreateWarning")}
        onConfirm={() => request(confirming === "repair" ? "repair" : "recreate")}
        onOpenChange={(open) => {
          if (!open) {
            setConfirming(null);
          }
        }}
        open={confirming !== null}
        pending={pending}
        title={translate(confirming === "repair" ? "repairTitle" : "recreateTitle")}
      />
    </Card>
  );
}

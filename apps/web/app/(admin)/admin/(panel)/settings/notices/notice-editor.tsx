"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { toast } from "@omniflow/ui/toast";
import { Eye, RotateCcw, Send } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import type { Notice, NoticePreview, NoticeTest } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

const LOCALES = ["en", "ru"] as const;

/**
 * Rewording the messages the installation sends on its own initiative.
 *
 * Campaign copy is written under marketing, where an operator composes a
 * message and chooses who receives it. These are different: they reach every
 * customer, they are triggered by something that happened to a subscription
 * rather than by a decision to send, and until now they were compiled in — so
 * the one voice every customer hears repeatedly was the one nobody could
 * change.
 *
 * The screen is shaped by the fact that nobody reads the text between the
 * operator writing it and a customer receiving it. The variable reference is
 * beside the editor rather than in documentation, the preview renders against
 * the same sample values a test send uses, and a body that names a value the
 * notice does not carry is refused on save with the offending placeholder in
 * the message.
 */
export function NoticeEditor() {
  const translate = useTranslations("admin.notices");
  const { can } = useSession();
  const { data, error, isLoading, mutate } = useSWR<{ items: Notice[] }, ApiError>(
    "/v1/panel/notices",
    fetcher,
  );

  const [selected, setSelected] = useState("");
  const [locale, setLocale] = useState<string>("en");

  const notices = data?.items ?? [];
  const active = notices.find((notice) => notice.code === selected) ?? notices[0];

  if (isLoading) {
    return <Skeleton className="h-96 w-full" />;
  }
  if (error || notices.length === 0) {
    return <StateNotice title={translate("failed")} variant="danger" />;
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,18rem)_minmax(0,1fr)]">
      <Card className="h-fit p-2">
        <ul className="flex flex-col">
          {notices.map((notice) => {
            const overridden = Object.keys(notice.overrides ?? {}).length;
            return (
              <li key={notice.code}>
                <button
                  className={`flex w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left text-sm transition-colors hover:bg-muted ${
                    notice.code === active?.code ? "bg-muted font-medium" : ""
                  }`}
                  onClick={() => setSelected(notice.code)}
                  type="button"
                >
                  {translate(`code.${notice.code}` as never)}
                  {overridden > 0 && (
                    <Badge variant="neutral">{translate("edited", { count: overridden })}</Badge>
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      </Card>

      {active && (
        <NoticeForm
          canWrite={can("settings.write")}
          key={`${active.code}-${locale}`}
          locale={locale}
          notice={active}
          onLocale={setLocale}
          onSaved={mutate}
        />
      )}
    </div>
  );
}

function NoticeForm({
  canWrite,
  locale,
  notice,
  onLocale,
  onSaved,
}: {
  canWrite: boolean;
  locale: string;
  notice: Notice;
  onLocale: (locale: string) => void;
  onSaved: () => void;
}) {
  const translate = useTranslations("admin.notices");
  const { run, pending } = useOperatorAction();
  const bodyId = useId();

  const override = notice.overrides?.[locale];
  const shipped = notice.default[locale] ?? "";
  const [body, setBody] = useState(override?.body ?? shipped);
  const [preview, setPreview] = useState<NoticePreview | null>(null);
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  // The editor starts on whatever is in force. An operator opening a notice
  // they have never touched should see the words their customers are receiving,
  // not an empty box that implies there is no message.
  useEffect(() => {
    setBody(override?.body ?? shipped);
    setPreview(null);
    setProblem("");
  }, [override?.body, shipped]);

  const tests = useSWR<{ items: NoticeTest[] }, ApiError>(
    `/v1/panel/notices/${notice.code}/tests?limit=5`,
    fetcher,
  );

  async function render() {
    setBusy(true);
    setProblem("");
    try {
      setPreview(
        await apiFetch<NoticePreview>(`/v1/panel/notices/${notice.code}/preview`, {
          body: JSON.stringify({ body, locale }),
          method: "POST",
        }),
      );
    } catch (caught) {
      setPreview(null);
      // The API refuses with the offending placeholder or tag in the detail, so
      // it is shown rather than replaced with "invalid" — which is not
      // something anybody can act on while looking at a text area.
      setProblem(problemDetail(caught) || translate("previewFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function save() {
    if (
      await run(`/v1/panel/notices/${notice.code}`, {
        body: { body, locale },
        method: "PUT",
      })
    ) {
      toast.success(translate("saved"));
      onSaved();
    }
  }

  async function revert() {
    if (
      await run(`/v1/panel/notices/${notice.code}?locale=${locale}`, {
        method: "DELETE",
      })
    ) {
      setBody(shipped);
      toast.success(translate("reverted"));
      onSaved();
    }
  }

  async function sendTest() {
    setBusy(true);
    try {
      await apiFetch<NoticeTest>(`/v1/panel/notices/${notice.code}/test`, {
        body: JSON.stringify({ body, locale }),
        method: "POST",
      });
      toast.success(translate("testQueued"));
      tests.mutate();
    } catch (caught) {
      setProblem(problemDetail(caught) || translate("testFailed"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>{translate(`code.${notice.code}` as never)}</CardTitle>
          <CardDescription>{translate(`about.${notice.code}` as never)}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex flex-col gap-1.5">
              <Label>{translate("language")}</Label>
              <Select onValueChange={onLocale} value={locale}>
                <SelectTrigger className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LOCALES.map((code) => (
                    <SelectItem key={code} value={code}>
                      {translate(`locale.${code}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <p className="text-subtle-foreground text-sm">
              {override ? translate("usingOverride") : translate("usingDefault")}
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor={bodyId}>{translate("body")}</Label>
            <textarea
              className="min-h-40 w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-[13px] leading-relaxed outline-none focus-visible:ring-[3px] focus-visible:ring-ring/30"
              disabled={!canWrite}
              id={bodyId}
              onChange={(event) => setBody(event.target.value)}
              spellCheck={false}
              value={body}
            />
            <p className="text-subtle-foreground text-xs">{translate("markup")}</p>
          </div>

          {notice.variables && notice.variables.length > 0 ? (
            <div className="rounded-lg border border-border p-3">
              <p className="mb-2 font-medium text-sm">{translate("variables")}</p>
              <dl className="grid gap-1.5 text-sm sm:grid-cols-[auto_minmax(0,1fr)]">
                {notice.variables.map((variable) => (
                  <div className="contents" key={variable.name}>
                    <dt className="font-mono text-accent text-xs">{`{${variable.name}}`}</dt>
                    <dd className="text-subtle-foreground">{variable.purpose}</dd>
                  </div>
                ))}
              </dl>
            </div>
          ) : (
            <p className="text-subtle-foreground text-sm">{translate("noVariables")}</p>
          )}

          {problem && <StateNotice title={problem} variant="danger" />}

          {preview && (
            <div className="flex flex-col gap-1.5">
              <Label>{translate("preview")}</Label>
              {/* Rendered as text, not as HTML. The body is Telegram markup and
                  this is a browser: interpreting it here would show something
                  neither Telegram nor the customer will produce, and injecting
                  operator-authored markup into the panel is not a thing worth
                  doing to save an operator one mental step. The test send is
                  what shows the real rendering. */}
              <pre className="whitespace-pre-wrap rounded-lg border border-border bg-muted p-3 text-[13px]">
                {preview.rendered}
              </pre>
            </div>
          )}

          {canWrite && (
            <div className="flex flex-wrap gap-2">
              <Button disabled={busy || pending} onClick={save}>
                {translate("save")}
              </Button>
              <Button disabled={busy} onClick={render} variant="secondary">
                <Eye aria-hidden className="size-4" />
                {translate("renderPreview")}
              </Button>
              <Button disabled={busy} onClick={sendTest} variant="secondary">
                <Send aria-hidden className="size-4" />
                {translate("sendTest")}
              </Button>
              {override && (
                <Button disabled={pending} onClick={revert} variant="ghost">
                  <RotateCcw aria-hidden className="size-4" />
                  {translate("revert")}
                </Button>
              )}
            </div>
          )}
        </CardContent>
      </Card>

      <TestHistory tests={tests.data?.items ?? []} />
    </div>
  );
}

/**
 * What has been queued for the operator group.
 *
 * Without it a test send is a button that appears to do nothing: the bot
 * collects on its own schedule, and the message arrives in Telegram rather than
 * on this screen.
 */
function TestHistory({ tests }: { tests: NoticeTest[] }) {
  const translate = useTranslations("admin.notices");
  if (tests.length === 0) {
    return null;
  }
  return (
    <Card className="p-4">
      <p className="mb-2 font-medium text-sm">{translate("tests")}</p>
      <ul className="flex flex-col gap-1 text-sm">
        {tests.map((test) => (
          <li className="flex items-center gap-3" key={test.id}>
            <Badge
              variant={
                test.status === "sent" ? "success" : test.status === "failed" ? "danger" : "warning"
              }
            >
              {translate(`testStatus.${test.status}`)}
            </Badge>
            <span className="text-subtle-foreground">
              {translate(`locale.${test.locale}` as never)}
            </span>
            {test.errorCode && <span className="text-subtle-foreground">{test.errorCode}</span>}
          </li>
        ))}
      </ul>
      <p className="mt-2 text-subtle-foreground text-xs">{translate("testsNote")}</p>
    </Card>
  );
}

/** The `detail` of an RFC 9457 problem, which is where the refusal says what is wrong. */
function problemDetail(caught: unknown): string {
  const problem = (caught as { problem?: { detail?: string } })?.problem;
  return problem?.detail ?? "";
}

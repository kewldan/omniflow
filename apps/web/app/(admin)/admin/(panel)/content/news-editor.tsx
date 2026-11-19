"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { toast } from "@omniflow/ui/toast";
import { Plus } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

const ENDPOINT = "/v1/panel/marketing/news";
const CATEGORIES = ["news", "announcement", "incident", "maintenance"] as const;
const LOCALES = ["ru", "en"] as const;

type Translation = { title: string; body: string };
type NewsPost = {
  id: string;
  slug: string;
  category: string;
  class: string;
  status: string;
  scheduledFor?: string;
  publishedAt?: string;
  expiresAt?: string;
  updatedAt: string;
  titleEn?: string;
  titleRu?: string;
  translations?: Record<string, Translation>;
  reads: number;
};

/** "2026-11-20T14:05" in local time, the shape DateTimeField speaks. */
function toLocalField(iso?: string): string {
  if (!iso) {
    return "";
  }
  const date = new Date(iso);
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function toISO(local: string): string | null {
  return local ? new Date(local).toISOString() : null;
}

/**
 * The news editor. A post is written in Russian, English, or both, saved as a
 * draft, and then published now, scheduled for later (the worker publishes it
 * on time), unpublished, or archived. Customers read it in the bot and the web
 * panel; a published post counts towards their unread badge.
 */
export function NewsEditorCard() {
  const translate = useTranslations("admin.news");
  const { can } = useSession();
  const editable = can("marketing.write");
  const { data, error, isLoading, mutate } = useSWR<{ items: NewsPost[] }, ApiError>(
    ENDPOINT,
    fetcher,
  );
  const [editing, setEditing] = useState<string | null>(null);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {isLoading ? <Skeleton className="h-24 w-full" /> : null}
        {error ? (
          <p className="text-destructive text-sm" role="alert">
            {translate("loadFailed")}
          </p>
        ) : null}
        {data && data.items.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : null}
        {(data?.items ?? []).map((post) => (
          <div
            className="flex flex-wrap items-center justify-between gap-2 rounded-md border p-3"
            key={post.id}
          >
            <div className="flex min-w-0 flex-col gap-1">
              <span className="truncate font-medium">
                {post.titleRu || post.titleEn || post.slug}
              </span>
              <span className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
                <Badge variant="outline">{translate(`status.${post.status}`)}</Badge>
                <span>{translate(`category.${post.category}`)}</span>
                {post.scheduledFor && post.status === "scheduled" ? (
                  <Timestamp value={post.scheduledFor} />
                ) : null}
                {post.publishedAt ? <Timestamp value={post.publishedAt} /> : null}
              </span>
            </div>
            <Button onClick={() => setEditing(post.id)} size="sm" variant="outline">
              {editable ? translate("edit") : translate("open")}
            </Button>
          </div>
        ))}
        {editable ? (
          <Button
            className="self-start"
            onClick={() => setEditing("")}
            size="sm"
            variant="secondary"
          >
            <Plus aria-hidden />
            {translate("add")}
          </Button>
        ) : null}
        {editing !== null ? (
          <NewsForm
            editable={editable}
            onClose={() => setEditing(null)}
            onSaved={(id) => {
              setEditing(id);
              mutate();
            }}
            postId={editing}
          />
        ) : null}
      </CardContent>
    </Card>
  );
}

function NewsForm({
  editable,
  onClose,
  onSaved,
  postId,
}: {
  editable: boolean;
  onClose: () => void;
  onSaved: (id: string) => void;
  postId: string;
}) {
  const translate = useTranslations("admin.news");
  const ids = { slug: useId(), category: useId(), expires: useId(), schedule: useId() };
  const { data } = useSWR<NewsPost, ApiError>(postId ? `${ENDPOINT}/${postId}` : null, fetcher);
  const [slug, setSlug] = useState("");
  const [category, setCategory] = useState<string>("news");
  const [expiresAt, setExpiresAt] = useState("");
  const [scheduleAt, setScheduleAt] = useState("");
  const [translations, setTranslations] = useState<Record<string, Translation>>({
    ru: { title: "", body: "" },
    en: { title: "", body: "" },
  });
  const [saving, setSaving] = useState(false);
  const { run, pending } = useOperatorAction();

  useEffect(() => {
    if (!data) {
      return;
    }
    setSlug(data.slug);
    setCategory(data.category);
    setExpiresAt(toLocalField(data.expiresAt));
    setTranslations({
      ru: data.translations?.ru ?? { title: "", body: "" },
      en: data.translations?.en ?? { title: "", body: "" },
    });
  }, [data]);

  async function save() {
    setSaving(true);
    try {
      const saved = await apiFetch<NewsPost>(postId ? `${ENDPOINT}/${postId}` : ENDPOINT, {
        method: postId ? "PUT" : "POST",
        body: JSON.stringify({
          slug,
          category,
          class: "transactional",
          expiresAt: toISO(expiresAt),
          translations,
        }),
      });
      toast.success(translate("saved"));
      onSaved(saved.id);
    } catch (failure) {
      const code = (failure as ApiError).status;
      toast.error(code === 409 ? translate("slugTaken") : translate("saveFailed"));
    } finally {
      setSaving(false);
    }
  }

  async function setState(status: string, scheduledFor?: string | null) {
    if (
      await run(`${ENDPOINT}/${postId}/state`, {
        method: "POST",
        body: { status, scheduledFor: scheduledFor ?? null },
        reason: `news ${status}`,
      })
    ) {
      toast.success(translate("stateChanged"));
      onSaved(postId);
    } else {
      toast.error(translate("stateFailed"));
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-md border bg-muted/30 p-4">
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.slug}>{translate("slug")}</Label>
          <Input
            disabled={!editable}
            id={ids.slug}
            onChange={(event) => setSlug(event.target.value)}
            placeholder="spring-sale"
            value={slug}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.category}>{translate("categoryLabel")}</Label>
          <Select disabled={!editable} onValueChange={setCategory} value={category}>
            <SelectTrigger id={ids.category}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {CATEGORIES.map((value) => (
                <SelectItem key={value} value={value}>
                  {translate(`category.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.expires}>{translate("expiresAt")}</Label>
          <DateTimeField
            disabled={!editable}
            hourLabel={translate("hour")}
            id={ids.expires}
            minuteLabel={translate("minute")}
            onChange={setExpiresAt}
            placeholder={translate("never")}
            value={expiresAt}
          />
        </div>
      </div>
      <Tabs defaultValue="ru">
        <TabsList>
          {LOCALES.map((locale) => (
            <TabsTrigger key={locale} value={locale}>
              {translate(`locale.${locale}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        {LOCALES.map((locale) => (
          <TabsContent className="flex flex-col gap-2" key={locale} value={locale}>
            <Label htmlFor={`${ids.slug}-${locale}-title`}>{translate("headline")}</Label>
            <Input
              disabled={!editable}
              id={`${ids.slug}-${locale}-title`}
              maxLength={200}
              onChange={(event) =>
                setTranslations({
                  ...translations,
                  [locale]: { ...translations[locale], title: event.target.value },
                })
              }
              value={translations[locale]?.title ?? ""}
            />
            <Label htmlFor={`${ids.slug}-${locale}-body`}>{translate("body")}</Label>
            <Textarea
              disabled={!editable}
              id={`${ids.slug}-${locale}-body`}
              maxLength={3500}
              onChange={(event) =>
                setTranslations({
                  ...translations,
                  [locale]: { ...translations[locale], body: event.target.value },
                })
              }
              rows={6}
              value={translations[locale]?.body ?? ""}
            />
          </TabsContent>
        ))}
      </Tabs>
      {data ? (
        <p className="text-muted-foreground text-xs">
          {translate("meta", { status: translate(`status.${data.status}`), reads: data.reads })}
        </p>
      ) : null}
      <div className="flex flex-wrap items-end gap-2">
        {editable ? (
          <Button disabled={saving} onClick={save}>
            {translate("save")}
          </Button>
        ) : null}
        {editable && postId ? (
          <>
            {data?.status !== "published" ? (
              <Button disabled={pending} onClick={() => setState("published")} variant="secondary">
                {translate("publishNow")}
              </Button>
            ) : (
              <Button disabled={pending} onClick={() => setState("unpublished")} variant="outline">
                {translate("unpublish")}
              </Button>
            )}
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.schedule}>{translate("scheduleAt")}</Label>
              <DateTimeField
                fromDate={new Date()}
                hourLabel={translate("hour")}
                id={ids.schedule}
                minuteLabel={translate("minute")}
                onChange={setScheduleAt}
                placeholder={translate("pickMoment")}
                value={scheduleAt}
              />
            </div>
            <Button
              disabled={pending || !scheduleAt}
              onClick={() => setState("scheduled", toISO(scheduleAt))}
              variant="outline"
            >
              {translate("schedule")}
            </Button>
            {data?.status !== "archived" ? (
              <Button disabled={pending} onClick={() => setState("archived")} variant="ghost">
                {translate("archive")}
              </Button>
            ) : (
              <Button disabled={pending} onClick={() => setState("draft")} variant="ghost">
                {translate("restore")}
              </Button>
            )}
          </>
        ) : null}
        <Button onClick={onClose} variant="ghost">
          {translate("close")}
        </Button>
      </div>
    </div>
  );
}

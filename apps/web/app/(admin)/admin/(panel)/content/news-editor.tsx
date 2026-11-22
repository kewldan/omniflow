"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
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
export const CATEGORIES_ENDPOINT = "/v1/panel/marketing/news-categories";
const LOCALES = ["ru", "en"] as const;

export type NewsCategory = {
  code: string;
  nameRu: string;
  nameEn: string;
  color: string;
  kind: string;
  sortOrder: number;
  posts: number;
};

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
  views: number;
  tags: string[];
  coverUrl?: string;
  featured: boolean;
};

/** "vpn, Скидки #sale" → ["vpn", "скидки", "sale"]; the server normalises too. */
function parseTags(value: string): string[] {
  return value
    .split(/[\s,]+/)
    .map((tag) => tag.replace(/^#/, "").toLowerCase())
    .filter(Boolean);
}

function CategoryDot({ color }: { color?: string }) {
  return (
    <span
      aria-hidden
      className="inline-block size-2.5 shrink-0 rounded-full"
      style={{ backgroundColor: color ?? "transparent" }}
    />
  );
}

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
  const { data: categories } = useSWR<{ items: NewsCategory[] }, ApiError>(
    CATEGORIES_ENDPOINT,
    fetcher,
  );
  const categoryOf = (code: string) => categories?.items.find((item) => item.code === code);

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
            className="flex flex-wrap items-center justify-between gap-2 rounded-md border bg-well p-3"
            key={post.id}
          >
            <div className="flex min-w-0 flex-col gap-1">
              <span className="truncate font-medium">
                {post.titleRu || post.titleEn || post.slug}
              </span>
              <span className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
                <Badge variant="outline">{translate(`status.${post.status}`)}</Badge>
                <span className="flex items-center gap-1">
                  <CategoryDot color={categoryOf(post.category)?.color} />
                  {categoryOf(post.category)?.nameRu ?? post.category}
                </span>
                {post.featured ? <Badge variant="solid">{translate("featured")}</Badge> : null}
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
            categories={categories?.items ?? []}
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
  categories,
  editable,
  onClose,
  onSaved,
  postId,
}: {
  categories: NewsCategory[];
  editable: boolean;
  onClose: () => void;
  onSaved: (id: string) => void;
  postId: string;
}) {
  const translate = useTranslations("admin.news");
  const ids = {
    slug: useId(),
    category: useId(),
    expires: useId(),
    schedule: useId(),
    tags: useId(),
    cover: useId(),
    featured: useId(),
  };
  const { data } = useSWR<NewsPost, ApiError>(postId ? `${ENDPOINT}/${postId}` : null, fetcher);
  const [slug, setSlug] = useState("");
  const [category, setCategory] = useState<string>("news");
  const [expiresAt, setExpiresAt] = useState("");
  const [tags, setTags] = useState("");
  const [coverUrl, setCoverUrl] = useState("");
  const [featured, setFeatured] = useState(false);
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
    setTags((data.tags ?? []).join(", "));
    setCoverUrl(data.coverUrl ?? "");
    setFeatured(data.featured);
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
          tags: parseTags(tags),
          coverUrl: coverUrl.trim(),
          featured,
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
              {categories.map((item) => (
                <SelectItem key={item.code} value={item.code}>
                  <span className="flex items-center gap-2">
                    <CategoryDot color={item.color} />
                    {item.nameRu} / {item.nameEn}
                  </span>
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
      <div className="flex items-center gap-2">
        <Switch
          checked={featured}
          disabled={!editable}
          id={ids.featured}
          onCheckedChange={setFeatured}
        />
        <Label htmlFor={ids.featured}>{translate("featuredLabel")}</Label>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.tags}>{translate("tags")}</Label>
          <Input
            disabled={!editable}
            id={ids.tags}
            onChange={(event) => setTags(event.target.value)}
            placeholder="vpn, sale"
            value={tags}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.cover}>{translate("cover")}</Label>
          <Input
            disabled={!editable}
            id={ids.cover}
            inputMode="url"
            maxLength={2000}
            onChange={(event) => setCoverUrl(event.target.value)}
            placeholder="https://"
            value={coverUrl}
          />
        </div>
      </div>
      <p className="text-muted-foreground text-xs">{translate("extrasHint")}</p>
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
          {translate("meta", {
            status: translate(`status.${data.status}`),
            reads: data.reads,
            views: data.views,
          })}
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

const KINDS = ["news", "announcement", "incident", "maintenance"] as const;
const EMPTY_CATEGORY: NewsCategory = {
  code: "",
  nameRu: "",
  nameEn: "",
  color: "#2563eb",
  kind: "news",
  sortOrder: 50,
  posts: 0,
};

/**
 * News categories: a name in both languages, a colour, an order, and the
 * notification kind posts in it are announced under — the kind decides which
 * of a customer's news preferences silences them. A category with posts
 * cannot be deleted.
 */
export function NewsCategoriesCard() {
  const translate = useTranslations("admin.news.categories");
  const { can } = useSession();
  const editable = can("marketing.write");
  const { data, isLoading, mutate } = useSWR<{ items: NewsCategory[] }, ApiError>(
    CATEGORIES_ENDPOINT,
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const [form, setForm] = useState<NewsCategory>(EMPTY_CATEGORY);
  const ids = {
    code: useId(),
    ru: useId(),
    en: useId(),
    color: useId(),
    kind: useId(),
    order: useId(),
  };

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (
      await run(CATEGORIES_ENDPOINT, {
        body: { ...form, code: form.code.trim().toLowerCase() },
        method: "PUT",
        reason: "news category",
      })
    ) {
      setForm(EMPTY_CATEGORY);
      void mutate();
    }
  }

  async function remove(code: string) {
    if (
      await run(`${CATEGORIES_ENDPOINT}/${code}`, {
        method: "DELETE",
        reason: "news category deleted",
      })
    ) {
      void mutate();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {isLoading ? <Skeleton className="h-16 w-full" /> : null}
        {(data?.items ?? []).map((category) => (
          <div
            className="flex flex-wrap items-center justify-between gap-2 rounded-md border bg-well p-3"
            key={category.code}
          >
            <span className="flex min-w-0 items-center gap-2">
              <CategoryDot color={category.color} />
              <span className="font-medium">{category.nameRu}</span>
              <span className="text-muted-foreground">/ {category.nameEn}</span>
              <Badge variant="outline">{translate(`kinds.${category.kind}`)}</Badge>
              <span className="font-mono text-muted-foreground text-xs">{category.code}</span>
              <span className="text-muted-foreground text-xs">
                {translate("posts", { count: category.posts })}
              </span>
            </span>
            {editable ? (
              <span className="flex gap-2">
                <Button onClick={() => setForm(category)} size="sm" variant="ghost">
                  {translate("edit")}
                </Button>
                <Button
                  disabled={pending || category.posts > 0}
                  onClick={() => void remove(category.code)}
                  size="sm"
                  variant="outline"
                >
                  {translate("delete")}
                </Button>
              </span>
            ) : null}
          </div>
        ))}
        {editable ? (
          <form className="grid gap-3 sm:grid-cols-3" onSubmit={save}>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.code}>{translate("code")}</Label>
              <Input
                id={ids.code}
                maxLength={32}
                onChange={(event) => setForm({ ...form, code: event.target.value })}
                placeholder="promo"
                value={form.code}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.ru}>{translate("nameRu")}</Label>
              <Input
                id={ids.ru}
                maxLength={40}
                onChange={(event) => setForm({ ...form, nameRu: event.target.value })}
                value={form.nameRu}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.en}>{translate("nameEn")}</Label>
              <Input
                id={ids.en}
                maxLength={40}
                onChange={(event) => setForm({ ...form, nameEn: event.target.value })}
                value={form.nameEn}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.color}>{translate("color")}</Label>
              <span className="flex items-center gap-2">
                <CategoryDot color={/^#[0-9a-f]{6}$/i.test(form.color) ? form.color : undefined} />
                <Input
                  id={ids.color}
                  maxLength={7}
                  onChange={(event) => setForm({ ...form, color: event.target.value })}
                  placeholder="#2563eb"
                  value={form.color}
                />
              </span>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.kind}>{translate("kind")}</Label>
              <Select onValueChange={(kind) => setForm({ ...form, kind })} value={form.kind}>
                <SelectTrigger id={ids.kind}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {KINDS.map((kind) => (
                    <SelectItem key={kind} value={kind}>
                      {translate(`kinds.${kind}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.order}>{translate("order")}</Label>
              <Input
                id={ids.order}
                inputMode="numeric"
                onChange={(event) =>
                  setForm({ ...form, sortOrder: Number(event.target.value) || 0 })
                }
                value={String(form.sortOrder)}
              />
            </div>
            <div className="flex gap-2 sm:col-span-3">
              <Button
                disabled={
                  pending || !form.code.trim() || !form.nameRu.trim() || !form.nameEn.trim()
                }
                type="submit"
              >
                {translate("save")}
              </Button>
              <Button onClick={() => setForm(EMPTY_CATEGORY)} type="button" variant="ghost">
                {translate("clear")}
              </Button>
            </div>
          </form>
        ) : null}
        {error ? (
          <p className="text-destructive text-sm" role="alert">
            {error.status === 409 ? translate("inUse") : translate("invalid")}
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

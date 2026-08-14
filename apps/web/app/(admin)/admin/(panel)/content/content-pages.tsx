"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { toast } from "@omniflow/ui/toast";
import { ExternalLink, Plus, Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import type { InfoPage, InfoPageList } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

const ENDPOINT = "/v1/panel/content/pages";
const LOCALES = ["en", "ru"] as const;

/**
 * The documents an operator publishes at addresses of their own: the FAQ, the
 * terms, the offer, the privacy policy.
 *
 * These are not news posts, and the difference decides the shape of the screen.
 * A news post is dated, expires, is read once, and counts towards an unread
 * badge. One of these is a permanent address whose content changes in place and
 * which anybody can read without signing in — which is the point, because
 * payment providers and application stores require an offer and a privacy
 * policy at a stable address before they approve an account.
 *
 * The body is the operator's own text in a small syntax, and it is never HTML.
 * The API parses it into a block structure and the browser renders text nodes,
 * so nothing typed here can become markup on the origin that holds the session
 * cookie.
 */
export function ContentPagesScreen() {
  const translate = useTranslations("admin.content");
  const { can } = useSession();
  const editable = can("marketing.write");

  const { data, isLoading, mutate } = useSWR<InfoPageList, ApiError>(ENDPOINT, fetcher);
  const [editing, setEditing] = useState<string | null>(null);

  if (isLoading || !data) {
    return (
      <div className="flex flex-col gap-5">
        <PageHeader description={translate("description")} title={translate("title")} />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />

      <Card>
        <CardHeader>
          <CardTitle>{translate("list.title")}</CardTitle>
          <CardDescription>{translate("list.description")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {(data.items ?? []).length === 0 ? (
            <StateNotice
              description={translate("list.emptyHint")}
              title={translate("list.empty")}
            />
          ) : (
            (data.items ?? []).map((page) => (
              <PageRow
                editable={editable}
                key={page.slug}
                onChanged={() => mutate()}
                onEdit={() => setEditing(page.slug)}
                page={page}
              />
            ))
          )}
          {editable ? (
            <Button
              className="self-start"
              onClick={() => setEditing("")}
              size="sm"
              variant="secondary"
            >
              <Plus aria-hidden />
              {translate("list.add")}
            </Button>
          ) : null}
        </CardContent>
      </Card>

      {editing !== null ? (
        <PageEditor
          kinds={data.kinds}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            mutate();
          }}
          slug={editing}
        />
      ) : null}
    </div>
  );
}

function PageRow({
  editable,
  onChanged,
  onEdit,
  page,
}: {
  editable: boolean;
  onChanged: () => void;
  onEdit: () => void;
  page: InfoPage;
}) {
  const translate = useTranslations("admin.content");
  const { run, pending } = useOperatorAction();
  const [confirming, setConfirming] = useState(false);
  const published = Boolean(page.publishedAt);

  async function setPublication(next: boolean) {
    if (
      await run(`${ENDPOINT}/${page.slug}/publication`, {
        body: { published: next },
        method: "POST",
      })
    ) {
      onChanged();
      toast.success(translate(next ? "published" : "withdrawn"));
    }
  }

  async function remove() {
    setConfirming(false);
    if (await run(`${ENDPOINT}/${page.slug}`, { method: "DELETE" })) {
      onChanged();
      toast.success(translate("deleted"));
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border p-3">
      <div className="min-w-0 flex-1">
        <p className="font-medium text-sm">
          /pages/{page.slug}
          {published ? (
            <a
              className="ml-2 inline-flex items-center gap-1 text-subtle-foreground text-xs underline underline-offset-2"
              href={`/pages/${page.slug}`}
              rel="noreferrer"
              target="_blank"
            >
              {translate("open")}
              <ExternalLink aria-hidden className="size-3" />
            </a>
          ) : null}
        </p>
        <p className="text-subtle-foreground text-xs">
          {translate(`kind.${page.kind}`)}
          {" · "}
          {(page.availableLocales ?? []).join(", ") || translate("noLanguages")}
        </p>
      </div>

      <Badge variant={published ? "success" : "neutral"}>
        {translate(published ? "state.published" : "state.draft")}
      </Badge>
      {published && !page.listed ? (
        <Badge variant="outline">{translate("state.unlisted")}</Badge>
      ) : null}

      {editable ? (
        <div className="flex items-center gap-2">
          <Button onClick={onEdit} size="sm" variant="secondary">
            {translate("edit")}
          </Button>
          <Button
            disabled={pending}
            onClick={() => setPublication(!published)}
            size="sm"
            variant="ghost"
          >
            {translate(published ? "withdraw" : "publish")}
          </Button>
          <Button onClick={() => setConfirming(true)} size="sm" variant="ghost">
            <Trash2 aria-hidden />
          </Button>
          {/* Withdrawing is reversible and takes the address out of the world;
              deleting takes the address itself, which is what a payment
              provider approved. Only the second one confirms. */}
          <ConfirmDialog
            cancelLabel={translate("cancel")}
            confirmationPhrase={page.slug}
            confirmationPrompt={translate("deletePrompt", { slug: page.slug })}
            confirmLabel={translate("delete")}
            description={translate("deleteWarning")}
            destructive
            onConfirm={remove}
            onOpenChange={setConfirming}
            open={confirming}
            title={translate("deleteTitle", { slug: page.slug })}
          />
        </div>
      ) : null}
    </div>
  );
}

/** The editor: identity, then one tab per language. */
function PageEditor({
  kinds,
  onClose,
  onSaved,
  slug,
}: {
  kinds: string[];
  onClose: () => void;
  onSaved: () => void;
  slug: string;
}) {
  const translate = useTranslations("admin.content");
  const { run, pending } = useOperatorAction();

  const { data } = useSWR<InfoPage, ApiError>(slug ? `${ENDPOINT}/${slug}` : null, fetcher);
  const [form, setForm] = useState<InfoPage>({
    availableLocales: [],
    kind: "custom",
    listed: true,
    locales: [],
    slug: "",
    sortOrder: 0,
    updatedAt: "",
  });
  const [loaded, setLoaded] = useState(!slug);

  // The editor is opened per page and unmounted on close, so seeding once from
  // the response is enough and avoids fighting the operator's own typing.
  if (!loaded && data) {
    setForm(data);
    setLoaded(true);
  }

  const slugId = useId();
  const orderId = useId();

  function localeValue(locale: string, field: "title" | "body"): string {
    return form.locales?.find((entry) => entry.locale === locale)?.[field] ?? "";
  }

  function setLocale(locale: string, field: "title" | "body", value: string) {
    setForm((current) => {
      const locales = [...(current.locales ?? [])];
      const index = locales.findIndex((entry) => entry.locale === locale);
      if (index === -1) {
        locales.push({ body: "", locale, title: "", [field]: value });
      } else {
        locales[index] = { ...locales[index], [field]: value };
      }
      return { ...current, locales };
    });
  }

  async function save() {
    if (await run(ENDPOINT, { body: form, method: "PUT" })) {
      onSaved();
      toast.success(translate("saved"));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{slug ? `/pages/${slug}` : translate("editor.new")}</CardTitle>
        <CardDescription>{translate("editor.syntax")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor={slugId}>{translate("editor.slug")}</Label>
            <Input
              className="font-mono"
              // The address is the page's identity, so renaming is creating a
              // different page rather than moving this one.
              disabled={Boolean(slug)}
              id={slugId}
              onChange={(event) => setForm({ ...form, slug: event.target.value })}
              placeholder="privacy"
              value={form.slug}
            />
          </div>
          <KindChoice
            kinds={kinds}
            onChange={(kind) => setForm({ ...form, kind })}
            value={form.kind}
          />
          <div className="flex flex-col gap-2">
            <Label htmlFor={orderId}>{translate("editor.order")}</Label>
            <Input
              id={orderId}
              onChange={(event) => setForm({ ...form, sortOrder: Number(event.target.value) || 0 })}
              value={String(form.sortOrder)}
            />
          </div>
          <ListedSwitch onChange={(listed) => setForm({ ...form, listed })} value={form.listed} />
        </div>

        <Tabs defaultValue="en">
          <TabsList>
            {LOCALES.map((locale) => (
              <TabsTrigger key={locale} value={locale}>
                {translate(`language.${locale}`)}
              </TabsTrigger>
            ))}
          </TabsList>
          {LOCALES.map((locale) => (
            <TabsContent className="flex flex-col gap-3 pt-4" key={locale} value={locale}>
              <LocaleFields
                body={localeValue(locale, "body")}
                onBody={(value) => setLocale(locale, "body", value)}
                onTitle={(value) => setLocale(locale, "title", value)}
                title={localeValue(locale, "title")}
              />
            </TabsContent>
          ))}
        </Tabs>

        <div className="flex items-center gap-2">
          <Button disabled={pending} onClick={save}>
            {translate("save")}
          </Button>
          <Button onClick={onClose} variant="ghost">
            {translate("cancel")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function KindChoice({
  kinds,
  onChange,
  value,
}: {
  kinds: string[];
  onChange: (value: string) => void;
  value: string;
}) {
  const translate = useTranslations("admin.content");
  const id = useId();
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{translate("editor.kind")}</Label>
      <Select onValueChange={onChange} value={value}>
        <SelectTrigger id={id}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {kinds.map((kind) => (
            <SelectItem key={kind} value={kind}>
              {translate(`kind.${kind}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function ListedSwitch({ onChange, value }: { onChange: (value: boolean) => void; value: boolean }) {
  const translate = useTranslations("admin.content");
  const id = useId();
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{translate("editor.listed")}</Label>
      <div className="flex items-center gap-2">
        <Switch checked={value} id={id} onCheckedChange={onChange} />
        <span className="text-subtle-foreground text-xs">{translate("editor.listedHint")}</span>
      </div>
    </div>
  );
}

function LocaleFields({
  body,
  onBody,
  onTitle,
  title,
}: {
  body: string;
  onBody: (value: string) => void;
  onTitle: (value: string) => void;
  title: string;
}) {
  const translate = useTranslations("admin.content");
  const titleId = useId();
  const bodyId = useId();
  return (
    <>
      <div className="flex flex-col gap-2">
        <Label htmlFor={titleId}>{translate("editor.pageTitle")}</Label>
        <Input id={titleId} onChange={(event) => onTitle(event.target.value)} value={title} />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor={bodyId}>{translate("editor.body")}</Label>
        <textarea
          className="min-h-72 w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs leading-relaxed outline-none focus-visible:ring-[3px] focus-visible:ring-ring/30"
          id={bodyId}
          onChange={(event) => onBody(event.target.value)}
          spellCheck={false}
          value={body}
        />
        <p className="text-subtle-foreground text-xs">{translate("editor.bodyHint")}</p>
      </div>
    </>
  );
}

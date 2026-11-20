"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useMemo, useState } from "react";
import useSWR from "swr";

import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type Section = { section: string; document: Record<string, unknown>; version: number };
type PreviewButton = { key: string; label: string; kind: "screen" | "url" | "webapp" };
type Preview = { rows: PreviewButton[][]; entries: string[] };

/** Telegram fits three buttons in a row legibly on a phone; the bot enforces it. */
const MAX_ROW = 3;
/** Buttons whose place is fixed: the lapsed-subscription restore and Refresh. */
const FIXED = new Set(["recover", "refresh", ""]);

/** The rows of entry keys a preview shows, without the fixed buttons. */
function layoutFrom(rows: PreviewButton[][]): string[][] {
  return rows
    .map((buttons) => buttons.map((button) => button.key).filter((key) => !FIXED.has(key)))
    .filter((keys) => keys.length > 0);
}

function readLayout(value: unknown): string[][] | null {
  if (!Array.isArray(value) || value.length === 0) {
    return null;
  }
  return value
    .filter((entries): entries is unknown[] => Array.isArray(entries))
    .map((entries) => entries.filter((entry): entry is string => typeof entry === "string"))
    .filter((entries) => entries.length > 0);
}

/**
 * The main-menu layout editor. An operator reorders the bot's built-in entries
 * and their own buttons row by row, with keyboard-reachable move controls, and
 * sees the menu the bot would draw — rendered by the bot's own code from the
 * unsaved layout — before saving it to the bot section. Entries the layout
 * leaves out keep their shipped place after it, hidden entries stay hidden,
 * and entries that appear only when usable (top-up, cart, shop, offers) take
 * their place when they do.
 */
export function MenuLayoutEditor() {
  const translate = useTranslations("admin.menuLayout");
  const { can } = useSession();
  const editable = can("settings.write");
  const { data: sections, mutate } = useSWR<Listing<Section>, ApiError>(
    "/v1/panel/settings",
    fetcher,
  );
  const bot = sections?.items?.find((item) => item.section === "bot");
  const [layout, setLayout] = useState<string[][] | null>(null);
  const [locale, setLocale] = useState<"ru" | "en">("ru");
  const { run, pending } = useOperatorAction();

  // The saved document without a layout: the shipped arrangement, with the
  // operator's own buttons, which the editor starts from when none is saved.
  const shipped = useMemo(() => {
    if (!bot) {
      return null;
    }
    const { menuLayout: _ignored, ...rest } = bot.document;
    return rest;
  }, [bot]);
  const draft = useMemo(
    () => (shipped ? { ...shipped, ...(layout ? { menuLayout: layout } : {}) } : null),
    [shipped, layout],
  );
  const { data: preview, error } = useSWR<Preview, ApiError>(
    draft ? ["/v1/panel/bot/menu-preview", JSON.stringify(draft), locale] : null,
    ([path, document, language]: [string, string, string]) =>
      apiFetch<Preview>(path, {
        method: "POST",
        body: JSON.stringify({ document: JSON.parse(document), locale: language }),
      }),
    { keepPreviousData: true },
  );

  useEffect(() => {
    if (bot && layout === null) {
      const saved = readLayout(bot.document.menuLayout);
      if (saved) {
        setLayout(saved);
      }
    }
  }, [bot, layout]);

  if (!bot) {
    return null;
  }

  const labels = new Map<string, string>();
  for (const buttons of preview?.rows ?? []) {
    for (const button of buttons) {
      labels.set(button.key, button.label);
    }
  }
  const current = layout ?? layoutFrom(preview?.rows ?? []);
  const label = (key: string) => labels.get(key) ?? translate("unavailable", { key });

  function update(next: string[][]) {
    setLayout(next.filter((entries) => entries.length > 0));
  }

  function move(row: number, index: number, direction: "left" | "right" | "up" | "down") {
    const next = current.map((entries) => [...entries]);
    const [entry] = next[row].splice(index, 1);
    if (direction === "left" || direction === "right") {
      next[row].splice(direction === "left" ? index - 1 : index + 1, 0, entry);
    } else if (direction === "up") {
      if (row === 0 || next[row - 1].length >= MAX_ROW) {
        next.splice(row, 0, [entry]);
      } else {
        next[row - 1].push(entry);
      }
    } else if (row === next.length - 1 || next[row + 1].length >= MAX_ROW) {
      next.splice(row + 1, 0, [entry]);
    } else {
      next[row + 1].unshift(entry);
    }
    update(next);
  }

  async function save(menuLayout: string[][] | null) {
    if (!bot) {
      return;
    }
    const { menuLayout: _previous, ...rest } = bot.document;
    if (
      await run("/v1/panel/settings/bot", {
        method: "PUT",
        body: {
          document: menuLayout ? { ...rest, menuLayout } : rest,
          version: bot.version,
          secrets: {},
        },
        reason: "bot menu layout",
      })
    ) {
      toast.success(translate("saved"));
      if (!menuLayout) {
        setLayout(null);
      }
      await mutate();
    } else {
      toast.error(translate("saveFailed"));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-6 lg:grid-cols-2">
        <div className="flex flex-col gap-3">
          <div className="font-medium text-sm">{translate("rows")}</div>
          <ol className="flex flex-col gap-2">
            {current.map((entries, row) => (
              <li
                className="flex flex-col gap-1 rounded-md border border-border p-2"
                key={entries.join("|")}
              >
                <span className="font-mono text-[10px] text-subtle-foreground uppercase">
                  {translate("row", { number: row + 1 })}
                </span>
                {entries.map((entry, index) => (
                  <div className="flex items-center justify-between gap-2 text-sm" key={entry}>
                    <span className="truncate">{label(entry)}</span>
                    {editable ? (
                      <span className="flex shrink-0 gap-0.5">
                        <Button
                          aria-label={translate("moveLeft", { entry: label(entry) })}
                          disabled={index === 0}
                          onClick={() => move(row, index, "left")}
                          size="icon"
                          variant="ghost"
                        >
                          <ArrowLeft aria-hidden />
                        </Button>
                        <Button
                          aria-label={translate("moveRight", { entry: label(entry) })}
                          disabled={index === entries.length - 1}
                          onClick={() => move(row, index, "right")}
                          size="icon"
                          variant="ghost"
                        >
                          <ArrowRight aria-hidden />
                        </Button>
                        <Button
                          aria-label={translate("moveUp", { entry: label(entry) })}
                          disabled={row === 0 && entries.length === 1}
                          onClick={() => move(row, index, "up")}
                          size="icon"
                          variant="ghost"
                        >
                          <ArrowUp aria-hidden />
                        </Button>
                        <Button
                          aria-label={translate("moveDown", { entry: label(entry) })}
                          disabled={row === current.length - 1 && entries.length === 1}
                          onClick={() => move(row, index, "down")}
                          size="icon"
                          variant="ghost"
                        >
                          <ArrowDown aria-hidden />
                        </Button>
                      </span>
                    ) : null}
                  </div>
                ))}
              </li>
            ))}
          </ol>
          <p className="text-muted-foreground text-xs">{translate("hint")}</p>
          {editable ? (
            <div className="flex flex-wrap gap-2">
              <Button disabled={pending || current.length === 0} onClick={() => save(current)}>
                {translate("save")}
              </Button>
              <Button disabled={pending} onClick={() => save(null)} variant="outline">
                {translate("reset")}
              </Button>
            </div>
          ) : null}
        </div>

        <div className="flex flex-col gap-3">
          <div className="flex items-center justify-between gap-2">
            <span className="font-medium text-sm">{translate("preview")}</span>
            <Select onValueChange={(value) => setLocale(value as "ru" | "en")} value={locale}>
              <SelectTrigger aria-label={translate("language")} className="w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="ru">{translate("languages.ru")}</SelectItem>
                <SelectItem value="en">{translate("languages.en")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {error ? (
            <p className="text-danger-foreground text-sm">{translate("previewFailed")}</p>
          ) : (
            <figure
              aria-label={translate("preview")}
              className="m-0 flex flex-col gap-1 rounded-lg bg-muted p-2"
            >
              {(preview?.rows ?? []).map((buttons) => (
                <div
                  className="grid gap-1"
                  key={buttons.map((button) => button.key + button.label).join("|")}
                  style={{ gridTemplateColumns: `repeat(${buttons.length}, minmax(0, 1fr))` }}
                >
                  {buttons.map((button) => (
                    <span
                      className="truncate rounded-md bg-background px-2 py-1.5 text-center text-xs shadow-xs"
                      key={button.key + button.label}
                      title={
                        button.kind === "screen" ? undefined : translate(`kinds.${button.kind}`)
                      }
                    >
                      {button.label}
                    </span>
                  ))}
                </div>
              ))}
            </figure>
          )}
          <p className="text-muted-foreground text-xs">{translate("previewHint")}</p>
        </div>
      </CardContent>
    </Card>
  );
}

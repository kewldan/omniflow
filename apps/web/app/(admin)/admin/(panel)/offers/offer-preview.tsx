"use client";

import { useTranslations } from "next-intl";

/**
 * What the customer will see, in both languages, before it is sent.
 *
 * This renders the same shape the bot renders: title, terms, an expiry
 * countdown, and a dismissal. It is a mock rather than a live render — the bot
 * is a separate process and the panel cannot ask it to draw a message — so it
 * is deliberately plain, and it states that it is an approximation rather than
 * implying pixel fidelity it cannot promise.
 *
 * It exists because an offer is a promise about price. Showing an operator the
 * promise as the customer will read it, in both languages side by side, is much
 * cheaper than correcting it after it has been sent.
 */
export function OfferPreview({
  expiresAt,
  termsEn,
  termsRu,
  titleEn,
  titleRu,
}: {
  expiresAt: string;
  termsEn: string;
  termsRu: string;
  titleEn: string;
  titleRu: string;
}) {
  const translate = useTranslations("admin.offers");

  if (titleEn.trim() === "" && titleRu.trim() === "") {
    return null;
  }

  return (
    <section aria-labelledby="offer-preview-heading" className="flex flex-col gap-2">
      <h3
        className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]"
        id="offer-preview-heading"
      >
        {translate("preview.title")}
      </h3>
      <div className="grid gap-3 sm:grid-cols-2">
        <TelegramBubble
          countdown={countdownLabel(expiresAt, "en")}
          dismiss="Not interested"
          language="EN"
          terms={termsEn}
          title={titleEn}
        />
        <TelegramBubble
          countdown={countdownLabel(expiresAt, "ru")}
          dismiss="Не интересно"
          language="RU"
          terms={termsRu}
          title={titleRu}
        />
      </div>
      <p className="text-muted-foreground text-xs">{translate("preview.approximation")}</p>
    </section>
  );
}

/**
 * One language's rendering, shaped like the Telegram message it becomes.
 *
 * Empty copy is shown as a visible gap rather than hidden, because a missing
 * translation is exactly what this screen exists to make obvious.
 */
function TelegramBubble({
  countdown,
  dismiss,
  language,
  terms,
  title,
}: {
  countdown: string;
  dismiss: string;
  language: string;
  terms: string;
  title: string;
}) {
  const translate = useTranslations("admin.offers");
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-3">
      <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {language}
      </span>
      <p className="font-semibold text-sm">
        {title.trim() === "" ? (
          <span className="text-danger-foreground">{translate("preview.missing")}</span>
        ) : (
          `🎁 ${title}`
        )}
      </p>
      {terms.trim() !== "" && <p className="text-muted-foreground text-sm">{terms}</p>}
      <p className="text-muted-foreground text-xs">{countdown}</p>
      <div className="flex flex-wrap gap-2 pt-1">
        <span className="rounded-md border border-border px-2 py-1 text-xs">
          {language === "RU" ? "Открыть предложение" : "View offer"}
        </span>
        <span className="rounded-md border border-border px-2 py-1 text-xs">{dismiss}</span>
      </div>
    </div>
  );
}

/**
 * The countdown as the bot phrases it.
 *
 * The bot counts whole days remaining because an offer measured in hours reads
 * as pressure rather than as information. An expiry that has not been chosen
 * yet says so instead of showing a countdown from nothing.
 */
function countdownLabel(expiresAt: string, language: "en" | "ru"): string {
  if (expiresAt === "") {
    return language === "ru" ? "Срок не выбран" : "No expiry chosen yet";
  }
  const expiry = new Date(expiresAt);
  if (Number.isNaN(expiry.getTime())) {
    return language === "ru" ? "Срок не выбран" : "No expiry chosen yet";
  }
  const days = Math.max(0, Math.ceil((expiry.getTime() - Date.now()) / 86_400_000));
  if (days === 0) {
    return language === "ru" ? "Истекает сегодня" : "Expires today";
  }
  return language === "ru" ? `Осталось ${days} дн.` : `${days} day(s) left`;
}

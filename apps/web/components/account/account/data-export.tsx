"use client";

import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import { Download } from "lucide-react";
import { useFormatter, useTranslations } from "next-intl";
import { useState } from "react";

import { useProblemMessage } from "@/components/account/account/problem";
import type { ExportDocument, PrivacyOverview } from "@/components/account/account/types";
import { apiFetch } from "@/lib/api";

/** The filename the browser saves, dated so two exports do not overwrite each other. */
function exportFilename(generatedAt: string): string {
  const stamp = generatedAt.slice(0, 10) || new Date().toISOString().slice(0, 10);
  return `omniflow-personal-data-${stamp}.json`;
}

/**
 * The personal-data export.
 *
 * Two things are deliberate here. The first is that the document is never
 * rendered on the page: it is the most sensitive thing this API produces, and a
 * screen that painted it would leave it in a scroll position, a screenshot, and
 * a browser's back-forward cache. It goes straight to a file the customer
 * chooses where to keep.
 *
 * The second is that the contents are described before the button is pressed
 * and the exclusions are repeated after. An export that quietly omits things
 * reads as complete, and a customer cannot ask about an absence they cannot
 * see — so the sections, the standing redactions, and any section the server
 * had to cut short are all said out loud.
 */
export function DataExport({ preview }: { preview: PrivacyOverview["export"] }) {
  const translate = useTranslations("account.account");
  const format = useFormatter();
  const describeProblem = useProblemMessage();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [saved, setSaved] = useState<{
    generatedAt: string;
    redactions: string[];
    truncated: string[];
  } | null>(null);

  async function download() {
    setBusy(true);
    setFailure(null);
    try {
      // The request goes through the shared transport so the session cookie and
      // the CSRF token are attached; the file is assembled from the parsed
      // document here rather than from a raw response stream, which is also what
      // lets the screen report what the server said it left out.
      const personalData = await apiFetch<ExportDocument>("/v1/account/privacy/export", {
        method: "POST",
      });
      save(personalData);
      setSaved({
        generatedAt: personalData.generatedAt,
        // The document declares its own exclusions. They normally match the
        // preview above, and the screen only repeats them when they do not —
        // which would mean the server changed its mind between the two calls,
        // and is exactly the case a customer should not have to notice for
        // themselves by diffing a file against a page.
        redactions: personalData.redactions ?? [],
        truncated: personalData.truncated ?? [],
      });
      toast.success(translate("export.saved"));
    } catch (exportError) {
      setFailure(describeProblem(exportError));
    } finally {
      setBusy(false);
    }
  }

  /** Hands the document to the browser's own save flow and releases it again. */
  function save(personalData: ExportDocument) {
    const blob = new Blob([JSON.stringify(personalData, null, 2)], {
      type: "application/json",
    });
    const url = URL.createObjectURL(blob);
    const anchor = window.document.createElement("a");
    anchor.href = url;
    anchor.download = exportFilename(personalData.generatedAt);
    anchor.click();
    // Revoked immediately: the object URL is a live handle on the document, and
    // leaving it alive keeps the whole payload in memory for the tab's lifetime.
    URL.revokeObjectURL(url);
  }

  return (
    <section className="space-y-3 rounded-xl border border-border bg-card p-4">
      <div>
        <p className="font-medium text-[13.5px]">{translate("export.title")}</p>
        <p className="mt-1 text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("export.description")}
        </p>
      </div>

      <div>
        <p className="font-mono text-[11px] text-subtle-foreground">
          {translate("export.includes")}
        </p>
        <ul className="mt-1.5 flex flex-wrap gap-1.5">
          {preview.sections.map((section) => (
            <li
              className="rounded-xs bg-secondary px-2 py-0.5 text-[11px] text-secondary-foreground"
              key={section}
            >
              {translate(`export.section.${section}`)}
            </li>
          ))}
        </ul>
      </div>

      <div>
        <p className="font-mono text-[11px] text-subtle-foreground">
          {translate("export.excludes")}
        </p>
        <ul className="mt-1.5 space-y-1">
          {preview.redactions.map((redaction) => (
            <li className="text-[12.5px] text-muted-foreground leading-relaxed" key={redaction}>
              {translate(`export.redaction.${redaction}`)}
            </li>
          ))}
        </ul>
      </div>

      {!preview.contactValuesAvailable && (
        <p className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2.5 text-[12.5px] leading-relaxed">
          {translate("export.contactValuesUnavailable")}
        </p>
      )}

      {failure && (
        <p className="text-[12.5px] text-destructive leading-relaxed" role="alert">
          {failure}
        </p>
      )}

      <Button className="w-full" disabled={busy} onClick={download} size="lg" variant="outline">
        <Download aria-hidden />
        {busy ? translate("export.preparing") : translate("export.download")}
      </Button>

      {saved && (
        <div className="space-y-1.5 rounded-lg border border-border px-3 py-2.5" role="status">
          <p className="text-[12.5px] leading-relaxed">
            {translate("export.savedAt", {
              time: format.dateTime(new Date(saved.generatedAt), {
                day: "numeric",
                hour: "2-digit",
                minute: "2-digit",
                month: "short",
              }),
            })}
          </p>
          {saved.redactions.some((redaction) => !preview.redactions.includes(redaction)) && (
            <p className="text-[12.5px] text-warning leading-relaxed">
              {translate("export.redactionsChanged", {
                items: saved.redactions
                  .map((redaction) => translate(`export.redaction.${redaction}`))
                  .join(" "),
              })}
            </p>
          )}
          {/* A section the server had to cut short is the one thing a customer
              could not discover by reading the file, so it is stated here. */}
          {saved.truncated.length > 0 && (
            <p className="text-[12.5px] text-warning leading-relaxed">
              {translate("export.truncated", {
                sections: saved.truncated
                  .map((section) => translate(`export.section.${section}`))
                  .join(", "),
              })}
            </p>
          )}
        </div>
      )}

      <p className="text-[12px] text-subtle-foreground leading-relaxed">
        {translate("export.rateHint")}
      </p>
    </section>
  );
}

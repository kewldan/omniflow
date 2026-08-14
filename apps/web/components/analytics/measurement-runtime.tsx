"use client";

import Script from "next/script";
import { useTranslations } from "next-intl";
import { Suspense, useEffect, useState } from "react";

import type { PublicAnalytics } from "@/lib/analytics";
import { clearAttribution, readMeasurementChoice, writeMeasurementChoice } from "@/lib/attribution";

import { AttributionCapture } from "./capture";

/**
 * The counters, the consent request, and the click capture.
 *
 * Loaded on demand by `measurement.tsx` rather than imported from the layout,
 * because the layout is shared by every route in both panels and most
 * installations configure no counter at all. An installation that measures
 * nothing should not pay for the code that would have measured.
 *
 * Three things are deliberately true of this component.
 *
 * **It renders nothing by default.** `measurable` is false on an installation
 * that has configured no counter, and then no banner appears and no script
 * loads. An operator who wants none of this does not have to turn anything off,
 * and a visitor is not asked to agree to nothing.
 *
 * **The counter is a provider and an identifier, never a snippet.** The script
 * below is written in this repository and the only operator-supplied value in
 * it is an identifier the API validated against that provider's shape. An
 * operator cannot paste code here, and that is the point: a settings field that
 * accepted a script would be a way to run arbitrary code in every customer's
 * browser, and a customer's browser holds subscription links.
 *
 * **Declining is a real decision.** It is remembered, it stops the counters
 * from ever loading, and it also clears the advertising parameters the landing
 * page captured — so a visitor who says no is not left with a click identifier
 * sitting in their browser waiting for a purchase to attach it to.
 */
export function MeasurementRuntime({ analytics }: { analytics: PublicAnalytics }) {
  const [choice, setChoice] = useState<"unknown" | "granted" | "declined">("unknown");

  // Read after mount rather than during render: the choice lives in the
  // browser, and the server has no way to know it. Rendering the banner on the
  // server would show it for a moment to somebody who already answered.
  useEffect(() => {
    setChoice(readMeasurementChoice());
  }, []);

  if (!analytics.measurable) {
    return null;
  }

  function decide(granted: boolean) {
    writeMeasurementChoice(granted);
    setChoice(granted ? "granted" : "declined");
    if (!granted) {
      clearAttribution();
    }
  }

  return (
    <>
      {choice === "granted" && (
        <>
          <Counters counters={analytics.counters ?? {}} />
          <Suspense fallback={null}>
            <AttributionCapture />
          </Suspense>
        </>
      )}
      {choice === "unknown" && <MeasurementRequest onDecide={decide} />}
    </>
  );
}

/**
 * The counter scripts themselves.
 *
 * Each is the provider's documented loader with one identifier interpolated.
 * They are `afterInteractive` so a measurement script cannot delay the page a
 * customer came to use.
 */
function Counters({ counters }: { counters: Record<string, string> }) {
  const metrica = counters.yandex_metrica;
  const ga4 = counters.google_analytics;

  return (
    <>
      {metrica && (
        <Script id="omniflow-metrica" strategy="afterInteractive">
          {`(function(m,e,t,r,i,k,a){m[i]=m[i]||function(){(m[i].a=m[i].a||[]).push(arguments)};
m[i].l=1*new Date();k=e.createElement(t),a=e.getElementsByTagName(t)[0],k.async=1,k.src=r,a.parentNode.insertBefore(k,a)})
(window,document,"script","https://mc.yandex.ru/metrika/tag.js","ym");
ym(${metrica},"init",{clickmap:true,trackLinks:true,accurateTrackBounce:true});`}
        </Script>
      )}
      {ga4 && (
        <>
          <Script
            src={`https://www.googletagmanager.com/gtag/js?id=${ga4}`}
            strategy="afterInteractive"
          />
          <Script id="omniflow-ga4" strategy="afterInteractive">
            {`window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments)}
gtag("js",new Date());gtag("config","${ga4}");`}
          </Script>
        </>
      )}
    </>
  );
}

/**
 * The request itself.
 *
 * Two buttons of equal weight. A design where declining is a small grey link
 * beside a large coloured accept is a design that collects a consent nobody
 * gave, and an installation selling privacy software is the last place that
 * should ship one.
 */
function MeasurementRequest({ onDecide }: { onDecide: (granted: boolean) => void }) {
  const translate = useTranslations("analytics");

  return (
    <div
      aria-label={translate("title")}
      className="fixed inset-x-0 bottom-0 z-50 border-border border-t bg-card/95 p-4 backdrop-blur"
      role="dialog"
    >
      <div className="mx-auto flex max-w-3xl flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-[13px] leading-relaxed">{translate("request")}</p>
        <div className="flex shrink-0 gap-2">
          <button
            className="rounded-md border border-border px-3 py-1.5 text-[13px] transition-colors hover:bg-muted"
            onClick={() => onDecide(false)}
            type="button"
          >
            {translate("decline")}
          </button>
          <button
            className="rounded-md border border-border px-3 py-1.5 text-[13px] transition-colors hover:bg-muted"
            onClick={() => onDecide(true)}
            type="button"
          >
            {translate("accept")}
          </button>
        </div>
      </div>
    </div>
  );
}

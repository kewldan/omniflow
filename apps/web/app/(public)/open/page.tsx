"use client";

import { Button } from "@omniflow/ui/button";
import { ExternalLink } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useState } from "react";

import { appLinkFromHash } from "@/lib/open-app";

/**
 * Forwards a browser to a VPN application.
 *
 * The bot links here because a Telegram button cannot carry `happ://…`. The
 * application link is in the fragment, read in the browser only, so neither
 * this server nor its access log ever sees the subscription link inside it.
 * When the forward does not happen on its own — an in-app browser that asks
 * first, an application that is not installed — the button does it.
 */
export default function OpenAppPage() {
  const translate = useTranslations("common.openApp");
  // undefined while the fragment has not been read yet, "" when it is unusable.
  const [target, setTarget] = useState<string | undefined>(undefined);

  useEffect(() => {
    const link = appLinkFromHash(window.location.hash);
    setTarget(link);
    if (link) {
      window.location.href = link;
    }
  }, []);

  return (
    <main className="mx-auto flex min-h-dvh w-full max-w-md flex-col justify-center gap-4 px-4 py-10">
      {target === undefined ? (
        <p aria-live="polite" className="text-center text-muted-foreground text-sm">
          {translate("opening")}
        </p>
      ) : target ? (
        <div className="space-y-4 rounded-xl border border-border bg-card p-6 text-center">
          <h1 className="font-semibold text-lg">{translate("title")}</h1>
          <p className="text-muted-foreground text-sm leading-relaxed">
            {translate("description")}
          </p>
          <Button asChild className="w-full justify-center gap-2" size="lg">
            <a href={target} rel="noreferrer">
              {translate("open")}
              <ExternalLink aria-hidden />
            </a>
          </Button>
          <p className="text-[12.5px] text-subtle-foreground leading-relaxed">
            {translate("hint")}
          </p>
        </div>
      ) : (
        <div className="space-y-2 rounded-xl border border-border bg-card p-6 text-center">
          <h1 className="font-semibold text-lg">{translate("invalidTitle")}</h1>
          <p className="text-muted-foreground text-sm leading-relaxed">
            {translate("invalidDescription")}
          </p>
        </div>
      )}
    </main>
  );
}

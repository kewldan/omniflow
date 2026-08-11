"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { toast } from "@omniflow/ui/toast";
import { Check, Copy, ExternalLink } from "lucide-react";
import { useParams, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useEffect, useState } from "react";
import useSWR from "swr";

import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { type ApiError, fetcher } from "@/lib/api";

type Connection = {
  subscriptionUrl: string;
  platform: string;
  platforms: string[];
  clients: { name: string; deepLink: string }[];
};

/**
 * The connect-a-device screen.
 *
 * Everything here is a way of moving one string — the subscription link — onto
 * another device: a deep link that imports it directly, a QR code for a second
 * device's camera, and a copy button for when neither works. The link itself is
 * shown truncated, because it is a credential and the customer has no reason to
 * read it, only to move it.
 */
export default function ConnectPage() {
  const translate = useTranslations("account");
  const params = useParams<{ subscriptionId: string }>();
  const search = useSearchParams();
  const [platform, setPlatform] = useState(search.get("platform") ?? "");

  const query = platform ? `?platform=${encodeURIComponent(platform)}` : "";
  const { data, error, isLoading } = useSWR<Connection, ApiError>(
    `/v1/account/subscriptions/${params.subscriptionId}/connection${query}`,
    fetcher,
  );

  if (isLoading) {
    return <ListSkeleton rows={2} />;
  }
  if (error) {
    const pending = error.status === 409;
    const offline = error.status === 503;
    return (
      <AccountNotice
        description={
          pending
            ? translate("connect.provisioningDescription")
            : offline
              ? translate("states.upstreamDescription")
              : translate("states.errorDescription")
        }
        title={
          pending
            ? translate("connect.provisioning")
            : offline
              ? translate("states.upstream")
              : translate("states.error")
        }
        variant={offline || pending ? "offline" : "danger"}
      />
    );
  }
  if (!data) {
    return null;
  }

  return (
    <div className="animate-step-in space-y-4">
      <fieldset className="flex flex-wrap gap-2">
        <legend className="sr-only">{translate("connect.platform")}</legend>
        {data.platforms.map((option) => (
          <Button
            aria-pressed={option === data.platform}
            className="rounded-full"
            key={option}
            onClick={() => setPlatform(option)}
            size="sm"
            variant={option === data.platform ? "secondary" : "outline"}
          >
            {translate(`connect.platforms.${option}`)}
          </Button>
        ))}
      </fieldset>

      <ol className="space-y-3 rounded-xl border border-border bg-card p-4">
        {[1, 2, 3].map((step) => (
          <li className="flex items-start gap-3" key={step}>
            <span
              aria-hidden
              className="flex size-[21px] shrink-0 items-center justify-center rounded-full bg-muted font-mono font-semibold text-[10.5px] text-muted-foreground"
            >
              {step}
            </span>
            <span className="text-[13.5px] leading-snug">
              {translate(`connect.step${step}`, { app: data.clients[0]?.name ?? "" })}
            </span>
          </li>
        ))}
      </ol>

      <SectionLabel>{translate("connect.openIn")}</SectionLabel>
      <div className="space-y-2">
        {data.clients.map((client) => (
          <Button asChild className="w-full justify-between" key={client.name} size="lg">
            {/* Deep links leave the page for an application, so they are ordinary
                anchors rather than router navigations. */}
            <a href={client.deepLink} rel="noreferrer">
              {translate("connect.openWith", { app: client.name })}
              <ExternalLink aria-hidden />
            </a>
          </Button>
        ))}
      </div>

      <QrPanel value={data.subscriptionUrl} />
      <CopyLink value={data.subscriptionUrl} />
    </div>
  );
}

/**
 * The QR code.
 *
 * It is rendered in the browser from the link the API just returned rather than
 * fetched as an image: an image URL carrying a subscription link would put that
 * credential into a request line, a proxy log, and the browser cache.
 */
function QrPanel({ value }: { value: string }) {
  const translate = useTranslations("account");
  const [dataUrl, setDataUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    // Imported lazily so the encoder is not in the bundle of every other screen.
    import("qrcode")
      .then((module) =>
        module.toDataURL(value, { errorCorrectionLevel: "M", margin: 1, width: 320 }),
      )
      .then((url) => {
        if (!cancelled) {
          setDataUrl(url);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setFailed(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [value]);

  if (failed) {
    return null;
  }
  return (
    <div className="flex items-center gap-4 rounded-xl border border-border bg-card p-4">
      <div className="flex size-[84px] shrink-0 items-center justify-center overflow-hidden rounded-lg border border-border bg-white">
        {dataUrl ? (
          // A plain <img>, not next/image: the source is a data URL produced in
          // the browser a moment ago, so there is nothing for the image pipeline
          // to optimise, and routing it through /_next/image would send a
          // credential-bearing payload to the server to be cached.
          // biome-ignore lint/performance/noImgElement: runtime-generated data URL, must not reach the image pipeline
          <img alt={translate("connect.qrAlt")} src={dataUrl} />
        ) : (
          <span aria-hidden className="size-full animate-pulse bg-muted" />
        )}
      </div>
      <p className="text-[12.5px] text-muted-foreground leading-relaxed">
        {translate("connect.qrHint")}
      </p>
    </div>
  );
}

/** The manual fallback: the link, truncated, with a copy button. */
function CopyLink({ value }: { value: string }) {
  const translate = useTranslations("account");
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1700);
    } catch {
      // A browser that refuses clipboard access is a real case, and silently
      // doing nothing would look like a broken button.
      toast.error(translate("connect.copyFailed"));
    }
  }

  return (
    <div className="rounded-xl border border-border bg-card p-4">
      <div className="flex items-center justify-between gap-3">
        <SectionLabel>{translate("connect.link")}</SectionLabel>
        <Button className="gap-1.5" onClick={copy} size="sm" variant="ghost">
          {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
          {copied ? translate("connect.copied") : translate("connect.copy")}
        </Button>
      </div>
      <p
        className={cn(
          "mt-2 truncate rounded-md bg-background px-3 py-2.5 font-mono text-[11px]",
          "text-subtle-foreground",
        )}
      >
        {value}
      </p>
    </div>
  );
}

"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { toast } from "@omniflow/ui/toast";
import { Check, Copy, ExternalLink } from "lucide-react";
import { useParams, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { QrCode } from "@/components/qr-code";
import { type ApiError, fetcher } from "@/lib/api";

type Connection = {
  subscriptionUrl: string;
  platform: string;
  /**
   * The platforms this installation documents, with their labels already
   * resolved to the customer's language.
   *
   * They arrive as text rather than as message keys because the catalogue is
   * operator-editable: somebody who adds a platform from the panel cannot add a
   * key to a compiled catalogue. The same is true of a client's instructions.
   */
  platforms: { slug: string; label: string }[];
  clients: {
    name: string;
    deepLink: string;
    downloadUrl?: string;
    instructions?: string;
  }[];
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
            aria-pressed={option.slug === data.platform}
            className="rounded-full"
            key={option.slug}
            onClick={() => setPlatform(option.slug)}
            size="sm"
            variant={option.slug === data.platform ? "secondary" : "outline"}
          >
            {option.label}
          </Button>
        ))}
      </fieldset>

      <Steps instructions={data.clients[0]?.instructions} name={data.clients[0]?.name ?? ""} />

      {data.clients.length === 0 ? (
        <AccountNotice
          description={translate("connect.noClientsDescription")}
          title={translate("connect.noClients")}
          variant="offline"
        />
      ) : (
        <>
          <SectionLabel>{translate("connect.openIn")}</SectionLabel>
          <div className="space-y-2">
            {data.clients.map((client) => (
              <div className="space-y-1" key={client.name}>
                <Button asChild className="w-full justify-between" size="lg">
                  {/* Deep links leave the page for an application, so they are
                      ordinary anchors rather than router navigations. The scheme
                      is validated by the API against an allowlist before it is
                      ever stored — see internal/commerce. */}
                  <a href={client.deepLink} rel="noreferrer">
                    {translate("connect.openWith", { app: client.name })}
                    <ExternalLink aria-hidden />
                  </a>
                </Button>
                {client.downloadUrl ? (
                  <Button asChild className="w-full justify-between" size="sm" variant="ghost">
                    <a href={client.downloadUrl} rel="noreferrer noopener" target="_blank">
                      {translate("connect.download", { app: client.name })}
                      <ExternalLink aria-hidden />
                    </a>
                  </Button>
                ) : null}
              </div>
            ))}
          </div>
        </>
      )}

      <QrPanel value={data.subscriptionUrl} />
      <CopyLink value={data.subscriptionUrl} />
    </div>
  );
}

/**
 * The setup steps.
 *
 * An operator who has written their own instructions for this client replaces
 * the generic three steps rather than appending to them: somebody who took the
 * trouble to describe their own setup knows something the generic copy does not,
 * and showing both would leave a customer choosing between two accounts of the
 * same thing.
 */
function Steps({ instructions, name }: { instructions?: string; name: string }) {
  const translate = useTranslations("account");

  if (instructions?.trim()) {
    return (
      <div className="whitespace-pre-line rounded-xl border border-border bg-card p-4 text-[13.5px] leading-snug">
        {instructions}
      </div>
    );
  }

  return (
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
            {translate(`connect.step${step}`, { app: name })}
          </span>
        </li>
      ))}
    </ol>
  );
}

/**
 * The QR beside the link.
 *
 * The encoder runs in the browser — see `components/qr-code.tsx` for why a
 * subscription link must never travel as an image URL.
 */
function QrPanel({ value }: { value: string }) {
  const translate = useTranslations("account");
  return (
    <div className="flex items-center gap-4 rounded-xl border border-border bg-card p-4">
      <div className="flex size-[84px] shrink-0 items-center justify-center overflow-hidden rounded-lg border border-border bg-white">
        <QrCode alt={translate("connect.qrAlt")} value={value} />
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

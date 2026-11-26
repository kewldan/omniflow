"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { toast } from "@omniflow/ui/toast";
import { Check, Copy, Download, ExternalLink } from "lucide-react";
import { useParams, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useEffect, useRef, useState } from "react";
import useSWR from "swr";

import {
  AccountNotice,
  ListSkeleton,
  SectionLabel,
  SlowWaitNotice,
} from "@/components/account/state";
import { QrCode } from "@/components/qr-code";
import { type ApiError, fetcher } from "@/lib/api";
import { useBytes } from "@/lib/format";
import { POLLING, useBoundedPolling } from "@/lib/polling";
import { openInTelegramBrowser } from "@/lib/tma";

/**
 * A store link, or an installer this installation serves from its own
 * address — for a customer who cannot open GitHub or the vendor's site without
 * the VPN they are trying to install.
 */
type ClientDownload = {
  kind: "store" | "file";
  label: string;
  /** Absolute for a store link; a path on this installation for a file. */
  url: string;
  version?: string;
  fileName?: string;
  sizeBytes?: number;
};

type Connection = {
  /** Empty when the operator hides the bare link; see `linkHidden`. */
  subscriptionUrl: string;
  linkHidden: boolean;
  /** The same link on the operator's alternative origins. */
  mirrors: { label: string; url: string }[];
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
    downloads: ClientDownload[];
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
  const [provisioning, setProvisioning] = useState(false);
  const polling = useBoundedPolling(provisioning);
  // Read inside SWR's retry callback, which outlives the render it was made in.
  const pollingRef = useRef(polling);
  useEffect(() => {
    pollingRef.current = polling;
  });
  const { data, error, isLoading, mutate } = useSWR<Connection, ApiError>(
    `/v1/account/subscriptions/${params.subscriptionId}/connection${query}`,
    fetcher,
    {
      // "Still being set up" is a stage, not a failure. A customer who just
      // paid lands here while the worker is provisioning, and the screen has
      // to turn into the link on its own when it is ready rather than sit on
      // a notice that tells them to come back later. Nothing else is worth
      // polling: an outage or a genuine error does not clear on a timer. The
      // wait is bounded (lib/polling.ts): it backs off, says when it has
      // become unusual, and stops rather than asking forever.
      shouldRetryOnError: (failure: ApiError) => failure.status === 409,
      onErrorRetry: (failure: ApiError, _key, _config, revalidate, { retryCount }) => {
        if (failure.status !== 409) {
          return;
        }
        if (pollingRef.current.stopped) {
          return;
        }
        setTimeout(
          () => void revalidate({ retryCount }),
          pollingRef.current.delay || POLLING.initialMs,
        );
      },
    },
  );
  const waiting = error?.status === 409;
  useEffect(() => {
    setProvisioning(waiting);
  }, [waiting]);

  if (isLoading) {
    return <ListSkeleton rows={2} />;
  }
  if (error) {
    const pending = error.status === 409;
    const offline = error.status === 503;
    if (pending && polling.slow) {
      return (
        <SlowWaitNotice
          onCheckAgain={() => {
            polling.restart();
            void mutate();
          }}
          reference={params.subscriptionId}
          stopped={polling.stopped}
        />
      );
    }
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
                {client.downloads?.length ? <Downloads downloads={client.downloads} /> : null}
              </div>
            ))}
          </div>
          {data.clients.some((client) => client.downloads?.some((item) => item.kind === "file")) ? (
            <p className="px-1 text-[12px] text-muted-foreground leading-relaxed">
              {translate("connect.downloadsHint")}
            </p>
          ) : null}
        </>
      )}

      {data.linkHidden || !data.subscriptionUrl ? (
        <p className="rounded-xl border border-border bg-card p-4 text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("connect.linkHidden")}
        </p>
      ) : (
        <>
          <QrPanel value={data.subscriptionUrl} />
          <CopyLink value={data.subscriptionUrl} />
          {data.mirrors.length > 0 ? <Mirrors mirrors={data.mirrors} /> : null}
        </>
      )}
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
            className="flex size-[21px] shrink-0 items-center justify-center rounded-full bg-muted font-semibold text-[12px] text-muted-foreground"
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
 * Where to get one application: installers served from this installation and
 * store links, side by side under its import button.
 *
 * Inside the Telegram Mini App a file is handed to Telegram's browser: the
 * web view drops a download the page starts itself, and an APK in particular
 * has to reach the system's installer.
 */
function Downloads({ downloads }: { downloads: ClientDownload[] }) {
  const bytes = useBytes();
  return (
    <div className="flex flex-wrap gap-1.5 pt-0.5">
      {downloads.map((item) =>
        item.kind === "file" ? (
          <Button
            asChild
            className="h-auto gap-1.5 py-1.5"
            key={item.url}
            size="sm"
            variant="outline"
          >
            <a
              download={item.fileName}
              href={item.url}
              onClick={(event) => {
                if (openInTelegramBrowser(item.url)) {
                  event.preventDefault();
                }
              }}
            >
              <Download aria-hidden />
              <span>{item.label}</span>
              <span className="text-[11px] text-muted-foreground">
                {[item.version, item.sizeBytes ? bytes(item.sizeBytes) : ""]
                  .filter(Boolean)
                  .join(" · ")}
              </span>
            </a>
          </Button>
        ) : (
          <Button
            asChild
            className="h-auto gap-1.5 py-1.5"
            key={item.url}
            size="sm"
            variant="outline"
          >
            <a href={item.url} rel="noreferrer noopener" target="_blank">
              {item.label}
              <ExternalLink aria-hidden />
            </a>
          </Button>
        ),
      )}
    </div>
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

/**
 * The link on the operator's mirrors, for a network that blocks the main
 * address. Each is the same credential, so each is shown the same way.
 */
function Mirrors({ mirrors }: { mirrors: { label: string; url: string }[] }) {
  const translate = useTranslations("account");
  return (
    <div className="space-y-2">
      <SectionLabel>{translate("connect.mirrors")}</SectionLabel>
      <p className="text-[12.5px] text-muted-foreground">{translate("connect.mirrorsHint")}</p>
      {mirrors.map((mirror) => (
        <CopyLink key={mirror.url} label={mirror.label} value={mirror.url} />
      ))}
    </div>
  );
}

/** The manual fallback: the link, truncated, with a copy button. */
function CopyLink({ value, label }: { value: string; label?: string }) {
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
        <SectionLabel>{label ?? translate("connect.link")}</SectionLabel>
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

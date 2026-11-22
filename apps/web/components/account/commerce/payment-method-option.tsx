"use client";

import { cn } from "@omniflow/ui/lib/utils";
import { Check, CreditCard, Star } from "lucide-react";
import type { ComponentProps, PointerEvent, ReactNode } from "react";

import type { PaymentChoice } from "./types";

/**
 * One way to pay, as a row the customer taps: the method's own mark, what it
 * is called, and a check on the right.
 *
 * The input is a real radio, visually hidden, so a form registers it and the
 * keyboard moves through the group exactly as before; the row restyles off
 * its `:checked` and `:focus-visible` state. A light follows the pointer
 * across the row, and once in a while a sheen passes over the group — both
 * are left out for anyone who asked for reduced motion.
 */
export function PaymentMethodOption({
  children,
  choice,
  index = 0,
  trailing,
  ...input
}: Omit<ComponentProps<"input">, "type" | "children"> & {
  children: ReactNode;
  choice: Pick<PaymentChoice, "provider" | "method">;
  /** The row's place in the list, so the idle sheen passes down it in turn. */
  index?: number;
  trailing?: ReactNode;
}) {
  function follow(event: PointerEvent<HTMLLabelElement>) {
    const box = event.currentTarget.getBoundingClientRect();
    event.currentTarget.style.setProperty("--mx", `${event.clientX - box.left}px`);
    event.currentTarget.style.setProperty("--my", `${event.clientY - box.top}px`);
  }

  return (
    <label
      className={cn(
        "pay-option group relative isolate flex min-h-[60px] cursor-pointer items-center gap-3 overflow-hidden",
        "rounded-xl border border-border bg-card py-2 pr-3 pl-2.5 transition-[border-color,transform] duration-150",
        "hover:border-foreground/25 active:scale-[0.985] motion-reduce:active:scale-100",
        "has-[:checked]:border-foreground has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-ring has-[:focus-visible]:outline-offset-2",
        "has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-60",
      )}
      onPointerMove={follow}
      style={{ "--sheen-delay": `${index * 0.25}s` } as React.CSSProperties}
    >
      <input className="sr-only" type="radio" {...input} />
      <span aria-hidden className="pay-option-sheen" />
      <span aria-hidden className="relative z-[1] grid h-10 w-[60px] shrink-0 place-items-center">
        <PaymentMethodMark choice={choice} />
      </span>
      <span className="relative z-[1] min-w-0 flex-1">{children}</span>
      {trailing ? <span className="relative z-[1] shrink-0">{trailing}</span> : null}
      <span
        aria-hidden
        className={cn(
          "relative z-[1] grid size-[22px] shrink-0 place-items-center rounded-full border-[1.5px] border-foreground/30 transition-colors",
          "group-has-[:checked]:border-foreground group-has-[:checked]:bg-foreground",
        )}
      >
        <Check
          className="size-3 scale-0 text-background transition-transform duration-200 group-has-[:checked]:scale-100"
          strokeWidth={3}
        />
      </span>
    </label>
  );
}

const CRYPTO_METHODS = new Set(["crypto", "cryptobot"]);
const CRYPTO_PROVIDERS = new Set(["cryptobot", "heleket"]);

/**
 * The mark a payment method is recognised by.
 *
 * The Faster Payments mark goes to any provider's SBP method, because it is
 * the system the customer pays through whoever settles it. ЮKassa and
 * CryptoBot have their own; another crypto method shows the coins it most
 * often takes; Telegram Stars a star; anything else a plain card.
 */
export function PaymentMethodMark({
  choice,
}: {
  choice: Pick<PaymentChoice, "provider" | "method">;
}) {
  const method = choice.method ?? "";
  if (method === "sbp") {
    return <SbpMark />;
  }
  if (choice.provider === "cryptobot" || method === "cryptobot") {
    return (
      <span className="flex items-center">
        {/* biome-ignore lint/performance/noImgElement: a 5 KB static icon; next/image adds nothing here */}
        <img
          alt=""
          className="size-10 rounded-[11px] shadow-[0_4px_10px_-5px_rgb(0_0_0/0.5)]"
          height={40}
          src="/payments/cryptobot.png"
          width={40}
        />
        <Coins className="-ml-[18px] self-end" />
      </span>
    );
  }
  if (CRYPTO_METHODS.has(method) || CRYPTO_PROVIDERS.has(choice.provider)) {
    return <Coins large />;
  }
  if (choice.provider === "yookassa") {
    return <YooKassaMark />;
  }
  if (choice.provider === "telegram_stars") {
    return <Star className="size-8 fill-[#f5b301] text-[#f5b301]" strokeWidth={1.5} />;
  }
  return (
    <span className="grid size-10 place-items-center rounded-[11px] bg-muted">
      <CreditCard className="size-5 text-muted-foreground" />
    </span>
  );
}

/** The Faster Payments System mark, from NSPK's own artwork (public domain). */
function SbpMark() {
  return (
    <svg aria-hidden="true" className="h-9 w-auto" viewBox="0 0 97.3 120">
      <path d="M0 26.12l14.532 25.975v15.844L.017 93.863 0 26.12z" fill="#5B57A2" />
      <path d="M55.797 42.643l13.617-8.346 27.868-.026-41.485 25.414V42.643z" fill="#D90751" />
      <path d="M55.72 25.967l.077 34.39-14.566-8.95V0l14.49 25.967z" fill="#FAB718" />
      <path d="M97.282 34.271l-27.869.026-13.693-8.33L41.231 0l56.05 34.271z" fill="#ED6F26" />
      <path d="M55.797 94.007V77.322l-14.566-8.78.008 51.458 14.558-25.993z" fill="#63B22F" />
      <path d="M69.38 85.737L14.531 52.095 0 26.12l97.223 59.583-27.844.034z" fill="#1487C9" />
      <path d="M41.24 120l14.556-25.993 13.583-8.27 27.843-.034L41.24 120z" fill="#017F36" />
      <path d="M.017 93.863l41.333-25.32-13.896-8.526-12.922 7.922L.017 93.863z" fill="#984995" />
    </svg>
  );
}

/** ЮKassa's "Ю" mark, without the wordmark. */
function YooKassaMark() {
  return (
    <svg aria-hidden="true" className="h-[30px] w-auto" viewBox="0 0 112 80">
      <path
        clipRule="evenodd"
        d="M72 80C94.0914 80 112 62.0914 112 40C112 17.9086 94.0914 0 72 0C54.283 0 39.2564 11.5184 34 27.4753V12H0L19 72H34V52.5247C39.2564 68.4816 54.283 80 72 80ZM72 55C80.2843 55 87 48.2843 87 40C87 31.7157 80.2843 25 72 25C63.7157 25 57 31.7157 57 40C57 48.2843 63.7157 55 72 55Z"
        fill="#0070F0"
        fillRule="evenodd"
      />
    </svg>
  );
}

/** USDT, TON, and BTC, overlapping; small as badges on CryptoBot, larger alone. */
function Coins({ className, large = false }: { className?: string; large?: boolean }) {
  const size = large ? "size-6 -ml-2" : "size-[18px] -ml-1.5";
  const coin = cn(size, "rounded-full shadow-[0_0_0_1.5px_var(--card)] first:ml-0");
  return (
    <span className={cn("flex", className)}>
      <svg aria-hidden="true" className={coin} viewBox="0 0 32 32">
        <circle cx="16" cy="16" fill="#26A17B" r="16" />
        <path
          d="M17.922 17.383v-.002c-.11.008-.677.042-1.942.042-1.01 0-1.721-.03-1.971-.042v.003c-3.888-.171-6.79-.848-6.79-1.658 0-.809 2.902-1.486 6.79-1.66v2.644c.254.018.982.061 1.988.061 1.207 0 1.812-.05 1.925-.06v-2.643c3.88.173 6.775.85 6.775 1.658 0 .81-2.895 1.485-6.775 1.657m0-3.59v-2.366h5.414V7.819H8.595v3.608h5.414v2.365c-4.4.202-7.709 1.074-7.709 2.118 0 1.044 3.309 1.915 7.709 2.118v7.582h3.913v-7.584c4.393-.202 7.694-1.073 7.694-2.116 0-1.043-3.301-1.914-7.694-2.117"
          fill="#fff"
        />
      </svg>
      <svg aria-hidden="true" className={coin} viewBox="0 0 32 32">
        <circle cx="16" cy="16" fill="#0098EA" r="16" />
        <path
          d="M10.6 9.4h10.8c1.33 0 2.17 1.44 1.5 2.6l-6.08 10.53a.95.95 0 01-1.64 0L9.1 12c-.67-1.16.17-2.6 1.5-2.6z"
          fill="none"
          stroke="#fff"
          strokeLinejoin="round"
          strokeWidth="1.9"
        />
        <path d="M16 9.6v13.3" stroke="#fff" strokeLinecap="round" strokeWidth="1.9" />
      </svg>
      <svg aria-hidden="true" className={coin} viewBox="0 0 32 32">
        <circle cx="16" cy="16" fill="#F7931A" r="16" />
        <path
          d="M23.189 14.02c.314-2.096-1.283-3.223-3.465-3.975l.708-2.84-1.728-.43-.69 2.765c-.454-.114-.92-.22-1.385-.326l.695-2.783L15.596 6l-.708 2.839c-.376-.086-.746-.17-1.104-.26l.002-.009-2.384-.595-.46 1.846s1.283.294 1.256.312c.7.175.826.638.805 1.006l-.806 3.235c.048.012.11.03.18.057l-.183-.045-1.13 4.532c-.086.212-.303.531-.793.41.018.025-1.256-.313-1.256-.313l-.858 1.978 2.25.561c.418.105.828.215 1.231.318l-.715 2.872 1.727.43.708-2.84c.472.127.93.245 1.378.357l-.706 2.828 1.728.43.715-2.866c2.948.558 5.164.333 6.097-2.333.752-2.146-.037-3.385-1.588-4.192 1.13-.26 1.98-1.003 2.207-2.538zm-3.95 5.538c-.533 2.147-4.148.986-5.32.695l.95-3.805c1.172.293 4.929.872 4.37 3.11zm.535-5.569c-.487 1.953-3.495.96-4.47.717l.86-3.45c.975.243 4.118.696 3.61 2.733z"
          fill="#fff"
        />
      </svg>
    </span>
  );
}

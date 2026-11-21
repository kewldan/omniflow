import type { SVGProps } from "react";

/**
 * Telegram's paper-plane mark, drawn in the current text colour so it sits
 * beside lucide's icons at the same size and weight of ink.
 */
export function TelegramLogo(props: SVGProps<SVGSVGElement>) {
  return (
    <svg
      aria-hidden
      fill="currentColor"
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      {...props}
    >
      <path d="M21.94 4.66a1.2 1.2 0 0 0-1.62-1.3L2.86 10.1c-1.05.4-1.03 1.9.03 2.27l4.43 1.52 1.7 5.36c.25.8 1.25 1.04 1.84.44l2.47-2.5 4.5 3.32c.7.52 1.71.13 1.88-.72l2.23-15.13ZM9.65 14.3l-.5 3.5-1.2-3.82 9.83-6.35-8.13 6.67Z" />
    </svg>
  );
}

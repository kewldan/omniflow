import { KeyRound, Mail } from "lucide-react";

/**
 * How a sign-in method is named and drawn. The API sends the provider's code
 * (`telegram`, `email`, or `oidc:<slug>`) and the label the
 * installation gave it; the services the panel knows by name get their own
 * mark and the name customers recognise, and anything else its label.
 */
type Mark = { name: string; paths: { d: string; fill: string }[] };

const KNOWN: Record<string, Mark> = {
  telegram: {
    name: "Telegram",
    paths: [
      {
        fill: "#26A5E4",
        d: "M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0a12 12 0 0 0-.056 0zm4.962 7.224c.1-.002.321.023.465.14a.506.506 0 0 1 .171.325c.016.093.036.306.02.472-.18 1.898-.962 6.502-1.36 8.627-.168.9-.499 1.201-.82 1.23-.696.065-1.225-.46-1.9-.902-1.056-.693-1.653-1.124-2.678-1.8-1.185-.78-.417-1.21.258-1.91.177-.184 3.247-2.977 3.307-3.23.007-.032.014-.15-.056-.212s-.174-.041-.249-.024c-.106.024-1.793 1.14-5.061 3.345-.48.33-.913.49-1.302.48-.428-.008-1.252-.241-1.865-.44-.752-.245-1.349-.374-1.297-.789.027-.216.325-.437.893-.663 3.498-1.524 5.83-2.529 6.998-3.014 3.332-1.386 4.025-1.627 4.476-1.635z",
      },
    ],
  },
  vk: {
    name: "VK ID",
    paths: [
      {
        fill: "#0077FF",
        d: "M15.684 0H8.316C1.592 0 0 1.592 0 8.316v7.368C0 22.408 1.592 24 8.316 24h7.368C22.408 24 24 22.408 24 15.684V8.316C24 1.592 22.391 0 15.684 0zm3.692 17.123h-1.744c-.66 0-.864-.525-2.05-1.727-1.033-1-1.49-1.135-1.744-1.135-.356 0-.458.102-.458.593v1.575c0 .424-.135.678-1.253.678-1.846 0-3.896-1.118-5.335-3.202C4.624 10.857 4.03 8.57 4.03 8.096c0-.254.102-.491.593-.491h1.744c.44 0 .61.203.78.677.863 2.49 2.303 4.675 2.896 4.675.22 0 .322-.102.322-.66V9.721c-.068-1.186-.695-1.287-.695-1.71 0-.204.17-.407.44-.407h2.744c.373 0 .508.203.508.643v3.473c0 .372.17.508.271.508.22 0 .407-.136.813-.542 1.254-1.406 2.151-3.574 2.151-3.574.119-.254.322-.491.763-.491h1.744c.525 0 .644.27.525.643-.22 1.017-2.354 4.031-2.354 4.031-.186.305-.254.44 0 .78.186.254.796.779 1.203 1.253.745.847 1.32 1.558 1.473 2.05.17.49-.085.744-.576.744z",
      },
    ],
  },
  google: {
    name: "Google",
    paths: [
      {
        fill: "#4285F4",
        d: "M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z",
      },
      {
        fill: "#34A853",
        d: "M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z",
      },
      {
        fill: "#FBBC05",
        d: "M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z",
      },
      {
        fill: "#EA4335",
        d: "M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z",
      },
    ],
  },
  yandex: {
    name: "Yandex ID",
    paths: [
      { fill: "#FC3F1D", d: "M24 12a12 12 0 1 1-24 0 12 12 0 0 1 24 0z" },
      {
        fill: "#FFFFFF",
        d: "M13.32 7.666h-.924c-1.694 0-2.585.858-2.585 2.123 0 1.43.616 2.1 1.881 2.959l1.045.704-3.003 4.487H7.49l2.695-4.014c-1.55-1.111-2.42-2.19-2.42-4.015 0-2.288 1.595-3.85 4.62-3.85h3.003v11.868H13.32V7.666z",
      },
    ],
  },
};

/**
 * The key a provider is known by. Linked identities arrive as `oidc:<slug>`,
 * buttons as the bare slug or the provider's configured icon name.
 */
function knownKey(provider: string): string {
  return provider.toLowerCase().replace(/^oidc:/, "");
}

/** The name to show for a provider: a known service's own, else the label. */
export function providerName(provider: string, label: string, emailName: string): string {
  const key = knownKey(provider);
  if (KNOWN[key]) {
    return KNOWN[key].name;
  }
  if (key === "email") {
    return emailName;
  }
  return label && label.toLowerCase() !== key
    ? label
    : label.charAt(0).toUpperCase() + label.slice(1);
}

/**
 * The provider's mark: the brand's own for a known service, else a key. 20px
 * by default; `className` resizes it where it sits inside a button.
 */
export function ProviderMark({
  provider,
  className = "size-5",
}: {
  provider: string;
  className?: string;
}) {
  const known = KNOWN[knownKey(provider)];
  if (known) {
    return (
      <svg aria-hidden className={`${className} shrink-0`} role="img" viewBox="0 0 24 24">
        {known.paths.map((path) => (
          <path d={path.d} fill={path.fill} key={path.d.slice(0, 24)} />
        ))}
      </svg>
    );
  }
  const Icon = knownKey(provider) === "email" ? Mail : KeyRound;
  return <Icon aria-hidden className={`${className} shrink-0 text-muted-foreground`} />;
}

"""Announce a published release in the community chat's updates topic.

Reads the release with the GitHub CLI and posts a short summary to Telegram:
the version, the features and fixes from release-please's changelog with
scopes and commit links dropped, and a link to the full notes. The bot token
comes from TELEGRAM_COMMUNITY_BOT_TOKEN and is never printed. With
ANNOUNCE_DRY_RUN=true the message is printed and the token only checked.

Inputs (environment): RELEASE_TAG, GITHUB_REPOSITORY, TELEGRAM_CHAT_ID,
TELEGRAM_THREAD_ID, TELEGRAM_COMMUNITY_BOT_TOKEN, ANNOUNCE_DRY_RUN.
"""

import html
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

# Telegram caps a message at 4096 characters; the rest is the header, the
# link, and room for the "and N more" line.
LIMIT = 3500

SECTIONS = {
    "Features": "✨ Новое",
    "Bug Fixes": "🛠 Исправления",
    "Performance Improvements": "⚡ Быстрее",
    "Reverts": "↩️ Откаты",
}
SKIPPED = {"Documentation", "Miscellaneous Chores", "Code Refactoring", "Tests", "Build System",
           "Continuous Integration", "Styles"}

ENTRY = re.compile(r"^\* (?:\*\*[^*]+:\*\* )?(?P<text>.+?)(?: \(\[[0-9a-f]{7,}\]\([^)]*\)\))*$")


def release(tag: str, repository: str) -> dict:
    output = subprocess.run(
        ["gh", "release", "view", tag, "--repo", repository, "--json", "name,tagName,url,body"],
        check=True, capture_output=True, text=True,
    ).stdout
    return json.loads(output)


def version(data: dict) -> str:
    match = re.search(r"v?(\d+\.\d+\.\d+(?:-[\w.]+)?)$", data["tagName"])
    return match.group(1) if match else data["tagName"]


def message(data: dict) -> str:
    """The announcement, HTML for Telegram."""
    sections: list[tuple[str, list[str]]] = []
    current: list[str] | None = None
    for line in data.get("body", "").splitlines():
        line = line.rstrip()
        if line.startswith("### "):
            title = line[4:].strip()
            if title in SKIPPED:
                current = None
                continue
            current = []
            sections.append((SECTIONS.get(title, title), current))
        elif current is not None and line.startswith("* "):
            match = ENTRY.match(line)
            text = match.group("text") if match else line[2:]
            # A changelog line may still carry Markdown emphasis or links.
            text = re.sub(r"\[([^\]]+)\]\([^)]*\)", r"\1", text).replace("**", "")
            current.append(text[:1].upper() + text[1:])

    head = f"🚀 <b>Omniflow {html.escape(version(data))}</b>"
    tail = f'\n\n<a href="{html.escape(data["url"], quote=True)}">Все изменения и инструкции по обновлению</a>'
    body = ""
    left = 0
    for title, entries in sections:
        if not entries:
            continue
        block = f"\n\n<b>{html.escape(title, quote=False)}</b>"
        for entry in entries:
            line = f"\n• {html.escape(entry, quote=False)}"
            if left or len(head) + len(body) + len(block) + len(line) + len(tail) > LIMIT:
                left += 1
                continue
            block += line
        body += block if block.count("\n•") else ""
    if left:
        body += f"\n\n…и ещё {left}"
    if not body:
        body = "\n\nТехнический релиз без заметных для пользователей изменений."
    return head + body + tail


def telegram(method: str, token: str, **params) -> dict:
    request = urllib.request.Request(
        f"https://api.telegram.org/bot{token}/{method}",
        data=urllib.parse.urlencode(params).encode(),
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            result = json.load(response)
    except urllib.error.HTTPError as error:
        result = json.load(error)
    if not result.get("ok"):
        # The description never contains the token; the URL, which does, is
        # not printed.
        sys.exit(f"Telegram {method} failed: {result.get('description')}")
    return result["result"]


def main() -> None:
    data = release(os.environ["RELEASE_TAG"], os.environ["GITHUB_REPOSITORY"])
    text = message(data)
    token = os.environ.get("TELEGRAM_COMMUNITY_BOT_TOKEN", "").strip()
    if not token:
        sys.exit("TELEGRAM_COMMUNITY_BOT_TOKEN is not set")
    if os.environ.get("ANNOUNCE_DRY_RUN") == "true":
        bot = telegram("getMe", token)
        print(f"Dry run as @{bot['username']}, nothing sent:\n\n{text}")
        return
    sent = telegram(
        "sendMessage", token,
        chat_id=os.environ["TELEGRAM_CHAT_ID"],
        message_thread_id=os.environ["TELEGRAM_THREAD_ID"],
        text=text, parse_mode="HTML",
        link_preview_options=json.dumps({"is_disabled": True}),
    )
    print(f"Announced {version(data)} as message {sent['message_id']}")


if __name__ == "__main__":
    main()

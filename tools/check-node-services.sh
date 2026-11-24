#!/usr/bin/env sh
# Checks which geo-sensitive sites answer from this machine's exit address.
#
# Run it on a VPN node (the exit a host's traffic leaves from), then mark the
# host in Settings → Smart subscription → Remnawave hosts. Customers see those
# marks on their servers screen. It only sends ordinary GET requests, plus one
# keyless request to the Anthropic API, so it changes nothing anywhere.
#
#   ssh <node> 'sh -s' < tools/check-node-services.sh
#
# What each line means:
#   google sees  the country Google places the address in. Gemini is not
#                offered in Russia, so "RUS" here means Gemini will refuse.
#   youtube      the watch page's playability; LOGIN_REQUIRED or BOTCHECK
#                means YouTube asks to sign in. "region" is the one it serves.
#   reddit       200 opens without signing in; 403 is Reddit's "blocked by
#                network security" page, which asks to sign in.
#   chatgpt      OpenAI's own country check.
#   claude       401 from the API means the country is allowed (the key is
#                missing, not the region); 403 means it is not.
set -u

agent='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36'
get() { curl -s -4 -m 12 -A "$agent" -H 'Accept-Language: en-US' "$@"; }

address=$(curl -s -4 -m 12 https://ipinfo.io/ip)
if [ -z "$address" ]; then
  echo "no IPv4 connectivity" >&2
  exit 1
fi
country=$(curl -s -4 -m 12 https://ipinfo.io/country)
echo "address      $address ($country by ipinfo)"

google=$(get -L https://gemini.google.com/ | grep -o ',2,1,200,"[A-Z]\{3\}"' | head -1 | sed 's/.*,"\(.*\)"/\1/')
case "$google" in
  RUS | BLR | CHN | IRN | PRK | CUB | SYR) gemini=no ;;
  "") gemini=unknown ;;
  *) gemini=yes ;;
esac
echo "google sees  ${google:-?}"
echo "gemini       $gemini"

watch=$(get 'https://www.youtube.com/watch?v=jNQXAC9IVRw')
playable=$(echo "$watch" | grep -o '"playabilityStatus":{"status":"[A-Z_]*"' | head -1 | cut -d'"' -f6)
if echo "$watch" | grep -q "confirm you.re not a bot"; then
  playable="$playable BOTCHECK"
fi
region=$(echo "$watch" | grep -o '"gl":"[A-Z]*"' | head -1 | cut -d'"' -f4)
echo "youtube      ${playable:-?} (region ${region:-?})"

page=$(mktemp)
status=$(get -o "$page" -w '%{http_code}' https://www.reddit.com/r/popular/)
if grep -qi 'blocked by network security' "$page"; then
  status="$status, asks to sign in"
fi
rm -f "$page"
echo "reddit       $status"

if get https://api.openai.com/compliance/cookie_requirements | grep -qi unsupported_country; then
  echo "chatgpt      no"
else
  echo "chatgpt      yes"
fi

claude=$(curl -s -4 -m 12 -o /dev/null -w '%{http_code}' https://api.anthropic.com/v1/messages \
  -H 'content-type: application/json' -d '{}')
case "$claude" in
  401) echo "claude       yes" ;;
  403) echo "claude       no" ;;
  *) echo "claude       unknown ($claude)" ;;
esac

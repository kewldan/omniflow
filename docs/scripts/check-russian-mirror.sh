#!/usr/bin/env sh
# Verifies the Russian documentation tree against the English one.
#
# `mint broken-links` resolves every link against the default language, so a
# correct `/ru/...` link on a Russian page is reported as broken even though the
# page is served at exactly that URL. The English tree is checked by that command
# with an explicit file list; this script covers what it cannot.
#
# Run from the docs directory.

set -u
status=0

for en in $(find . -name '*.mdx' -not -path './ru/*' | sed 's|^\./||'); do
	if [ ! -f "ru/$en" ]; then
		echo "missing Russian page: ru/$en"
		status=1
	fi
done

for ru in $(find ru -name '*.mdx'); do
	en="${ru#ru/}"
	if [ ! -f "$en" ]; then
		echo "Russian page has no English counterpart: $ru"
		status=1
	fi

	for link in $(grep -ohE '(\]\(|href=")/[a-zA-Z0-9/._-]+' "$ru" \
		| sed -E 's/^(\]\(|href=")//' | sort -u); do
		case "$link" in
		/ru/*) ;;
		*)
			echo "$ru links out of the Russian tree: $link"
			status=1
			continue
			;;
		esac

		if [ ! -f "${link#/}.mdx" ]; then
			echo "$ru has a broken link: $link"
			status=1
		fi
	done
done

if [ "$status" -eq 0 ]; then
	echo "russian mirror ok"
fi

exit $status

#!/bin/sh
# Verifies that `make save-images` bundles exactly the images an install pulls.
# Runs inside the pinned helm-unittest image (`make images-check`, and the
# images-check CI job) from the repository root.
#
# An air-gapped site installs from that bundle and nothing else, so an image
# the install pulls and the bundle lacks is an ImagePullBackOff on a network
# that cannot fetch it. The names are written down in several places — the
# Makefile, the server's built-in defaults, the chart and the compose file —
# and each moves on its own: a release cut, a PostgreSQL bump, a new debug
# image. This reads every one of them and fails when they disagree.
set -eu

chart=deploy/helm/kubemg
mirror=mirror.images-check.invalid
scratch=$(mktemp -d)

# canonical names a reference the way a container runtime resolves it, so
# `postgres:16-alpine` and `docker.io/library/postgres:16-alpine` compare equal.
canonical() {
	ref=$1
	first=${ref%%/*}
	if [ "$first" = "$ref" ]; then
		ref=docker.io/library/$ref
	else
		case $first in
		*.* | *:* | localhost) ;;
		*) ref=docker.io/$ref ;;
		esac
	fi
	case $ref in
	docker.io/*/*) ;;
	docker.io/*) ref=docker.io/library/${ref#docker.io/} ;;
	esac
	echo "$ref"
}

# Each source writes "<reference> <where it came from>" lines.
emit() {
	while read -r ref; do
		[ -n "$ref" ] || continue
		echo "$(canonical "$ref") $1"
	done
}

# The bundle: SAVE_IMAGES as the Makefile's own defaults expand it.
awk '
	# RSTART and RLENGTH are global, so they are copied before the recursion
	# overwrites them.
	function expand(name,   value, start, length_, inner) {
		value = vars[name]
		while (match(value, /[$][(][A-Z_]+[)]/)) {
			start = RSTART
			length_ = RLENGTH
			inner = expand(substr(value, start + 2, length_ - 3))
			value = substr(value, 1, start - 1) inner substr(value, start + length_)
		}
		return value
	}
	/^[A-Z_]+ *[?:]?= / {
		name = $1
		sub(/^[A-Z_]+ *[?:]?= */, "")
		vars[name] = $0
	}
	END {
		n = split(expand("SAVE_IMAGES"), images, " ")
		for (i = 1; i <= n; i++) print images[i]
	}
' Makefile | emit Makefile >"$scratch/bundle"

if [ "$(wc -l <"$scratch/bundle")" -eq 0 ]; then
	echo "could not read SAVE_IMAGES out of the Makefile" >&2
	exit 1
fi

# The server's built-in defaults: what it renders into an agent package, runs a
# browser shell with, and offers for a debug container when nothing is set.
go_default() {
	value=$(sed -n "s/.*$2 *= *\"\([^\"]*\)\".*/\1/p" "$1" | head -n 1)
	if [ -z "$value" ]; then
		echo "could not read $2 out of $1" >&2
		exit 1
	fi
	echo "$value" | emit "$1 ($2)"
}

{
	go_default backend/pkg/agentpkg/agentpkg.go DefaultImage
	go_default backend/pkg/shell/shell.go DefaultImage
	go_default backend/pkg/config/config.go defaultDebugImage
} >"$scratch/pulled"

# The compose install: the default after `:-` in each image line.
sed -n 's/.*image: *\${[A-Z_]*:-\([^}]*\)}.*/\1/p' deploy/compose/docker-compose.yml |
	emit deploy/compose/docker-compose.yml >>"$scratch/pulled"

# The chart as installed with no mirror, in-chart PostgreSQL on so its image is
# rendered too. The console's own images are left to the server's defaults.
render() {
	helm template kubemg "$chart" \
		--set publicURL=https://kubemg.images-check.invalid \
		--set postgresql.enabled=true \
		--set database.password=images-check "$@"
}

# Every image the pods run, and every image the server is told to hand out.
images_in() {
	awk '
		$1 == "image:" { print $2 }
		$1 == "-" && $2 == "name:" && $3 ~ /^KUBEMG_(AGENT|SHELL|DEBUG)_IMAGE$/ { want = 1; next }
		want && $1 == "value:" { gsub(/"/, "", $2); print $2 }
		{ want = 0 }
	' | sort -u
}

render | images_in | emit "$chart" >>"$scratch/pulled"

status=0

# Everything an install pulls is in the bundle.
while read -r ref source; do
	if ! cut -d ' ' -f 1 "$scratch/bundle" | grep -qxF "$ref"; then
		echo "$source pulls $ref, which make save-images does not bundle" >&2
		status=1
	fi
done <"$scratch/pulled"

# Nothing is bundled that no install pulls: a stale entry is the sign that the
# image the install moved to is missing.
while read -r ref source; do
	if ! cut -d ' ' -f 1 "$scratch/pulled" | grep -qxF "$ref"; then
		echo "make save-images bundles $ref, which no install pulls" >&2
		status=1
	fi
done <"$scratch/bundle"

# Under a mirror the chart rewrites every image to the mirror's host, keeping
# the published repository paths; the debug image's path is written out in
# _helpers.tpl. A site that pushes the bundle under its registry with those
# paths has to find every one of them there.
cut -d ' ' -f 1 "$scratch/bundle" | cut -d / -f 2- | sort -u >"$scratch/bundle-paths"
render --set global.imageRegistry=$mirror | images_in >"$scratch/mirrored"
while read -r ref; do
	case $ref in
	"$mirror"/*) ;;
	*)
		echo "$chart under a mirror still pulls $ref from outside it" >&2
		status=1
		continue
		;;
	esac
	if ! grep -qxF "${ref#"$mirror"/}" "$scratch/bundle-paths"; then
		echo "$chart under a mirror pulls ${ref#"$mirror"/}, which make save-images does not bundle" >&2
		status=1
	fi
done <"$scratch/mirrored"
while read -r path; do
	if ! grep -qxF "$mirror/$path" "$scratch/mirrored"; then
		echo "make save-images bundles $path, which $chart under a mirror never pulls" >&2
		status=1
	fi
done <"$scratch/bundle-paths"

if [ "$status" -ne 0 ]; then
	echo "make save-images and the images an install pulls have drifted; update them together." >&2
fi
exit "$status"

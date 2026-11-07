#!/bin/sh
# Verifies the management plane chart the way a GitOps controller will render
# it. Runs inside the pinned helm-unittest image (`make chart-test`, and the
# chart-test CI job) from the repository root.
set -eu

chart=deploy/helm/kubemg

# Argo CD and Flux render with `helm template`, where a cluster lookup answers
# nothing and a random or generated value differs on every render. A template
# that used either would install one thing and replace it on the next sync — a
# password PostgreSQL no longer accepts, a certificate every agent has pinned.
if grep -rnwE 'lookup|randAlpha|randAlphaNum|randNumeric|randAscii|randBytes|genCA|genSelfSignedCert|genSignedCert|genPrivateKey|uuidv4' "$chart/templates"; then
	echo "the chart must render the same objects every time; the calls above do not" >&2
	exit 1
fi

# backend/pkg/config is the authority on what the server reads. A variable it
# reads has to be reachable from the chart — set by a template — or documented
# with its default in docs/install/environment.md, where an operator finds it
# and sets it through extraEnv. One with neither is a setting nobody installing
# from the chart can know exists. A row names its variables in its first cell.
#
# Join lines before matching so that a call whose arguments start on the next
# line is not missed — gofmt keeps arguments there once they get long:
#   envList(
#     "CORS_ALLOWED_ORIGINS",   ← would be missed by a line-oriented grep
#     ...
#   )
config=backend/pkg/config/config.go
reference=docs/install/environment.md
names=$(tr '\n' ' ' < "$config" | grep -o 'env[A-Za-z]*([[:space:]]*"[A-Z][A-Z0-9_]*"' | grep -o '"[A-Z][A-Z0-9_]*"' | tr -d '"' | sort -u)
if [ -z "$names" ]; then
	echo "could not read the variables $config reads" >&2
	exit 1
fi
documented=$(awk -F '|' '/^\|/ { print $2 }' "$reference" | grep -o '`[A-Z][A-Z0-9_]*`' | tr -d '`' | sort -u)
uncovered=0
for name in $names; do
	if ! grep -rqw "$name" "$chart/templates" && ! echo "$documented" | grep -qxF "$name"; then
		echo "$config reads $name, which has neither a chart key nor a row in $reference" >&2
		uncovered=1
	fi
done
if [ "$uncovered" -ne 0 ]; then
	exit 1
fi

scratch=$(mktemp -d)
for values in "$chart"/ci/*-values.yaml; do
	helm lint --strict "$chart" -f "$values"
	helm template kubemg "$chart" -f "$values" >"$scratch/first.yaml"
	helm template kubemg "$chart" -f "$values" >"$scratch/second.yaml"
	if ! cmp -s "$scratch/first.yaml" "$scratch/second.yaml"; then
		echo "$values renders differently on a second render" >&2
		exit 1
	fi
done

# helm-unittest writes tests/__snapshot__ inside the chart it runs, and CI
# mounts the repository read-only, so it runs against a copy.
cp -R "$chart" "$scratch/chart"
helm unittest "$scratch/chart"

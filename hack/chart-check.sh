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

helm unittest "$chart"

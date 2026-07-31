package db

/*
 * The preset catalogue.
 *
 * A guardrail is a regular expression matched against a subject most operators
 * have never had to write down, so an empty rule list with a blank text box is a
 * feature nobody turns on. These are the rules worth having on the day the
 * feature is installed, written correctly once.
 *
 * They are also what the defaults are seeded from — see SeedGuardrailPolicies,
 * which stores them *disabled*. An upgrade that silently started refusing calls
 * an operator made yesterday would be a worse platform than one that leaves the
 * switch in reach.
 */

// GuardrailTemplate is one preset rule an administrator can apply as-is or edit first.
type GuardrailTemplate struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Pattern     string `json:"pattern"`
	Target      string `json:"target"`
	Action      string `json:"action"`
}

// GuardrailTemplates is the preset catalogue, ordered by how likely an operator is to
// want it rather than alphabetically.
var GuardrailTemplates = []GuardrailTemplate{
	{
		Key:  "delete-namespace",
		Name: "Block namespace deletion",
		Description: "Deleting a namespace deletes everything in it, and it is one " +
			"tab-completion away from the namespace next to it. This is the rule most " +
			"fleets want first.",
		// Anchored at both ends on purpose. Without the tail anchor this also
		// matches DELETE of anything *inside* a namespace — deleting one pod would
		// be refused by the rule about deleting the namespace.
		Pattern: `^DELETE /api/v1/namespaces/[^/?]+(\?.*)?$`,
		Target:  GuardrailTargetAPIRequest,
		Action:  GuardrailActionBlock,
	},
	{
		Key:  "delete-collection",
		Name: "Block bulk deletion of a resource collection",
		Description: "A DELETE against a collection rather than a named object — " +
			"`kubectl delete pods --all`. One character separates it from deleting one pod.",
		Pattern: `^DELETE /api(/v[0-9a-z]+|s/[^/]+/v[0-9a-z]+)/namespaces/[^/]+/[a-z]+(\?|$)`,
		Target:  GuardrailTargetAPIRequest,
		Action:  GuardrailActionBlock,
	},
	{
		Key:  "rm-rf-root",
		Name: "Block `rm -rf /` in a container",
		Description: "Recursive deletion from the filesystem root, in any of the ways it " +
			"is usually typed. Matches the command line, so it catches the shell a " +
			"container image happens to ship.",
		// The flags are matched in either order and either case — `-rf`, `-fr`,
		// `-Rf` — with earlier long flags skipped, because a rule that only knew
		// one spelling of the most famous destructive command in Unix would be
		// worse than no rule at all.
		Pattern: `\brm\s+(-[a-zA-Z-]+\s+)*-[a-zA-Z]*([rR][a-zA-Z]*[fF]|[fF][a-zA-Z]*[rR])[a-zA-Z]*\s+/(\s|$|\*)`,
		Target:  GuardrailTargetTerminalExec,
		Action:  GuardrailActionBlock,
	},
	{
		Key:  "fork-bomb",
		Name: "Block the classic fork bomb",
		Description: "`:(){ :|:& };:` and its variations. It takes a node down rather than " +
			"a pod, because a container with no PID limit exhausts the host's process table.",
		Pattern: `:\(\)\s*\{.*\|.*&.*\}\s*;?\s*:`,
		Target:  GuardrailTargetTerminalExec,
		Action:  GuardrailActionBlock,
	},
	{
		Key:  "disk-overwrite",
		Name: "Block writing directly to a block device",
		Description: "`dd` or `mkfs` against /dev/sd*, /dev/nvme* or /dev/xvd*. A container " +
			"with a host device mounted can destroy the node's disk from inside a pod.",
		Pattern: `\b(dd\s+[^|;]*of=|mkfs(\.[a-z0-9]+)?\s+)/dev/(sd|nvme|xvd|vd)`,
		Target:  GuardrailTargetTerminalExec,
		Action:  GuardrailActionBlock,
	},
	{
		Key:  "delete-crd",
		Name: "Block deleting a CustomResourceDefinition",
		Description: "Deleting a CRD deletes every object of that kind across the cluster, " +
			"silently and without a second prompt. It is how an operator loses a database " +
			"they did not know was a custom resource.",
		Pattern: `^DELETE /apis/apiextensions\.k8s\.io/v[0-9a-z]+/customresourcedefinitions/`,
		Target:  GuardrailTargetAPIRequest,
		Action:  GuardrailActionBlock,
	},
	{
		Key:  "delete-node",
		Name: "Block deleting a Node object",
		Description: "Deleting the Node evicts everything on it and, on most managed " +
			"platforms, is not how a machine is meant to be removed.",
		Pattern: `^DELETE /api/v1/nodes/`,
		Target:  GuardrailTargetAPIRequest,
		Action:  GuardrailActionBlock,
	},
	{
		Key:  "flag-secret-reads",
		Name: "Flag reads of a single Secret",
		Description: "Warn rather than block: reading a Secret is legitimate and constant, " +
			"but a burst of it is worth a line in the trail. A good rule to run in warn " +
			"first — that is what the mode is for.",
		Pattern: `^GET /api/v1/namespaces/[^/]+/secrets/[^/?]+`,
		Target:  GuardrailTargetAPIRequest,
		Action:  GuardrailActionWarn,
	},
}

// GuardrailTemplateByKey finds one preset.
func GuardrailTemplateByKey(key string) (GuardrailTemplate, bool) {
	for _, template := range GuardrailTemplates {
		if template.Key == key {
			return template, true
		}
	}
	return GuardrailTemplate{}, false
}

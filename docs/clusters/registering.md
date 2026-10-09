# Adding a cluster

Registration is a five-step wizard at `/admin/clusters/new`, available to administrators. It takes you from naming a cluster to giving the first people access to it.

The steps are **Identity**, **Connection**, **Handshake**, **Observability** and **Access**. You can jump back to any completed step, but the first two lock once the cluster exists, because steps three to five act on the real cluster.

## Step 1: Identity

Nothing is saved yet; this step only holds the form.

| Field | Notes |
|---|---|
| Name | Required and unique. A duplicate is refused with a 409 ("cluster name already registered"). |
| Environment | `prod`, `staging` or `dev`. Colours the environment band on the fleet overview. |
| Rail chip | Optional, up to four characters, letters and digits only, upper-cased (`eu-west-1` is stored as `EUWE`). Empty keeps the abbreviation the console derives from the name, shown as the placeholder. Worth setting once you have a handful of clusters, because `prod-eu-west-1` and `prod-eu-west-2` derive to the same three letters. |
| Description | Optional free text. |

All four can be edited later ([Managing a cluster](managing.md#editing-a-clusters-labels)). The name and the connection cannot.

## Step 2: Connection

Pick **Agent-based** (recommended) or **Direct API access**; see [Connection modes](connection-modes.md). Submitting this step **creates the cluster**, and steps 1 and 2 lock from then on.

=== "Agent mode"

    No further fields. kubemg mints a registration token and prepares the install command.

=== "Direct mode"

    | Field | Rule |
    |---|---|
    | API server URL | Required; must be a valid URL. |
    | CA certificate | Optional; PEM or base64-encoded PEM. A bad value is refused now rather than at kubeconfig generation. Leave empty for a publicly trusted certificate. |
    | Service account token | Required. Must be allowed to create service accounts and request tokens. |

    A missing URL or token is refused with a 400.

??? example "The same call over REST"
    ```bash
    curl -X POST https://your-kubemg/api/v1/clusters \
      -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
      -d '{"name": "prod-eu-west-1", "environment": "prod", "connection_mode": "agent"}'
    ```

    For direct mode add `"connection_mode": "direct"`, `"api_url"`, `"ca_cert_data"` and `"service_account_token"`. See the [REST API reference](../dev/api.md).

<figure markdown>
  ![Step 2 of the wizard](../assets/screenshots/cluster-wizard-connection.png)
  <figcaption>Step 2 picks the connection mode, and submitting it is what creates the cluster record. Steps 1 and 2 lock once it does.</figcaption>
</figure>

## Step 3: Handshake

- **Agent mode** shows the install command, a Kustomize alternative, a YAML download and the manifest to review. The URL in the commands is **single-use** and expires after 15 minutes; **New URL** mints another. The step checks every three seconds and stops when the agent attaches, even if the tab is in the background. Details are in [The agent](agent.md#what-the-install-command-fetches).
- **Direct mode** shows a **Run check** button that probes the stored API URL.

You can **Skip for now** before the cluster connects; its state is visible everywhere else in the console.

<figure markdown>
  ![Step 3 after the agent attaches](../assets/screenshots/cluster-wizard-handshake.png)
  <figcaption>Step 3 once the agent has dialled in: the tunnel is open, and the cluster reports its own Kubernetes and agent versions.</figcaption>
</figure>

## Step 4: Observability

Optional. This is the same panel as the cluster's own Observability settings; a cluster works fine with no metrics or logs source. See [Observability datasources](../observability/datasources.md).

## Step 5: Access

Grants the first permissions on this cluster, the same operation as the permissions matrix narrowed to one cluster.

- **Grant to**: a group (every member inherits it) or one user.
- **Kubernetes role**: `view`, `edit` or `cluster-admin`.
- **Namespaces**: comma-separated, or empty for every namespace the role allows.

Existing grants on the cluster are listed below the form, each with a revoke action. The step ends with a note that depends on the mode:

- **Direct**: grants govern kubemg's own authorization only. kubemg creates no RoleBinding, so the grant decides what a kubeconfig claims, not what the cluster allows.
- **Agent**: the grant decides which cluster and namespaces kubemg will carry someone to; the cluster's own RBAC decides what they may do there.

## After registration

The cluster appears in the fleet overview and in `/admin/clusters`. See [Managing a cluster](managing.md) for day-2 operations. Access can also be granted later from **Admin → Permissions**, or requested through [just-in-time elevation](../access/jit.md).

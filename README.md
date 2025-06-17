# external-dns-porkbun-webhook

External-DNS Webhook Provider to manage Porkbun DNS Records

> [!NOTE]
> This repository is not affiliated with Porkbun.

> [!WARNING]
> Code tested in production only on my homelab. Might eat your DNS records. You have been warned.

## Setting up external-dns for Porkbun

This tutorial describes how to setup external-dns for usage within a Kubernetes cluster using Porkbun as the domain provider.

Make sure to use external-dns version 0.14.0 or later for this tutorial.

### Creating Porkbun Credentials

A secret containing the a Porkbun API token and an API Password is needed for this provider.
You can get an api and secret key for your user on the Porkbun administration page.

### Deploy external-dns with the webhook

Besides the API key and password, it is mandatory to provide a customer id as well as a list of DNS zones you want external-dns to manage. The hosted DNS zones will be provides via the `--domain-filter`.

You can then modify the following base manifest to fit your specific use case (may vary based on source, the example below uses Traefik CRDs as sources):

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: porkbun-secrets
type: Opaque
data:
  api-key: <base64 encoded API key>
  secret-key: <base64 encoded secret key>
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: external-dns
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: external-dns
rules:
  - apiGroups: [""]
    resources: ["services", "endpoints", "pods"]
    verbs: ["get", "watch", "list"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["list", "watch"]
  - apiGroups: ["traefik.io"]
    resources: ["ingressroutes", "ingressroutetcps", "ingressrouteudps"]
    verbs: ["get", "watch", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: external-dns-viewer
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: external-dns
subjects:
  - kind: ServiceAccount
    name: external-dns
    namespace: external-dns
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: external-dns
spec:
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: external-dns
  template:
    metadata:
      labels:
        app: external-dns
    spec:
      serviceAccountName: external-dns
      containers:
        - name: external-dns
          image: registry.k8s.io/external-dns/external-dns:v0.14.0
          args:
            - --log-level=info
            - --source=traefik-proxy
            - --traefik-disable-legacy
            - --provider=webhook
            - --registry=txt
            - --txt-owner-id=external-dns
        - name: external-dns-webhook-provider
          image: ghcr.io/alessandrozanatta/external-dns-porkbun-webhook:1.0.0
          imagePullPolicy: Always
          args:
            - --log.level=debug
            - --domain-filter=YOUR_DOMAIN
          env:
            - name: PORKBUN_API_KEY
              valueFrom:
                secretKeyRef:
                  key: api-key
                  name: porkbun-secrets
            - name: PORKBUN_SECRET_KEY
              valueFrom:
                secretKeyRef:
                  key: secret-key
                  name: porkbun-secrets
```

You can then follow the external-dns docs on how to configure your specific source.

### Verifying Porkbun DNS records

Check your Porkbun administator page to view the domains associated with your Porkbun account. There you can view the records for each domain.

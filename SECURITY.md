# Security policy

tmp stores private content on behalf of people who trust it. If you find a way to read, change or delete content you are not authorized to reach, or any other weakness, please tell us privately first.

## Reporting a vulnerability

- Email **dev@remarqable.io**. Include the affected version or commit, steps to reproduce, and what you were able to reach. A proof of concept against your own account on a local install is welcome; do not test against other people's data on tmp.io.
- You will get an acknowledgement within **3 business days** and a first assessment within **10 business days**.
- We fix confirmed issues in the main branch, publish a release note that credits you (unless you prefer not to be named), and tell you when it is deployed to tmp.io.
- Please give us **90 days** before publishing details, or until the fix is released, whichever is sooner. We will not take legal action against good-faith research that respects this policy and does not access, modify or destroy data belonging to others.

Do not open a public GitHub issue for a security problem.

## Scope

Everything in this repository and the hosted service at tmp.io: the web application, the REST API, the MCP server, the OAuth authorization server, secret sharing links, Markdown rendering and sanitization, tenant isolation, and the deployment scripts under `scripts/deploy/`.

Out of scope: denial of service by volume, reports from automated scanners without a demonstrated impact, missing best-practice headers with no exploit, social engineering of maintainers, and vulnerabilities in third-party services (Google sign-in, Anthropic, DigitalOcean) that are not caused by how tmp uses them.

## What tmp does and does not protect

Read [docs/SECURITY.md](docs/SECURITY.md) for the threat model and the controls, with file references so each claim can be checked. In short:

- Tenant isolation is enforced by PostgreSQL row-level security; the runtime database role cannot bypass it.
- Credentials are stored as hashes only. Sessions, API tokens, OAuth tokens and sharing links are random 32-byte values.
- Content is encrypted in transit and at rest by the hosting provider. It is **not** end-to-end encrypted: the server reads content to render, search and serve it, and an operator with server access could read it. If that is inside your threat model, self-host.
- AI-assisted filing sends a document excerpt to Anthropic only when the organization owner has turned it on in Settings. It is off by default.

## Supported versions

The `main` branch and the latest tagged release receive fixes. Self-hosters should track releases.

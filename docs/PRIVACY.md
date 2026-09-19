# Privacy

This page describes what the hosted service at **tmp.io** collects and why. It applies to the hosted service only; if you self-host tmp, the operator of that install is responsible for its own notice. Plain language on purpose; where it is unclear, ask.

*Draft for legal review before the hosted service opens. Last updated 2026-09-19.*

## Who

The hosted service is operated by remarQable ("we"). Contact: dev@remarqable.io.

## What we store

| Data | Why | How long |
|---|---|---|
| Your Google account's email address, display name and avatar URL, plus the Google subject identifier | To sign you in and show who you are | Until you delete your account |
| The content you or your connected AI clients write: pages, files, images, PDFs, `tmp.yaml`, and every revision | It is the product | Until you delete it; deleted pages stay in your trash and history until you delete the account |
| Hashes of your session, API tokens, OAuth tokens and sharing links; the names, scopes and last-used times of tokens and connections | To authenticate requests and let you revoke them | Until revoked or expired, then removed by the operator's retention procedure |
| An audit log of actions on your site (who, what, when; never the content) | So you can see what your clients did | Until you delete your account |
| Server logs: request method, route template, status, duration, request ID, and your user and organization IDs when signed in. Not the URL of sharing links, not query strings, not request bodies, not page content | To run the service and investigate problems | 30 days |
| If you join the launch waitlist: your email address | To send one email when the hosted service opens | Deleted after that email, or on request |

We do not use analytics or advertising trackers. There are no third-party scripts on any page.

## Who else sees data

- **Google** handles sign-in. We receive your verified email, name, avatar and subject identifier; we do not keep a Google refresh token.
- **Anthropic** receives a document excerpt (at most the first 2,500 bytes), your folder list and page titles when AI-assisted filing is **turned on by you** in Settings. It is off by default and nothing is sent while it is off. Anthropic's API terms state that API inputs are not used to train their models.
- **DigitalOcean** hosts the servers and the managed PostgreSQL database in their data centres. Storage volumes and backups are encrypted by the provider.
- **AI clients you connect** (Claude, ChatGPT, or any MCP client you authorize) read and write your content on your instruction through the tokens you grant. Revoke them at any time under Connections.
- **People you give a sharing link to** can read, and if you chose so, edit exactly the one document the link is for.

We do not sell data and do not share it with anyone else, except when required by law, in which case we will tell you unless we are legally prevented from doing so.

## Encryption

Data is encrypted in transit (TLS to the browser, to AI clients, and between the application and the database) and at rest by the hosting provider. It is **not** end-to-end encrypted: the application decrypts and reads your content to render pages, build search results and serve them, and an operator with server access could read it. Application-level encryption with per-organization keys is on the roadmap; until then, if that matters for your content, self-host.

## Your rights and controls

- **Export** everything as a ZIP at any time (Admin → Export, or the API).
- **Delete your account** from Site settings. It removes your organization, every page and revision, all files, links, tokens and connections immediately and permanently. Backups that already contain your data are rotated out within 14 days.
- **Ask** for a copy, correction or deletion of anything about you by emailing dev@remarqable.io. We answer within 30 days.

If you are in the EU, UK or another jurisdiction with a data protection law, you have those rights under that law and the same address is the contact.

## Changes

Material changes are announced on the landing page and in the repository at least 14 days before they take effect.

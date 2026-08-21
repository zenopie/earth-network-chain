# Earth Network documentation

The docs site, built with [Docusaurus](https://docusaurus.io) — the same thing
the Cosmos SDK, Osmosis and most Cosmos chains use. Standard shape on purpose:
someone who has read another chain's documentation already knows how to move
around this one.

Published at **https://docs.erth.network**.

## Contributing

The pages are markdown in [`docs/`](./docs). Nothing else is needed to change
one — no React, no build, no local setup.

Every published page has an **Edit this page** link at the bottom that opens the
right file on GitHub. Fix it there and open a pull request.

### Adding a page

Drop a markdown file in `docs/` with front matter:

```markdown
---
sidebar_position: 7
---

# Page title
```

`sidebar_position` decides where it lands in the sidebar. Nothing else to
register — the sidebar is generated from the directory.

### Running it locally

```bash
cd docs-site
npm install
npm start          # http://localhost:3000
```

```bash
npm run build      # production build, fails on a broken link
npm run serve      # preview the production build
```

## Deploying

Pushed to master, `.github/workflows/docs.yml` builds and publishes to GitHub
Pages. Two things have to be set up once, outside this repo:

1. **DNS** — a `CNAME` record for `docs` pointing at `zenopie.github.io`. If it
   is proxied through Cloudflare, SSL mode must be **Full**; Flexible loops
   against Pages, which always serves HTTPS.
2. **GitHub** — Settings → Pages → Source: *GitHub Actions*, and Custom domain:
   `docs.erth.network`. The `static/CNAME` file in this directory is what tells
   Pages to keep that domain across deploys; deleting it resets the setting on
   the next publish.

A subdomain rather than `erth.network/docs` because it is the Cosmos convention
(`docs.cosmos.network`, `docs.osmosis.zone`, `docs.celestia.org`) and because it
needs no proxy: Pages takes a custom domain natively, whereas a path would have
to be routed in front of the app, and the app's single-page router would
otherwise swallow `/docs` itself.

## What lives here, and what does not

This site is for **people using the network** — what Earth is, how registering
works, where ERTH comes from, how to take part.

The **operational** guides stay in [`../docs`](../docs), next to the code they
describe, so they cannot drift from it:

| | |
| --- | --- |
| [`JOIN.md`](../docs/JOIN.md) | running a node |
| [`UPGRADES.md`](../docs/UPGRADES.md) | coordinated upgrades |
| [`TRUST_STORE_RUNBOOK.md`](../docs/TRUST_STORE_RUNBOOK.md) | revoking a passport certificate |

This site links to them rather than copying them. A copy would be wrong within a
release.

## Accuracy

These pages state numbers — 4 ERTH/sec, 21-day unbonding, 0.3% swap fee, 7-day
voting. Every one is in the chain's genesis or its source. If you change a
parameter, change it here too; a confidently wrong number is worse than a missing
one.

# Earth Network documentation

The docs site, built with [Docusaurus](https://docusaurus.io) — the same thing
the Cosmos SDK, Osmosis and most Cosmos chains use. Standard shape on purpose:
someone who has read another chain's documentation already knows how to move
around this one.

Published at **https://erth.network/docs**.

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
npm start          # http://localhost:3000/docs/
```

```bash
npm run build      # production build, fails on a broken link
npm run serve      # preview that build at the real /docs/ base path
```

`npm run serve`, not a generic static server: the site is built for the `/docs/`
base path, so serving `build/` at the root gives an unstyled page with 404s for
every asset.

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

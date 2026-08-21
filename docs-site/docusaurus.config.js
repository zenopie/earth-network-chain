// @ts-check
// Docusaurus, which is what the Cosmos SDK, Osmosis, Celestia and most Cosmos
// chains use for their docs. Standard shape on purpose: someone who has read
// another chain's documentation already knows how to move around this one.
//
// Docs-only mode — no blog, no separate landing page. The site *is* the docs, and
// `/` is the introduction.
//
// Served from its own subdomain rather than erth.network/docs. That is the Cosmos
// convention (docs.cosmos.network, docs.osmosis.zone, docs.celestia.org) and it is
// also the only one that deploys without a proxy: GitHub Pages takes a custom
// domain natively, whereas a path would need a rule in front of the app — and the
// app is a single-page router that would otherwise swallow /docs itself.

import { themes as prismThemes } from "prism-react-renderer";

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: "Earth Network",
  tagline: "One human, one account",
  favicon: "img/favicon.ico",

  url: "https://docs.erth.network",
  baseUrl: "/",

  organizationName: "zenopie",
  projectName: "earth-network-chain",

  // A broken link is a page that lies about where something is, which is worse
  // than a missing page. Fail the build.
  onBrokenLinks: "throw",
  markdown: { hooks: { onBrokenMarkdownLinks: "throw" } },

  i18n: { defaultLocale: "en", locales: ["en"] },

  presets: [
    [
      "classic",
      /** @type {import('@docusaurus/preset-classic').Options} */
      ({
        docs: {
          routeBasePath: "/",
          sidebarPath: "./sidebars.js",
          // Every page gets an "Edit this page" link straight to the markdown.
          // That is most of what makes docs contributable: the barrier is
          // usually not willingness, it is finding the file.
          editUrl:
            "https://github.com/zenopie/earth-network-chain/tree/master/docs-site/",
        },
        blog: false,
        theme: { customCss: "./src/css/custom.css" },
      }),
    ],
  ],

  themeConfig:
    /** @type {import('@docusaurus/preset-classic').ThemeConfig} */
    ({
      colorMode: { respectPrefersColorScheme: true },
      navbar: {
        title: "Earth Network",
        logo: { alt: "Earth Network", src: "img/logo.png" },
        items: [
          { to: "/", label: "Docs", position: "left", activeBaseRegex: "^/$" },
          { href: "https://erth.network", label: "App", position: "right" },
          {
            href: "https://github.com/zenopie/earth-network-chain",
            label: "GitHub",
            position: "right",
          },
        ],
      },
      footer: {
        style: "dark",
        links: [
          {
            title: "Docs",
            items: [
              { label: "Introduction", to: "/" },
              { label: "Registering", to: "/registering" },
              { label: "Emission", to: "/emission" },
              { label: "Running a node", to: "/running-a-node" },
            ],
          },
          {
            title: "Code",
            items: [
              {
                label: "Chain",
                href: "https://github.com/zenopie/earth-network-chain",
              },
              {
                label: "Releases",
                href: "https://github.com/zenopie/earth-network-chain/releases",
              },
              {
                label: "Running a node",
                href: "https://github.com/zenopie/earth-network-chain/blob/master/docs/JOIN.md",
              },
            ],
          },
          {
            title: "More",
            items: [{ label: "App", href: "https://erth.network" }],
          },
        ],
        copyright: `Earth Network. Documentation licensed under Apache 2.0.`,
      },
      prism: {
        theme: prismThemes.github,
        darkTheme: prismThemes.dracula,
        additionalLanguages: ["bash", "json", "toml"],
      },
    }),
};

export default config;

# Official Bugsnitch brand assets

<img src="../../web/public/brand/app-icon-192.png" alt="Bugsnitch bug logo" width="80" height="80">

`bugsnitch-logo.svg` is the canonical icon. Use **Bugsnitch** in product copy and in the wordmark, set in IBM Plex Mono Bold (700). Use lowercase for `bugsnitch.com` and the `bugsnitch` command. The palette uses red `#d40000`, ink `#201e1d`, and a light canvas `#faf9f6`. Place the icon on a light surface.

| Use | Asset |
| --- | --- |
| Icon artwork | [Transparent SVG](bugsnitch-logo.svg) |
| Horizontal wordmark | [Outlined SVG](../../web/public/brand/bugsnitch-wordmark.svg) · [Transparent PNG](../../web/public/brand/bugsnitch-wordmark.png) |
| Browser tabs | [SVG favicon](../../web/public/brand/favicon.svg) · [Multi-size ICO](../../web/public/favicon.ico) · [16 px](../../web/public/brand/favicon-16.png) · [32 px](../../web/public/brand/favicon-32.png) · [48 px](../../web/public/brand/favicon-48.png) |
| Home screen and profile images | [180 px Apple touch icon](../../web/public/brand/apple-touch-icon.png) · [192 px icon](../../web/public/brand/app-icon-192.png) · [512 px icon or avatar](../../web/public/brand/app-icon-512.png) |
| Social link preview | [1200 × 630 PNG](../../web/public/brand/social-share.png), served at `https://bugsnitch.com/brand/social-share.png` after deployment |

The [project README](../../README.md) uses the 192 px app icon. Its light background keeps the black and red artwork visible in both GitHub themes. The homepage and its social card pair the icon with the Bugsnitch wordmark. [Current social card](../../web/public/brand/social-share.png)

With the web dependencies and Chrome installed, run `node web/brand/render.mjs` from the repository root to regenerate the favicon, PNG exports, and social image. The horizontal SVG uses glyph outlines from the bundled Bold font. The font is accompanied by its [SIL Open Font License](../../web/public/licenses/ibm-plex-mono-OFL.txt).

These files are brand assets. They are **not licensed under MPL-2.0 merely because they are stored in this public repository**. The software license covers source code, not the Bugsnitch name, logo, visual identity, or derived brand images.

Official Bugsnitch releases and documentation may display this logo. Forks should provide their own identity unless permission is granted. See [TRADEMARKS.md](../../TRADEMARKS.md).

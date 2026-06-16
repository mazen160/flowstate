# Launching the rebrand

This is a one-shot runbook for applying the visual / narrative rebrand on
github.com itself. The repo content is already on `main`; the steps below
take effect on the GitHub side (repo About, topics, social preview,
Discussions).

Run them once. None of this requires any code changes.

## Prerequisites

```sh
gh auth status                                  # logged in as the repo owner
gh repo set-default mazen160/flowstate          # so subsequent gh calls infer the repo
```

## 1. Update the repo About (description, homepage, topics)

```sh
gh repo edit mazen160/flowstate \
  --description "Think out loud. Ship in text. A speech-to-text CLI for Mac, Linux, and Windows — Whisper transcription + LLM cleanup, piped wherever you need it." \
  --homepage "https://github.com/mazen160/flowstate#flowstate" \
  --add-topic speech-to-text \
  --add-topic dictation \
  --add-topic cli \
  --add-topic groq \
  --add-topic whisper \
  --add-topic voice \
  --add-topic productivity \
  --add-topic developer-tools \
  --add-topic cross-platform \
  --add-topic golang \
  --add-topic terminal \
  --add-topic flow-state
```

This sets the line that shows under the repo name on github.com, plus the
topics that drive search / topic-page discoverability.

## 2. Enable Discussions

```sh
gh repo edit mazen160/flowstate --enable-discussions
```

Then visit the Discussions tab once and create three categories so the
templates under `.github/DISCUSSION_TEMPLATE/` light up:

- **Show & Tell** — Discussion format, slug `show-and-tell`.
- **Q&A** — Q&A format, slug `q-a`.
- **Ideas** — Discussion format, slug `ideas`.

Category slugs must match the URLs referenced in the README and the
issue-template `config.yml`.

## 3. Upload the social preview image

The GitHub REST API doesn't (yet) expose social preview uploads via
`gh api`, so the canonical path is the web UI:

1. Open <https://github.com/mazen160/flowstate/settings>.
2. Scroll to **Social preview** and click **Edit**.
3. Upload `assets/social-card.png` (committed in this branch).

That image is what X/Twitter, LinkedIn, Bluesky, Hacker News, and Slack
will render when someone posts the repo URL.

## 4. (Optional) Set the repo avatar to the new icon

Same place as social preview:
<https://github.com/mazen160/flowstate/settings>. Upload
`assets/logo-icon.png` under your org / user avatar if you want the icon
to show on the repo card.

## 5. Cut the release

The release pipeline is already wired up (see [docs/RELEASING.md](RELEASING.md)).
To ship a release, follow that runbook: update `CHANGELOG.md`, push a
`vX.Y.Z` tag, and let CI build + draft the release.

Once a release is published, the README badges (release version, and the
download count once that's added) populate with real data, which is what
makes the front of the README feel alive when a stranger lands on it for
the first time.

## 6. Publish the one-page site (`website/`)

The repo ships a self-contained one-page marketing site under `website/`
(`index.html` + `style.css` + assets + `CNAME` + `robots.txt` + `sitemap.xml` +
`llms.txt`). The canonical URL baked into its meta tags is
`https://flowstate.mazin.xyz/`. If you deploy somewhere else, update the
`canonical` / `og:url` / `sitemap.xml` / `robots.txt` URLs and the `CNAME`
file first.

The simplest path is GitHub Pages from a `gh-pages` branch:

```sh
git subtree push --prefix website origin gh-pages
```

Then in the repo settings, set Pages to build from the `gh-pages` branch
(root). The `website/CNAME` file pins the custom domain, so the site serves
at `https://flowstate.mazin.xyz/` once the DNS record is in place.

DNS: add a `CNAME` record for `flowstate` in the `mazin.xyz` zone pointing to
`mazen160.github.io.` (an apex would use GitHub Pages' A/AAAA records, but a
subdomain `CNAME` is the right call here). Enable "Enforce HTTPS" in the Pages
settings once the cert provisions.

To preview locally before deploying:

```sh
cd website && python3 -m http.server 8080   # then open http://127.0.0.1:8080
```

Cloudflare Pages or Netlify work too: point them at the `website/` directory
with no build command (it is static HTML), then set the custom domain to
`flowstate.mazin.xyz`.

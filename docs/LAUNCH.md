# Launching the rebrand

This is a one-shot runbook for applying the visual / narrative rebrand on
github.com itself. The repo content is already updated on the
`rebrand/viral-makeover` branch; the steps below take effect on the GitHub
side (repo About, topics, social preview, Discussions).

Run them once, after the rebrand PR merges to `main`. None of this requires
any code changes.

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

## 5. Cut the v0.1.0 release

The release pipeline is already wired up (see [docs/RELEASING.md](RELEASING.md)).
Once you're happy with the rebrand, follow that runbook to push a `v0.1.0`
tag and let CI build + draft the release.

After the release is published, the README badges (release version,
download count once that's added) start populating with real data — which
is what makes the front of the README feel alive when a stranger lands on
it for the first time.

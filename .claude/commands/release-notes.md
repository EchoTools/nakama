---
description: "Generate audience-targeted release notes and post to Discord. Use when tagging a release, preparing changelog, or user says /release-notes."
allowed-tools: Bash, Read, Write, Edit, Glob, Grep, Agent, WebFetch
---

# Release Notes Generator

Follow the instructions in `docs/CHANGELOG_PROMPT.md`.

**Input:** A release tag (e.g. `v3.27.2-evr.263`) and optionally a base tag. If no base tag is given, use the immediately preceding tag.

If no tag is provided, determine the latest tag from `git tag --sort=-v:refname | head -5` and use it.

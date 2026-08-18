# Public Release Security Checklist

Track the remaining work before making this repository public and publishing signed releases.

## Completed in Repo

- [x] Keep normal PR CI on `pull_request`.
- [x] Confirm no workflow uses `pull_request_target`.
- [x] Keep Apple signing/notarization secrets out of PR/test workflows.
- [x] Restrict the release workflow to trusted release triggers: `v*` tag pushes and manual `workflow_dispatch`.
- [x] Add default read-only `GITHUB_TOKEN` permissions to workflows.
- [x] Grant `contents: write` only to the release publishing job.
- [x] Decode signing/notarization secrets from environment variables instead of direct shell interpolation.
- [x] Write decoded `.p12`, `.p8`, and `.pfx` files only under `RUNNER_TEMP`.
- [x] Clean up temporary certificate/key files and temporary macOS keychain with `if: always()`.
- [x] Import the macOS Developer ID Application certificate into a temporary CI keychain.
- [x] Fail the release job if no Developer ID Application identity is found.
- [x] Preserve hardened runtime signing, notarization wait, stapling, and Gatekeeper verification.
- [x] Validate manual release version input before using it in release outputs and build commands.
- [x] Ignore local signing/notarization credential exports: `*.p8`, `*.p12`, `*.pfx`.
- [x] Search current tracked files and Git history for `.p8`, `.p12`, `.pfx`, `AuthKey*`, `.env`, private-key headers, and obvious hardcoded key/cert blobs.
- [x] Harden Ubuntu apt update steps against the flaky Azure apt mirror.
- [x] Pin all external GitHub Actions in `.github/workflows/` to full commit SHAs.

## GitHub Settings To Do

- [x] Protect `v*` tags so only trusted maintainers can create or update release tags.
- [x] Restrict who can manually run the `Release` workflow, if repo/org policy supports it.
- [x] Keep Actions approval required for outside/fork contributors.
- [x] Set repository default `GITHUB_TOKEN` permissions to read-only in GitHub Settings.
- [x] Enable GitHub secret scanning.
- [x] Enable GitHub push protection.
- [x] Review branch protection for `main`: require CI, require reviews, and block force-pushes.

## Release Validation To Do

- [ ] Run a manual `Release` workflow dry run with a test version such as `0.1.0`.
- [ ] Confirm the macOS job passes: certificate import, notarization key write, package build, and verification.
- [ ] Download the `macos-dmg` artifact from the dry run.
- [ ] Confirm `xcrun stapler validate` passes on the downloaded DMG.
- [ ] Confirm `spctl --assess --type open --context context:primary-signature --verbose` passes on the downloaded DMG.
- [ ] Push the real `v*` tag only after the dry run passes.

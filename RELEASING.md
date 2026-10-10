# Releasing

Releases are built by GoReleaser in GitHub Actions
(`.github/workflows/release.yml`) when a tag starting with `v` is pushed.

```
git tag v0.1.0
git push origin v0.1.0
```

The workflow runs the tests, builds Linux (amd64, arm64, armv7), Windows
(amd64, arm64) and macOS (amd64, arm64) binaries, and publishes them with
checksums and a changelog on the GitHub release. The version is stamped into
the binary, so `bauddy --version` and the `?` overlay show it.

It runs on a macOS runner because the macOS build needs cgo (the serial
library lists ports through IOKit); Linux and Windows cross-compile from there.

To try a release locally without publishing:

```
make snapshot        # goreleaser release --snapshot --clean
```

## macOS signing (optional, paid)

Unsigned macOS binaries work, but a binary downloaded with a browser is
quarantined and Gatekeeper refuses to open it until the user allows it in
System Settings → Privacy & Security, or runs
`xattr -d com.apple.quarantine bauddy`. `go install`, `curl` and Homebrew
formulae don't set the quarantine flag.

Signing and notarizing need a paid Apple Developer Program membership
($99/year); a free Apple account can't get a Developer ID certificate or use
notarization. GoReleaser does both from CI once these repository secrets
exist; until then the `notarize` step is skipped.

| Secret | What it is |
|---|---|
| `MACOS_SIGN_P12` | Developer ID Application certificate and key, exported as .p12, base64-encoded |
| `MACOS_SIGN_PASSWORD` | Password for the .p12 |
| `MACOS_NOTARY_ISSUER_ID` | Issuer ID from App Store Connect → Users and Access → Integrations → Keys |
| `MACOS_NOTARY_KEY_ID` | ID of an App Store Connect API key |
| `MACOS_NOTARY_KEY` | That key's `AuthKey_XXXX.p8` file, base64-encoded |

## Repository images

`docs/logo.svg` is the README logo. `docs/avatar.png` and
`docs/social-preview.png` are for the repository settings: upload the social
preview under Settings → General → Social preview.

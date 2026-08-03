# Release signing setup

The chatmem release workflow (`.github/workflows/release.yml`) signs
artifacts when the right GitHub secrets are present:

- **RPM GPG signing** (openSUSE / Fedora / RHEL) — kicks in when
  `GPG_PRIVATE_KEY` + `GPG_PASSPHRASE` are set. Users stop seeing
  *"Package … is not signed!"* warnings on `zypper in chatmem`.
- **macOS Developer-ID signing + notarization** — kicks in when
  `MACOS_CERT_P12` + `MACOS_CERT_PWD` + `APP_STORE_CONNECT_KEY` are set.
  Users stop seeing *"Apple could not verify …"* Gatekeeper dialogs on
  `brew install --cask chatmem`.

Both are optional. The workflow degrades cleanly when secrets are
missing — it just publishes unsigned artifacts as v0.3.1 did.

---

## Part 1: RPM GPG signing (free, ~15 minutes)

### 1.1 Generate a GPG key

```bash
# On your local machine:
gpg --batch --generate-key <<EOF
%no-protection
Key-Type: RSA
Key-Length: 4096
Name-Real: chatmem release signing
Name-Email: siddhant.d777@gmail.com
Expire-Date: 5y
%commit
EOF
```

`%no-protection` skips a passphrase so we can sign non-interactively in
CI. If you'd rather passphrase-protect it, remove that line and set the
passphrase as `GPG_PASSPHRASE` below.

### 1.2 Export the private key

```bash
gpg --list-secret-keys --with-colons | awk -F: '/^sec:/ {print $5; exit}'
# → e.g. 3F8B5C7A9E1D4A2F6C8B1E5D9F2A4C7B8E1D5F9A
```

Save that key id somewhere. Then:

```bash
gpg --armor --export-secret-keys <KEY_ID> > /tmp/chatmem-signing.asc
```

### 1.3 Add the secrets

```bash
# From ~/chatmem:
gh secret set GPG_PRIVATE_KEY --repo sid077/chatmem < /tmp/chatmem-signing.asc

# If you set a passphrase in 1.1, also:
gh secret set GPG_PASSPHRASE --repo sid077/chatmem
# → paste the passphrase, press ⏎, Ctrl-D

# Immediately shred the private key from disk:
shred -u /tmp/chatmem-signing.asc
```

### 1.4 Confirm

Next tag push (`git tag -a v0.x.y ... && git push origin v0.x.y`) will:
- rpm --addsign every .rpm before uploading
- publish `chatmem.gpg` (your public key) alongside the .repo file
- flip the .repo file to `gpgcheck=1 gpgkey=<url>/chatmem.gpg`

Users get automatic key import on first `zypper ar` (with `--gpg-auto-import-keys`).

---

## Part 2: macOS Developer ID + notarization ($99/year, ~1 hour)

### 2.1 Enroll in the Apple Developer Program

https://developer.apple.com/programs/ — $99/year. Takes 24–48h for approval.

### 2.2 Create a Developer ID Application certificate

Once enrolled:

1. Open **Keychain Access** on your Mac.
2. Menu → **Keychain Access → Certificate Assistant → Request a Certificate From a Certificate Authority…**
3. Fill in your Apple ID email + name. Choose **Saved to disk**. Save the `.certSigningRequest`.
4. Go to https://developer.apple.com/account/resources/certificates/list
5. **+** → **Developer ID Application** → upload the `.certSigningRequest` → download the `.cer` file.
6. Double-click the `.cer` to install it into Keychain Access.
7. In Keychain, find "Developer ID Application: Your Name (TEAMID)" under **login** → right-click → **Export** → save as `chatmem-signing.p12` with a strong password.

### 2.3 Base64-encode for GitHub secrets

```bash
base64 -i chatmem-signing.p12 -o /tmp/cert.p12.b64
gh secret set MACOS_CERT_P12 --repo sid077/chatmem < /tmp/cert.p12.b64
gh secret set MACOS_CERT_PWD --repo sid077/chatmem
# → paste the .p12 password, ⏎, Ctrl-D

shred -u chatmem-signing.p12 /tmp/cert.p12.b64
```

### 2.4 Create an App Store Connect API key (for notarization)

1. https://appstoreconnect.apple.com/access/integrations/api → **Users and Access → Integrations**.
2. Generate a key with **Developer** role.
3. Download the `.p8` key file. Note the **Key ID** and **Issuer ID** displayed on the page.

Compose the JSON `rcodesign` expects:

```bash
cat > /tmp/appstore.json <<JSON
{
  "key_id": "ABCD1234EF",
  "issuer_id": "12345678-90ab-cdef-1234-567890abcdef",
  "key_pem": "$(awk 'BEGIN{ORS="\\n"} {print}' AuthKey_ABCD1234EF.p8)"
}
JSON

base64 -i /tmp/appstore.json -o /tmp/appstore.b64
gh secret set APP_STORE_CONNECT_KEY --repo sid077/chatmem < /tmp/appstore.b64
shred -u /tmp/appstore.json /tmp/appstore.b64 AuthKey_ABCD1234EF.p8
```

### 2.5 Confirm

Next tag push will:
- run `rcodesign sign` on the chatmem binary inside each darwin tarball
- submit each signed binary to Apple's notary service, wait for approval
- repack the tarball with the signed+notarized binary
- overwrite the release asset

Users get **zero Gatekeeper prompts** on `brew install --cask chatmem` from then on. They also don't need to run `xattr -d com.apple.quarantine` — Homebrew Cask honors valid Developer-ID signatures.

---

## Troubleshooting

- **Workflow says "Sign RPMs" step skipped** → `GPG_PRIVATE_KEY` secret is empty or wasn't imported correctly. Re-run `gh secret set` and re-tag.
- **rpm --addsign fails with "gpg: signing failed"** → passphrase mismatch. Verify `GPG_PASSPHRASE` matches the key you generated.
- **rcodesign notary-submit says "invalid credentials"** → the App Store Connect JSON is missing a field or the .p8 wasn't included correctly. Regenerate `APP_STORE_CONNECT_KEY` from scratch.
- **Notarization takes >10 minutes** → normal for the first submission of a new binary. Subsequent releases are usually <2 min because Apple caches.

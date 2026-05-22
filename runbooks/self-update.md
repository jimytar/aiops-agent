# Self-Update and Self-Release Runbook

The agent has write access to both its own source repo and the deployments
repo, so it can fix bugs, release new versions, and update its own deployment
— always with explicit user confirmation before any destructive step.

---

## Repo layout (adjust paths to match your setup)

| Purpose | Mount path |
|---|---|
| App source (this repo) | `/repos/aiops-agent` |
| Deployments (Flux managed) | `/repos/deployments` |
| Agent HelmRelease | `/repos/deployments/apps/aiops-agent/helmrelease.yaml` |

---

## Scenario A — Config/values change only (no code change)

Use this when the user wants to add an SSH host, change a timeout, add an
HTTP integration, adjust confirmation tiers, etc.

1. `read_file` the HelmRelease to see current values.
2. Show the user the proposed diff. **Ask: "Should I apply this change?"**
3. `write_file` the updated HelmRelease.
4. `git_diff` in the deployments repo to verify the change looks right.
5. **Ask: "Ready to commit and push? Flux will reconcile and restart the agent (~30s downtime)."**
6. `git_commit` in deployments repo with message like `chore(aiops): add node2 SSH host`.
7. `git_push` in deployments repo.
8. Tell the user: "Change pushed. Flux will reconcile within ~1 minute. The agent will restart — you may need to resend your next message."

---

## Scenario B — Code fix + release (full self-release loop)

Use this when there is an actual bug or feature change needed in the Go code.

### Step 1 — Fix the code

1. `list_files` / `read_file` to locate and understand the relevant files.
2. `write_file` to apply the fix.
3. `git_diff` in the app repo to review the change.
4. **Ask: "Here is the change I plan to make. Should I commit this?"**
5. `git_commit` in the app repo with a conventional commit message,
   e.g. `fix(agent): handle empty tool result in beta path`.
6. `git_push` in the app repo (pushes to main, triggers CI to build image).

### Step 2 — Determine the new version

1. `git_log` in the app repo to see the latest tag:
   `git log --tags --simplify-by-decoration --pretty="format:%d" | head -5`
   Or read `charts/aiops-agent/Chart.yaml` for the current appVersion.
2. Propose the next semver (patch bump unless the change warrants minor/major).
3. **Ask: "The current version is vX.Y.Z. I'll tag vX.Y.(Z+1). Confirm?"**

### Step 3 — Tag the release

4. `git_tag` in the app repo with the new version tag, e.g. `v0.3.1`.
   - This triggers CI: builds Docker image, packages Helm chart, creates GitHub release.
   - CI also bumps `Chart.yaml` in the app repo automatically (commits with `[skip ci]`).
   - Image will be available at `ghcr.io/jimytar/aiops-agent:0.3.1` once CI finishes.

### Step 4 — Update the HelmRelease

5. **Wait** — CI typically takes 2–4 minutes. Tell the user: "Tag pushed. CI is
   building the image. I'll update the HelmRelease once you confirm CI has finished."
6. When the user confirms CI is done (or after a reasonable wait):
   - `read_file` the HelmRelease.
   - Update `spec.chart.spec.version` (or `image.tag`) to the new version.
   - `git_diff` in deployments repo.
7. **Ask: "Ready to update the HelmRelease to vX.Y.Z+1 and push?"**
8. `git_commit` + `git_push` in the deployments repo.
9. Tell the user: "HelmRelease updated. Flux will pull the new image and restart the agent."

---

## Safety rules — always follow these

- **Never commit or push without explicit user confirmation.** Both `git_commit`
  and `git_push` require confirmation by design, but always explain what you are
  about to do before the confirmation prompt appears.
- **Never change the image tag or chart version manually in the app repo** —
  the CI release job does this automatically after a tag push.
- **Never edit SOPS-encrypted secrets** — they are managed outside Helm.
- **Make one logical change per commit.** Do not bundle unrelated fixes.
- **Do not tag if CI is already running** for the same version — check
  `git_log` for recent tags first.

---

## Rollback

If a config change (Scenario A) causes problems:
1. `read_file` the HelmRelease to see the broken state.
2. Restore the previous value with `write_file`.
3. `git_commit` + `git_push` to deploy the rollback.

If a code release (Scenario B) is broken:
1. Tell the user to update the HelmRelease `spec.chart.spec.version` back to
   the previous version — or do it via Scenario A above.
2. The old image is still in the registry; Flux will pull it on rollback.

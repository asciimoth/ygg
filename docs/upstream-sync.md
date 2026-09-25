# Upstream Sync Checkpoint

The last completed upstream review includes original Yggdrasil changes through
[`422836eeb21a99790caa286aa63d493bbc4766d7`](https://github.com/yggdrasil-network/yggdrasil-go/commit/422836eeb21a99790caa286aa63d493bbc4766d7),
inclusive. This commit is the target of the annotated `v0.5.14` tag and was
released on 2026-06-19.

This is a review and adaptation checkpoint. The fork has a different source
tree and commit history, so it is not tree-identical to this upstream revision.

## Completed local changes

The sync started after the fork point
`be5daeba7ad6b9eb3a30a3fa84e58d3962322dbd` and was completed on 2026-09-25
at local revision `1bcb33b972d7075ab927d806b532a7edd18b9895`.

The upstream work was adapted in these local commits:

1. `ddfbf7d7314dae90fa8091dfb108f3708d16ea29` — input and panic hardening.
2. `57fad43af8ffab27045a909d9277ce3750b310b0` — UNIX admin socket ownership.
3. `a99973f717a04551118462a725d342389bdca6b6` — bounded Ironwood packet queues.
4. `e47ae54052abf98b3625dbe7f906f0bae4ca14ca` — dependency updates.
5. `1bcb33b972d7075ab927d806b532a7edd18b9895` — optional group-password
   authentication.

On the completion date, the upstream `develop` and `master` branches both
pointed to the checkpoint revision.

## Start the next sync

Start the next upstream review after the checkpoint. Do not review earlier
commits again unless the previous adaptation needs an audit.

If the `upstream` remote is not configured, add it and fetch its branches:

```sh
git remote add upstream https://github.com/yggdrasil-network/yggdrasil-go.git
git fetch upstream
```

Inspect new commits and changes with this exclusive range:

```sh
git log --reverse --oneline 422836eeb21a99790caa286aa63d493bbc4766d7..upstream/develop
git diff 422836eeb21a99790caa286aa63d493bbc4766d7..upstream/develop
```

After the next sync, replace the checkpoint with the new inclusive upstream
revision and record the last local adaptation commit.

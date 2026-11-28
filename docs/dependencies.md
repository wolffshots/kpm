# Package dependencies

kpm 0.17.0 adds a `requires` field to a package def. It names the packages that must be on the device for the package to work. For example, Kobo-UNCaGED needs NickelMenu.

```toml
[packages.example-addon]
name     = "Example add-on"
source   = "github.com/example/example-addon"
forge    = "github"
asset    = "KoboRoot.tgz"
requires = ["nickelmenu"]
min_kpm  = "0.17.0"
```

## Rules

1. `requires` holds package ids only. It has no version ranges.
2. The relation is one way. If A requires B, B does not need to know about A.
3. kpm reads `requires` from the registry entry when one exists, else from the local def. This is the same rule as for `conflicts`.
4. A requirement is met when the package is installed or staged. kpm itself is always met. On firmware 6.x, a package with `builtin_fw6` is met, because the firmware has the feature.

## What each command does

| Command | Behavior |
|---|---|
| `kpm install A` | Registers each requirement of A that has no def yet, from the registry, in install order. The confirm pause lists them on an `also installs` line. The JSON `changed` list names every def that kpm wrote. |
| `kpm update A` | Adds each registered requirement of A that is not installed yet to the same run. kpm handles a requirement before the packages that need it. |
| `kpm update --all` | Handles each requirement before the packages that need it. |
| `kpm uninstall B` | Refuses while an installed or staged package requires B. The message names that package. |
| `kpm doctor` | Adds `requires X, which is not installed` to the detail of a package with an unmet requirement. |
| `kpm info A` | Shows a `requires:` line. |

## Refusals

`kpm install A` refuses, and writes no def, when:

- the requirements make a loop, for example `a -> b -> a`,
- no registry offers a requirement,
- a requirement needs a newer kpm, or has no build for the firmware line of the device,
- a requirement conflicts with an installed package, or with another package in the same install.

`kpm update` blocks a package that is not installed yet when a requirement is not met at its turn in the run. Under `--all` this is a skip with a note. When you name the package, it is a failure. A package that is already installed still updates: the update does not make a missing requirement worse.

`kpm uninstall B` refuses while a package that requires B is installed or staged. `--yes` and `--force` do not override it, because the UI always passes `--yes`.

## JSON fields

`kpm search --json` and `kpm status --json` rows have three new arrays. Each is never null.

| Field | Meaning |
|---|---|
| `requires` | The ids that the effective def requires, sorted. |
| `requires_missing` | The subset of `requires` that is not met on this device. |
| `required_by` | The installed or staged packages that require this one. If it is not empty, uninstall is refused. |

`kpm info --json` has `def.requires`. `kpm doctor --json` rows have `requires_missing`.

## UI

The package page shows a `Requires:` line. It also says:

- "Install also installs X, which this package needs." for a package that is not installed,
- "Needs X, which is not installed." for an installed package with an unmet requirement,
- "Needed by X. Uninstall it first to uninstall this." for a package that another installed package needs. The Uninstall button is not shown then.

## Limits

- No version ranges.
- `kpm remove` does not check `requires`. It removes only the def and leaves the files.
- A package that is not installed but has a def (for example after a failed update) is not counted as a dependent.
- When a def in `packages.d` does not parse, kpm cannot see what it requires. Install and update refuse, as for conflicts. Uninstall goes ahead with the defs it can read.

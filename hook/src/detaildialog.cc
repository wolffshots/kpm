// NICKEL-UI.md §6 — DetailDialog + action flow.

#include <QHBoxLayout>
#include <QJsonArray>
#include <QLabel>
#include <QStringList>
#include <QTimer>
#include <QVBoxLayout>

#include <NickelHook.h>

#include "configdialog.h"
#include "detaildialog.h"
#include "files.h"
#include "nkpm.h"
#include "widgets/elidedlabel.h"
#include "widgets/label.h"

// INTEGRITY.md §4 — at most this many "Manage <blocker>" buttons are offered. The
// blocker list comes from the registry, i.e. third-party data, and the action
// footer is a single row: two is already more than any real conflict declaration
// needs, and the advisory line always names every blocker regardless.
static const int kMaxBlockerButtons = 2;

// jsonStrings flattens a JSON string array, dropping non-strings and empties.
// Used for the conflicts / conflicts_active arrays, which INTEGRITY.md §6.1
// guarantees are always arrays (normalized to [], never null) — this is defence
// in depth against a payload from some other kpm build.
static QStringList jsonStrings(const QJsonArray &array) {
  QStringList out;
  for (const QJsonValue &v : array) {
    if (v.isString() && !v.toString().isEmpty()) {
      out.append(v.toString());
    }
  }
  return out;
}

DetailDialog *DetailDialog::show(QJsonObject package, const QHash<QString, QJsonObject> &packages) {
  return new DetailDialog(package, packages);
}

// displayName resolves a package id to the name the rest of the UI shows
// (NickelClock, not nickelclock). It falls back to the raw id when the package is
// absent from the search list, which is a real case and not a theoretical one: a
// package that was `kpm remove`d but never uninstalled still has files on the device
// and still blocks (INTEGRITY.md §4.3) while no longer appearing in search. A raw id
// on the button is far better than a blank one.
QString DetailDialog::displayName(const QString &packageId) const {
  QString name = packageIndex.value(packageId).value("name").toString();
  return name.isEmpty() ? packageId : name;
}

// blockerStagedOnly distinguishes the two ways a blocker can be "active".
// conflicts_active counts a package that is installed OR staged (§4.3 — staged
// files land at the next reboot), so calling every blocker "installed" is simply
// wrong for half the cases, and the remedy differs: a staged package cannot be
// uninstalled until the reboot that installs it has happened.
//
// A blocker absent from the search list reads as installed, which is the only way
// it can block while being invisible: `kpm remove` deleted its def and left its
// files on the device (§4.3).
bool DetailDialog::blockerStagedOnly(const QString &packageId) const {
  QJsonObject blocker = packageIndex.value(packageId);
  return blocker.value("installed").toString().isEmpty() && blocker.value("staged").toBool();
}

DetailDialog::DetailDialog(QJsonObject package, const QHash<QString, QJsonObject> &packages)
    : Dialog(package.value("name").toString().isEmpty() ? package.value("id").toString()
                                                        : package.value("name").toString()),
      pkg(package), id(package.value("id").toString()), packageIndex(packages) {
  // Guard against a null parent (see BrowseDialog): a deref here would crash
  // Nickel the moment a package detail is opened.
  if (QWidget *parent = parentWidget()) {
    setFixedSize(parent->size());
  }

  // Title-bar buttons. The back arrow (top-left) closes only this detail view,
  // returning to the browse list; the close X (top-right) dismisses the whole
  // package manager — the base Dialog already deletes this view on closeTapped,
  // and closeRequested tells the browse dialog to close underneath us.
  if (N3Dialog__enableBackButton) {
    N3Dialog__enableBackButton(dialog, true);
  }
  QObject::connect(dialog, SIGNAL(backTapped()), dialog, SLOT(deleteLater()));
  QObject::connect(dialog, SIGNAL(closeTapped()), this, SIGNAL(closeRequested()));

  QString installed = pkg.value("installed").toString();
  bool staged = pkg.value("staged").toBool();
  bool updateAvailable = pkg.value("update").toBool();
  bool uninstallable = pkg.value("uninstallable").toBool();
  bool isKpm = id == QStringLiteral("kpm"); // §6: never offer to uninstall kpm itself
  // Self-update enrolment state (kpm-self-enrol-plan §5): is_self marks the kpm
  // row; self_configured says whether its self-update is wired up. Both false for
  // every non-kpm row (server default), so selfUnenrolled gates the advisory line
  // and the Enable button on kpm alone, and only while un-adopted.
  bool selfUnenrolled = pkg.value("is_self").toBool() && !pkg.value("self_configured").toBool();
  // min_kpm gate (§2.1): don't offer an Install/Update kpm itself would refuse.
  bool minKpmOk = pkg.value("min_kpm_ok").toBool(true);
  // APP-TREE.md §11.1: install_method is a closed-set string, never null, and is
  // the ONE new field the feature adds. It changes copy only — which action verb
  // the button carries, how long the wait is described, and whether an uninstall
  // promises a reboot — never which buttons exist. A kpm that predates the field
  // omits it, so this is false and every string below is the pre-0.11.0 one.
  appTree = pkg.value("install_method").toString() == QStringLiteral("app-tree");
  // Settings opens the config editor; shown only when installed and the package
  // declares an editable config (has_config, from the search payload — CONFIG.md §4).
  bool hasConfig = !installed.isEmpty() && pkg.value("has_config").toBool();
  // Conflicts (INTEGRITY.md §4, contract §6.1). `conflicts` is what the effective
  // def DECLARES (disclosure only); `conflicts_active` is the subset that is
  // installed or staged on this device right now, in either direction. Both are
  // server-normalized to [] rather than null, and both are absent (⇒ empty) on a
  // pre-0.10.0 kpm, which degrades this whole surface to today's behaviour.
  QStringList declaredConflicts = jsonStrings(pkg.value("conflicts").toArray());
  QStringList activeConflicts = jsonStrings(pkg.value("conflicts_active").toArray());
  // §4.3's refusal rule, mirrored exactly: install is refused when the target is
  // NOT itself installed or staged and something it conflicts with is. So this is
  // precisely the state in which offering Install would guarantee a hard failure.
  bool blockedFromInstall = !activeConflicts.isEmpty() && installed.isEmpty() && !staged;

  QVBoxLayout *layout = new QVBoxLayout(this);
  // 30px padding, plus the home-screen chrome insets on top/bottom (base Dialog).
  layout->setContentsMargins(30, 30 + chrome.top(), 30, 30 + chrome.bottom());
  layout->setSpacing(8);

  auto addInfo = [&](const QString &labelText, const QString &value) {
    if (!value.isEmpty()) {
      layout->addWidget(new Label(Label::Small, labelText + ": " + value));
    }
  };

  QString description = pkg.value("description").toString();
  if (!description.isEmpty()) {
    QLabel *desc = new QLabel(description);
    // Registry strings are third-party input: never let QLabel's AutoText
    // interpret them as rich text (UI spoofing / local-file <img> loads).
    desc->setTextFormat(Qt::PlainText);
    desc->setWordWrap(true);
    desc->setStyleSheet(Label::Stylesheet);
    desc->setProperty("textSize", Label::Medium);
    layout->addWidget(desc);
  }

  QString latest = pkg.value("latest").toString();
  addInfo("Source", pkg.value("source").toString());
  addInfo("Registry", pkg.value("registry").toString());
  addInfo("Installed", installed);
  addInfo("Latest", latest);
  addInfo("Pinned", pkg.value("pinned").toString());
  // §4.6 disclosure line: mirrors the `conflicts <id>` line printInstallDef adds to
  // `kpm install`'s def review, so a user reading this page sees the declaration
  // even when nothing on the device is currently blocking.
  addInfo("Conflicts", declaredConflicts.join(", "));
  if (!installed.isEmpty() && !uninstallable && !isKpm) {
    addInfo("Uninstall", "not available for this package");
  }

  // §6.3: a rolling def's tag never moves, so `check` reports an update while
  // Latest still reads identical to Installed. Without this line the Update button
  // below looks like a bug. Same fact the CLI renders as
  // "UPDATE AVAILABLE (release rebuilt)" (§3.7).
  if (updateAvailable && pkg.value("rolling").toBool() && (latest.isEmpty() || latest == installed)) {
    Label *rebuilt = new Label(Label::Small, "This package republishes under the same tag — the release was rebuilt.");
    rebuilt->setWordWrap(true);
    layout->addWidget(rebuilt);
  }

  // §A (post-install manifest verification): name the absent manifest members on
  // the detail page too, elided (paths run deep and the list can be long).
  // missing_files is null/absent when the package verified clean, so an empty
  // array skips this. device_fw/tested_fw values are registry/device-sourced;
  // Label and ElidedLabel both render as plain text (no rich-text interpretation).
  QJsonArray missing = pkg.value("missing_files").toArray();
  if (!missing.isEmpty()) {
    QString firstMissing = missing.first().toString();
    QString missingLine = "Missing files: " + firstMissing;
    if (missing.size() > 1) {
      missingLine += QStringLiteral("…"); // more members absent than shown
    }
    layout->addWidget(new ElidedLabel(Label::Small, missingLine));
  }

  // INTEGRITY.md §4 — the conflict surface. Two cases, deliberately worded apart
  // because kpm treats them completely differently:
  //
  //  - NOT installed → `kpm install` is a HARD refusal (§4.3), and `--yes` does not
  //    override it. That is not an oversight: the hook always passes `--yes`
  //    (kpmprocess.cc install()), so a confirm-and-proceed design would be silently
  //    bypassed by the only surface most users have, which is exactly why the Go
  //    side made the refusal non-overridable. The Install button is therefore
  //    suppressed below — offering it would guarantee an error dialog — and
  //    replaced with a route to the blocker, so this page is not a dead end.
  //    Uninstalling the blocker clears the block with no state fixup at all: the
  //    check is a pure function of live state (§4.4).
  //  - ALREADY installed → the pair is REPORTED, never blocked (§4.5 / §1.2 item 3):
  //    the collision has already happened, so refusing to update either one would
  //    strand both and undo nothing. Every action stays available.
  //
  // The ids are registry-sourced third-party strings; Label forces Qt::PlainText.
  if (!activeConflicts.isEmpty()) {
    bool one = activeConflicts.size() == 1;
    // Instructions name packages the way the rest of the UI does (NickelClock), with
    // a raw-id fallback for a blocker that is not in the search list at all. The
    // `Conflicts:` disclosure line above deliberately keeps raw ids: it mirrors
    // printInstallDef's def review, which prints ids. Declarations show ids;
    // instructions show names.
    //
    // Split by how each blocker is actually present, because conflicts_active
    // covers both installed AND staged (§4.3) and the two need different sentences:
    // a staged blocker is not installed yet, so "uninstall it first" is an
    // instruction the user cannot carry out until the next reboot.
    QStringList shown, installedShown, stagedShown;
    for (const QString &blocker : activeConflicts) {
      QString name = displayName(blocker);
      shown.append(name);
      if (blockerStagedOnly(blocker)) {
        stagedShown.append(name);
      } else {
        installedShown.append(name);
      }
    }
    QString blockerNames = shown.join(", ");
    bool allStaged = installedShown.isEmpty();
    // Keyed on blockedFromInstall, not on `installed`, so the wording and the button
    // suppression below can never disagree: a package that is STAGED but not yet
    // installed already has files landing at the next reboot, so §4.3's refusal does
    // not apply to it and it is a reported pair, not a blocked install.
    QString headline = "Conflicts with an installed package";
    if (blockedFromInstall) {
      headline = "Can't be installed yet";
    } else if (allStaged) {
      headline = "Conflicts with a staged package";
    }
    Label *conflictWarn = new Label(Label::Small, headline);
    conflictWarn->setStyleSheet(conflictWarn->styleSheet() + "\nLabel { font-weight: bold; }");
    layout->addWidget(conflictWarn);
    // For an all-installed blocker set this is the same sentence shape kpm's own
    // refusal uses (§4.6), so the on-screen copy and the error text in kpm.log read
    // as the same instruction. The staged variants have no CLI counterpart: kpm
    // prints one refusal sentence for both states.
    QString detail;
    if (blockedFromInstall) {
      if (stagedShown.isEmpty()) {
        detail = one ? "Conflicts with installed package " + blockerNames + ". Uninstall it first, then install this."
                     : "Conflicts with installed packages " + blockerNames +
                           ". Uninstall them first, then install this.";
      } else if (allStaged) {
        detail = one ? "Conflicts with " + blockerNames +
                           ", which is staged to install at the next reboot. Reboot, then uninstall it to install this."
                     : "Conflicts with " + blockerNames +
                           ", which are staged to install at the next reboot. Reboot, then uninstall them to install "
                           "this.";
      } else {
        detail = "Conflicts with " + installedShown.join(", ") + " (installed) and " + stagedShown.join(", ") +
                 " (staged for the next reboot). Uninstall them first — the staged ones after rebooting — then "
                 "install this.";
      }
    } else {
      if (stagedShown.isEmpty()) {
        detail = one ? blockerNames + " is also installed. These cannot both be installed — uninstall one."
                     : blockerNames + " are also installed. These cannot all be installed — uninstall all but one.";
      } else if (allStaged) {
        detail = one ? blockerNames + " is staged to install at the next reboot. These cannot both be installed — "
                                      "uninstall one after rebooting."
                     : blockerNames + " are staged to install at the next reboot. These cannot all be installed — "
                                      "uninstall all but one after rebooting.";
      } else {
        detail = installedShown.join(", ") + " (installed) and " + stagedShown.join(", ") +
                 " (staged for the next reboot) are also present. These cannot all be installed — uninstall all but "
                 "one.";
      }
    }
    Label *conflictDetail = new Label(Label::Small, detail);
    conflictDetail->setWordWrap(true); // never elide the one instruction that unblocks the user
    layout->addWidget(conflictDetail);
  }

  // §D (firmware-compat): fw_untested is server-computed and true only when both
  // the def's tested_fw and the device firmware are known and the device is newer
  // by major.minor. The copy says "untested", never "broken" — advisory only.
  if (pkg.value("fw_untested").toBool()) {
    Label *fwWarn = new Label(Label::Small, "Untested on your firmware");
    fwWarn->setStyleSheet(fwWarn->styleSheet() + "\nLabel { font-weight: bold; }");
    layout->addWidget(fwWarn);
    QString testedFw = pkg.value("tested_fw").toString();
    QString deviceFw = pkg.value("device_fw").toString(); // folded in by BrowseDialog::onSearch
    QString fwDetail;
    if (!testedFw.isEmpty() && !deviceFw.isEmpty()) {
      fwDetail = "Last confirmed on " + testedFw + " · you run " + deviceFw;
    } else if (!deviceFw.isEmpty()) {
      fwDetail = "Your device runs " + deviceFw;
    }
    if (!fwDetail.isEmpty()) {
      layout->addWidget(new Label(Label::Small, fwDetail));
    }
  }

  // Self-update enrolment advisory (kpm-self-enrol-plan §5): tell the user that
  // kpm's self-update is not set up and point at the Enable button below. Mirrors
  // the fw_untested block's bold advisory line; a static literal (plain text via
  // Label). Gated on the server-computed is_self && !self_configured.
  if (selfUnenrolled) {
    Label *selfWarn = new Label(Label::Small, "Self-update is not set up. Enable it to get kpm updates here.");
    selfWarn->setStyleSheet(selfWarn->styleSheet() + "\nLabel { font-weight: bold; }");
    layout->addWidget(selfWarn);
  }

  layout->addSpacing(16);

  statusLabel = new Label(Label::Small, "");
  layout->addWidget(statusLabel);

  // Action buttons sit in a right-aligned footer pinned to the bottom of the
  // view (added after the stretch below). primaryButton is Nickel's dark-filled
  // style: light text on a dark button.
  actionsLayout = new QHBoxLayout();
  actionsLayout->setSpacing(16);
  actionsLayout->addStretch(1);

  if (staged) {
    layout->addWidget(new Label(Label::Medium, "Changes staged — reboot to apply."));
    // The reboot IS the action here, so offer it: this branch used to render a
    // label and nothing else, which is a dead end on any page the user was routed
    // to rather than one they chose (a conflict advisory can point at a STAGED
    // blocker — §4.3 counts staged as active). Same prompt as the browse list's
    // staged banner.
    addActionButton("Reboot now", SLOT(promptReboot()));
  } else if (!minKpmOk) {
    layout->addWidget(
        new Label(Label::Medium, "Requires kpm >= " + pkg.value("min_kpm").toString() + " — update kpm first."));
    if (!installed.isEmpty() && uninstallable && !isKpm) {
      addActionButton("Uninstall", SLOT(uninstall()));
    }
  } else if (blockedFromInstall) {
    // Install is deliberately absent (see the conflict advisory above — kpm would
    // hard-refuse it). Offer the way out instead: open the blocking package, where
    // the ordinary Uninstall button and its ordinary confirmation live.
    for (int i = 0; i < activeConflicts.size() && i < kMaxBlockerButtons; i++) {
      addBlockerButton(activeConflicts.at(i));
    }
    // ...and find out whether there is a second way out. This page is reachable in
    // a state where the blocker is not the user's problem at all: the hook's
    // Install is two commands (`install` writes the def, `update` fetches), so a
    // failed fetch leaves the package REGISTERED BUT NOT INSTALLED (§4.3). Such an
    // orphan is now skipped by every `kpm update --all` for good, and until it is
    // unregistered the only remedy on offer is uninstalling a package the user
    // wants. `kpm list --json` is the one command that reports registration —
    // search's rows are the union of the registry and packages.d, so it cannot —
    // and it is a read-only offline query, so it costs nothing to ask here, on a
    // page that is otherwise a dead end. A failure just means no extra button.
    if (KpmProcess *probe = KpmProcess::list()) {
      QObject::connect(probe, &KpmProcess::response, this, &DetailDialog::onRegistered);
    }
  } else if (installed.isEmpty()) {
    addActionButton(actionLabel("Install"), SLOT(install()));
  } else {
    if (updateAvailable) {
      addActionButton(actionLabel("Update"), SLOT(update()));
    }
    // Enable self-update (kpm-self-enrol-plan §5): for an un-adopted kpm this is
    // the only button on the page — Uninstall is suppressed for self and, with no
    // configured source, check never reports an update so updateAvailable is false.
    if (selfUnenrolled) {
      addActionButton("Enable self-update", SLOT(enableSelfUpdate()));
    }
    if (uninstallable && !isKpm) {
      addActionButton("Uninstall", SLOT(uninstall()));
    }
  }

  // Settings is independent of the install/update branch above: editing an
  // already-installed mod's config needs no min_kpm gate and no update state, so
  // it is offered whenever the package is installed and declares a config (§4.1).
  if (hasConfig) {
    addActionButton("Settings", SLOT(openConfig()));
  }

  layout->addStretch(1);
  layout->addLayout(actionsLayout);
}

// addActionButton appends one right-aligned footer button. A method rather than a
// constructor lambda because the orphaned-def probe (INTEGRITY.md §4.3) answers
// asynchronously and adds its button after the page is built.
void DetailDialog::addActionButton(const QString &text, const char *slot) {
  N3ButtonLabel *button = construct_N3ButtonLabel(this);
  button->setProperty("primaryButton", true);
  button->setText(text);
  button->setEnabled(!busyNow); // a button added mid-action joins the busy state
  actionsLayout->addWidget(button);
  QObject::connect(button, SIGNAL(tapped(bool)), this, slot);
  buttons.append(button);
}

// actionLabel appends "(no reboot)" to an Install/Update button for an app-tree
// package (APP-TREE.md §11.3 item 2). This is the single visible payoff of the new
// field, and it is on the button on purpose: every other kpm install ends in a
// reboot prompt, so the expectation to set is the one the user has before the tap,
// not an advisory line they read after it. Only Install and Update take it —
// Uninstall says its piece in its confirmation, and the reboot/blocked/min_kpm
// branches never offer either verb.
QString DetailDialog::actionLabel(const QString &verb) const {
  return appTree ? verb + QStringLiteral(" (no reboot)") : verb;
}

// installBusyLabel names what an Install/Update is actually doing while the button
// is disabled (APP-TREE.md §11.3 item 3). An app-tree install writes the whole tree
// itself — 84 MiB for KOReader — where a tgz install downloads a few MB and hands
// them to the firmware, so the wait is appreciably longer and the generic "Working…"
// undersells it. Deliberately NOT used by uninstall/enrol/drop: "Installing…" during
// a removal would be worse than saying nothing specific. Returns EMPTY for a tgz
// package — setBusy resolves that to "Working…", so the default lives in one place.
QString DetailDialog::installBusyLabel() const {
  return appTree ? QStringLiteral("Installing…") : QString();
}

// addBlockerButton adds a button routing to ANOTHER package's detail page
// (INTEGRITY.md §4). The Nickel tapped(bool) signal is only reachable through the
// string-based connect, which cannot bind a lambda, so the target id rides on the
// button as a dynamic property and openBlocker() reads it back off sender() — one
// slot, N buttons, no per-button state to keep in sync.
void DetailDialog::addBlockerButton(const QString &blocker) {
  N3ButtonLabel *button = construct_N3ButtonLabel(this);
  button->setProperty("primaryButton", true);
  button->setText("Manage " + displayName(blocker));
  // The PROPERTY stays the raw id — that is what the route resolves against. Only
  // the label is humanised.
  button->setProperty("blockerId", blocker);
  actionsLayout->addWidget(button);
  QObject::connect(button, SIGNAL(tapped(bool)), this, SLOT(openBlocker()));
  buttons.append(button);
}

// setBusy disables the footer and shows what the action is doing. An empty
// busyLabel means the generic "Working…", which is every action except an app-tree
// Install/Update (see installBusyLabel).
void DetailDialog::setBusy(bool busy, const QString &busyLabel) {
  busyNow = busy;
  for (QWidget *b : buttons) {
    b->setEnabled(!busy);
  }
  if (statusLabel) {
    statusLabel->setText(busy ? (busyLabel.isEmpty() ? QStringLiteral("Working…") : busyLabel) : QString());
  }
}

void DetailDialog::run(KpmProcess *proc, const QString &busyLabel) {
  if (!proc) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  setBusy(true, busyLabel);
  QObject::connect(proc, &KpmProcess::response, this, &DetailDialog::onResponse);
  QObject::connect(proc, &KpmProcess::failure, this, &DetailDialog::onFailure);
}

// install chains two kpm commands (§6, deep-review fix): `kpm install` only
// registers the package def — the software itself is fetched and staged by
// `kpm update`. One Install tap must do both, or the row stays "not installed"
// with nothing on the device.
void DetailDialog::install() {
  KpmProcess *proc = KpmProcess::install(id);
  if (!proc) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  setBusy(true, installBusyLabel());
  QObject::connect(proc, &KpmProcess::response, this, &DetailDialog::onInstallRegistered);
  QObject::connect(proc, &KpmProcess::failure, this, &DetailDialog::onFailure);
}

void DetailDialog::onInstallRegistered(int exitCode, QJsonObject payload) {
  (void)payload; // install's payload carries no staging info (it registers a def)
  if (exitCode != 0) {
    setBusy(false);
    ConfirmationDialogFactory__showErrorDialog("kpm", "Registering the package failed. See the kpm log.");
    changed();
    return;
  }
  // Defer one event-loop turn: response() is emitted before the install
  // process clears the mutating guard, so starting update synchronously here
  // would bounce off it.
  QTimer::singleShot(0, this, [this] { run(KpmProcess::update(id), installBusyLabel()); });
}

void DetailDialog::update() { run(KpmProcess::update(id), installBusyLabel()); }

// enableSelfUpdate enrols kpm's own self-update (kpm-self-enrol-plan §5). It does
// NOT reuse the shared run()/onResponse: that path closes the view and reboots on
// a staged change, but adopt-self stages nothing — it only records kpm's adoption
// identity in state.json. Success is routed to onSelfEnrolled (a post-enrol check
// prompt); a hard failure rides KpmProcess's error-dialog + onFailure path.
void DetailDialog::enableSelfUpdate() {
  KpmProcess *proc = KpmProcess::adoptSelf();
  if (!proc) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  setBusy(true);
  QObject::connect(proc, &KpmProcess::response, this, &DetailDialog::onSelfEnrolled);
  QObject::connect(proc, &KpmProcess::failure, this, &DetailDialog::onFailure);
}

// onSelfEnrolled handles a completed adopt-self. A hard error (exit 1, no payload)
// never reaches here — it goes through onFailure — so a non-zero code here is the
// defensive partial path only. On success, enrol wired up the source but ran no
// version check, so offer one now (a check needs the network, hence opt-in),
// copying the promptReboot ConfirmationDialog shape. "Check now" reloads browse
// WITH a check (selfEnrolled → loadSearch(true)) so the kpm row repaints with its
// real update badge; "Not now" reloads without a check (changed()) so the row just
// loses its "self-update off" badge. Either way the detail view then closes.
void DetailDialog::onSelfEnrolled(int exitCode, QJsonObject payload) {
  (void)payload; // adopt-self stages nothing; its payload carries no reboot state
  setBusy(false);
  if (exitCode != 0) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "Enabling self-update failed. See the kpm log.");
    return;
  }
  ConfirmationDialog *d = ConfirmationDialogFactory__getConfirmationDialog(nullptr);
  ConfirmationDialog__setTitle(d, "kpm");
  ConfirmationDialog__setText(d, "Self-update enabled. Check for an update now?");
  ConfirmationDialog__setAcceptButtonText(d, "Check now");
  ConfirmationDialog__setRejectButtonText(d, "Not now");
  QObject::connect(d, &QDialog::accepted, this, [this] {
    selfEnrolled();        // browse: loadSearch(true) — search + check, repaint the badge
    dialog->deleteLater(); // close this detail view
  });
  QObject::connect(d, &QDialog::rejected, this, [this] {
    changed();             // browse reloads without a check — the row drops "self-update off"
    dialog->deleteLater();
  });
  QObject::connect(d, &QDialog::accepted, d, &QDialog::deleteLater);
  QObject::connect(d, &QDialog::rejected, d, &QDialog::deleteLater);
  d->open();
}

// openBlocker routes to the package that is blocking this one, so a conflict is a
// signpost rather than a dead end (INTEGRITY.md §4.3/§4.4): the user lands on the
// blocker's own detail page, where the ordinary Uninstall button — with its
// ordinary confirmation — clears the block. No state fixup follows, because the
// refusal is a pure function of live state (§4.4).
//
// BrowseDialog owns both the lookup and the CLOSE. It reports a miss rather than
// no-oping (openConflictPackage) — a package can block while being absent from the
// browse list — and on a hit it calls closeView() here, because once the blocker is
// uninstalled everything on this page is stale, and a stale conflict advisory is
// exactly the dead end the routing exists to prevent. On a miss the page stays: it
// is the only thing on screen that explains the conflict, and losing it to an error
// dialog leaves the user with strictly less than they started with.
//
// setBusy comes first, like every other action in this class. Without it two taps
// stack two blocker views, and re-tapping a button that looks unresponsive is
// normal e-ink behaviour: the screen has not refreshed yet.
void DetailDialog::openBlocker() {
  QObject *button = sender();
  if (button == nullptr) {
    return;
  }
  QString blocker = button->property("blockerId").toString();
  if (blocker.isEmpty()) {
    return;
  }
  setBusy(true);
  openPackage(blocker);
}

void DetailDialog::closeView() { dialog->deleteLater(); }

void DetailDialog::routeFailed() { setBusy(false); }

// onRegistered completes the orphaned-def probe started in the constructor: when
// `kpm list --json` reports this package as registered while the page itself shows
// it as not installed, the def is an orphan (INTEGRITY.md §4.3) and the user gets a
// way to drop it. When it is absent — the ordinary blocked-install case, where the
// package was never registered at all — nothing is added and the page is unchanged.
void DetailDialog::onRegistered(int exitCode, QJsonObject payload) {
  if (exitCode != 0) {
    return;
  }
  for (const QJsonValue &v : payload.value("packages").toArray()) {
    if (v.toObject().value("id").toString() == id) {
      addActionButton("Drop registration", SLOT(dropRegistration()));
      return;
    }
  }
}

// dropRegistration unregisters an orphaned def: `kpm remove` deletes
// packages.d/<id>.toml and touches nothing else (cmd_manage.go), so there is
// nothing to undo and no files are at risk — but it is still the only destructive-
// sounding action on the page, so it confirms like Uninstall does. Dropping the
// registration is what stops `kpm update --all` reporting the package as skipped
// forever: the run iterates packages.d, not the registry.
void DetailDialog::dropRegistration() {
  QString name = pkg.value("name").toString();
  if (name.isEmpty()) {
    name = id;
  }
  ConfirmationDialog *d = ConfirmationDialogFactory__getConfirmationDialog(nullptr);
  ConfirmationDialog__setTitle(d, "kpm");
  ConfirmationDialog__setText(d, name + " is registered with kpm but is not installed. Drop the registration? No "
                                        "files are removed, and you can install it again later.");
  ConfirmationDialog__setAcceptButtonText(d, "Drop");
  ConfirmationDialog__setRejectButtonText(d, "Cancel");
  QObject::connect(d, &QDialog::accepted, this, [this] { run(KpmProcess::remove(id)); });
  QObject::connect(d, &QDialog::accepted, d, &QDialog::deleteLater);
  QObject::connect(d, &QDialog::rejected, d, &QDialog::deleteLater);
  d->open();
}

// openConfig pushes the ConfigDialog over this detail view (CONFIG.md §4). Its
// back arrow returns here; its close X chains up through closeRequested so the
// whole package manager dismisses (browse closes underneath — see BrowseDialog).
void DetailDialog::openConfig() {
  ConfigDialog *c = ConfigDialog::show(id, pkg.value("name").toString().isEmpty() ? id : pkg.value("name").toString());
  QObject::connect(c, &ConfigDialog::closeRequested, this, [this] {
    closeRequested();      // propagate up: browse closes too
    dialog->deleteLater(); // and this detail view
  });
}

// uninstall confirms first, and the confirmation has to be honest about WHEN the
// files go (APP-TREE.md §6.5 / §11.3 item 1). An app-tree package is deleted from
// .adds/<name>/ by kpm itself while the command runs, so "removed on reboot" is
// simply false for it — and telling a user to reboot for something already done is
// how a reboot prompt stops meaning anything. The tgz wording is unchanged.
void DetailDialog::uninstall() {
  QString name = pkg.value("name").toString();
  if (name.isEmpty()) {
    name = id;
  }
  QString removal = appTree ? QStringLiteral("Its files will be removed now.")
                            : QStringLiteral("Its files will be removed on reboot.");
  ConfirmationDialog *d = ConfirmationDialogFactory__getConfirmationDialog(nullptr);
  ConfirmationDialog__setTitle(d, "kpm");
  ConfirmationDialog__setText(d, "Uninstall " + name + "? " + removal);
  ConfirmationDialog__setAcceptButtonText(d, "Uninstall");
  ConfirmationDialog__setRejectButtonText(d, "Cancel");
  QObject::connect(d, &QDialog::accepted, this, [this] { run(KpmProcess::uninstall(id)); });
  QObject::connect(d, &QDialog::accepted, d, &QDialog::deleteLater);
  QObject::connect(d, &QDialog::rejected, d, &QDialog::deleteLater);
  d->open();
}

void DetailDialog::onResponse(int exitCode, QJsonObject payload) {
  setBusy(false);

  if (exitCode == 2) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "Some changes failed. See the kpm log for details.");
  }

  bool reboot = payload.value("reboot_required").toBool() || payload.value("staged").toBool();

  changed(); // browse list reloads underneath

  if (reboot) {
    promptReboot();
  }
  dialog->deleteLater(); // close the detail view; browse has refreshed
}

void DetailDialog::onFailure(QString reason) {
  (void)reason; // KpmProcess already showed the error dialog (§4)
  setBusy(false);
}

void DetailDialog::promptReboot() {
  ConfirmationDialog *d = ConfirmationDialogFactory__getConfirmationDialog(nullptr);
  ConfirmationDialog__setTitle(d, "kpm");
  ConfirmationDialog__setText(d, "Changes staged. Reboot now?");
  ConfirmationDialog__setAcceptButtonText(d, "Reboot now");
  ConfirmationDialog__setRejectButtonText(d, "Later");
  QObject::connect(d, &QDialog::accepted, [] { rebootDevice(); });
  QObject::connect(d, &QDialog::accepted, d, &QDialog::deleteLater);
  QObject::connect(d, &QDialog::rejected, d, &QDialog::deleteLater);
  d->open();
}

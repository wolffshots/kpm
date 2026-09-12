// NICKEL-UI.md §5 — BrowseDialog. Layout/keyboard/pagination patterns adapted
// from NickelHardcover's searchdialog.cc (MIT) per §0; the data comes from
// `kpm search --json` (JSON-OUTPUT.md §2.1), filtered client-side.

#include <QDateTime>
#include <QFile>
#include <QHBoxLayout>
#include <QHash>
#include <QPair>
#include <QPointer>
#include <QStyle>
#include <QVBoxLayout>

#include <NickelHook.h>

#include "browsedialog.h"
#include "detaildialog.h"
#include "files.h"
#include "kpmprocess.h"
#include "nkpm.h"
#include "widgets/label.h"
#include "widgets/packagerow.h"

// Cap on the skip lines drawn on page 1 (INTEGRITY.md §6.4). A skip is per-package
// and permanent, so a run can in principle skip every package it was asked about;
// three lines name the common cases (one conflicting pair blocks at most two) and
// the rest are counted, keeping the banner from pushing the list off the page.
static const int kMaxSkipLines = 3;

void BrowseDialog::show() {
  // Relaunch = close-and-reopen. A prior browser can be left buried but alive by
  // a view change we don't intercept (e.g. a USB connect/eject) — NickelHardcover
  // auto-closes on currentViewChanged, which we don't hook — so tapping the menu
  // item again must dismiss the stale one and open a fresh, foreground browser
  // rather than no-op behind it. The QPointer auto-nulls once destroyed.
  static QPointer<BrowseDialog> open;
  if (open) {
    open->dialog->deleteLater();
    open = nullptr;
  }
  open = new BrowseDialog();
}

BrowseDialog::BrowseDialog() : Dialog("Package manager") {
  // getDialog is expected to reparent this under an N3Dialog synchronously, but
  // don't assume it: a null parent here would crash Nickel on open.
  if (QWidget *parent = parentWidget()) {
    setFixedSize(parent->size());
  }

  QVBoxLayout *layout = new QVBoxLayout(this);
  // chrome is zero when the base Dialog hid the status/nav bars; on firmware
  // without those symbols it holds fallback insets past the persistent chrome.
  layout->setContentsMargins(0, chrome.top(), 0, chrome.bottom());
  layout->setSpacing(0);

  // Tab bar (fonts plan §3.8): one N3ButtonLabel per `kind` the list can hold,
  // above the search box. A switch runs no kpm command — it re-filters the payload
  // already in hand — so Refresh/Sync/Update-all, the staged banner, the stale hint
  // and the skip notes all stay global and show on page 1 of every tab (D7).
  QHBoxLayout *tabBar = new QHBoxLayout();
  tabBar->setContentsMargins(14, 6, 14, 6);
  tabBar->setSpacing(16);
  modsTab = construct_N3ButtonLabel(this);
  modsTab->setText("Mods");
  tabBar->addWidget(modsTab);
  fontsTab = construct_N3ButtonLabel(this);
  fontsTab->setText("Fonts");
  tabBar->addWidget(fontsTab);
  dictsTab = construct_N3ButtonLabel(this);
  dictsTab->setText("Dictionaries");
  tabBar->addWidget(dictsTab);
  // Left-aligned: the trailing stretch keeps the three tabs together at the
  // leading edge, where the footer's does the opposite for the actions.
  tabBar->addStretch(1);
  layout->addLayout(tabBar);
  QObject::connect(modsTab, SIGNAL(tapped(bool)), this, SLOT(showModsTab()));
  QObject::connect(fontsTab, SIGNAL(tapped(bool)), this, SLOT(showFontsTab()));
  QObject::connect(dictsTab, SIGNAL(tapped(bool)), this, SLOT(showDictsTab()));
  styleTabs(); // Mods is the tab the view opens on

  lineEdit = construct_TouchLineEdit(nullptr);
  layout->addWidget(lineEdit);

  pages = new PagedStack(this);
  layout->addWidget(pages, 1);
  QObject::connect(pages, &PagedStack::requestPage, this, &BrowseDialog::requestPage);
  QObject::connect(pages, &PagedStack::afterLayout, this, &BrowseDialog::onFirstLayout);

  QHBoxLayout *footer = new QHBoxLayout();
  footer->setContentsMargins(14, 6, 14, 6);
  footer->setSpacing(16); // gap between the action buttons
  // Right-align: the leading stretch pushes the buttons to the trailing edge.
  footer->addStretch(1);
  // primaryButton: Nickel's dark-filled button style (light text on dark), the
  // same property NickelHardcover's journal buttons use.
  refreshButton = construct_N3ButtonLabel(this);
  refreshButton->setProperty("primaryButton", true);
  refreshButton->setText("Refresh");
  footer->addWidget(refreshButton);
  // Sync: re-copy registry defs into packages.d so an existing install picks up
  // new [[configs]]/uninstall declarations without SSH (JSON-OUTPUT.md §2.3).
  syncButton = construct_N3ButtonLabel(this);
  syncButton->setProperty("primaryButton", true);
  syncButton->setText("Sync");
  footer->addWidget(syncButton);
  updateAllButton = construct_N3ButtonLabel(this);
  updateAllButton->setProperty("primaryButton", true);
  updateAllButton->setText("Update all");
  footer->addWidget(updateAllButton);
  layout->addLayout(footer);
  QObject::connect(refreshButton, SIGNAL(tapped(bool)), this, SLOT(refresh()));
  QObject::connect(syncButton, SIGNAL(tapped(bool)), this, SLOT(sync()));
  QObject::connect(updateAllButton, SIGNAL(tapped(bool)), this, SLOT(updateAll()));

  buildKeyboardFrame(lineEdit, "Search");

  // Never crash on a missing binary: show a setup message, disable everything (§5).
  if (!QFile::exists(Files::kpm)) {
    kpmMissing = true;
    pages->setStatusText("kpm not found — install kpm first");
    setActionsEnabled(false);
    return;
  }

  loadSearch(false);
}

void BrowseDialog::onFirstLayout() {
  if (laidOut) {
    return;
  }
  laidOut = true;
  QObject::disconnect(pages, &PagedStack::afterLayout, this, &BrowseDialog::onFirstLayout);
  if (dataReady) {
    render(false); // first paint: nothing on screen to repaint in place
  }
}

void BrowseDialog::loadSearch(bool thenCheck) {
  pendingCheck = thenCheck;
  KpmProcess *p = KpmProcess::search(); // read-only, no network, no lock (§2.1)
  QObject::connect(p, &KpmProcess::response, this, &BrowseDialog::onSearch);
  QObject::connect(p, &KpmProcess::failure, this, &BrowseDialog::onProcessFailure);
}

void BrowseDialog::onSearch(int exitCode, QJsonObject payload) {
  (void)exitCode;
  allPackages = payload.value("packages").toArray();
  // device_fw is a top-level search field (D), but PackageRow and DetailDialog
  // only ever receive a single package object. Fold the device firmware into each
  // one — mirroring how mergeCheck folds in per-package latest/update — so the
  // detail page's firmware-compat line can pair it with the package's tested_fw.
  QJsonValue deviceFw = payload.value("device_fw");
  if (!allPackages.isEmpty()) {
    QJsonArray withFw;
    for (const QJsonValue &v : allPackages) {
      QJsonObject o = v.toObject();
      o.insert("device_fw", deviceFw);
      withFw.append(o);
    }
    allPackages = withFw;
  }
  stagedSummary = payload.value("staged").toObject();
  registries = payload.value("registries").toArray();
  dataReady = true;

  bool checkStarted = false;
  if (pendingCheck) {
    pendingCheck = false;
    // check rides the mutating guard (it takes kpm's lock), so it can be
    // refused if another mutation is somehow in flight — degrade to no badges.
    KpmProcess *p = KpmProcess::check(); // network: latest/update badges (§5)
    if (p) {
      checkStarted = true;
      QObject::connect(p, &KpmProcess::response, this, &BrowseDialog::onCheckDone);
      QObject::connect(p, &KpmProcess::failure, this, &BrowseDialog::onProcessFailure);
    } else {
      setActionsEnabled(true);
    }
  } else {
    setActionsEnabled(true);
  }

  // Win 3 (kpm-flash-reduction-plan): when a check actually launched, onCheckDone
  // renders the merged latest/update result — skip this intermediate no-badge
  // render so a Refresh repaints once, not twice. The degraded path (check refused,
  // p == nullptr) and the plain no-check path still render here.
  if (laidOut && !checkStarted) {
    render(true); // win 4: fresh search data — repaint only the rows that changed
  }
}

void BrowseDialog::onRefreshDone(int exitCode, QJsonObject payload) {
  (void)exitCode;
  (void)payload;
  // Registry cache refreshed; reload the list, then run check to merge latest.
  loadSearch(true);
}

void BrowseDialog::onCheckDone(int exitCode, QJsonObject payload) {
  (void)exitCode;
  mergeCheck(payload.value("packages").toArray());
  setActionsEnabled(true);
  if (laidOut) {
    render(true); // win 4: the check merge usually moves badges and nothing else
  }
}

void BrowseDialog::mergeCheck(const QJsonArray &checkPackages) {
  QHash<QString, QJsonObject> byId;
  for (const QJsonValue &v : checkPackages) {
    QJsonObject o = v.toObject();
    byId.insert(o.value("id").toString(), o);
  }
  QJsonArray merged;
  for (const QJsonValue &v : allPackages) {
    QJsonObject o = v.toObject();
    auto it = byId.constFind(o.value("id").toString());
    if (it != byId.constEnd()) {
      o.insert("latest", it.value().value("latest"));
      o.insert("update", it.value().value("update"));
    }
    merged.append(o);
  }
  allPackages = merged;
}

void BrowseDialog::onActionDone(int exitCode, QJsonObject payload) {
  setActionsEnabled(true);
  if (exitCode == 2) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "Some changes failed. See the kpm log for details.");
  }
  // Skips ride the payload, never the exit code (INTEGRITY.md §4.3/§6.4): a
  // conflict refusal under `update --all` is permanent, so counting it as a failure
  // would poison "Update all" for good. That is exactly why it must not be silent
  // either — the command succeeds and a package was left behind. Recorded before
  // loadSearch, so the render it triggers already draws them.
  skipNotes = skipNotices(payload);
  bool reboot = payload.value("reboot_required").toBool() || payload.value("staged").toBool();
  // Worded BEFORE loadSearch: rebootPromptText partitions this run's `changed[]`
  // against the rows currently on screen, and loadSearch replaces those rows with a
  // fresh search. The reload is asynchronous so reading afterwards would happen to
  // work today, but the dependency would be invisible and one event-loop turn from
  // being wrong.
  QString rebootText = reboot ? rebootPromptText(payload) : QString();
  loadSearch(false);
  if (reboot) {
    showRebootPrompt(rebootText);
  }
}

// rebootPromptText decides what the reboot prompt says about the run that just
// finished. Since 0.11.0 a single "Update all" can do two different things
// (APP-TREE.md §6.4): app-tree packages are extracted into .adds/<name>/ and are
// live the moment the command returns, while tgz packages are merged into one
// KoboRoot.tgz the firmware unpacks at the next boot. The run's `staged` and
// `reboot_required` describe the RUN, so both are true whenever any tgz was staged
// — correct, and exactly why the prompt must not keep saying only "Changes staged":
// on a mixed run that reads as if nothing had landed, and a user who taps Later is
// left believing KOReader is still pending when it is already installed and
// launchable.
//
// The split is derived, not asked for: `changed[]` crossed with each row's
// install_method, both of which the hook already holds. §11.2 is explicit that
// jsonMutation gains nothing, and this needs nothing. A kpm that predates the field
// omits install_method entirely, so every id reads as tgz, `live` stays 0, and the
// sentence is byte-identical to 0.10.0's.
//
// Only a genuinely mixed run is reworded. An app-tree-only run does not reach here
// (both bools false, no prompt); a tgz-only run, or one where `changed` is empty
// while something else is staged, keeps the original sentence — which is still true.
//
// 0.12.0 adds a third bucket (fonts plan §3.9): a content package installs live and
// is still unusable until the restart, so it counts toward what needs a reboot
// without ever having been staged. A content-only run therefore takes its own
// sentence rather than "Changes staged", which would be false.
QString BrowseDialog::rebootPromptText(const QJsonObject &payload) const {
  QHash<QString, QJsonObject> index = packageIndex();
  int live = 0, staged = 0, restart = 0;
  for (const QJsonValue &v : payload.value("changed").toArray()) {
    QString changedId = v.toString();
    if (changedId.isEmpty()) {
      continue;
    }
    // The same two predicates DetailDialog words its page from (detaildialog.h),
    // asked in the same order: content first, because a font is live too and would
    // otherwise be counted as "installed and ready" when it is not usable yet.
    QJsonObject row = index.value(changedId);
    if (isContentKind(row)) {
      restart++; // installed, used after a reboot
    } else if (isLiveMethod(row)) {
      live++; // installed and ready
    } else {
      staged++; // a tgz, landing at the next boot
    }
  }
  if (live == 0 && staged == 0 && restart > 0) {
    return contentRebootText();
  }
  int needs = staged + restart;
  if (live == 0 || needs == 0) {
    return QStringLiteral("Changes staged. Reboot now?");
  }
  // Two short clauses and the question, mirroring the CLI's own mixed tail
  // ("N package(s) installed, M staged — reboot to install", §6.4 step 3). Counts
  // rather than names: a name list grows with the run and this is an e-ink
  // confirmation box, while the browse list behind it already names every row.
  QString done = live == 1 ? QStringLiteral("1 package installed and ready")
                           : QString("%1 packages installed and ready").arg(live);
  QString pending = needs == 1 ? QStringLiteral("1 more needs a reboot")
                               : QString("%1 more need a reboot").arg(needs);
  return done + ". " + pending + ". Reboot now?";
}

// skipNotices renders a mutation payload's `skipped` array as one line per package
// (INTEGRITY.md §6.4). Absent on every command that cannot skip, and on every
// pre-0.10.0 kpm, so this degrades to an empty list rather than a special case.
QStringList BrowseDialog::skipNotices(const QJsonObject &payload) const {
  QStringList out;
  QHash<QString, QJsonObject> index = packageIndex();
  for (const QJsonValue &v : payload.value("skipped").toArray()) {
    QJsonObject entry = v.toObject();
    QString skippedId = entry.value("id").toString();
    if (skippedId.isEmpty()) {
      continue;
    }
    QString name = index.value(skippedId).value("name").toString();
    if (name.isEmpty()) {
      name = skippedId;
    }
    QString reason = entry.value("reason").toString().trimmed();
    out.append(reason.isEmpty() ? "Skipped " + name : "Skipped " + name + ": " + reason);
  }
  return out;
}

void BrowseDialog::onProcessFailure(QString reason) {
  (void)reason; // KpmProcess already showed the error dialog (§4)
  if (!dataReady) {
    // The initial search failed — leave a status in the empty list, not a
    // blank page (§5).
    pages->setStatusText("Failed to load packages.");
  }
  setActionsEnabled(true);
}

void BrowseDialog::refresh() {
  if (kpmMissing) {
    return;
  }
  KpmProcess *p = KpmProcess::registryRefresh();
  if (!p) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  skipNotes.clear(); // a new gesture: the last run's skips are no longer the news
  setActionsEnabled(false);
  QObject::connect(p, &KpmProcess::response, this, &BrowseDialog::onRefreshDone);
  QObject::connect(p, &KpmProcess::failure, this, &BrowseDialog::onProcessFailure);
}

void BrowseDialog::sync() {
  if (kpmMissing) {
    return;
  }
  KpmProcess *p = KpmProcess::sync();
  if (!p) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  skipNotes.clear();
  setActionsEnabled(false);
  // Reuse onActionDone: sync's payload reports staged=false/reboot_required=false,
  // so it just re-runs search to repaint rows (has_config may have changed) with
  // no reboot prompt — the same chain-back-into-search Update-all uses.
  QObject::connect(p, &KpmProcess::response, this, &BrowseDialog::onActionDone);
  QObject::connect(p, &KpmProcess::failure, this, &BrowseDialog::onProcessFailure);
}

void BrowseDialog::updateAll() {
  if (kpmMissing) {
    return;
  }
  KpmProcess *p = KpmProcess::updateAll();
  if (!p) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  skipNotes.clear();
  setActionsEnabled(false);
  QObject::connect(p, &KpmProcess::response, this, &BrowseDialog::onActionDone);
  QObject::connect(p, &KpmProcess::failure, this, &BrowseDialog::onProcessFailure);
}

// rebootConfirm is the staged banner's button target — connected by NAME through
// the string-based SIGNAL/SLOT macros, so its signature is fixed. The banner only
// appears for an already-staged tgz, which is what makes the plain sentence right
// there; the per-run wording lives in showRebootPrompt.
void BrowseDialog::rebootConfirm() { showRebootPrompt(QStringLiteral("Changes staged. Reboot now?")); }

void BrowseDialog::showRebootPrompt(const QString &text) {
  ConfirmationDialog *d = ConfirmationDialogFactory__getConfirmationDialog(nullptr);
  ConfirmationDialog__setTitle(d, "kpm");
  ConfirmationDialog__setText(d, text);
  ConfirmationDialog__setAcceptButtonText(d, "Reboot now");
  ConfirmationDialog__setRejectButtonText(d, "Later");
  QObject::connect(d, &QDialog::accepted, [] { rebootDevice(); });
  QObject::connect(d, &QDialog::accepted, d, &QDialog::deleteLater);
  QObject::connect(d, &QDialog::rejected, d, &QDialog::deleteLater);
  d->open();
}

void BrowseDialog::openDetail(QString id) {
  for (const QJsonValue &v : allPackages) {
    QJsonObject o = v.toObject();
    if (o.value("id").toString() == id) {
      DetailDialog *d = DetailDialog::show(o, packageIndex());
      QObject::connect(d, &DetailDialog::changed, this, [this] { loadSearch(false); });
      // Post-enrol "Check now" (kpm-self-enrol-plan §5): reload WITH a check so the
      // freshly-adopted kpm row repaints with its real update badge, like onRefreshDone.
      QObject::connect(d, &DetailDialog::selfEnrolled, this, [this] { loadSearch(true); });
      // Conflict route (INTEGRITY.md §4): a blocked package's detail page sends the
      // user to the package blocking it, where Uninstall lives. The source view is
      // passed along because whether it closes depends on whether the route lands —
      // see openConflictPackage.
      QObject::connect(d, &DetailDialog::openPackage, this,
                       [this, d](QString blockerId) { openConflictPackage(d, blockerId); });
      // Detail's close (X) closes the whole package manager: dismiss this browse
      // dialog underneath it (the detail view deletes itself via the base Dialog).
      QObject::connect(d, &DetailDialog::closeRequested, this, [this] { dialog->deleteLater(); });
      return;
    }
  }
}

// packageIndex maps package id -> the whole package object across the search list.
// A detail view holds only its own package object, so this is how it describes a
// package it does not hold: the conflict route's blocker button takes the name from
// here, and the advisory takes the blocker's installed/staged state (INTEGRITY.md
// §4). Ids with no entry simply do not appear; DetailDialog::displayName falls back
// to the raw id and blockerStagedOnly reads an absent blocker as installed.
QHash<QString, QJsonObject> BrowseDialog::packageIndex() const {
  QHash<QString, QJsonObject> index;
  for (const QJsonValue &v : allPackages) {
    QJsonObject o = v.toObject();
    QString packageId = o.value("id").toString();
    if (!packageId.isEmpty()) {
      index.insert(packageId, o);
    }
  }
  return index;
}

// openConflictPackage opens another package's detail view on behalf of a conflict
// advisory (INTEGRITY.md §4). It is openDetail plus an explicit miss branch, and
// the miss is a real case rather than paranoia: `kpm remove <id>` deletes only the
// packages.d def and leaves state, so a removed-but-not-uninstalled package still
// has files on the device and still blocks (§4.3) while no longer appearing in the
// merged registry+local search list. openDetail returns silently on a miss — which
// here would be precisely the dead end this route exists to avoid — so name the
// package and give the one command that clears it (§4.6's remedy).
//
// Closing the source view belongs HERE, not in DetailDialog, because only this
// function knows whether the route landed. Closing it unconditionally meant the
// miss branch showed an error dialog AND took away the page that explained the
// conflict, leaving the user with less than they started with. On a hit the new
// view is pushed first and the stale one closed after, preserving the order the
// device-validated code used.
//
// The interpolated id is registry-sourced but not free text: `kpm search --json`
// only ever emits ids that passed config.ValidID (^[a-z0-9][a-z0-9-]*$ — see
// internal/config), and conflict ids are re-filtered through ValidID by
// normalizeConflicts at every use (cmd/kpm/conflicts.go), so nothing here can carry
// markup or newlines into Nickel's dialog text. Verified rather than assumed,
// because this is the one place the hook puts third-party data into a dialog whose
// text format it does not control.
void BrowseDialog::openConflictPackage(DetailDialog *from, const QString &id) {
  for (const QJsonValue &v : allPackages) {
    if (v.toObject().value("id").toString() == id) {
      openDetail(id);
      if (from != nullptr) {
        from->closeView();
      }
      return;
    }
  }
  if (from != nullptr) {
    from->routeFailed(); // stay put: this page is the only account of the conflict
  }
  ConfirmationDialogFactory__showErrorDialog(
      "kpm", id + " is still on this device but is no longer in the package list. Remove it over SSH with:\nkpm "
                  "uninstall " + id);
}

void BrowseDialog::commit() {
  filter = lineEdit->text().trimmed();
  if (laidOut && dataReady) {
    render(false); // a search commit re-pages from page 1 — see render()
  }
}

// One named slot per tab: the Nickel tapped(bool) signal is only reachable through
// the string-based connect, which cannot bind a lambda (the same constraint
// DetailDialog's blocker buttons work around with a dynamic property — here there
// are exactly three tabs, so three slots is the smaller answer).
void BrowseDialog::showModsTab() { selectTab(QStringLiteral("mod")); }
void BrowseDialog::showFontsTab() { selectTab(QStringLiteral("font")); }
void BrowseDialog::showDictsTab() { selectTab(QStringLiteral("dictionary")); }

// selectTab switches which `kind` the list shows. render(false) is the path a
// search commit already takes (fonts plan D7): it never takes the in-place row
// repaint, it re-records the page-1 banner states, and it rebuilds from page 1 —
// which is every tab-switch hazard handled by one call that already exists.
void BrowseDialog::selectTab(const QString &kind) {
  if (kind == activeKind) {
    return;
  }
  activeKind = kind;
  styleTabs();
  if (laidOut && dataReady) {
    render(false);
  }
}

// styleTabs marks the active tab with primaryButton, Nickel's dark-filled style —
// the same property the footer actions carry. A dynamic property change does not
// restyle a widget by itself, hence the unpolish/polish pair. If that does not take
// on Kobo's Qt 5.2.1 (device test T8), the documented fallback is to disable the
// active tab instead, which greys it through the ordinary disabled style.
void BrowseDialog::styleTabs() {
  const QPair<N3ButtonLabel *, QString> tabs[] = {
      {modsTab, QStringLiteral("mod")}, {fontsTab, QStringLiteral("font")}, {dictsTab, QStringLiteral("dictionary")}};
  for (const QPair<N3ButtonLabel *, QString> &t : tabs) {
    if (t.first == nullptr) {
      continue;
    }
    t.first->setProperty("primaryButton", t.second == activeKind);
    t.first->style()->unpolish(t.first);
    t.first->style()->polish(t.first);
  }
}

QList<QJsonObject> BrowseDialog::filteredPackages() const {
  QList<QJsonObject> out;
  QString f = filter.toLower();
  for (const QJsonValue &v : allPackages) {
    QJsonObject o = v.toObject();
    // Tab filter (§3.8). `kind` is a closed-set string that search --json derives
    // from where the package installs (D1), so it can never disagree with where the
    // files actually go. A kpm that predates the field omits it, and every package
    // without a content destination is a mod anyway — which is also the field's own
    // default — so an absent value reads as "mod" and the Mods tab is unchanged.
    QString kind = o.value("kind").toString();
    if (kind.isEmpty()) {
      kind = QStringLiteral("mod");
    }
    if (kind != activeKind) {
      continue;
    }
    if (f.isEmpty() || o.value("id").toString().toLower().contains(f) ||
        o.value("name").toString().toLower().contains(f) ||
        o.value("description").toString().toLower().contains(f)) {
      out.append(o);
    }
  }
  return out;
}

// emptyStateText words the status line for a tab with nothing in it (§3.8). The
// ORDER is the point:
//
//  1. Dictionaries first, because that note is true whatever the list and the
//     search box hold: nothing emits kind = "dictionary" in 0.12.0 (D8), so the tab
//     is always empty and "No packages match your search" would blame the user's
//     search for a feature that has not shipped. The dictionary release changes
//     this one string and nothing else — the tab already filters on `kind` exactly
//     like the other two.
//  2. Then "no packages at all", which is about the whole list rather than the tab
//     and has its own remedy (Refresh).
//  3. Then an empty Fonts tab with an empty search box: the registries simply carry
//     no fonts yet, which again is not a failed search.
//  4. Otherwise the ordinary no-match line.
QString BrowseDialog::emptyStateText() const {
  if (activeKind == QStringLiteral("dictionary")) {
    return QStringLiteral("Dictionaries are a work in progress. A later kpm release adds them.");
  }
  if (allPackages.isEmpty()) {
    return QStringLiteral("No packages. Tap Refresh for the package list.");
  }
  if (activeKind == QStringLiteral("font") && filter.isEmpty()) {
    return QStringLiteral("No fonts in your registries yet.");
  }
  return QStringLiteral("No packages match your search.");
}

bool BrowseDialog::registriesStale() const {
  if (registries.isEmpty()) {
    return true;
  }
  for (const QJsonValue &v : registries) {
    QJsonValue r = v.toObject().value("refreshed");
    if (!r.isString()) {
      return true; // null/never refreshed
    }
    QDateTime t = QDateTime::fromString(r.toString(), Qt::ISODate);
    if (!t.isValid() || t.msecsTo(QDateTime::currentDateTimeUtc()) > 24LL * 3600 * 1000) {
      return true;
    }
  }
  return false;
}

void BrowseDialog::setActionsEnabled(bool enabled) {
  if (kpmMissing) {
    enabled = false;
    // The tabs are a client-side filter, so they stay live while a command runs —
    // but with no kpm binary there is no list to filter and never will be, and a tap
    // that only moves the active highlight over an unchanging page reads as a broken
    // control. This is the one state that disables them.
    for (N3ButtonLabel *tab : {modsTab, fontsTab, dictsTab}) {
      if (tab != nullptr) {
        tab->setEnabled(false);
      }
    }
  }
  if (refreshButton) {
    refreshButton->setEnabled(enabled);
  }
  if (syncButton) {
    syncButton->setEnabled(enabled);
  }
  if (updateAllButton) {
    updateAllButton->setEnabled(enabled);
  }
}

// repaintRowsInPlace is flash-reduction win 4 (kpm-flash-reduction-plan, deferred
// from 0.9.2): after a refresh, repaint the rows whose badge actually changed
// instead of rebuilding the page. A rebuild swaps the QStackedWidget's current
// widget and repaints the whole list region; setting one label's text repaints one
// label. Returns false when it declines, leaving the caller to rebuild.
//
// The state-correctness argument is the model/view split, NOT the completeness of
// this diff: onSearch has already replaced allPackages wholesale before render()
// runs, so DetailDialog (which reads allPackages) and every later page build see
// fresh data whichever branch is taken. The diff decides only how much of the view
// is redrawn, and it declines the moment anything other than a badge differs — a
// different row count, a different id sequence, or a changed name/description/
// source, which are the only other things PackageRow draws. A miss therefore costs
// a redundant repaint, never a stale one.
//
// Rows on pages the user has paged away from are updated too: findChildren reaches
// every cached page, and setText on a hidden widget costs no paint. Skipping them
// would leave a stale badge behind a page turn — exactly the state-correctness risk
// that got win 4 held back out of 0.9.2.
bool BrowseDialog::repaintRowsInPlace(const QList<QJsonObject> &previous) {
  if (previous.isEmpty() || previous.size() != filtered.size()) {
    return false;
  }
  QHash<QString, QString> newBadges; // id -> the badge every live row must now show
  for (int i = 0; i < filtered.size(); i++) {
    const QJsonObject &was = previous.at(i);
    const QJsonObject &now = filtered.at(i);
    QString id = now.value("id").toString();
    if (id.isEmpty() || was.value("id").toString() != id) {
      return false; // the row set moved under us
    }
    if (was.value("name") != now.value("name") || was.value("description") != now.value("description") ||
        was.value("source") != now.value("source")) {
      return false; // the rest of the row changed; only the badge is repaintable
    }
    // Every id, not just the ones whose badge moved between these two payloads.
    // A differential set is only correct under the induction "every live row
    // already shows the previous payload's badge" — true today, but not checkable
    // from here, and a single missed update leaves a stale badge on screen. Absolute
    // is self-healing and free: PackageRow::setBadgeText no-ops on an unchanged
    // string, so an unchanged row costs one QString compare and no paint.
    newBadges.insert(id, PackageRow::badgeText(now));
  }
  for (PackageRow *row : pages->findChildren<PackageRow *>()) {
    auto it = newBadges.constFind(row->packageId());
    if (it != newBadges.constEnd()) {
      row->setBadgeText(it.value());
    }
  }
  return true;
}

// render repaints the list. allowInPlace marks the callers whose data came from a
// fresh kpm payload (search / check), where win 4's row-level repaint applies. A
// search-box commit passes false on purpose: it is a deliberate user gesture whose
// documented response is to re-page the list from page 1, and the paging reset is
// what --exercise-reload pins.
void BrowseDialog::render(bool allowInPlace) {
  QList<QJsonObject> previous = filtered; // exactly what is on screen right now
  bool hadStagedBanner = renderedStagedBanner;
  bool hadStaleHint = renderedStaleHint;
  bool hadSkipNotice = renderedSkipNotice;

  filtered = filteredPackages();
  // Recorded per render, because onSearch overwrites stagedSummary/registries
  // before render() runs — these are the page-1 banner/hint states actually drawn.
  renderedStagedBanner = stagedSummary.value("count").toInt() > 0;
  renderedStaleHint = registriesStale();
  renderedSkipNotice = !skipNotes.isEmpty();

  // Which page the user ends up on is deliberate, not incidental: an in-place
  // repaint leaves them where they were (nothing moved, so nothing should scroll
  // under them), and a rebuild resets to page 1 (the row set may have changed, and
  // page 1 is the only page carrying the banners). A search commit always takes the
  // rebuild path for that reason — --exercise-reload pins the reset.
  //
  // Win 4: only when the page chrome is unchanged too — the staged banner, the skip
  // notice and the stale hint live inside page 1's widget and can only be added or
  // removed by rebuilding it, so a flip in any of them forces the full path.
  // The gate is `showingContent`, not `hasContent`: PagedStack::next() can switch to
  // the status page without moving its page counter, and returning early there would
  // leave "Loading. Please wait..." on screen permanently. Before win 4 the next
  // render healed that; the in-place branch has to check what is actually shown.
  if (allowInPlace && pages->showingContent() && hadStagedBanner == renderedStagedBanner &&
      hadStaleHint == renderedStaleHint && hadSkipNotice == renderedSkipNotice && repaintRowsInPlace(previous)) {
    return;
  }

  // Win 2 (kpm-flash-reduction-plan): if the list has already been built once this
  // is a data-in-hand re-render (post-action reload, post-check merge, in-search
  // filter, tab switch) — swap straight to the new list, no "Loading..." flip. Only
  // the genuine initial load (nothing rendered yet) takes clear()/Loading.
  //
  // hasContent() reads PagedStack's page counter, which a render building NO page
  // resets to 0 — and the next render would then flip the whole view through
  // "Loading. Please wait..." for a gesture that runs no command. That cannot happen
  // because requestPage builds a real page-1 even for an empty list (see below), so
  // the counter never drops once the list exists; --exercise-tabs fails if an empty
  // tab ever stops building that page.
  if (pages->hasContent()) {
    pages->reload();
  } else {
    pages->clear();
    pages->next();
  }
}

void BrowseDialog::requestPage(int index) {
  if (kpmMissing || !dataReady) {
    return;
  }
  // An empty tab is still page 1, and page 1 carries the global chrome (D7). Build
  // the real page rather than falling back to the bare status page: that fallback
  // took the staged banner and its Reboot now button, the last action's skip notes
  // and the stale hint off screen with the rows. On the Dictionaries tab, which is
  // empty for this whole release, that removed the only reboot affordance in the
  // browse view for as long as the user stayed on it.
  if (filtered.isEmpty()) {
    if (index != 1) {
      return; // an empty list has no page 2
    }
    pages->setTotal(1);
    QWidget *box = new QWidget(pages);
    QVBoxLayout *v = new QVBoxLayout(box);
    v->setContentsMargins(0, 0, 0, 0);
    v->setSpacing(0);
    addPageOneChrome(box, v);
    // Centred between two stretches, where the status page used to put it.
    v->addStretch(1);
    Label *empty = new Label(Label::Small, emptyStateText());
    empty->setAlignment(Qt::AlignCenter);
    empty->setWordWrap(true); // the Dictionaries note is a sentence, never elide it
    empty->setContentsMargins(14, 10, 14, 10);
    v->addWidget(empty);
    v->addStretch(1);
    pages->addPage(box);
    return;
  }

  // Page size from a dummy row's height, like SearchDialog::requestPage.
  PackageRow *dummy = new PackageRow(QJsonObject(), this);
  int rowHeight = dummy->sizeHint().height();
  dummy->deleteLater();
  if (rowHeight <= 0) {
    rowHeight = 1;
  }
  int limit = pages->getAvailableHeight() / rowHeight;
  if (limit < 1) {
    limit = 1;
  }

  int total = (filtered.size() + limit - 1) / limit;
  pages->setTotal(total);

  int start = (index - 1) * limit;
  if (start < 0 || start >= filtered.size()) {
    return;
  }
  int end = qMin(start + limit, filtered.size());

  QWidget *box = new QWidget(pages);
  QVBoxLayout *v = new QVBoxLayout(box);
  v->setContentsMargins(0, 0, 0, 0);
  v->setSpacing(0);

  if (index == 1) {
    addPageOneChrome(box, v);
  }

  for (int i = start; i < end; i++) {
    PackageRow *row = new PackageRow(filtered.at(i));
    QObject::connect(row, &PackageRow::selected, this, &BrowseDialog::openDetail);
    v->addWidget(row);
  }
  v->addStretch(1);
  pages->addPage(box);
}

// addPageOneChrome draws page 1's banners: the staged reboot affordance, the last
// action's skip notes and the stale-registry hint. All three are GLOBAL — facts
// about the device and about the last command, not about the active tab (D7) — so
// they belong to whatever page 1 turns out to be, including an empty one.
void BrowseDialog::addPageOneChrome(QWidget *box, QVBoxLayout *v) {
  if (stagedSummary.value("count").toInt() > 0) {
    QWidget *banner = new QWidget(box);
    QHBoxLayout *bl = new QHBoxLayout(banner);
    bl->setContentsMargins(14, 10, 14, 10);
    bl->addWidget(new Label(Label::Small, "Changes staged — reboot to apply"), 1);
    N3ButtonLabel *rebootButton = construct_N3ButtonLabel(banner);
    rebootButton->setText("Reboot now");
    bl->addWidget(rebootButton);
    QObject::connect(rebootButton, SIGNAL(tapped(bool)), this, SLOT(rebootConfirm()));
    v->addWidget(banner);
  }
  // Skips from the last action (INTEGRITY.md §6.4). Deliberately a line in the
  // list rather than a dialog: it is neither a failure (no error dialog) nor
  // something to hide (the run "succeeded" while leaving a package behind), and
  // it must survive the reload the action triggers, which a dialog fired before
  // that reload would not.
  for (int i = 0; i < skipNotes.size() && i < kMaxSkipLines; i++) {
    Label *note = new Label(Label::Small, skipNotes.at(i));
    note->setWordWrap(true); // the reason carries kpm's own remedy — never elide it
    note->setContentsMargins(14, 10, 14, 0);
    v->addWidget(note);
  }
  if (skipNotes.size() > kMaxSkipLines) {
    Label *more =
        new Label(Label::Small, QString("+%1 more skipped — see the kpm log").arg(skipNotes.size() - kMaxSkipLines));
    more->setContentsMargins(14, 10, 14, 0);
    v->addWidget(more);
  }
  if (registriesStale()) {
    Label *hint = new Label(Label::Small, "Tap Refresh for the latest package list");
    hint->setContentsMargins(14, 10, 14, 10);
    v->addWidget(hint);
  }
}

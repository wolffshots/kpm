// NICKEL-UI.md §5 — BrowseDialog. Layout/keyboard/pagination patterns adapted
// from NickelHardcover's searchdialog.cc (MIT) per §0; the data comes from
// `kpm search --json` (JSON-OUTPUT.md §2.1), filtered client-side.

#include <QDateTime>
#include <QFile>
#include <QHBoxLayout>
#include <QHash>
#include <QPointer>
#include <QVBoxLayout>

#include <NickelHook.h>

#include "browsedialog.h"
#include "detaildialog.h"
#include "files.h"
#include "kpmprocess.h"
#include "nkpm.h"
#include "widgets/label.h"
#include "widgets/packagerow.h"

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
    render();
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

  if (pendingCheck) {
    pendingCheck = false;
    // check rides the mutating guard (it takes kpm's lock), so it can be
    // refused if another mutation is somehow in flight — degrade to no badges.
    KpmProcess *p = KpmProcess::check(); // network: latest/update badges (§5)
    if (p) {
      QObject::connect(p, &KpmProcess::response, this, &BrowseDialog::onCheckDone);
      QObject::connect(p, &KpmProcess::failure, this, &BrowseDialog::onProcessFailure);
    } else {
      setActionsEnabled(true);
    }
  } else {
    setActionsEnabled(true);
  }

  if (laidOut) {
    render();
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
    render();
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
  bool reboot = payload.value("reboot_required").toBool() || payload.value("staged").toBool();
  loadSearch(false);
  if (reboot) {
    rebootConfirm();
  }
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
  setActionsEnabled(false);
  QObject::connect(p, &KpmProcess::response, this, &BrowseDialog::onActionDone);
  QObject::connect(p, &KpmProcess::failure, this, &BrowseDialog::onProcessFailure);
}

void BrowseDialog::rebootConfirm() {
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

void BrowseDialog::openDetail(QString id) {
  for (const QJsonValue &v : allPackages) {
    QJsonObject o = v.toObject();
    if (o.value("id").toString() == id) {
      DetailDialog *d = DetailDialog::show(o);
      QObject::connect(d, &DetailDialog::changed, this, [this] { loadSearch(false); });
      // Post-enrol "Check now" (kpm-self-enrol-plan §5): reload WITH a check so the
      // freshly-adopted kpm row repaints with its real update badge, like onRefreshDone.
      QObject::connect(d, &DetailDialog::selfEnrolled, this, [this] { loadSearch(true); });
      // Detail's close (X) closes the whole package manager: dismiss this browse
      // dialog underneath it (the detail view deletes itself via the base Dialog).
      QObject::connect(d, &DetailDialog::closeRequested, this, [this] { dialog->deleteLater(); });
      return;
    }
  }
}

void BrowseDialog::commit() {
  filter = lineEdit->text().trimmed();
  if (laidOut && dataReady) {
    render();
  }
}

QList<QJsonObject> BrowseDialog::filteredPackages() const {
  QList<QJsonObject> out;
  QString f = filter.toLower();
  for (const QJsonValue &v : allPackages) {
    QJsonObject o = v.toObject();
    if (f.isEmpty() || o.value("id").toString().toLower().contains(f) ||
        o.value("name").toString().toLower().contains(f) ||
        o.value("description").toString().toLower().contains(f)) {
      out.append(o);
    }
  }
  return out;
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

void BrowseDialog::render() {
  filtered = filteredPackages();
  pages->clear();
  pages->next();
}

void BrowseDialog::requestPage(int index) {
  if (kpmMissing || !dataReady) {
    return;
  }
  if (filtered.isEmpty()) {
    pages->setStatusText(allPackages.isEmpty() ? "No packages. Tap Refresh for the package list."
                                               : "No packages match your search.");
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
    if (registriesStale()) {
      Label *hint = new Label(Label::Small, "Tap Refresh for the latest package list");
      hint->setContentsMargins(14, 10, 14, 10);
      v->addWidget(hint);
    }
  }

  for (int i = start; i < end; i++) {
    PackageRow *row = new PackageRow(filtered.at(i));
    QObject::connect(row, &PackageRow::selected, this, &BrowseDialog::openDetail);
    v->addWidget(row);
  }
  v->addStretch(1);
  pages->addPage(box);
}

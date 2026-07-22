// NICKEL-UI.md §6 — DetailDialog + action flow.

#include <QHBoxLayout>
#include <QJsonArray>
#include <QLabel>
#include <QTimer>
#include <QVBoxLayout>

#include <NickelHook.h>

#include "configdialog.h"
#include "detaildialog.h"
#include "files.h"
#include "nkpm.h"
#include "widgets/elidedlabel.h"
#include "widgets/label.h"

DetailDialog *DetailDialog::show(QJsonObject package) { return new DetailDialog(package); }

DetailDialog::DetailDialog(QJsonObject package)
    : Dialog(package.value("name").toString().isEmpty() ? package.value("id").toString()
                                                        : package.value("name").toString()),
      pkg(package), id(package.value("id").toString()) {
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
  // min_kpm gate (§2.1): don't offer an Install/Update kpm itself would refuse.
  bool minKpmOk = pkg.value("min_kpm_ok").toBool(true);
  // Settings opens the config editor; shown only when installed and the package
  // declares an editable config (has_config, from the search payload — CONFIG.md §4).
  bool hasConfig = !installed.isEmpty() && pkg.value("has_config").toBool();

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

  addInfo("Source", pkg.value("source").toString());
  addInfo("Registry", pkg.value("registry").toString());
  addInfo("Installed", installed);
  addInfo("Latest", pkg.value("latest").toString());
  addInfo("Pinned", pkg.value("pinned").toString());
  if (!installed.isEmpty() && !uninstallable && !isKpm) {
    addInfo("Uninstall", "not available for this package");
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

  layout->addSpacing(16);

  statusLabel = new Label(Label::Small, "");
  layout->addWidget(statusLabel);

  // Action buttons sit in a right-aligned footer pinned to the bottom of the
  // view (added after the stretch below). primaryButton is Nickel's dark-filled
  // style: light text on a dark button.
  QHBoxLayout *actions = new QHBoxLayout();
  actions->setSpacing(16);
  actions->addStretch(1);
  auto addButton = [&](const QString &text, const char *slot) {
    N3ButtonLabel *button = construct_N3ButtonLabel(this);
    button->setProperty("primaryButton", true);
    button->setText(text);
    actions->addWidget(button);
    QObject::connect(button, SIGNAL(tapped(bool)), this, slot);
    buttons.append(button);
  };

  if (staged) {
    layout->addWidget(new Label(Label::Medium, "Changes staged — reboot to apply."));
  } else if (!minKpmOk) {
    layout->addWidget(
        new Label(Label::Medium, "Requires kpm >= " + pkg.value("min_kpm").toString() + " — update kpm first."));
    if (!installed.isEmpty() && uninstallable && !isKpm) {
      addButton("Uninstall", SLOT(uninstall()));
    }
  } else if (installed.isEmpty()) {
    addButton("Install", SLOT(install()));
  } else {
    if (updateAvailable) {
      addButton("Update", SLOT(update()));
    }
    if (uninstallable && !isKpm) {
      addButton("Uninstall", SLOT(uninstall()));
    }
  }

  // Settings is independent of the install/update branch above: editing an
  // already-installed mod's config needs no min_kpm gate and no update state, so
  // it is offered whenever the package is installed and declares a config (§4.1).
  if (hasConfig) {
    addButton("Settings", SLOT(openConfig()));
  }

  layout->addStretch(1);
  layout->addLayout(actions);
}

void DetailDialog::setBusy(bool busy) {
  for (QWidget *b : buttons) {
    b->setEnabled(!busy);
  }
  if (statusLabel) {
    statusLabel->setText(busy ? QStringLiteral("Working…") : QString());
  }
}

void DetailDialog::run(KpmProcess *proc) {
  if (!proc) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  setBusy(true);
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
  setBusy(true);
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
  QTimer::singleShot(0, this, [this] { run(KpmProcess::update(id)); });
}

void DetailDialog::update() { run(KpmProcess::update(id)); }

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

void DetailDialog::uninstall() {
  QString name = pkg.value("name").toString();
  if (name.isEmpty()) {
    name = id;
  }
  ConfirmationDialog *d = ConfirmationDialogFactory__getConfirmationDialog(nullptr);
  ConfirmationDialog__setTitle(d, "kpm");
  ConfirmationDialog__setText(d, "Uninstall " + name + "? Its files will be removed on reboot.");
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

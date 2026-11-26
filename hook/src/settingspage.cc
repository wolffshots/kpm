// The Settings tab. See settingspage.h.
//
// UI rules of nickel-ui-research-2026-11-17.md: the page is built once, and a
// change sets label text and button state in place. A bold name label adds to
// the Label sheet, as on the SSH tab.

#include <QGridLayout>
#include <QHBoxLayout>
#include <QStyle>
#include <QVBoxLayout>

#include <NickelHook.h>

#include "kpmprocess.h"
#include "morerow.h"
#include "settingspage.h"
#include "widgets/label.h"

static Label *nameLabel(const QString &text) {
  Label *n = new Label(Label::Small, text);
  n->setStyleSheet(n->styleSheet() + "\nLabel { font-weight: bold; }");
  return n;
}

static Label *noteLabel(const QString &text) {
  Label *n = new Label(Label::Small, text);
  n->setWordWrap(true);
  return n;
}

// markActive gives the chosen option Nickel's dark-filled style, as styleTabs
// does for the tabs.
static void markActive(N3ButtonLabel *b, bool active) {
  b->setProperty("primaryButton", active);
  b->style()->unpolish(b);
  b->style()->polish(b);
}

SettingsPage::SettingsPage(QWidget *parent) : QWidget(parent) {
  QVBoxLayout *v = new QVBoxLayout(this);
  v->setContentsMargins(30, 20, 30, 6);
  v->setSpacing(12);

  QGridLayout *grid = new QGridLayout();
  grid->setHorizontalSpacing(24);
  grid->setVerticalSpacing(12);
  grid->setColumnStretch(1, 1);

  grid->addWidget(nameLabel("Open kpm from"), 0, 0, Qt::AlignLeft | Qt::AlignVCenter);
  QHBoxLayout *choice = new QHBoxLayout();
  choice->setContentsMargins(0, 0, 0, 0);
  choice->setSpacing(16);
  moreButton = construct_N3ButtonLabel(this);
  moreButton->setObjectName("settingsMoreRow");
  moreButton->setText("Row on the More screen");
  choice->addWidget(moreButton);
  nmButton = construct_N3ButtonLabel(this);
  nmButton->setObjectName("settingsNickelMenu");
  nmButton->setText("NickelMenu");
  choice->addWidget(nmButton);
  choice->addStretch(1);
  grid->addLayout(choice, 0, 1);
  openNote = noteLabel("");
  grid->addWidget(openNote, 1, 1); // no alignment: a wrapped label must fill its cell to get its full height

  hideName = nameLabel("NickelMenu button");
  grid->addWidget(hideName, 2, 0, Qt::AlignLeft | Qt::AlignVCenter);
  QHBoxLayout *hide = new QHBoxLayout();
  hide->setContentsMargins(0, 0, 0, 0);
  hide->setSpacing(12);
  hideValue = new Label(Label::Small, "");
  hide->addWidget(hideValue, 0, Qt::AlignVCenter);
  hideButton = construct_N3ButtonLabel(this);
  hideButton->setObjectName("settingsHideButton");
  hideButton->setProperty("borderedButton", true); // Nickel draws a 1px outline, so the row reads as a button
  hide->addWidget(hideButton, 0, Qt::AlignVCenter);
  hide->addStretch(1);
  grid->addLayout(hide, 2, 1);
  hideNote = noteLabel("");
  grid->addWidget(hideNote, 3, 1);

  syncName = nameLabel("Hide in library");
  grid->addWidget(syncName, 4, 0, Qt::AlignLeft | Qt::AlignVCenter);
  QHBoxLayout *sync = new QHBoxLayout();
  sync->setContentsMargins(0, 0, 0, 0);
  sync->setSpacing(16);
  dotButton = construct_N3ButtonLabel(this);
  dotButton->setObjectName("settingsDotFolders");
  dotButton->setText("Dot folders");
  sync->addWidget(dotButton);
  syncOffButton = construct_N3ButtonLabel(this);
  syncOffButton->setObjectName("settingsSyncOff");
  syncOffButton->setText("Off");
  sync->addWidget(syncOffButton);
  sync->addStretch(1);
  grid->addLayout(sync, 4, 1);
  syncNote = noteLabel("");
  syncNote->setObjectName("settingsSyncNote");
  grid->addWidget(syncNote, 5, 1);
  v->addLayout(grid);
  v->addStretch(1);

  QObject::connect(moreButton, SIGNAL(tapped(bool)), this, SLOT(chooseMoreRow()));
  QObject::connect(nmButton, SIGNAL(tapped(bool)), this, SLOT(chooseNickelMenu()));
  QObject::connect(hideButton, SIGNAL(tapped(bool)), this, SLOT(toggleHide()));
  QObject::connect(dotButton, SIGNAL(tapped(bool)), this, SLOT(chooseDotFolders()));
  QObject::connect(syncOffButton, SIGNAL(tapped(bool)), this, SLOT(chooseSyncOff()));
  load();
}

void SettingsPage::load() {
  nkpm_launch_read(NKPM_UI_CONFIG, &settings);
  nmInstalled = nkpm_nm_installed(NKPM_IMAGEFORMATS) != 0;
  rowAvailable = nkpm_morerow_available() != 0;
  struct nkpm_launch_state st = nkpm_launch_decide(&settings, nmInstalled, rowAvailable);

  markActive(moreButton, st.use_row);
  markActive(nmButton, !st.use_row);
  // Without NickelMenu its options do nothing, so they do not show.
  nmButton->setVisible(nmInstalled);
  hideName->setVisible(nmInstalled);
  hideValue->setVisible(nmInstalled);
  hideButton->setVisible(nmInstalled);
  hideNote->setVisible(nmInstalled);
  if (!nmInstalled) {
    openNote->setText("NickelMenu is not installed, so kpm opens from the row on the More screen.");
  } else if (!rowAvailable) {
    openNote->setText("The row on the More screen is off after an error, so kpm opens from NickelMenu.");
  } else if (st.use_row) {
    openNote->setText("Tap More, then Package manager. kpm removes its items from NickelMenu.");
  } else {
    openNote->setText("Tap the NickelMenu button, then kpm - Package manager.");
  }

  // The hide setting applies only with the row: the button must stay while it
  // is the way to open kpm.
  bool const canHide = st.use_row;
  hideButton->setEnabled(canHide);
  hideValue->setText(st.hide ? "Hidden" : "Shown");
  hideButton->setText(st.hide ? "Show" : "Hide");
  hideNote->setText(canHide ? QString("Hides the NickelMenu button in the bottom bar of the home screen. "
                                      "Restart the device after you change this setting.")
                            : QString("Hides the NickelMenu button in the bottom bar of the home screen. "
                                      "Only available when kpm opens from the row on the More screen. "
                                      "Restart the device after you change this setting."));

  loadSync();
}

void SettingsPage::save(struct nkpm_launch const &next) {
  int rc = nkpm_launch_write(NKPM_UI_CONFIG, &next);
  struct nkpm_launch_state st = nkpm_launch_decide(&next, nmInstalled, rowAvailable);
  rc |= nkpm_launch_apply(NKPM_NM_FILE, NKPM_NM_HIDE, st.entries, st.hide);
  nh_log("NickelKPM: settings saved: open from %s, button %s%s", st.use_row ? "the More row" : "NickelMenu",
         st.hide ? "hidden" : "shown", rc == 0 ? "" : " (write error)");
  if (rc != 0) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "Could not save the setting to the user partition.");
  }
  load();
}

void SettingsPage::chooseMoreRow() {
  if (settings.more_row) {
    return;
  }
  struct nkpm_launch next = settings;
  next.more_row = 1;
  save(next);
}

void SettingsPage::chooseNickelMenu() {
  if (!settings.more_row || !nmInstalled) {
    return;
  }
  // The button must show while NickelMenu is the way to open kpm.
  struct nkpm_launch next = {0, 0};
  save(next);
}

void SettingsPage::toggleHide() {
  if (!hideButton->isEnabled()) {
    return;
  }
  struct nkpm_launch next = settings;
  next.hide_nm_button = !next.hide_nm_button;
  save(next);
}

// ---- Hide in library (EXCLUDE-SYNC.md section 5) --------------------------

void SettingsPage::loadSync() {
  if (syncBusy) {
    return;
  }
  KpmProcess *p = KpmProcess::excludeSync();
  if (p == nullptr) {
    return; // start() refuses only mutating commands; the read is not one
  }
  setSyncBusy(true);
  QObject::connect(p, &KpmProcess::response, this, [this](int, QJsonObject payload) {
    setSyncBusy(false);
    applySync(payload);
  });
  QObject::connect(p, &KpmProcess::failure, this, [this](QString) { setSyncBusy(false); });
}

void SettingsPage::applySync(const QJsonObject &payload) {
  syncSource = payload.value("source").toString();
  bool const fromKpm = syncSource == "kpm" || syncSource == "kpm+custom";
  markActive(dotButton, fromKpm);
  markActive(syncOffButton, syncSource == "none");
  setSyncBusy(false); // kpm leaves a value that it did not write: Off cannot remove it

  QString text;
  if (syncSource == "none") {
    text = "Nickel adds books and images from folders whose name starts with a dot, for example .adds, "
           "to the library.";
  } else if (syncSource == "custom") {
    text = "Kobo eReader.conf has its own ExcludeSyncFolders value. Dot folders adds the kpm preset to it.";
  } else {
    text = "Nickel skips folders whose name starts with a dot, except .kobo and .adobe-digital-editions.";
    if (syncSource == "kpm+custom") {
      text += " Your own value stays.";
    }
  }
  text += syncChanged ? QString(" Restart the device to apply the change.")
                      : QString(" Restart the device after you change this setting.");
  syncNote->setText(text);
}

void SettingsPage::setSyncBusy(bool busy) {
  syncBusy = busy;
  dotButton->setEnabled(!busy);
  syncOffButton->setEnabled(!busy && syncSource != "custom");
}

void SettingsPage::runSync(KpmProcess *p) {
  if (p == nullptr) {
    return; // another mutating kpm runs
  }
  setSyncBusy(true);
  QObject::connect(p, &KpmProcess::response, this, [this](int, QJsonObject) {
    syncChanged = true;
    setSyncBusy(false);
    loadSync();
  });
  // KpmProcess shows the error. Read the row again: the file may have changed.
  QObject::connect(p, &KpmProcess::failure, this, [this](QString) {
    setSyncBusy(false);
    loadSync();
  });
}

void SettingsPage::chooseDotFolders() {
  if (syncBusy || syncSource == "kpm" || syncSource == "kpm+custom") {
    return;
  }
  runSync(KpmProcess::excludeSyncSet("dot-folders"));
}

void SettingsPage::chooseSyncOff() {
  if (syncBusy || syncSource == "none" || syncSource == "custom") {
    return;
  }
  runSync(KpmProcess::excludeSyncOff());
}

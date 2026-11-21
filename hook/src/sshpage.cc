// kpm-archive/docs/SSH.md section 7: the SSH tab. See sshpage.h.
//
// UI rules of nickel-ui-research-2026-11-17.md: the page is built once, before
// its first show, and later statuses change label text in place. No style sheet
// of Nickel is replaced: a bold name label adds to the Label sheet. kpm opens
// from the home screen, which has no dark mode, so the page stays light.

#include <QGridLayout>
#include <QHBoxLayout>
#include <QJsonArray>
#include <QVBoxLayout>

#include "kpmprocess.h"
#include "sshpage.h"
#include "widgets/label.h"

SshPage::SshPage(QWidget *parent) : QWidget(parent) {
  QVBoxLayout *v = new QVBoxLayout(this);
  v->setContentsMargins(30, 20, 30, 6);
  v->setSpacing(12);

  QGridLayout *grid = new QGridLayout();
  grid->setHorizontalSpacing(24);
  grid->setVerticalSpacing(12);
  grid->setColumnStretch(1, 1);
  enabledValue = addRow(grid, 0, "Enabled");
  statusValue = addRow(grid, 1, "Status");
  addressValue = addRow(grid, 2, "Address");
  hostKeyValue = addRow(grid, 3, "Host key");
  keysValue = addRow(grid, 4, "Keys");
  errorValue = addRow(grid, 5, "Last error", &errorName);
  errorValue->setWordWrap(true);
  v->addLayout(grid);

  noKeyNote = new Label(Label::Small, "Add your public key to .adds/kpm/ssh/authorized_keys over USB.");
  noKeyNote->setWordWrap(true);
  noKeyNote->setStyleSheet(noKeyNote->styleSheet() + "\nLabel { font-weight: bold; }");
  v->addWidget(noKeyNote);
  v->addStretch(1);

  QHBoxLayout *buttons = new QHBoxLayout();
  buttons->setContentsMargins(0, 0, 0, 0);
  buttons->setSpacing(16);
  buttons->addStretch(1);
  toggleButton = construct_N3ButtonLabel(this);
  toggleButton->setProperty("primaryButton", true);
  toggleButton->setText("Enable");
  buttons->addWidget(toggleButton);
  restartButton = construct_N3ButtonLabel(this);
  restartButton->setProperty("primaryButton", true);
  restartButton->setText("Restart");
  buttons->addWidget(restartButton);
  v->addLayout(buttons);
  QObject::connect(toggleButton, SIGNAL(tapped(bool)), this, SLOT(toggle()));
  QObject::connect(restartButton, SIGNAL(tapped(bool)), this, SLOT(restart()));

  apply(QJsonObject());
}

Label *SshPage::addRow(QGridLayout *grid, int row, const QString &name, Label **nameOut) {
  Label *n = new Label(Label::Small, name);
  if (nameOut != nullptr) {
    *nameOut = n;
  }
  n->setStyleSheet(n->styleSheet() + "\nLabel { font-weight: bold; }");
  grid->addWidget(n, row, 0, Qt::AlignLeft | Qt::AlignTop);
  Label *value = new Label(Label::Small, ""); // Label is plain text
  grid->addWidget(value, row, 1, Qt::AlignLeft | Qt::AlignTop);
  return value;
}

void SshPage::load() {
  KpmProcess *p = KpmProcess::sshStatus();
  if (p == nullptr) {
    return; // start() refuses only mutating commands; status is not one
  }
  QObject::connect(p, &KpmProcess::response, this, &SshPage::onStatus);
  QObject::connect(p, &KpmProcess::failure, this, &SshPage::onFailure);
}

void SshPage::onStatus(int exitCode, QJsonObject payload) {
  (void)exitCode;
  apply(payload);
  setBusy(false);
  loaded();
}

void SshPage::onFailure(QString reason) {
  (void)reason; // KpmProcess already showed the error dialog
  setBusy(false);
  loaded(); // show the page with what it has, rather than a tab that never opens
}

void SshPage::onActionDone(int exitCode, QJsonObject payload) {
  (void)exitCode;
  (void)payload;
  load(); // refresh after each button (SSH.md section 7); load clears busy
}

// apply fills the rows from a status payload. Each setText is a no-op for an
// unchanged string, so a refresh repaints only what changed.
void SshPage::apply(const QJsonObject &st) {
  bool installed = st.value("installed").toBool();
  enabled = st.value("enabled").toBool();
  bool running = st.value("running").toBool();
  int port = st.value("port").toInt(22);
  int keys = st.value("keys").toInt();
  int badLines = st.value("bad_key_lines").toInt();

  enabledValue->setText(enabled ? "On" : (installed ? "Off" : "Off (not set up)"));
  statusValue->setText(running ? QString("Running on port %1").arg(port) : QString("Stopped"));

  QJsonArray addrs = st.value("addresses").toArray();
  QString user = st.value("user").toString();
  if (user.isEmpty()) {
    user = "root";
  }
  addressValue->setText(addrs.isEmpty() ? QString("No network address") : user + "@" + addrs.at(0).toString());

  QString fp = st.value("host_key").toString();
  hostKeyValue->setText(fp.isEmpty() ? QString("Made at the first start") : fp);

  QString keysText = QString::number(keys);
  if (badLines > 0) {
    keysText += QString(" (%1 bad line%2 skipped)").arg(badLines).arg(badLines == 1 ? "" : "s");
  }
  keysValue->setText(keysText);

  // The last error matters only when the server does not run.
  QString err = running ? QString() : st.value("last_error").toString();
  errorName->setVisible(!err.isEmpty());
  errorValue->setVisible(!err.isEmpty());
  errorValue->setText(err);

  // With no key, the note takes the place of the buttons (SSH.md section 7).
  // Disable stays while ssh is on, so the user can always turn it off.
  bool noKey = keys == 0;
  noKeyNote->setVisible(noKey);
  toggleButton->setText(enabled ? "Disable" : "Enable");
  toggleButton->setVisible(!noKey || enabled);
  restartButton->setVisible(!noKey);
}

void SshPage::setBusy(bool b) {
  busy = b;
  toggleButton->setEnabled(!b);
  restartButton->setEnabled(!b);
}

void SshPage::toggle() {
  if (busy) {
    return;
  }
  KpmProcess *p = enabled ? KpmProcess::sshDisable() : KpmProcess::sshEnable();
  if (p == nullptr) {
    return;
  }
  setBusy(true);
  QObject::connect(p, &KpmProcess::response, this, &SshPage::onActionDone);
  // A failed enable (a used port, for example) still changes nothing on screen
  // until the status is read again.
  QObject::connect(p, &KpmProcess::failure, this, [this](QString) { load(); });
}

void SshPage::restart() {
  if (busy) {
    return;
  }
  KpmProcess *p = KpmProcess::sshRestart();
  if (p == nullptr) {
    return;
  }
  setBusy(true);
  QObject::connect(p, &KpmProcess::response, this, &SshPage::onActionDone);
  QObject::connect(p, &KpmProcess::failure, this, [this](QString) { load(); });
}

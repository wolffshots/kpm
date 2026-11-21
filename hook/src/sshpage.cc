// kpm-archive/docs/SSH.md section 7: the SSH tab. See sshpage.h.
//
// UI rules of nickel-ui-research-2026-11-17.md: the page is built once, before
// its first show, and later statuses change label text in place. No style sheet
// of Nickel is replaced: a bold name label adds to the Label sheet. kpm opens
// from the home screen, which has no dark mode, so the page stays light. The eye
// icon still has a dark variant: it follows a darkMode=true parent.

#include <QGridLayout>
#include <QHBoxLayout>
#include <QImage>
#include <QJsonArray>
#include <QPainter>
#include <QPainterPath>
#include <QPixmap>
#include <QVBoxLayout>

#include "forcewifi.h"
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
  // The IP address is hidden until the user taps the eye (SSH.md section 7).
  eyeButton = construct_TouchLabel(this);
  eyeButton->setObjectName("sshAddressEye");
  eyeButton->setAlignment(Qt::AlignCenter);
  addressValue = addRow(grid, 2, "Address", nullptr, eyeButton);
  QObject::connect(eyeButton, SIGNAL(tapped(bool)), this, SLOT(toggleAddress()));
  // Force Wi-Fi is a Nickel setting that the hook changes in place
  // (forcewifi.h). The row hides when the firmware does not have it.
  wifiButton = construct_N3ButtonLabel(this);
  wifiButton->setObjectName("sshForceWifiButton");
  wifiValue = addRow(grid, 3, "Force Wi-Fi", &wifiName, wifiButton);
  wifiValue->setObjectName("sshForceWifiValue");
  QObject::connect(wifiButton, SIGNAL(tapped(bool)), this, SLOT(toggleForceWifi()));
  wifiNote = new Label(Label::Small, "Keeps Wi-Fi on while the device is awake, so SSH stays reachable. "
                                     "Sleep still turns Wi-Fi off.");
  wifiNote->setWordWrap(true);
  grid->addWidget(wifiNote, 4, 1); // no alignment: a wrapped label must fill its cell to get its full height
  hostKeyValue = addRow(grid, 5, "Host key");
  keysValue = addRow(grid, 6, "Keys");
  errorValue = addRow(grid, 7, "Last error", &errorName);
  errorValue->setWordWrap(true);
  v->addLayout(grid);

  noKeyNote = new Label(Label::Small, "Add your public key to .adds/authorized_keys over USB.");
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

// addRow adds a name and a value label. An extra widget goes right of the value.
Label *SshPage::addRow(QGridLayout *grid, int row, const QString &name, Label **nameOut, QWidget *extra) {
  Label *n = new Label(Label::Small, name);
  if (nameOut != nullptr) {
    *nameOut = n;
  }
  n->setStyleSheet(n->styleSheet() + "\nLabel { font-weight: bold; }");
  Label *value = new Label(Label::Small, ""); // Label is plain text
  if (extra == nullptr) {
    grid->addWidget(n, row, 0, Qt::AlignLeft | Qt::AlignTop);
    grid->addWidget(value, row, 1, Qt::AlignLeft | Qt::AlignTop);
    return value;
  }
  grid->addWidget(n, row, 0, Qt::AlignLeft | Qt::AlignVCenter);
  QHBoxLayout *h = new QHBoxLayout();
  h->setContentsMargins(0, 0, 0, 0);
  h->setSpacing(12);
  h->addWidget(value, 0, Qt::AlignVCenter);
  h->addWidget(extra, 0, Qt::AlignVCenter);
  h->addStretch(1);
  grid->addLayout(h, row, 1);
  return value;
}

// eyeIcon draws the eye of the Address row: open (tap to show) or crossed out
// (tap to hide). The light variant is black and the dark variant is white. Each
// variant is drawn in its own color, so no invertPixels() call is necessary (the
// Qt 5.2 fault in nickel-ui-research-2026-11-17.md).
static QPixmap eyeIcon(int size, bool crossed, bool dark) {
  QImage img(size, size, QImage::Format_ARGB32);
  img.fill(Qt::transparent);
  QPainter p(&img);
  p.setRenderHint(QPainter::Antialiasing);
  QColor c = dark ? Qt::white : Qt::black;
  qreal w = qMax<qreal>(2.0, size / 12.0);
  p.setPen(QPen(c, w, Qt::SolidLine, Qt::RoundCap, Qt::RoundJoin));

  // The outline: two curves that meet at the corners of the eye.
  qreal left = w, right = size - w, mid = size / 2.0, h = size * 0.3;
  QPainterPath outline;
  outline.moveTo(left, mid);
  outline.quadTo(mid, mid - 2 * h, right, mid);
  outline.quadTo(mid, mid + 2 * h, left, mid);
  p.drawPath(outline);
  p.setBrush(c);
  p.drawEllipse(QPointF(mid, mid), size * 0.14, size * 0.14);

  if (crossed) {
    // Clear a gap along the line, then draw the line in it.
    QLineF slash(size * 0.15, size * 0.85, size * 0.85, size * 0.15);
    p.setCompositionMode(QPainter::CompositionMode_Clear);
    p.setPen(QPen(Qt::transparent, 3 * w, Qt::SolidLine, Qt::RoundCap));
    p.drawLine(slash);
    p.setCompositionMode(QPainter::CompositionMode_SourceOver);
    p.setPen(QPen(c, w, Qt::SolidLine, Qt::RoundCap));
    p.drawLine(slash);
  }
  p.end();
  return QPixmap::fromImage(img);
}

// darkPage is true when a parent has the Nickel darkMode property set
// (nickel-ui-research-2026-11-17.md, dark mode).
bool SshPage::darkPage() const {
  for (const QWidget *w = this; w != nullptr; w = w->parentWidget()) {
    if (w->property("darkMode").toBool()) {
      return true;
    }
  }
  return false;
}

void SshPage::hideAddress() {
  addressShown = false;
  updateAddress();
}

void SshPage::toggleAddress() {
  addressShown = !addressShown;
  updateAddress();
}

void SshPage::showEvent(QShowEvent *event) {
  QWidget::showEvent(event);
  updateAddress(); // a parent can change darkMode while the page is hidden
}

// updateAddress sets the Address row text and the eye. It sets a new pixmap only
// when the size or the variant changes, so a status refresh does not repaint the
// icon.
void SshPage::updateAddress() {
  bool has = !address.isEmpty();
  addressValue->setText(!has ? QString("No network address") : (addressShown ? address : QString("Hidden")));
  eyeButton->setVisible(has);
  if (!has) {
    return;
  }
  addressValue->ensurePolished();
  int size = qMax(24, addressValue->fontMetrics().height() * 6 / 5);
  bool dark = darkPage();
  int state = size * 4 + (addressShown ? 2 : 0) + (dark ? 1 : 0);
  if (state == eyeState) {
    return;
  }
  eyeState = state;
  eyeButton->setFixedSize(size * 2, size + size / 2); // a larger tap area than the icon
  eyeButton->setPixmap(eyeIcon(size, addressShown, dark));
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
  address = addrs.isEmpty() ? QString() : QString("%1@%2:%3").arg(user, addrs.at(0).toString()).arg(port);
  updateAddress();

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

  // Nickel holds this setting, not kpm: read it in place at each refresh.
  updateForceWifi();
}

// updateForceWifi reads the setting from Nickel and fills the row. A value of
// -1 means the firmware does not have it: the row and its note hide.
void SshPage::updateForceWifi() {
  int on = nkpm_force_wifi();
  wifiName->setVisible(on >= 0);
  wifiValue->setVisible(on >= 0);
  wifiButton->setVisible(on >= 0);
  wifiNote->setVisible(on >= 0);
  if (on < 0) {
    return;
  }
  wifiValue->setText(on ? "On" : "Off");
  wifiButton->setText(on ? "Turn off" : "Turn on");
}

void SshPage::toggleForceWifi() {
  int on = nkpm_force_wifi();
  if (on < 0) {
    return;
  }
  nkpm_set_force_wifi(on == 0); // updateForceWifi shows the value that Nickel now has
  updateForceWifi();
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

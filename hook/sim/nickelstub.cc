// hook/sim — desktop implementations of the nkpm.h Nickel shim. See nickelstub.h.

#include <QDialogButtonBox>
#include <QHBoxLayout>
#include <QLocale>
#include <QMouseEvent>
#include <QPushButton>
#include <QStackedWidget>
#include <QVBoxLayout>

#include "nkpm.h"
#include "nickelstub.h"

// ---- globals ------------------------------------------------------------

static SimMainWindow *g_mainWindow = nullptr;
static SimWireless *g_wireless = nullptr;
static QObject *g_controller = nullptr;
SimConfirmationDialog *g_lastConfirmation = nullptr;

static const int kChromeHeight = 52;

// e-ink-ish plain styling: large black text on white, visible borders.
static const char *kAppStyle = R"(
  QWidget { background: #ffffff; color: #000000; font-size: 18px; }
  SimTouchLabel[simButton="true"] {
    border: 2px solid #000000; border-radius: 4px; padding: 8px 16px; font-size: 18px;
  }
  SimTouchLabel[simButton="true"]:disabled { color: #999999; border-color: #999999; }
  QPushButton { border: 2px solid #000000; border-radius: 4px; padding: 8px 20px; font-size: 18px; background: #ffffff; }
  QLineEdit { border: 2px solid #000000; padding: 10px; font-size: 20px; }
)";

// ---- SimTouchLabel ------------------------------------------------------

SimTouchLabel::SimTouchLabel(QWidget *parent, bool button) : QLabel(parent), button(button) {
  if (button) {
    setProperty("simButton", true);
    setAlignment(Qt::AlignCenter);
  }
}

void SimTouchLabel::mouseReleaseEvent(QMouseEvent *event) {
  QLabel::mouseReleaseEvent(event);
  if (event->button() == Qt::LeftButton && isEnabled() && rect().contains(event->pos())) {
    emit tapped(true);
  }
}

// ---- SimTouchLineEdit ---------------------------------------------------

SimTouchLineEdit::SimTouchLineEdit(QWidget *parent) : QLineEdit(parent) {}

void SimTouchLineEdit::mousePressEvent(QMouseEvent *event) {
  emit tapped();
  QLineEdit::mousePressEvent(event);
}

// ---- keyboard shim ------------------------------------------------------

SimKeyboardReceiver::SimKeyboardReceiver(QLineEdit *lineEdit) : QWidget(nullptr), lineEdit(lineEdit) {}

SimKeyboardController::SimKeyboardController() : QObject(nullptr) {}

void SimKeyboardController::bindReceiver(SimKeyboardReceiver *receiver) {
  if (receiver && receiver->lineEdit) {
    // The desktop has a real keyboard: typing goes straight into the line edit,
    // and Return commits (drives Dialog::commit via commitRequested()).
    QObject::connect(receiver->lineEdit, &QLineEdit::returnPressed, this,
                     &SimKeyboardController::commitRequested);
  }
}

// ---- SimN3Dialog --------------------------------------------------------

SimN3Dialog::SimN3Dialog(QWidget *content) : QDialog(nullptr), m_content(content) {
  QVBoxLayout *layout = new QVBoxLayout(this);
  layout->setContentsMargins(0, 0, 0, 0);
  layout->setSpacing(0);
  if (content) {
    content->setParent(this);
    layout->addWidget(content, 1);
  }
  m_keyboardFrame = new QWidget(this);
  m_keyboardFrame->hide();
}

void SimN3Dialog::setTitle(const QString &title) { m_title = title; }

QWidget *SimN3Dialog::keyboardFrame() { return m_keyboardFrame; }

// ---- SimConfirmationDialog ----------------------------------------------

SimConfirmationDialog::SimConfirmationDialog(QWidget *parent) : QDialog(parent) {
  setModal(true);
  setMinimumWidth(460);
  QVBoxLayout *layout = new QVBoxLayout(this);
  layout->setContentsMargins(24, 24, 24, 24);
  layout->setSpacing(16);

  m_title = new QLabel(this);
  QFont tf = m_title->font();
  tf.setPointSize(tf.pointSize() + 3);
  tf.setBold(true);
  m_title->setFont(tf);
  layout->addWidget(m_title);

  m_text = new QLabel(this);
  m_text->setWordWrap(true);
  m_text->setTextFormat(Qt::PlainText);
  layout->addWidget(m_text, 1);

  QHBoxLayout *buttons = new QHBoxLayout();
  buttons->addStretch(1);
  m_reject = new QPushButton("Cancel", this);
  m_accept = new QPushButton("OK", this);
  buttons->addWidget(m_reject);
  buttons->addWidget(m_accept);
  layout->addLayout(buttons);

  QObject::connect(m_accept, &QPushButton::clicked, this, &QDialog::accept);
  QObject::connect(m_reject, &QPushButton::clicked, this, &QDialog::reject);

  g_lastConfirmation = this;
  QObject::connect(this, &QObject::destroyed, [] { g_lastConfirmation = nullptr; });
}

void SimConfirmationDialog::setTitleText(const QString &text) { m_title->setText(text); }
void SimConfirmationDialog::setBodyText(const QString &text) { m_text->setText(text); }
void SimConfirmationDialog::setAcceptText(const QString &text) { m_accept->setText(text); }
void SimConfirmationDialog::setRejectText(const QString &text) { m_reject->setText(text); }

// simShowMessage displays a non-blocking (never exec()) informational dialog, so
// error/reboot popups don't wedge the offscreen screenshot/exercise event loop.
static void simShowMessage(const QString &title, const QString &body) {
  QDialog *d = new QDialog(g_mainWindow);
  d->setAttribute(Qt::WA_DeleteOnClose, true);
  d->setModal(false);
  QVBoxLayout *layout = new QVBoxLayout(d);
  layout->setContentsMargins(24, 24, 24, 24);
  QLabel *t = new QLabel(title, d);
  QFont tf = t->font();
  tf.setBold(true);
  t->setFont(tf);
  layout->addWidget(t);
  QLabel *b = new QLabel(body, d);
  b->setWordWrap(true);
  b->setTextFormat(Qt::PlainText);
  layout->addWidget(b);
  QPushButton *ok = new QPushButton("OK", d);
  layout->addWidget(ok, 0, Qt::AlignRight);
  QObject::connect(ok, &QPushButton::clicked, d, &QDialog::close);
  d->show();
}

// ---- SimMainWindow ------------------------------------------------------

SimMainWindow::SimMainWindow(int w, int h) : QWidget(nullptr) {
  setFixedSize(w, h);
  setStyleSheet(kAppStyle);

  QVBoxLayout *layout = new QVBoxLayout(this);
  layout->setContentsMargins(0, 0, 0, 0);
  layout->setSpacing(0);

  QWidget *chrome = new QWidget(this);
  chrome->setFixedHeight(kChromeHeight);
  chrome->setStyleSheet("QWidget { border-bottom: 2px solid #000000; }");
  QHBoxLayout *chromeLayout = new QHBoxLayout(chrome);
  chromeLayout->setContentsMargins(16, 0, 8, 0);
  m_titleLabel = new QLabel(chrome);
  QFont f = m_titleLabel->font();
  f.setPointSize(f.pointSize() + 3);
  f.setBold(true);
  m_titleLabel->setFont(f);
  m_titleLabel->setStyleSheet("border: none;");
  chromeLayout->addWidget(m_titleLabel, 1);
  m_closeButton = new SimTouchLabel(chrome, false);
  m_closeButton->setText("  ✕  "); // ✕
  m_closeButton->setStyleSheet("border: none; font-size: 24px;");
  chromeLayout->addWidget(m_closeButton, 0);
  layout->addWidget(chrome, 0);

  m_stack = new QStackedWidget(this);
  layout->addWidget(m_stack, 1);

  QObject::connect(m_closeButton, &SimTouchLabel::tapped, this, &SimMainWindow::closeTop);
  refreshChrome();
}

void SimMainWindow::pushView(QWidget *view) {
  int cw = width();
  int chh = height() - kChromeHeight;
  // Override the primary-screen size widgets/dialog.cc set on the dialog, so the
  // content sizes to the Kobo-portrait window rather than the host monitor.
  view->setFixedSize(cw, chh);
  m_stack->addWidget(view);
  m_stack->setCurrentWidget(view);
  m_views.append(view);
  QObject::connect(view, &QObject::destroyed, this, [this](QObject *obj) {
    m_views.removeOne(static_cast<QWidget *>(obj));
    if (!m_views.isEmpty()) {
      m_stack->setCurrentWidget(m_views.last());
    }
    refreshChrome();
  });
  refreshChrome();
}

QWidget *SimMainWindow::currentView() { return m_views.isEmpty() ? this : m_views.last(); }

void SimMainWindow::closeTop() {
  if (m_views.isEmpty()) {
    return;
  }
  if (SimN3Dialog *dlg = qobject_cast<SimN3Dialog *>(m_views.last())) {
    dlg->requestClose();
  }
}

void SimMainWindow::refreshChrome() {
  if (m_views.isEmpty()) {
    m_titleLabel->setText("");
    m_closeButton->hide();
    return;
  }
  QString title;
  if (SimN3Dialog *dlg = qobject_cast<SimN3Dialog *>(m_views.last())) {
    title = dlg->title();
  }
  m_titleLabel->setText(title);
  m_closeButton->show();
}

// ---- nkpm.h function-pointer definitions --------------------------------
//
// These mirror nkpm.cc's declarations (nkpm.cc is NOT compiled into the sim).

MainWindowController *(*MainWindowController__sharedInstance)();
QWidget *(*MainWindowController__currentView)(MainWindowController *mwc);
QWidget *(*MainWindowController__pushView)(MainWindowController *mwc, QWidget *view);

void (*ConfirmationDialogFactory__showErrorDialog)(QString const &title, QString const &body);
ConfirmationDialog *(*ConfirmationDialogFactory__getConfirmationDialog)(QWidget *parent);
void (*ConfirmationDialog__setTitle)(ConfirmationDialog *_this, QString const &);
void (*ConfirmationDialog__setText)(ConfirmationDialog *_this, QString const &);
void (*ConfirmationDialog__setAcceptButtonText)(ConfirmationDialog *_this, QString const &);
void (*ConfirmationDialog__setRejectButtonText)(ConfirmationDialog *_this, QString const &);

WirelessWorkflowManager *(*WirelessWorkflowManager__sharedInstance)();
bool (*WirelessWorkflowManager__isInternetAccessible)(WirelessWorkflowManager *);
void (*WirelessWorkflowManager__connectWirelessSilently)(WirelessWorkflowManager *);
void (*WirelessWorkflowManager__connectWireless)(WirelessWorkflowManager *_this, bool, bool);
WirelessManager *(*WirelessManager__sharedInstance)();

SearchKeyboardController *(*KeyboardFrame__createKeyboard)(KeyboardFrame *__this, int keyboardScript, QLocale locale);
void (*SearchKeyboardController__setReceiver)(SearchKeyboardController *__this, KeyboardReceiver *receiver, bool idk);
void (*SearchKeyboardController__setGoText)(SearchKeyboardController *__this, QString const &text);

N3Dialog *(*N3DialogFactory__getDialog)(QWidget *content, bool idk);
void (*N3Dialog__setTitle)(N3Dialog *__this, QString const &);
KeyboardFrame *(*N3Dialog__keyboardFrame)(N3Dialog *__this);
void (*N3Dialog__showKeyboard)(N3Dialog *__this);
void (*N3Dialog__hideKeyboard)(N3Dialog *__this);

void (*TouchLabel__setHitStateEnabled)(TouchLabel *_this, bool enabled);

// ---- sim implementations of the pointers --------------------------------

static MainWindowController *sim_mwc_sharedInstance() { return g_controller; }
static QWidget *sim_mwc_currentView(MainWindowController *) { return g_mainWindow->currentView(); }
static QWidget *sim_mwc_pushView(MainWindowController *, QWidget *view) {
  g_mainWindow->pushView(view);
  return view;
}

static void sim_showErrorDialog(QString const &title, QString const &body) { simShowMessage(title, body); }
static ConfirmationDialog *sim_getConfirmationDialog(QWidget *parent) {
  return new SimConfirmationDialog(parent ? parent : g_mainWindow);
}
static void sim_conf_setTitle(ConfirmationDialog *d, QString const &s) {
  static_cast<SimConfirmationDialog *>(d)->setTitleText(s);
}
static void sim_conf_setText(ConfirmationDialog *d, QString const &s) {
  static_cast<SimConfirmationDialog *>(d)->setBodyText(s);
}
static void sim_conf_setAccept(ConfirmationDialog *d, QString const &s) {
  static_cast<SimConfirmationDialog *>(d)->setAcceptText(s);
}
static void sim_conf_setReject(ConfirmationDialog *d, QString const &s) {
  static_cast<SimConfirmationDialog *>(d)->setRejectText(s);
}

static WirelessWorkflowManager *sim_wwm_sharedInstance() { return g_wireless; }
// Always-connected: the Wi-Fi gate in kpmprocess.cc short-circuits, so a
// network command runs kpm immediately without a real radio.
static bool sim_isInternetAccessible(WirelessWorkflowManager *) { return true; }
static void sim_connectWirelessSilently(WirelessWorkflowManager *) {}
static void sim_connectWireless(WirelessWorkflowManager *, bool, bool) {}
static WirelessManager *sim_wm_sharedInstance() { return g_wireless; }

static SearchKeyboardController *sim_createKeyboard(KeyboardFrame *frame, int, QLocale) {
  return new SimKeyboardController();
  (void)frame;
}
static void sim_setReceiver(SearchKeyboardController *ctl, KeyboardReceiver *receiver, bool) {
  SimKeyboardController *c = qobject_cast<SimKeyboardController *>(ctl);
  SimKeyboardReceiver *r = qobject_cast<SimKeyboardReceiver *>(receiver);
  if (c && r) {
    c->bindReceiver(r);
  }
}
static void sim_setGoText(SearchKeyboardController *, QString const &) {}

static N3Dialog *sim_getDialog(QWidget *content, bool) { return new SimN3Dialog(content); }
static void sim_n3_setTitle(N3Dialog *d, QString const &s) { static_cast<SimN3Dialog *>(d)->setTitle(s); }
static KeyboardFrame *sim_n3_keyboardFrame(N3Dialog *d) { return static_cast<SimN3Dialog *>(d)->keyboardFrame(); }
static void sim_n3_showKeyboard(N3Dialog *) {}
static void sim_n3_hideKeyboard(N3Dialog *) {}

static void sim_setHitStateEnabled(TouchLabel *, bool) {}

// ---- construct_* helpers (declared in nkpm.h) ---------------------------

TouchLineEdit *construct_TouchLineEdit(QWidget *parent) { return new SimTouchLineEdit(parent); }
TouchLabel *construct_TouchLabel(QWidget *parent) { return new SimTouchLabel(parent, false); }
N3ButtonLabel *construct_N3ButtonLabel(QWidget *parent) { return new SimTouchLabel(parent, true); }
KeyboardReceiver *construct_KeyboardReceiver(QLineEdit *lineEdit) { return new SimKeyboardReceiver(lineEdit); }

void rebootDevice() { simShowMessage("kpm", "The device would reboot now (simulated)."); }

// ---- install ------------------------------------------------------------

void nickelstub_install(int screenW, int screenH) {
  g_controller = new QObject();
  g_wireless = new SimWireless();
  g_mainWindow = new SimMainWindow(screenW, screenH);

  MainWindowController__sharedInstance = &sim_mwc_sharedInstance;
  MainWindowController__currentView = &sim_mwc_currentView;
  MainWindowController__pushView = &sim_mwc_pushView;

  ConfirmationDialogFactory__showErrorDialog = &sim_showErrorDialog;
  ConfirmationDialogFactory__getConfirmationDialog = &sim_getConfirmationDialog;
  ConfirmationDialog__setTitle = &sim_conf_setTitle;
  ConfirmationDialog__setText = &sim_conf_setText;
  ConfirmationDialog__setAcceptButtonText = &sim_conf_setAccept;
  ConfirmationDialog__setRejectButtonText = &sim_conf_setReject;

  WirelessWorkflowManager__sharedInstance = &sim_wwm_sharedInstance;
  WirelessWorkflowManager__isInternetAccessible = &sim_isInternetAccessible;
  WirelessWorkflowManager__connectWirelessSilently = &sim_connectWirelessSilently;
  WirelessWorkflowManager__connectWireless = &sim_connectWireless;
  WirelessManager__sharedInstance = &sim_wm_sharedInstance;

  KeyboardFrame__createKeyboard = &sim_createKeyboard;
  SearchKeyboardController__setReceiver = &sim_setReceiver;
  SearchKeyboardController__setGoText = &sim_setGoText;

  N3DialogFactory__getDialog = &sim_getDialog;
  N3Dialog__setTitle = &sim_n3_setTitle;
  N3Dialog__keyboardFrame = &sim_n3_keyboardFrame;
  N3Dialog__showKeyboard = &sim_n3_showKeyboard;
  N3Dialog__hideKeyboard = &sim_n3_hideKeyboard;

  TouchLabel__setHitStateEnabled = &sim_setHitStateEnabled;
}

QWidget *nickelstub_mainWindow() { return g_mainWindow; }

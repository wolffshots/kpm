#pragma once

// hook/sim — desktop implementations of every Nickel touchpoint declared in
// hook/src/nkpm.h. On device nkpm.cc assigns those function pointers via dlsym
// against libnickel; nkpm.cc is NOT compiled into the sim, so this file (with
// nickelstub.cc) takes its place: nickelstub_install() defines and assigns every
// pointer to a plain-Qt implementation before any dialog code runs.
//
// The subclasses below exist because the dialog sources use STRING-based Qt
// connects (SIGNAL()/SLOT()) that resolve at runtime against the concrete object:
//   - N3Dialog     needs a real closeTapped()   signal   (widgets/dialog.cc)
//   - TouchLineEdit needs a real tapped()        signal   (widgets/dialog.cc)
//   - TouchLabel    needs a real tapped(bool)    signal   (browse/detail/pagedstack)
//   - the Wireless singletons need networkConnected()/connectingFailed()
//     (kpmprocess.cc) — declared though the Wi-Fi gate short-circuits (below)
//   - the keyboard controller needs commitRequested()     (widgets/dialog.cc)
// nkpm.h's typedefs (N3Dialog=QDialog, TouchLabel=QLabel, ...) stay untouched;
// returning a subclass instance through the base-typedef pointer is fine.

#include <QDialog>
#include <QLabel>
#include <QLineEdit>
#include <QList>
#include <QObject>
#include <QString>
#include <QWidget>

class QStackedWidget;
class QPushButton;
class QMouseEvent;

// nickelstub_install assigns every nkpm.h function pointer to a sim implementation
// and creates the fixed-size main window (Kobo portrait, default 758x1024). Call
// once, before BrowseDialog::show().
void nickelstub_install(int screenW, int screenH);

// nickelstub_mainWindow returns the top-level widget the screenshot driver grabs.
QWidget *nickelstub_mainWindow();

// ---- tappable label (TouchLabel / N3ButtonLabel) ------------------------

class SimTouchLabel : public QLabel {
  Q_OBJECT
public:
  explicit SimTouchLabel(QWidget *parent, bool button);

Q_SIGNALS:
  void tapped(bool checked = false);

protected:
  void mouseReleaseEvent(QMouseEvent *event) override;

private:
  bool button;
};

// ---- tappable line edit (TouchLineEdit) ---------------------------------

class SimTouchLineEdit : public QLineEdit {
  Q_OBJECT
public:
  explicit SimTouchLineEdit(QWidget *parent);

Q_SIGNALS:
  void tapped();

protected:
  void mousePressEvent(QMouseEvent *event) override;
};

// ---- keyboard shim ------------------------------------------------------

class SimKeyboardReceiver : public QWidget {
  Q_OBJECT
public:
  explicit SimKeyboardReceiver(QLineEdit *lineEdit);
  QLineEdit *lineEdit;
};

class SimKeyboardController : public QObject {
  Q_OBJECT
public:
  SimKeyboardController();
  // bindReceiver wires the receiver's line edit Return key to commitRequested(),
  // the signal widgets/dialog.cc connects to Dialog::commit().
  void bindReceiver(SimKeyboardReceiver *receiver);

Q_SIGNALS:
  void commitRequested();
};

// ---- Wi-Fi singletons ---------------------------------------------------

class SimWireless : public QObject {
  Q_OBJECT
public:
Q_SIGNALS:
  void networkConnected();
  void connectingFailed();
};

// ---- full-screen dialog (N3Dialog) --------------------------------------

class SimN3Dialog : public QDialog {
  Q_OBJECT
public:
  explicit SimN3Dialog(QWidget *content);
  void setTitle(const QString &title);
  QString title() const { return m_title; }
  QWidget *keyboardFrame();
  // requestClose lets the main-window chrome close button fire the dialog's own
  // closeTapped() (widgets/dialog.cc connects closeTapped -> deleteLater).
  void requestClose() { emit closeTapped(); }

Q_SIGNALS:
  void closeTapped();

private:
  QString m_title;
  QWidget *m_content;
  QWidget *m_keyboardFrame = nullptr;
};

// ---- confirmation dialog (ConfirmationDialog) ---------------------------

class SimConfirmationDialog : public QDialog {
  Q_OBJECT
public:
  explicit SimConfirmationDialog(QWidget *parent);
  void setTitleText(const QString &text);
  void setBodyText(const QString &text);
  void setAcceptText(const QString &text);
  void setRejectText(const QString &text);

private:
  QLabel *m_title;
  QLabel *m_text;
  QPushButton *m_accept;
  QPushButton *m_reject;
};

// ---- main window / navigation stack (MainWindowController) ---------------

class SimMainWindow : public QWidget {
  Q_OBJECT
public:
  SimMainWindow(int w, int h);
  // pushView adds a full-screen view (an N3Dialog) to the stack, sizes it to the
  // content area, and shows it on top — the sim's MainWindowController::pushView.
  void pushView(QWidget *view);
  QWidget *currentView();

public Q_SLOTS:
  void closeTop();

private:
  void refreshChrome();

  QStackedWidget *m_stack;
  QLabel *m_titleLabel;
  SimTouchLabel *m_closeButton;
  QList<QWidget *> m_views;
};

// g_lastConfirmation is the most recently opened confirmation dialog, exposed so
// the offscreen mutating-flow driver (main.cc --exercise-uninstall) can accept it.
extern SimConfirmationDialog *g_lastConfirmation;

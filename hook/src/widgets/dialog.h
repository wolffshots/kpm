#pragma once

// Adapted from NickelHardcover hook/src/widgets/dialog.h (MIT,
// https://codeberg.org/StrayRose/NickelHardcover) per NICKEL-UI.md §0/§5.
// Changes: NickelKPM is launched from the home menu, not a book, so NH's
// ReadingView-only auto-close (currentViewChanged / SyncController) is dropped;
// the dialog closes only when the user taps its close button (closeTapped).

#include <QEvent>
#include <QFrame>
#include <QMargins>
#include <QPointer>
#include <QTextEdit>

#include "../nkpm.h"

class ChromeGuard;

class Dialog : public QFrame {
  Q_OBJECT

public Q_SLOTS:
  void showKeyboard();
  void hideKeyboard();

  virtual void commit() {}

protected:
  Dialog(QString title);
  ~Dialog() override; // restores any chrome hidden by the constructor

  N3Dialog *dialog = nullptr;

  // Insets of the home-screen content area (status bar top, nav bar bottom).
  // Zero when the chrome-hiding path is available (the bars are hidden while
  // the dialog is open); otherwise measured before the dialog is pushed as a
  // fallback so subclass layouts clear the persistent chrome. See dialog.cc.
  QMargins chrome;

  KeyboardFrame *buildKeyboardFrame(TouchLineEdit *lineEdit, QString goText);

private:
  KeyboardFrame *buildKeyboardFrame(KeyboardReceiver *receiver, QString goText);

  void hideChrome(MainWindowController *mwc);

  // reassertChrome re-hides chrome Nickel re-shows behind our back (sleep/wake).
  // Called by the ChromeGuard app-wide event filter; see dialog.cc.
  void reassertChrome(QWidget *shownNav);
  bool reassertStatusBar(); // re-hide the status bar if we recorded hiding it
  friend class ChromeGuard;

  // What hideChrome() actually hid, so ~Dialog restores exactly that. The nav
  // view is a QPointer: Nickel owns it and could delete it under us.
  bool hidStatusBar = false;
  QPointer<QWidget> hiddenNavView;

  // App-wide event filter that re-hides chrome after sleep/wake re-asserts it.
  // Installed only by the recording dialog (the one that actually hid chrome);
  // removed at the very start of ~Dialog before the restore. See dialog.cc.
  ChromeGuard *chromeGuard = nullptr;
};

// ChromeGuard watches every widget Show in the process for a MainNavView Nickel
// re-shows out from under us (the wake/power-view teardown re-asserts the home
// chrome), and tells the owning Dialog to re-hide it. Installed on the
// QApplication, mirroring the app-wide filter idiom in widgets/pagedstack.cc.
class ChromeGuard : public QObject {
  Q_OBJECT

public:
  explicit ChromeGuard(Dialog *owner);

protected:
  bool eventFilter(QObject *obj, QEvent *event) override;

private:
  Dialog *owner = nullptr;
};

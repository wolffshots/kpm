#pragma once

// Adapted from NickelHardcover hook/src/widgets/dialog.h (MIT,
// https://codeberg.org/StrayRose/NickelHardcover) per NICKEL-UI.md §0/§5.
// Changes: NickelKPM is launched from the home menu, not a book, so NH's
// ReadingView-only auto-close (currentViewChanged / SyncController) is dropped;
// the dialog closes only when the user taps its close button (closeTapped).

#include <QFrame>
#include <QMargins>
#include <QPointer>
#include <QTextEdit>

#include "../nkpm.h"

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

  // What hideChrome() actually hid, so ~Dialog restores exactly that. The nav
  // view is a QPointer: Nickel owns it and could delete it under us.
  bool hidStatusBar = false;
  QPointer<QWidget> hiddenNavView;
};
